// Package web is the browser-facing edge of the application: a small REST API
// for the three commands, a one-way WebSocket carrying roster snapshots, and
// the embedded frontend.
//
// Commands travel over REST rather than over the socket on purpose. The socket
// then needs no request/response protocol of its own, and an HTTP status code
// carries a failed kick or invite back to the browser with no extra machinery.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/dmalkin/westbridge/internal/ami"
	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/conference"
	"github.com/dmalkin/westbridge/internal/hub"
	"github.com/dmalkin/westbridge/internal/phonebook"
	"github.com/dmalkin/westbridge/internal/rooms"
	"github.com/dmalkin/westbridge/internal/telephony"
	"github.com/dmalkin/westbridge/internal/web/assets"
)

// maxRequestBody bounds the small JSON objects used by commands and forms.
const maxRequestBody = 4 << 10

// Conference is the slice of *conference.Service the HTTP layer needs.
type Conference interface {
	Snapshot() conference.Snapshot
	Subscribe(fn func(conference.Snapshot)) func()
	Kick(ctx context.Context, uniqueID string) error
	SetMuted(ctx context.Context, uniqueID string, muted bool) error
	Invite(ctx context.Context, number string) (string, error)
	CancelCall(ctx context.Context, id string) error
	RetryCall(ctx context.Context, id string) (string, error)
}

// Phonebook exposes owner-scoped operations independently of authentication.
type Phonebook interface {
	Contacts(context.Context, int64) ([]phonebook.Contact, error)
	SaveContact(context.Context, int64, int64, string, string) (phonebook.Contact, error)
	DeleteContact(context.Context, int64, int64) error
}

// Config tunes the server. Auth and Phonebook are required.
type Config struct {
	Manager                      *conference.Manager
	Rooms                        *rooms.Store
	RoomID, RoomName, RoomNumber string
	RoomAccess                   func(context.Context, auth.User) bool
	AccessLock                   *sync.RWMutex
	Auth                         *auth.Store
	Phonebook                    Phonebook
	SecureCookies                bool
	// Logger receives request logs and recovered panics.
	Logger *slog.Logger
	// ActionTimeout bounds a kick or an invite, so a wedged AMI link cannot
	// hold an HTTP request open indefinitely. Default 10s.
	ActionTimeout time.Duration
	// PingInterval is the WebSocket keepalive period; a socket that fails to
	// answer a ping is dropped. Default 30s.
	PingInterval time.Duration
	// WriteTimeout bounds a single WebSocket write or ping. Default 10s.
	WriteTimeout time.Duration
	// AllowedOrigins lists extra Origin host patterns accepted on the
	// WebSocket handshake, in the syntax of websocket.AcceptOptions. The
	// request's own host is always allowed; this is for a browser served from
	// somewhere else, i.e. the Vite dev server. Empty means same-origin only.
	AllowedOrigins []string
	// Frontend overrides the embedded SPA handler. Tests set it; production
	// leaves it nil and gets the build baked into the binary.
	Frontend http.Handler
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	if out.ActionTimeout <= 0 {
		out.ActionTimeout = 10 * time.Second
	}
	if out.PingInterval <= 0 {
		out.PingInterval = 30 * time.Second
	}
	if out.WriteTimeout <= 0 {
		out.WriteTimeout = 10 * time.Second
	}
	return out
}

// Server routes HTTP and WebSocket traffic to the conference service. It owns
// the hub the snapshots fan out through.
type Server struct {
	closeOnce  sync.Once
	done       chan struct{}
	children   roomServers
	loginLimit loginLimiter
	loginSlots chan struct{}
	cfg        Config
	log        *slog.Logger
	svc        Conference
	hub        *hub.Hub
	mux        http.Handler
	unsub      func()
}

