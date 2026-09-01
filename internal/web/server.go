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
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/dmalkin/westbridge/internal/ami"
	"github.com/dmalkin/westbridge/internal/conference"
	"github.com/dmalkin/westbridge/internal/hub"
	"github.com/dmalkin/westbridge/internal/web/assets"
)

// maxRequestBody caps a request body. The only body the API accepts is a
// object with a phone number in it, so anything larger is a mistake or an
// attack.
const maxRequestBody = 4 << 10

// Conference is the slice of *conference.Service the HTTP layer needs.
type Conference interface {
	Room() string
	Snapshot() conference.Snapshot
	Subscribe(fn func(conference.Snapshot)) func()
	Kick(ctx context.Context, uniqueID string) error
	Invite(ctx context.Context, number string) (string, error)
}

// Config tunes the server. Every field has a usable default.
type Config struct {
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
	cfg   Config
	log   *slog.Logger
	svc   Conference
	hub   *hub.Hub
	mux   http.Handler
	unsub func()
}

// New wires a server to svc and starts fanning snapshots out to the hub.
// Call Close to stop.
func New(svc Conference, cfg Config) (*Server, error) {
	resolved := cfg.withDefaults()

	s := &Server{
		cfg: resolved,
		log: resolved.Logger,
		svc: svc,
		hub: hub.New(hub.Config{Logger: resolved.Logger}),
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

	// Publish before subscribing so a browser connecting during startup is
	// never served an empty hub, and so the WebSocket handler needs no
	// special case for "no snapshot yet".
	s.publish(svc.Snapshot())
	s.unsub = svc.Subscribe(s.publish)

	return s, nil
}

// Close stops the fan-out and disconnects every WebSocket client. It does not
// shut down an http.Server built on top of this handler; that is the caller's
// job, and should happen first.
func (s *Server) Close() {
	if s.unsub != nil {
		s.unsub()
	}
	s.hub.Close()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes(frontend http.Handler) http.Handler {
	mux := http.NewServeMux()

	s.handleMethods(mux, "/api/conference", map[string]http.HandlerFunc{
		http.MethodGet: s.handleGetConference,
	})
	s.handleMethods(mux, "/api/conference/participants", map[string]http.HandlerFunc{
		http.MethodPost: s.handleAddParticipant,
	})
	s.handleMethods(mux, "/api/conference/participants/{uniqueid}", map[string]http.HandlerFunc{
		http.MethodDelete: s.handleKickParticipant,
	})
	s.handleMethods(mux, "/ws", map[string]http.HandlerFunc{
		http.MethodGet: s.handleWebSocket,
	})

	// Anything else under /api/ is a genuine 404 in JSON. Without this the
	// SPA catch-all below would answer a mistyped endpoint with an HTML page.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeError(r.Context(), w, http.StatusNotFound, "no such endpoint")
	})

	mux.Handle("/", frontend)

	return s.recoverPanics(s.logRequests(mux))
}

// handleMethods registers one handler per method on path, plus a method-less
// pattern answering 405.
//
// The 405 is registered explicitly because the SPA catch-all on "/" matches
// every request: ServeMux only synthesises a method-not-allowed response when
// nothing else matches, so without this a DELETE to a GET-only endpoint would
// quietly return the HTML shell with a 200.
func (s *Server) handleMethods(mux *http.ServeMux, path string, handlers map[string]http.HandlerFunc) {
	allowed := make([]string, 0, len(handlers)+1)
	for method, h := range handlers {
		mux.HandleFunc(method+" "+path, h)
		allowed = append(allowed, method)
	}
	// Go's ServeMux answers HEAD with the GET handler, so advertise it too.
	if _, ok := handlers[http.MethodGet]; ok {
		allowed = append(allowed, http.MethodHead)
	}
	sort.Strings(allowed)
	allow := strings.Join(allowed, ", ")

	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		s.writeError(r.Context(), w, http.StatusMethodNotAllowed,
			fmt.Sprintf("method not allowed, try %s", allow))
	})
}

// publish marshals a snapshot once and hands it to the hub. It runs on the
// conference service's goroutine, so it must never block.
func (s *Server) publish(snap conference.Snapshot) {
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
	writeJSON(r.Context(), s.log, w, http.StatusOK, s.svc.Snapshot())
}

type addParticipantRequest struct {
	Number string `json:"number"`
}

type addParticipantResponse struct {
	ActionID string `json:"actionId"`
}

func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
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
		// The app has no authentication and is meant for a trusted network,
		// so an origin check would provide no protection while breaking the
		// Vite dev proxy. See README.
		InsecureSkipVerify: true,
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
	updates, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()

	ping := time.NewTicker(s.cfg.PingInterval)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case payload, ok := <-updates:
			if !ok {
				// Evicted for falling behind, or the server is shutting down.
				_ = conn.Close(websocket.StatusTryAgainLater, "client is not keeping up")
				return
			}
			if err := s.writeWS(ctx, conn, payload); err != nil {
				s.log.Debug("web: websocket write failed", "error", err, "remote", r.RemoteAddr)
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
	case errors.Is(err, conference.ErrParticipantNotFound):
		s.writeError(ctx, w, http.StatusNotFound, "participant is no longer in the conference")
	case errors.Is(err, conference.ErrInvalidNumber):
		s.writeError(ctx, w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ami.ErrNotConnected), errors.Is(err, ami.ErrDisconnected):
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
