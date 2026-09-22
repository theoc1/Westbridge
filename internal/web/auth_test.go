package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/web"
)

func authRequest(srv *web.Server, method, target, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}
func loginCookie(t *testing.T, srv *web.Server, login, password string) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"login": login, "password": password})
	rec := authRequest(srv, "POST", "/api/auth/login", string(body), nil, "")
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	return cookies[0]
}
func TestAuthBoundaryAndAdmin(t *testing.T) {
	var store *auth.Store
	svc := newFakeConference()
	srv := newServer(t, svc, func(cfg *web.Config) { store = cfg.Auth; cfg.SecureCookies = true })
	for _, target := range []string{"/api/conference", "/api/auth/me", "/api/users", "/ws"} {
		rec := authRequest(srv, "GET", target, "", nil, "")
		if rec.Code != 401 {
			t.Fatalf("anonymous %s: %d", target, rec.Code)
		}
	}
	for _, method := range []string{"POST", "DELETE"} {
		target := "/api/conference/participants"
		if method == "DELETE" {
			target += "/123"
		}
		if rec := authRequest(srv, method, target, `{"number":"1001"}`, nil, ""); rec.Code != 401 {
			t.Fatal(rec.Code)
		}
	}
	admin := loginCookie(t, srv, "tester", "test-password-123")
	if !admin.HttpOnly || !admin.Secure || admin.SameSite != http.SameSiteStrictMode || admin.MaxAge != 43200 {
		t.Fatal("unsafe cookie", admin)
	}
	rec := authRequest(srv, "POST", "/api/users", `{"login":"alice","password":"alice-password-123","role":"user"}`, admin, "")
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	user := loginCookie(t, srv, "alice", "alice-password-123")
	if rec = authRequest(srv, "GET", "/api/conference", "", user, ""); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	for _, method := range []string{"GET", "POST", "PATCH"} {
		target := "/api/users"
		if method == "PATCH" {
			target += "/1"
		}
		if rec = authRequest(srv, method, target, `{}`, user, ""); rec.Code != 403 {
			t.Fatal(method, rec.Code)
		}
	}
	if rec = authRequest(srv, "POST", "/api/conference/participants", `{"number":"1001"}`, user, "http://evil.example"); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	if rec = authRequest(srv, "POST", "/api/auth/login", `{"login":"tester","password":"test-password-123"}`, nil, "http://evil.example"); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	if rec = authRequest(srv, "POST", "/api/conference/participants", `{"number":"1001"}`, user, ""); rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	if rec = authRequest(srv, "GET", "/api/users", "", admin, ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "password") {
		t.Fatal(rec.Body.String())
	}
	if rec = authRequest(srv, "POST", "/api/auth/logout", `{}`, user, ""); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	if rec = authRequest(srv, "GET", "/api/conference", "", user, ""); rec.Code != 401 {
		t.Fatal("logout did not revoke", rec.Code)
	}
	// A tampered token never authenticates.
	if _, err := store.Authenticate(admin.Value + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
}
func TestLoginFailuresAndRateLimit(t *testing.T) {
	srv := newServer(t, newFakeConference(), nil)
	for i := 0; i < 10; i++ {
		rec := authRequest(srv, "POST", "/api/auth/login", `{"login":"tester","password":"wrong"}`, nil, "")
		if rec.Code != 401 || len(rec.Result().Cookies()) != 0 {
			t.Fatal(rec.Code)
		}
	}
	if rec := authRequest(srv, "POST", "/api/auth/login", `{"login":"tester","password":"test-password-123"}`, nil, ""); rec.Code != 429 {
		t.Fatal(rec.Code)
	}
}
func TestWebSocketRevokedOnLogout(t *testing.T) {
	srv := newServer(t, newFakeConference(), nil)
	cookie := loginCookie(t, srv, "tester", "test-password-123")
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {cookie.String()}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	if _, _, err = conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := authRequest(srv, "POST", "/api/auth/logout", `{}`, cookie, ""); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("socket not revoked: %v", err)
	}
}

func TestAdminUpdatesRevokeAccess(t *testing.T) {
	srv := newServer(t, newFakeConference(), nil)
	admin := loginCookie(t, srv, "tester", "test-password-123")
	rec := authRequest(srv, "POST", "/api/users", `{"login":"alice","password":"alice-password-123","role":"user"}`, admin, "")
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var user auth.User
	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	cookie := loginCookie(t, srv, "alice", "alice-password-123")
	target := fmt.Sprintf("/api/users/%d", user.ID)
	rec = authRequest(srv, "PATCH", target, `{"password":"changed-password-123"}`, admin, "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec = authRequest(srv, "GET", "/api/auth/me", "", cookie, ""); rec.Code != 401 {
		t.Fatal("reset kept session", rec.Code)
	}
	cookie = loginCookie(t, srv, "alice", "changed-password-123")
	rec = authRequest(srv, "PATCH", target, `{"enabled":false}`, admin, "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec = authRequest(srv, "GET", "/api/auth/me", "", cookie, ""); rec.Code != 401 {
		t.Fatal("disabled session survived", rec.Code)
	}
	rec = authRequest(srv, "PATCH", "/api/users/1", `{"role":"user"}`, admin, "")
	if rec.Code != 409 {
		t.Fatal("last admin demoted", rec.Code)
	}
	rec = authRequest(srv, "GET", "/api/auth/me", "", admin, "")
	if rec.Code != 200 {
		t.Fatal("failed edit revoked admin", rec.Code)
	}
}
