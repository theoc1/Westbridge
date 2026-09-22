package ami

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

type observedWrite struct {
	net.Conn
	started chan struct{}
}

func (c *observedWrite) Write(p []byte) (int, error) { close(c.started); return c.Conn.Write(p) }

func TestActionInterruptsBlockedWrite(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			conn := &observedWrite{Conn: a, started: make(chan struct{})}
			c := New(Config{})
			c.setSession(newSession(conn))
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.Action(ctx, NewAction("Ping")); done <- err }()
			select {
			case <-conn.started:
			case <-time.After(time.Second):
				t.Fatal("write did not start")
			}
			if !deadline {
				cancel()
			}
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			case <-time.After(time.Second):
				t.Fatal("write ignored cancellation")
			}
			select {
			case <-c.session().done:
			default:
				t.Fatal("possibly partial packet left session open")
			}
		})
	}
}

func TestQueuedWriteCancellationDoesNotInterruptActiveWrite(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	conn := &observedWrite{Conn: a, started: make(chan struct{})}
	s := newSession(conn)
	first := make(chan error, 1)
	go func() { first <- s.write(context.Background(), NewAction("Ping")) }()
	select {
	case <-conn.started:
	case <-time.After(time.Second):
		t.Fatal("first write did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { second <- s.write(ctx, NewAction("Logoff")) }()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued write ignored deadline")
	}
	select {
	case <-s.done:
		t.Fatal("queued cancellation closed active session")
	default:
	}
	if err := b.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	msg, err := NewDecoder(b).Decode()
	if err != nil || msg.Get("Action") != "Ping" {
		t.Fatalf("first packet: %v, %v", msg, err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestEventSequenceContinuesAcrossSessions(t *testing.T) {
	c := New(Config{})
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	first := newSession(a)
	old := NewEvent("ConfbridgeEnd")
	c.route(first, old)
	boundary := c.EventSequence()
	if boundary == 0 || (<-c.Events()).Sequence != boundary {
		t.Fatal("event has no snapshot ordering boundary")
	}
	first.close()
	next := newSession(b)
	fresh := NewEvent("ConfbridgeJoin")
	c.route(next, fresh)
	if got := (<-c.Events()).Sequence; got <= boundary || got != c.EventSequence() {
		t.Fatalf("sequence restarted on reconnect: old=%d new=%d", boundary, got)
	}
}
