package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dmalkin/westbridge/internal/ami"
	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/conference"
	"github.com/dmalkin/westbridge/internal/web"
)

// fakeConference stands in for the domain service: the HTTP layer only needs
// to be shown that it calls the right method with the right arguments and maps
// the result onto the right status code.
type fakeConference struct {
	mu        sync.Mutex
	snap      conference.Snapshot
	subs      map[int]func(conference.Snapshot)
	nextSub   int
	kicked    []string
	kickErr   error
	invited   []string
	inviteErr error
}

func newFakeConference(participants ...conference.Participant) *fakeConference {
	if participants == nil {
		participants = []conference.Participant{}
	}
	return &fakeConference{
		snap: conference.Snapshot{
			Room:              "1000",
			AsteriskConnected: true,
			Participants:      participants,
		},
		subs: make(map[int]func(conference.Snapshot)),
	}
}

func (f *fakeConference) Snapshot() conference.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

// Subscribe mirrors conference.Service: the current snapshot is delivered
// before it returns.
func (f *fakeConference) Subscribe(fn func(conference.Snapshot)) func() {
	f.mu.Lock()
	id := f.nextSub
	f.nextSub++
	f.subs[id] = fn
	snap := f.snap
	f.mu.Unlock()

	fn(snap)

	return func() {
		f.mu.Lock()
		delete(f.subs, id)
		f.mu.Unlock()
	}
}

// setSnapshot replaces the state and notifies subscribers, the way the real
// service does when an AMI event lands.
func (f *fakeConference) setSnapshot(snap conference.Snapshot) {
	f.mu.Lock()
	f.snap = snap
	subs := make([]func(conference.Snapshot), 0, len(f.subs))
	for _, fn := range f.subs {
		subs = append(subs, fn)
	}
	f.mu.Unlock()

	for _, fn := range subs {
		fn(snap)
	}
}

func (f *fakeConference) Kick(_ context.Context, uniqueID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kicked = append(f.kicked, uniqueID)
	return f.kickErr
}

func (f *fakeConference) Invite(_ context.Context, number string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invited = append(f.invited, number)
	if f.inviteErr != nil {
		return "", f.inviteErr
	}
	return "wb-invite-1234", nil
}

func (f *fakeConference) calls() (kicked, invited []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.kicked...), append([]string(nil), f.invited...)
}

var serverTokens sync.Map

func testCookie(srv *web.Server) *http.Cookie {
	token, _ := serverTokens.Load(srv)
	return &http.Cookie{Name: "westbridge_session", Value: token.(string)}
}

func newServer(t *testing.T, svc web.Conference, tune func(*web.Config)) *web.Server {
	t.Helper()

	store, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err = store.Bootstrap("tester", "test-password-123"); err != nil {
		t.Fatal(err)
	}
	_, token, err := store.Login("tester", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	cfg := web.Config{
		Auth:          store,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		ActionTimeout: 2 * time.Second,
		PingInterval:  50 * time.Millisecond,
		WriteTimeout:  time.Second,
		Frontend: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/boom" {
				panic("frontend exploded")
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<!doctype html><title>spa</title>%s", r.URL.Path)
		}),
	}
	if tune != nil {
		tune(&cfg)
	}

	srv, err := web.New(svc, cfg)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	serverTokens.Store(srv, token)
	t.Cleanup(func() { serverTokens.Delete(srv) })
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *web.Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if method != http.MethodGet && method != http.MethodHead {
		// What the frontend sends, and what the server now insists on.
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	req.AddCookie(testCookie(srv))
	srv.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestGetConferenceReturnsTheCurrentSnapshot(t *testing.T) {
	joined := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	svc := newFakeConference(conference.Participant{
		UniqueID:     "1756-1",
		Channel:      "PJSIP/1001-0000000a",
		CallerIDNum:  "1001",
		CallerIDName: "Alice",
		JoinedAt:     joined,
	})
	srv := newServer(t, svc, nil)

	rec := do(t, srv, http.MethodGet, "/api/conference", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}

	got := decodeJSON[conference.Snapshot](t, rec)
	if got.Room != "1000" || !got.AsteriskConnected {
		t.Errorf("snapshot = %+v, want room 1000 and a connected Asterisk", got)
	}
	if len(got.Participants) != 1 || got.Participants[0].UniqueID != "1756-1" {
		t.Fatalf("participants = %+v", got.Participants)
	}
	if !got.Participants[0].JoinedAt.Equal(joined) {
		t.Errorf("joinedAt = %s, want %s", got.Participants[0].JoinedAt, joined)
	}
}

func TestGetConferenceEncodesAnEmptyRosterAsAnArray(t *testing.T) {
	srv := newServer(t, newFakeConference(), nil)

	rec := do(t, srv, http.MethodGet, "/api/conference", "")
	// A null here would force the frontend to guard every .map() call.
	if !strings.Contains(rec.Body.String(), `"participants":[]`) {
		t.Errorf("body = %s, want an empty participants array", rec.Body)
	}
}

func TestAddParticipantQueuesAnInvite(t *testing.T) {
	svc := newFakeConference()
	srv := newServer(t, svc, nil)

	rec := do(t, srv, http.MethodPost, "/api/conference/participants", `{"number":"1002"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}

	got := decodeJSON[struct {
		ActionID string `json:"actionId"`
	}](t, rec)
	if got.ActionID != "wb-invite-1234" {
		t.Errorf("actionId = %q", got.ActionID)
	}

	if _, invited := svc.calls(); len(invited) != 1 || invited[0] != "1002" {
		t.Errorf("Invite called with %v, want [1002]", invited)
	}
}

func TestAddParticipantRejectsBadInput(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		inviteErr error
		want      int
	}{
		{name: "malformed json", body: `{"number":`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"number":"1002","admin":true}`, want: http.StatusBadRequest},
		{name: "empty body", body: "", want: http.StatusBadRequest},
		{
			name:      "invalid number",
			body:      `{"number":"1002; DROP"}`,
			inviteErr: fmt.Errorf("%w: %q", conference.ErrInvalidNumber, "1002; DROP"),
			want:      http.StatusBadRequest,
		},
		{
			name:      "asterisk down",
			body:      `{"number":"1002"}`,
			inviteErr: fmt.Errorf("invite 1002: %w", ami.ErrNotConnected),
			want:      http.StatusServiceUnavailable,
		},
		{
			name:      "unexpected failure",
			body:      `{"number":"1002"}`,
			inviteErr: errors.New("boom"),
			want:      http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeConference()
			svc.inviteErr = tc.inviteErr
			srv := newServer(t, svc, nil)

			rec := do(t, srv, http.MethodPost, "/api/conference/participants", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}

			body := decodeJSON[map[string]string](t, rec)
			if body["error"] == "" {
				t.Errorf("body = %s, want an error message", rec.Body)
			}
		})
	}
}