// New wires a server to svc and starts fanning snapshots out to the hub.
// Call Close to stop.
func New(svc Conference, cfg Config) (*Server, error) {
	resolved := cfg.withDefaults()
	if resolved.Auth == nil {
		return nil, errors.New("authentication store is required")
	}
	if resolved.Phonebook == nil {
		return nil, errors.New("phonebook store is required")
	}

	s := &Server{
		cfg:        resolved,
		done:       make(chan struct{}),
		children:   roomServers{servers: map[string]*Server{}},
		loginSlots: make(chan struct{}, 4),
		log:        resolved.Logger,
		svc:        svc,
		hub:        hub.New(hub.Config{Logger: resolved.Logger}),
	}

	frontend := resolved.Frontend
	if frontend == nil {
		h, err := assets.Handler()
		switch {
		case errors.Is(err, assets.ErrNotBuilt):
			// An API-only binary is a legitimate thing to build and run; only
			// the browser UI is missing, so say so instead of failing to start.
			resolved.Logger.Warn("web: no frontend embedded in this binary, serving the API only")
			frontend = notBuiltHandler()
		case err != nil:
			return nil, fmt.Errorf("web: frontend assets: %w", err)
		default:
			frontend = h
		}
	}
	s.mux = s.routes(frontend)

	// Subscribe delivers the current snapshot before it returns, so the hub
	// always has something to replay to a browser that connects during
	// startup, with no window in which an update could be missed.
	if svc != nil {
		s.unsub = svc.Subscribe(s.publish)
	}

	if resolved.Manager != nil {
		go s.pruneRooms()
	}
	return s, nil
}

// Close stops the fan-out and disconnects every WebSocket client. It does not
// shut down an http.Server built on top of this handler; that is the caller's
// job, and should happen first.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.children.mu.Lock()
		for _, child := range s.children.servers {
			child.Close()
		}
		s.children.mu.Unlock()
		if s.unsub != nil {
			s.unsub()
		}
		s.hub.Close()
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes(frontend http.Handler) http.Handler {
	mux := http.NewServeMux()
	s.handleMethod(mux, http.MethodPost, "/api/auth/login", s.handleLogin)
	s.handleMethod(mux, http.MethodGet, "/api/auth/me", s.handleMe)
	s.handleMethod(mux, http.MethodPost, "/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/api/contacts", s.handleContacts)
	mux.HandleFunc("/api/contacts/{id}", s.handleContact)
	mux.HandleFunc("/api/users", s.handleUsers)
	s.handleMethod(mux, http.MethodPatch, "/api/users/{id}", s.handleUpdateUser)

	if s.cfg.Manager != nil {
		s.handleMethod(mux, http.MethodGet, "/api/rooms", s.handleRooms)
		mux.HandleFunc("/api/admin/rooms", s.handleAdminRooms)
		mux.HandleFunc("/api/admin/rooms/{roomID}", s.handleAdminRooms)
		mux.HandleFunc("/api/admin/rooms/{roomID}/users", s.handleAdminRooms)
		s.handleMethod(mux, http.MethodGet, "/api/rooms/{roomID}/conference", s.handleRoomRequest)
		s.handleMethod(mux, http.MethodPost, "/api/rooms/{roomID}/participants", s.handleRoomRequest)
		s.handleMethod(mux, http.MethodDelete, "/api/rooms/{roomID}/participants/{uniqueid}", s.handleRoomRequest)
		s.handleMethod(mux, http.MethodPut, "/api/rooms/{roomID}/participants/{uniqueid}/mute", s.handleRoomRequest)
		s.handleMethod(mux, http.MethodDelete, "/api/rooms/{roomID}/calls/{id}", s.handleRoomRequest)
		s.handleMethod(mux, http.MethodPost, "/api/rooms/{roomID}/calls/{id}/retry", s.handleRoomRequest)
		s.handleMethod(mux, http.MethodGet, "/ws", s.handleRoomRequest)
	} else {
		s.handleMethod(mux, http.MethodGet, "/api/conference", s.handleGetConference)
		s.handleMethod(mux, http.MethodPost, "/api/conference/participants", s.handleAddParticipant)
		s.handleMethod(mux, http.MethodDelete, "/api/conference/participants/{uniqueid}", s.handleKickParticipant)
		s.handleMethod(mux, http.MethodPut, "/api/conference/participants/{uniqueid}/mute", s.handleMuteParticipant)
		s.handleMethod(mux, http.MethodGet, "/ws", s.handleWebSocket)
		s.handleMethod(mux, http.MethodDelete, "/api/conference/calls/{id}", s.handleCancelCall)
		s.handleMethod(mux, http.MethodPost, "/api/conference/calls/{id}/retry", s.handleRetryCall)

	}

	// Anything else under /api/ is a genuine 404 in JSON. Without this the
	// SPA catch-all below would answer a mistyped endpoint with an HTML page.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeError(r.Context(), w, http.StatusNotFound, "no such endpoint")
	})

	mux.Handle("/", frontend)

	// Recovery inside the access log, so a panicking request still produces
	// its log line with the 500 and the duration on it.
	return s.logRequests(s.recoverPanics(s.authMiddleware(mux)))
}

