package web_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dmalkin/westbridge/internal/ami"
	"github.com/dmalkin/westbridge/internal/auth"
	"github.com/dmalkin/westbridge/internal/conference"
	"github.com/dmalkin/westbridge/internal/database"
	"github.com/dmalkin/westbridge/internal/phonebook"
	"github.com/dmalkin/westbridge/internal/rooms"
	"github.com/dmalkin/westbridge/internal/web"
)

type roomAMI struct{}

func (roomAMI) Connected() bool             { return true }
func (roomAMI) EventSequence() uint64       { return 0 }
func (roomAMI) Events() <-chan *ami.Message { return nil }
func (roomAMI) Action(context.Context, *ami.Message) (*ami.Message, error) {
	return &ami.Message{}, nil
}
func (roomAMI) ActionList(context.Context, *ami.Message, string) ([]*ami.Message, error) {
	return nil, nil
}

type roomProvisioner struct{}

func (roomProvisioner) Ensure(context.Context, string, string) error   { return nil }
func (roomProvisioner) Disable(context.Context, string, string) error  { return nil }
func (roomProvisioner) Occupied(context.Context, string) (bool, error) { return false, nil }

func TestRoomAuthorizationAndSocketRevocation(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	accounts := auth.New(db)
	_, err = accounts.Bootstrap("tester", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := accounts.CreateUser("alice", "abc", "user")
	if err != nil {
		t.Fatal(err)
	}
	store := rooms.New(db, 7000, 7999)
	ctx := context.Background()
	a, err := store.Create(ctx, "A", "7000")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Create(ctx, "B", "7001")
	if err != nil {
		t.Fatal(err)
	}
	for _, room := range []rooms.Room{a, b} {
		if err = store.Result(ctx, room, "ready", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.SetGrants(ctx, a.ID, []int64{alice.ID}); err != nil {
		t.Fatal(err)
	}
	manager := conference.NewManager(roomAMI{}, store, roomProvisioner{}, conference.Config{OriginateContext: "out"})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { manager.Run(runCtx); close(done) }()
	defer func() { stop(); <-done }()
	for i := 0; i < 100; i++ {
		if manager.Ready(rooms.Room{ID: a.ID, State: "ready"}) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv, err := web.New(nil, web.Config{Auth: accounts, Phonebook: phonebook.New(db), Rooms: store, Manager: manager, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	user := loginCookie(t, srv, "alice", "abc")
	admin := loginCookie(t, srv, "tester", "test-password-123")
	for _, path := range []string{"/api/admin/rooms", "/api/admin/rooms/" + a.ID + "/users"} {
		if rec := authRequest(srv, "POST", path, `{}`, user, ""); rec.Code != 403 {
			t.Fatal(path, rec.Code)
		}
	}
	if rec := authRequest(srv, "GET", "/api/rooms/"+b.ID+"/conference", "", user, ""); rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	if rec := authRequest(srv, "GET", "/api/conference", "", admin, ""); rec.Code != 404 {
		t.Fatal("unscoped route remains", rec.Code)
	}
	rec := authRequest(srv, "GET", "/api/rooms", "", user, "")
	var visible []rooms.Room
	if err = json.Unmarshal(rec.Body.Bytes(), &visible); err != nil || len(visible) != 1 || visible[0].ID != a.ID {
		t.Fatal(rec.Body.String(), err)
	}
	rec = authRequest(srv, "POST", "/api/rooms/"+a.ID+"/participants", `{"number":"1002"}`, user, "")
	var call struct {
		ID string `json:"actionId"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &call); err != nil || rec.Code != 202 {
		t.Fatal(rec.Code, rec.Body.String(), err)
	}
	rec = authRequest(srv, "DELETE", "/api/rooms/"+b.ID+"/calls/"+call.ID, `{}`, admin, "")
	if rec.Code != 404 {
		t.Fatal("cross-room cancellation", rec.Code)
	}
	rec = authRequest(srv, "DELETE", "/api/admin/rooms/"+a.ID, `{}`, admin, "")
	if rec.Code != 409 {
		t.Fatal("busy room deletion", rec.Code, rec.Body.String())
	}
	httpServer := httptest.NewServer(srv)
	defer httpServer.Close()
	socketCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(socketCtx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws?roomId="+a.ID, &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {user.String()}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = socket.CloseNow() }()
	_, payload, err := socket.Read(socketCtx)
	if err != nil {
		t.Fatal(err)
	}
	var snap conference.Snapshot
	if err = json.Unmarshal(payload, &snap); err != nil || snap.RoomID != a.ID || snap.Room != "7000" {
		t.Fatal(string(payload), err)
	}
	rec = authRequest(srv, "PUT", "/api/admin/rooms/"+a.ID+"/users", `{"userIds":[]}`, admin, "")
	if rec.Code != 204 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	_, _, err = socket.Read(socketCtx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatal("socket survived revoke", err)
	}
	rec = authRequest(srv, "POST", "/api/rooms/"+a.ID+"/participants", `{"number":"1002"}`, user, "")
	if rec.Code != 404 {
		t.Fatal("revoked command accepted", rec.Code)
	}
	rec = authRequest(srv, "POST", "/api/admin/rooms", `{"name":"C","number":"7002"}`, admin, "http://evil.example")
	if rec.Code != 403 {
		t.Fatal("origin check missing", rec.Code)
	}
}

func (roomProvisioner) Entries(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