func TestAddParticipantDoesNotLeakInternalErrorText(t *testing.T) {
	svc := newFakeConference()
	svc.inviteErr = errors.New("dial tcp 10.0.0.1:5038: connection refused")
	srv := newServer(t, svc, nil)

	rec := do(t, srv, http.MethodPost, "/api/conference/participants", `{"number":"1002"}`)
	if strings.Contains(rec.Body.String(), "10.0.0.1") {
		t.Errorf("body = %s, want the internal error withheld", rec.Body)
	}
}

func TestKickParticipant(t *testing.T) {
	svc := newFakeConference()
	srv := newServer(t, svc, nil)

	rec := do(t, srv, http.MethodDelete, "/api/conference/participants/1756-1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %s, want empty", rec.Body)
	}

	if kicked, _ := svc.calls(); len(kicked) != 1 || kicked[0] != "1756-1" {
		t.Errorf("Kick called with %v, want [1756-1]", kicked)
	}
}

func TestKickParticipantMapsErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "unknown participant",
			err:  fmt.Errorf("%w: %q", conference.ErrParticipantNotFound, "nope"),
			want: http.StatusNotFound,
		},
		{
			name: "connection lost mid-action",
			err:  fmt.Errorf("kick PJSIP/1001-1: %w", ami.ErrDisconnected),
			want: http.StatusServiceUnavailable,
		},
		{
			name: "asterisk too slow",
			err:  fmt.Errorf("kick PJSIP/1001-1: %w", context.DeadlineExceeded),
			want: http.StatusGatewayTimeout,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeConference()
			svc.kickErr = tc.err
			srv := newServer(t, svc, nil)

			rec := do(t, srv, http.MethodDelete, "/api/conference/participants/1756-1", "")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestUnsupportedMethodsAreRejected(t *testing.T) {
	srv := newServer(t, newFakeConference(), nil)

	for _, tc := range []struct{ method, target string }{
		{http.MethodDelete, "/api/conference"},
		{http.MethodPost, "/api/conference"},
		{http.MethodGet, "/api/conference/participants"},
		{http.MethodPut, "/api/conference/participants/1756-1"},
	} {
		rec := do(t, srv, tc.method, tc.target, "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status = %d, want 405", tc.method, tc.target, rec.Code)
		}
	}
}

func TestUnknownPathsFallBackToTheFrontend(t *testing.T) {
	srv := newServer(t, newFakeConference(), nil)

	rec := do(t, srv, http.MethodGet, "/some/deep/link", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Errorf("body = %s, want the SPA shell", rec.Body)
	}
}

func TestPanicsAreRecovered(t *testing.T) {
	srv := newServer(t, newFakeConference(), nil)

	rec := do(t, srv, http.MethodGet, "/boom", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := decodeJSON[map[string]string](t, rec); body["error"] == "" {
		t.Errorf("body = %s, want a JSON error", rec.Body)
	}
}

// wsSnapshot mirrors the socket envelope: a snapshot plus a discriminator.
type wsSnapshot struct {
	Type string `json:"type"`
	conference.Snapshot
}

func readSnapshot(ctx context.Context, t *testing.T, conn *websocket.Conn) wsSnapshot {
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("reading from the websocket: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("message type = %v, want text", typ)
	}

	var msg wsSnapshot
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decoding %q: %v", data, err)
	}
	if msg.Type != "snapshot" {
		t.Fatalf("type = %q, want snapshot", msg.Type)
	}
	return msg
}