// handleMethod registers h for method on path, plus a method-less pattern
// answering 405.
//
// The 405 is registered explicitly because the SPA catch-all on "/" matches
// every request: ServeMux only synthesises a method-not-allowed response when
// nothing else matches, so without this a DELETE to a GET-only endpoint would
// quietly return the HTML shell with a 200.
func (s *Server) handleMethod(mux *http.ServeMux, method, path string, h http.HandlerFunc) {
	mux.HandleFunc(method+" "+path, h)

	// Go's ServeMux answers HEAD with the GET handler, so advertise it too.
	allow := method
	if method == http.MethodGet {
		allow = "GET, HEAD"
	}

	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		s.writeError(r.Context(), w, http.StatusMethodNotAllowed,
			fmt.Sprintf("method not allowed, try %s", allow))
	})
}

// publish marshals a snapshot once and hands it to the hub. It runs on the
// conference service's goroutine, so it must never block.
func (s *Server) publish(snap conference.Snapshot) {
	snap = s.roomSnapshot(snap)
	payload, err := json.Marshal(wsMessage{Type: "snapshot", Snapshot: snap})
	if err != nil {
		// Unreachable with the current types, but a silent stall of every
		// browser would be a miserable thing to debug.
		s.log.Error("web: cannot marshal a snapshot", "error", err)
		return
	}
	s.hub.Broadcast(payload)
}

// wsMessage is the envelope sent over the WebSocket. The snapshot is inlined
// so the socket and REST payloads share one shape apart from "type".
type wsMessage struct {
	Type string `json:"type"`
	conference.Snapshot
}

func (s *Server) handleGetConference(w http.ResponseWriter, r *http.Request) {
	writeJSON(r.Context(), s.log, w, http.StatusOK, s.roomSnapshot(s.svc.Snapshot()))
}

type addParticipantRequest struct {
	Number string `json:"number"`
}

type addParticipantResponse struct {
	ActionID string `json:"actionId"`
}

func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	// A cross-site HTML form can POST text/plain, multipart or urlencoded
	// bodies with no preflight, and a body like `{"number":"1900..."}=` parses
	// as JSON. Insisting on application/json forces a preflight, which the
	// browser refuses without CORS headers this server never sends.
	if !isJSONRequest(r) {
		s.writeError(r.Context(), w, http.StatusUnsupportedMediaType,
			"expected Content-Type: application/json")
		return
	}

	var req addParticipantRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.writeError(r.Context(), w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	defer cancel()

	actionID, err := s.svc.Invite(ctx, req.Number)
	if err != nil {
		s.writeServiceError(ctx, w, "invite", err)
		return
	}

	// 202: the call has been queued with Asterisk, but the callee has not
	// picked up yet. They appear in the roster if and when they do.
	writeJSON(ctx, s.log, w, http.StatusAccepted, addParticipantResponse{ActionID: actionID})
}

// isJSONRequest reports whether the request body is declared as JSON, ignoring
// any charset or other parameter after the media type.
func isJSONRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func (s *Server) handleKickParticipant(w http.ResponseWriter, r *http.Request) {
	uniqueID := r.PathValue("uniqueid")
	if uniqueID == "" {
		s.writeError(r.Context(), w, http.StatusBadRequest, "missing participant id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ActionTimeout)
	defer cancel()

	if err := s.svc.Kick(ctx, uniqueID); err != nil {
		s.writeServiceError(ctx, w, "kick", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Browsers do not apply CORS to a WebSocket handshake, so without an
		// origin check any page the operator happens to visit could open a
		// socket to this server and read the roster. Empty patterns mean
		// same-origin; WB_ALLOWED_ORIGINS widens it for the Vite dev proxy.
		OriginPatterns: s.cfg.AllowedOrigins,
	})
	if err != nil {
		s.log.Warn("web: websocket handshake failed", "error", err, "remote", r.RemoteAddr)
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Nothing is expected from the browser, but a reader has to be running:
	// it is what processes pongs and notices the client going away.
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	// The hub replays the latest snapshot to a new subscriber, so the browser
	// has state to render before anything else happens in the conference.
	cookie, _ := r.Cookie(sessionCookie)
	sessionCheck := time.NewTicker(time.Second)
	defer sessionCheck.Stop()
	validSession := func() bool {
		user, err := s.cfg.Auth.Authenticate(cookie.Value)
		if err != nil {
			_ = conn.Close(websocket.StatusPolicyViolation, "session ended")
			return false
		}
		if s.cfg.RoomAccess != nil && !s.cfg.RoomAccess(ctx, user) {
			_ = conn.Close(websocket.StatusPolicyViolation, "room access revoked")
			return false
		}
		return true
	}

	updates, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()

	ping := time.NewTicker(s.cfg.PingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-sessionCheck.C:
			if !validSession() {
				return
			}

		case payload, ok := <-updates:
			if !ok {
				// Evicted for falling behind, or the server is shutting down.
				_ = conn.Close(websocket.StatusTryAgainLater, "client is not keeping up")
				return
			}
			alive := func() bool {
				if s.cfg.AccessLock != nil {
					s.cfg.AccessLock.RLock()
					defer s.cfg.AccessLock.RUnlock()
				}
				if !validSession() {
					return false
				}
				if err := s.writeWS(ctx, conn, payload); err != nil {
					s.log.Debug("web: websocket write failed", "error", err)
					return false
				}
				return true
			}()
			if !alive {
				return
			}

		case <-ping.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, s.cfg.WriteTimeout)
			err := conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				s.log.Debug("web: websocket keepalive failed", "error", err, "remote", r.RemoteAddr)
				return
			}
		}
	}
}

