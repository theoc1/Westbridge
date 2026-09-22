package web_test

import (
	"github.com/dmalkin/westbridge/internal/conference"
	"testing"
)

func TestMuteEndpoint(t *testing.T) {
	svc := newFakeConference()
	srv := newServer(t, svc, nil)
	path := "/api/conference/participants/one/mute"
	if rec := authRequest(srv, "PUT", path, `{"muted":true}`, nil, ""); rec.Code != 401 {
		t.Fatal(rec.Code)
	}
	cookie := loginCookie(t, srv, "tester", "test-password-123")
	if rec := authRequest(srv, "PUT", path, `{"muted":true}`, cookie, "http://evil.example"); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	for _, body := range []string{`{}`, `{"muted":null}`, `{"muted":"true"}`, `{"muted":true,"channel":"all"}`} {
		if rec := authRequest(srv, "PUT", path, body, cookie, ""); rec.Code != 400 {
			t.Fatal(body, rec.Code)
		}
	}
	for _, muted := range []bool{true, false} {
		body := `{"muted":false}`
		if muted {
			body = `{"muted":true}`
		}
		if rec := authRequest(srv, "PUT", path, body, cookie, ""); rec.Code != 204 {
			t.Fatal(rec.Code)
		}
		if svc.muteID != "one" || svc.muteValue != muted {
			t.Fatal("incorrect target or state")
		}
	}
	svc.kickErr = conference.ErrParticipantNotFound
	if rec := authRequest(srv, "PUT", path, `{"muted":true}`, cookie, ""); rec.Code != 404 {
		t.Fatal(rec.Code)
	}
}
