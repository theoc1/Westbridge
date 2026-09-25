package web_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/phonebook"
	"github.com/dmalkin/westbridge/internal/web"
)

func TestPersonalContactAPI(t *testing.T) {
	var store *auth.Store
	srv := newServer(t, newFakeConference(), func(cfg *web.Config) { store = cfg.Auth })
	if _, err := store.CreateUser("alice", "abc", "user"); err != nil {
		t.Fatal(err)
	}
	alice := loginCookie(t, srv, "alice", "abc")
	admin := loginCookie(t, srv, "tester", "test-password-123")
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		path := "/api/contacts"
		if method == "PUT" || method == "DELETE" {
			path += "/1"
		}
		if rec := authRequest(srv, method, path, `{}`, nil, ""); rec.Code != 401 {
			t.Fatal(method, rec.Code)
		}
	}
	rec := authRequest(srv, "POST", "/api/contacts", `{"name":"Friend","number":"1002"}`, alice, "")
	if rec.Code != 201 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var contact phonebook.Contact
	if err := json.Unmarshal(rec.Body.Bytes(), &contact); err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("/api/contacts/%d", contact.ID)
	for _, method := range []string{"PUT", "DELETE"} {
		rec = authRequest(srv, method, target, `{"name":"Other","number":"1003"}`, admin, "")
		if rec.Code != 404 {
			t.Fatal(method, rec.Code, rec.Body.String())
		}
	}
	if rec = authRequest(srv, "GET", "/api/contacts", "", admin, ""); rec.Code != 200 || rec.Body.String() != "[]" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"name":"Friend","number":"1002","user_id":1}`, `{"name":"","number":"1002"}`, `{"name":"Bad","number":"1002/evil"}`} {
		if rec = authRequest(srv, "POST", "/api/contacts", body, alice, ""); rec.Code != 400 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	if rec = authRequest(srv, "POST", "/api/contacts", `{"name":"Duplicate","number":"1002"}`, alice, ""); rec.Code != 409 {
		t.Fatal(rec.Code)
	}
	if rec = authRequest(srv, "PUT", target, `{"name":"Changed","number":"1003"}`, alice, "http://evil.example"); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	if rec = authRequest(srv, "PUT", target, `{"name":"Changed","number":"1003"}`, alice, ""); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec = authRequest(srv, "DELETE", target, `{}`, alice, ""); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	if rec = authRequest(srv, "GET", "/api/contacts", "", alice, ""); rec.Code != 200 || rec.Body.String() != "[]" {
		t.Fatal(rec.Code, rec.Body.String())
	}
}