func TestWebSocketSendsTheCurrentSnapshotThenStreamsUpdates(t *testing.T) {
	svc := newFakeConference(conference.Participant{
		UniqueID: "1756-1", Channel: "PJSIP/1001-0000000a", CallerIDNum: "1001",
	})
	srv := newServer(t, svc, nil)

	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {testCookie(srv).String()}}})
	if err != nil {
		t.Fatalf("dialing /ws: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	first := readSnapshot(ctx, t, conn)
	if len(first.Participants) != 1 || first.Participants[0].UniqueID != "1756-1" {
		t.Fatalf("initial snapshot = %+v, want the current roster", first.Participants)
	}

	// A change in the conference reaches the socket without the browser
	// asking for anything.
	svc.setSnapshot(conference.Snapshot{
		Room:              "1000",
		AsteriskConnected: false,
		Participants:      []conference.Participant{},
	})

	second := readSnapshot(ctx, t, conn)
	if second.AsteriskConnected {
		t.Errorf("asteriskConnected = true, want the disconnected state to propagate")
	}
	if len(second.Participants) != 0 {
		t.Errorf("participants = %+v, want empty", second.Participants)
	}
}

func TestWebSocketClientsAreDisconnectedOnClose(t *testing.T) {
	svc := newFakeConference()
	srv := newServer(t, svc, nil)

	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {testCookie(srv).String()}}})
	if err != nil {
		t.Fatalf("dialing /ws: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readSnapshot(ctx, t, conn)

	srv.Close()

	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("read succeeded after Close, want the socket to be shut")
	}
}

// A cross-site form can POST urlencoded, multipart or text/plain with no
// preflight, and `{"number":"1900..."}=` is a valid body for all three while
// still parsing as JSON. Requiring application/json forces a preflight the
// browser will not get past, since this server sends no CORS headers.
func TestAddParticipantRequiresJSONContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        int
	}{
		{"no content type", "", http.StatusUnsupportedMediaType},
		{"form encoded", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"multipart", "multipart/form-data; boundary=x", http.StatusUnsupportedMediaType},
		{"text", "text/plain;charset=UTF-8", http.StatusUnsupportedMediaType},
		{"json", "application/json", http.StatusAccepted},
		{"json with charset", "application/json; charset=utf-8", http.StatusAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newFakeConference()
			srv := newServer(t, svc, nil)

			req := httptest.NewRequest(http.MethodPost, "/api/conference/participants",
				strings.NewReader(`{"number":"1002"}`))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()
			req.AddCookie(testCookie(srv))
			srv.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}

			_, invited := svc.calls()
			if tt.want == http.StatusAccepted {
				if len(invited) != 1 {
					t.Errorf("invited = %v, want the call to go through", invited)
				}
				return
			}
			if len(invited) != 0 {
				t.Errorf("invited = %v, want no call placed", invited)
			}
			if body := decodeJSON[map[string]string](t, rec); body["error"] == "" {
				t.Error("error body is empty, want an explanation")
			}
		})
	}
}

// Browsers do not apply CORS to a WebSocket handshake, so the origin check is
// the only thing keeping a page the operator happens to visit from opening a
// socket and reading the roster.
func TestWebSocketChecksOrigin(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		origin  func(host string) string
		wantOK  bool
	}{
		{"no origin header", nil, func(string) string { return "" }, true},
		{"same origin", nil, func(host string) string { return "http://" + host }, true},
		{"foreign origin", nil, func(string) string { return "http://evil.example" }, false},
		{
			"foreign origin not on the allow list",
			[]string{"localhost:5173"},
			func(string) string { return "http://evil.example" },
			false,
		},
		{
			"allow-listed origin, i.e. the Vite dev server",
			[]string{"localhost:5173"},
			func(string) string { return "http://localhost:5173" },
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed := tt.allowed
			srv := newServer(t, newFakeConference(), func(cfg *web.Config) {
				cfg.AllowedOrigins = allowed
			})

			httpSrv := httptest.NewServer(srv)
			defer httpSrv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			host := strings.TrimPrefix(httpSrv.URL, "http://")
			opts := &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {testCookie(srv).String()}}}
			if origin := tt.origin(host); origin != "" {
				opts.HTTPHeader.Set("Origin", origin)
			}

			conn, resp, err := websocket.Dial(ctx, "ws://"+host+"/ws", opts)
			if conn != nil {
				defer func() { _ = conn.CloseNow() }()
			}

			if !tt.wantOK {
				if err == nil {
					t.Fatal("handshake succeeded, want it rejected")
				}
				if resp != nil && resp.StatusCode != http.StatusForbidden {
					t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
				}
				return
			}
			if err != nil {
				t.Fatalf("dialing /ws: %v", err)
			}
			readSnapshot(ctx, t, conn)
		})
	}
}

func (f *fakeConference) CancelCall(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kicked = append(f.kicked, id)
	return f.kickErr
}
func (f *fakeConference) RetryCall(ctx context.Context, id string) (string, error) {
	return f.Invite(ctx, id)
}
