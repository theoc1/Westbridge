package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmalkin/westbridge/internal/auth"
)

const sessionCookie = "westbridge_session"

type userKey struct{}

type loginWindow struct {
	count int
	until time.Time
}
type loginLimiter struct {
	mu      sync.Mutex
	windows map[string]loginWindow
}

func (l *loginLimiter) allow(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = map[string]loginWindow{}
	}
	now := time.Now()
	for key, w := range l.windows {
		if !now.Before(w.until) {
			delete(l.windows, key)
		}
	}
	w := l.windows[host]
	if w.count >= 10 {
		return false
	}
	if w.count == 0 {
		if len(l.windows) >= 4096 {
			return false
		}
		w.until = now.Add(time.Minute)
	}
	w.count++
	l.windows[host] = w
	return true
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protected := strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws"
		if !protected {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		// JSON-only mutations plus Origin validation prevent cross-site requests,
		// including same-site requests from a different port or sibling application.
		if r.Method != "GET" && r.Method != "HEAD" {
			if !s.validOrigin(r) {
				s.writeError(r.Context(), w, 403, "origin not allowed")
				return
			}
			if !isJSONRequest(r) {
				s.writeError(r.Context(), w, 415, "expected Content-Type: application/json")
				return
			}
		}
		if r.URL.Path == "/api/auth/login" || (r.URL.Path == "/api/locale" && (r.Method == "GET" || r.Method == "HEAD")) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			s.writeError(r.Context(), w, 401, "please sign in")
			return
		}
		user, err := s.cfg.Auth.Authenticate(cookie.Value)
		if err != nil {
			if errors.Is(err, auth.ErrSession) {
				s.writeError(r.Context(), w, 401, err.Error())
			} else {
				s.writeError(r.Context(), w, 503, "authentication is temporarily unavailable")
			}
			return
		}
		if (strings.HasPrefix(r.URL.Path, "/api/users") || strings.HasPrefix(r.URL.Path, "/api/admin/")) && user.Role != "admin" {
			s.writeError(r.Context(), w, 403, "administrator access required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}
func (s *Server) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, allowed := range s.cfg.AllowedOrigins {
		if ok, _ := path.Match(allowed, u.Host); ok {
			return true
		}
	}
	return false
}
func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, dest any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		s.writeError(r.Context(), w, 400, "invalid request body")
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		s.writeError(r.Context(), w, 400, "invalid request body")
		return false
	}
	return true
}
func (s *Server) setSession(w http.ResponseWriter, token string, maxAge int) {
	// Secure defaults to true in production; local HTTP requires an explicit opt-out.
	//nolint:gosec // WB_COOKIE_SECURE=false supports the documented loopback-only development setup.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.allow(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		s.writeError(r.Context(), w, 429, "too many login attempts; try again in a minute")
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		s.writeError(r.Context(), w, 429, "login is busy; try again shortly")
		return
	}
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}
	user, token, err := s.cfg.Auth.Login(req.Login, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrCredentials) {
			s.writeError(r.Context(), w, 401, err.Error())
		} else {
			s.log.Error("login failed", "error", err)
			s.writeError(r.Context(), w, 503, "authentication is temporarily unavailable")
		}
		return
	}
	if old, err := r.Cookie(sessionCookie); err == nil {
		_ = s.cfg.Auth.Logout(old.Value)
	}
	s.setSession(w, token, int(auth.SessionTTL.Seconds()))
	writeJSON(r.Context(), s.log, w, 200, user)
}
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(r.Context(), s.log, w, 200, r.Context().Value(userKey{}))
}
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookie)
	if err := s.cfg.Auth.Logout(cookie.Value); err != nil {
		s.writeError(r.Context(), w, 500, "could not sign out; try again")
		return
	}
	s.setSession(w, "", -1)
	w.WriteHeader(204)
}
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET", "HEAD":
		users, err := s.cfg.Auth.Users()
		if err != nil {
			s.writeAuthError(w, r, err)
			return
		}
		writeJSON(r.Context(), s.log, w, 200, users)
	case "POST":
		var req struct {
			Login    string `json:"login"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if !s.decodeBody(w, r, &req) {
			return
		}
		u, err := s.cfg.Auth.CreateUser(req.Login, req.Password, req.Role)
		if err != nil {
			s.writeAuthError(w, r, err)
			return
		}
		writeJSON(r.Context(), s.log, w, 201, u)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		s.writeError(r.Context(), w, 405, "method not allowed")
	}
}
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(r.Context(), w, 400, "invalid user id")
		return
	}
	var update auth.Update
	if !s.decodeBody(w, r, &update) {
		return
	}
	if update.Role == nil && update.Enabled == nil && update.Password == nil {
		s.writeError(r.Context(), w, 400, "no changes supplied")
		return
	}
	u, err := s.cfg.Auth.UpdateUser(id, update)
	if err != nil {
		s.writeAuthError(w, r, err)
		return
	}
	writeJSON(r.Context(), s.log, w, 200, u)
}
func (s *Server) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalid):
		s.writeError(r.Context(), w, 400, err.Error())
	case errors.Is(err, auth.ErrConflict), errors.Is(err, auth.ErrLastAdmin):
		s.writeError(r.Context(), w, 409, err.Error())
	case errors.Is(err, auth.ErrNotFound):
		s.writeError(r.Context(), w, 404, err.Error())
	default:
		s.log.Error("user management failed", "error", err)
		s.writeError(r.Context(), w, 500, "could not save user")
	}
}
