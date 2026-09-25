package web_test

import (
	"context"
	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/database"
	"github.com/dmalkin/westbridge/internal/phonebook"
	"github.com/dmalkin/westbridge/internal/settings"
	"github.com/dmalkin/westbridge/internal/web"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalLocaleAuthorizationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	accounts := auth.New(db)
	if _, err = accounts.Bootstrap("tester", "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err = accounts.CreateUser("alice", "abc", "user"); err != nil {
		t.Fatal(err)
	}
	srv, err := web.New(nil, web.Config{Auth: accounts, Phonebook: phonebook.New(db), Settings: settings.New(db)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	admin := loginCookie(t, srv, "tester", "abc")
	user := loginCookie(t, srv, "alice", "abc")
	if rec := authRequest(srv, "GET", "/api/locale", "", nil, ""); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "{\"locale\":\"en\"}" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"locale":"ru"}`, 200}, {`{"locale":"de"}`, 400}, {`{"locale":""}`, 400}} {
		if rec := authRequest(srv, "PUT", "/api/admin/locale", tc.body, admin, ""); rec.Code != tc.status {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	if rec := authRequest(srv, "PUT", "/api/admin/locale", `{"locale":"en"}`, user, ""); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	if rec := authRequest(srv, "PUT", "/api/admin/locale", `{"locale":"en"}`, nil, ""); rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	if rec := authRequest(srv, "PUT", "/api/admin/locale", `{"locale":"en"}`, admin, "https://other.example"); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if locale, err := settings.New(db).Locale(context.Background()); err != nil || locale != "ru" {
		t.Fatal(locale, err)
	}
}
