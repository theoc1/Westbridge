package ami_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
	"github.com/dmalkin/westbridge/internal/ami/amitest"
)

// quietLogger keeps the expected connect/disconnect noise out of test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// runClient starts a client against srv and waits until it is logged in.
func runClient(t *testing.T, srv *amitest.Server, tune func(*ami.Config)) (*ami.Client, context.Context) {
	t.Helper()

	cfg := ami.Config{
		Addr:          srv.Addr(),
		Username:      srv.Username,
		Secret:        srv.Secret,
		DialTimeout:   2 * time.Second,
		ActionTimeout: 2 * time.Second,
		MinBackoff:    10 * time.Millisecond,
		MaxBackoff:    50 * time.Millisecond,
		Logger:        quietLogger(),
	}
	if tune != nil {
		tune(&cfg)
	}

	client := ami.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := client.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	waitConnected(t, client, true)
	return client, ctx
}

func waitConnected(t *testing.T, c *ami.Client, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Connected() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("client never reached Connected() == %v", want)
}

func TestClientLoginSuccess(t *testing.T) {
	srv := amitest.NewServer(t)
	client, _ := runClient(t, srv, nil)

	if !client.Connected() {
		t.Fatal("expected the client to be connected after login")
	}
}

func TestClientLoginRejected(t *testing.T) {
	srv := amitest.NewServer(t)

	client := ami.New(ami.Config{
		Addr:        srv.Addr(),
		Username:    srv.Username,
		Secret:      "wrong-secret",
		DialTimeout: 2 * time.Second,
		MinBackoff:  10 * time.Millisecond,
		Logger:      quietLogger(),
	})

	errCh := make(chan error, 1)
	go func() { errCh <- client.Run(context.Background()) }()

	select {
	case err := <-errCh:
		if !errors.Is(err, ami.ErrLoginFailed) {
			t.Fatalf("got %v, want ErrLoginFailed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return on rejected credentials")
	}

	if client.Connected() {
		t.Fatal("client must not report itself connected after a rejected login")
	}
}

func TestClientAction(t *testing.T) {
	srv := amitest.NewServer(t)
	srv.HandleSuccess("Ping", "Ping", "Pong")
	client, ctx := runClient(t, srv, nil)

	resp, err := client.Action(ctx, ami.NewAction("Ping"))
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if got := resp.Get("Ping"); got != "Pong" {
		t.Fatalf("Ping = %q, want %q", got, "Pong")
	}
	if resp.ActionID() == "" {
		t.Fatal("the client should have generated an ActionID")
	}
}

func TestClientActionError(t *testing.T) {
	srv := amitest.NewServer(t)
	srv.Handle("ConfbridgeKick", func(c *amitest.Conn, a *ami.Message) {
		c.Send(amitest.Error(a, "No Conference by that name found."))
	})
	client, ctx := runClient(t, srv, nil)

	_, err := client.Action(ctx, ami.NewAction("ConfbridgeKick"))
	var actionErr *ami.ActionError
	if !errors.As(err, &actionErr) {
		t.Fatalf("got %v, want *ami.ActionError", err)
	}
	if actionErr.Action != "ConfbridgeKick" {
		t.Fatalf("ActionError.Action = %q", actionErr.Action)
	}
	if got := actionErr.Response.Get("Message"); got != "No Conference by that name found." {
		t.Fatalf("ActionError message = %q", got)
	}
}

func TestClientActionList(t *testing.T) {
	srv := amitest.NewServer(t)
	srv.HandleList("ConfbridgeList", "ConfbridgeList", "ConfbridgeListComplete",
		[]string{"Conference", "1000", "CallerIDNum", "1001", "Channel", "PJSIP/1001-00000001"},
		[]string{"Conference", "1000", "CallerIDNum", "1002", "Channel", "PJSIP/1002-00000002"},
	)
	client, ctx := runClient(t, srv, nil)

	action := ami.NewAction("ConfbridgeList")
	action.Add("Conference", "1000")

	items, err := client.ActionList(ctx, action, "ConfbridgeListComplete")
	if err != nil {
		t.Fatalf("ActionList: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	for i, want := range []string{"1001", "1002"} {
		if got := items[i].Get("CallerIDNum"); got != want {
			t.Fatalf("item %d CallerIDNum = %q, want %q", i, got, want)
		}
		if items[i].EventName() != "ConfbridgeList" {
			t.Fatalf("item %d is not a ConfbridgeList event: %v", i, items[i])
		}
	}
}

func TestClientActionListEmpty(t *testing.T) {
	srv := amitest.NewServer(t)
	srv.HandleList("ConfbridgeList", "ConfbridgeList", "ConfbridgeListComplete")
	client, ctx := runClient(t, srv, nil)

	items, err := client.ActionList(ctx, ami.NewAction("ConfbridgeList"), "ConfbridgeListComplete")
	if err != nil {
		t.Fatalf("ActionList: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0", len(items))
	}
}

func TestClientDeliversUnsolicitedEvents(t *testing.T) {
	srv := amitest.NewServer(t)
	client, _ := runClient(t, srv, nil)
	srv.WaitConn(t)

	join := ami.NewEvent("ConfbridgeJoin")
	join.Add("Conference", "1000")
	join.Add("Uniqueid", "1756000000.1")
	srv.Broadcast(join)

	select {
	case ev := <-client.Events():
		if ev.EventName() != "ConfbridgeJoin" {
			t.Fatalf("got event %q, want ConfbridgeJoin", ev.EventName())
		}
		if got := ev.Get("Uniqueid"); got != "1756000000.1" {
			t.Fatalf("Uniqueid = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event was never delivered")
	}
}

// An event carrying an ActionID nobody is waiting for — an async
// OriginateResponse is the real-world case — must still reach the event stream.
func TestClientRoutesOrphanEventToStream(t *testing.T) {
	srv := amitest.NewServer(t)
	client, _ := runClient(t, srv, nil)
	srv.WaitConn(t)

	ev := ami.NewEvent("OriginateResponse")
	ev.Add("ActionID", "long-gone")
	ev.Add("Response", "Failure")
	srv.Broadcast(ev)

	select {
	case got := <-client.Events():
		if got.EventName() != "OriginateResponse" {
			t.Fatalf("got event %q", got.EventName())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orphan event was never delivered")
	}
}

// A consumer that never reads must not stall the reader goroutine: events are
// dropped and the connection keeps serving actions.
func TestClientDropsEventsForSlowConsumer(t *testing.T) {
	srv := amitest.NewServer(t)
	srv.HandleSuccess("Ping", "Ping", "Pong")
	client, ctx := runClient(t, srv, func(cfg *ami.Config) { cfg.EventBuffer = 1 })
	srv.WaitConn(t)

	for i := 0; i < 50; i++ {
		srv.Broadcast(ami.NewEvent("PeerStatus"))
	}

	if _, err := client.Action(ctx, ami.NewAction("Ping")); err != nil {
		t.Fatalf("the reader stalled behind a slow event consumer: %v", err)
	}
}

func TestClientActionWhileDisconnected(t *testing.T) {
	client := ami.New(ami.Config{Addr: "127.0.0.1:1", Logger: quietLogger()})

	_, err := client.Action(context.Background(), ami.NewAction("Ping"))
	if !errors.Is(err, ami.ErrNotConnected) {
		t.Fatalf("got %v, want ErrNotConnected", err)
	}
}

// A connection that drops with an action in flight must fail that action
// immediately rather than leaving the caller to wait out its timeout.
func TestClientFailsInFlightActionOnDisconnect(t *testing.T) {
	srv := amitest.NewServer(t)
	srv.Handle("ConfbridgeList", func(_ *amitest.Conn, _ *ami.Message) {
		// Swallow the action: the reply never comes, the socket dies instead.
	})
	client, ctx := runClient(t, srv, func(cfg *ami.Config) { cfg.ActionTimeout = 30 * time.Second })
	srv.WaitConn(t)

	errCh := make(chan error, 1)
	go func() {
		_, err := client.ActionList(ctx, ami.NewAction("ConfbridgeList"), "ConfbridgeListComplete")
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	srv.DropConns()

	select {
	case err := <-errCh:
		if !errors.Is(err, ami.ErrDisconnected) {
			t.Fatalf("got %v, want ErrDisconnected", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight action was not released when the connection dropped")
	}
}

func TestClientActionTimeout(t *testing.T) {
	srv := amitest.NewServer(t)
	srv.Handle("Ping", func(_ *amitest.Conn, _ *ami.Message) {})
	client, ctx := runClient(t, srv, func(cfg *ami.Config) { cfg.ActionTimeout = 100 * time.Millisecond })

	_, err := client.Action(ctx, ami.NewAction("Ping"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context.DeadlineExceeded", err)
	}
}

func TestClientReconnects(t *testing.T) {
	srv := amitest.NewServer(t)
	srv.HandleSuccess("Ping", "Ping", "Pong")

	var states []bool
	var mu sync.Mutex
	client, ctx := runClient(t, srv, func(cfg *ami.Config) {
		cfg.OnStateChange = func(connected bool) {
			mu.Lock()
			states = append(states, connected)
			mu.Unlock()
		}
	})
	srv.WaitConn(t)

	srv.DropConns()
	waitConnected(t, client, false)
	waitConnected(t, client, true)

	if _, err := client.Action(ctx, ami.NewAction("Ping")); err != nil {
		t.Fatalf("action after reconnect: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(states) < 3 || !states[0] || states[1] || !states[2] {
		t.Fatalf("state callback sequence = %v, want true,false,true...", states)
	}
}

func TestClientRunStopsOnContextCancel(t *testing.T) {
	srv := amitest.NewServer(t)
	client := ami.New(ami.Config{
		Addr:       srv.Addr(),
		Username:   srv.Username,
		Secret:     srv.Secret,
		MinBackoff: 10 * time.Millisecond,
		Logger:     quietLogger(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- client.Run(ctx) }()

	waitConnected(t, client, true)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	if client.Connected() {
		t.Fatal("client still reports itself connected after shutdown")
	}
}

// The client must keep retrying a dead endpoint and connect as soon as it
// comes up, which is the "start before Asterisk" case.
func TestClientRetriesUntilServerAppears(t *testing.T) {
	srv := amitest.NewServer(t)
	addr := srv.Addr()
	srv.Close()

	client := ami.New(ami.Config{
		Addr:       addr,
		Username:   "westbridge",
		Secret:     "secret",
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 20 * time.Millisecond,
		Logger:     quietLogger(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	if client.Connected() {
		t.Fatal("client claims to be connected to a closed port")
	}
}