func (s *Server) writeWS(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.WriteTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, payload)
}

// logRequests records one line per request with its status and duration.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if rec.status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		s.log.Log(r.Context(), level, "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(started),
			"remote", r.RemoteAddr,
		)
	})
}

// recoverPanics keeps one bad request from taking the process down with it.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// A panic after the hijack that starts a WebSocket cannot be
			// answered with a status code; ErrAbortHandler is the stdlib's
			// own "the connection is already gone" signal.
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			s.log.Error("web: recovered from a panic",
				"panic", rec,
				"method", r.Method,
				"path", r.URL.Path,
				"stack", string(debug.Stack()),
			)
			s.writeError(r.Context(), w, http.StatusInternalServerError, "internal server error")
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.written {
		r.status = status
		r.written = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

// Unwrap lets the WebSocket handshake reach the underlying ResponseWriter's
// Hijack method through the logging wrapper.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// errorResponse is the body of every failed API call.
type errorResponse struct {
	Error string `json:"error"`
}

// writeServiceError maps a domain error onto a status code. Anything
// unrecognised is a 500, and only the recognised cases get their message
// forwarded to the browser.
func (s *Server) writeServiceError(ctx context.Context, w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, conference.ErrCallNotFound):
		s.writeError(ctx, w, http.StatusNotFound, err.Error())
	case errors.Is(err, conference.ErrCallState), errors.Is(err, conference.ErrCallLimit):
		s.writeError(ctx, w, http.StatusConflict, err.Error())
	case errors.Is(err, conference.ErrParticipantNotFound):
		s.writeError(ctx, w, http.StatusNotFound, "participant is no longer in the conference")
	case errors.Is(err, conference.ErrInvalidNumber):
		s.writeError(ctx, w, http.StatusBadRequest, err.Error())
	case errors.Is(err, telephony.ErrUnavailable), errors.Is(err, ami.ErrNotConnected), errors.Is(err, ami.ErrDisconnected):
		s.writeError(ctx, w, http.StatusServiceUnavailable, "not connected to Asterisk")
	case errors.Is(err, context.DeadlineExceeded):
		s.writeError(ctx, w, http.StatusGatewayTimeout, "Asterisk did not answer in time")
	default:
		s.log.Error("web: request failed", "op", op, "error", err)
		s.writeError(ctx, w, http.StatusInternalServerError, "internal server error")
	}
}

func (s *Server) writeError(ctx context.Context, w http.ResponseWriter, status int, message string) {
	writeJSON(ctx, s.log, w, status, errorResponse{Error: message})
}

// writeJSON marshals before touching the ResponseWriter, so a marshalling
// failure does not leave a half-written body behind a 200.
func writeJSON(ctx context.Context, log *slog.Logger, w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		log.ErrorContext(ctx, "web: cannot marshal a response", "error", err)
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(payload); err != nil {
		log.DebugContext(ctx, "web: cannot write a response", "error", err)
	}
}

// notBuiltHandler stands in for a frontend that was never compiled into the
// binary.
func notBuiltHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w,
			"westbridge was built without a frontend; run `make build` to embed it",
			http.StatusServiceUnavailable)
	})
}

func (s *Server) roomSnapshot(snap conference.Snapshot) conference.Snapshot {
	if s.cfg.RoomID != "" {
		snap.RoomID = s.cfg.RoomID
		snap.RoomName = s.cfg.RoomName
		snap.Room = s.cfg.RoomNumber
	}
	return snap
}
