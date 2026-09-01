package hub_test

import (
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dmalkin/westbridge/internal/hub"
)

func newHub(t *testing.T, bufferSize int) *hub.Hub {
	t.Helper()
	h := hub.New(hub.Config{
		BufferSize: bufferSize,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(h.Close)
	return h
}

// recv reads one payload with a deadline, so a broken hub fails the test
// instead of hanging it.
func recv(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case p, ok := <-ch:
		if !ok {
			t.Fatal("channel closed, want a payload")
		}
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a payload")
		return nil
	}
}

func TestBroadcastReachesEverySubscriber(t *testing.T) {
	h := newHub(t, 4)

	a, _ := h.Subscribe()
	b, _ := h.Subscribe()
	if h.Len() != 2 {
		t.Fatalf("Len = %d, want 2", h.Len())
	}

	h.Broadcast([]byte("one"))

	for name, ch := range map[string]<-chan []byte{"a": a, "b": b} {
		if got := string(recv(t, ch)); got != "one" {
			t.Errorf("%s received %q, want %q", name, got, "one")
		}
	}
}

func TestSubscribeReplaysTheLastPayload(t *testing.T) {
	h := newHub(t, 4)

	h.Broadcast([]byte("first"))
	h.Broadcast([]byte("second"))

	ch, _ := h.Subscribe()
	if got := string(recv(t, ch)); got != "second" {
		t.Errorf("replayed %q, want the most recent payload %q", got, "second")
	}

	select {
	case p := <-ch:
		t.Fatalf("received an extra payload %q, want only the latest", p)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUnsubscribeClosesTheChannelAndStopsDelivery(t *testing.T) {
	h := newHub(t, 4)

	ch, cancel := h.Subscribe()
	cancel()

	if _, ok := <-ch; ok {
		t.Fatal("channel yielded a payload after unsubscribe, want it closed")
	}
	if h.Len() != 0 {
		t.Fatalf("Len = %d after unsubscribe, want 0", h.Len())
	}

	// A second cancel must not panic on an already-closed channel.
	cancel()
	h.Broadcast([]byte("ignored"))
}

func TestSlowClientIsEvictedWithoutBlockingTheBroadcaster(t *testing.T) {
	h := newHub(t, 2)

	slow, _ := h.Subscribe()
	fast, _ := h.Subscribe()

	drained := make(chan []byte, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for p := range fast {
			drained <- p
		}
	}()

	// One more broadcast than the slow client's buffer can hold.
	for i := range 5 {
		h.Broadcast([]byte(strconv.Itoa(i)))
	}

	// The slow client's channel is closed once its buffer overflows; what was
	// already queued stays readable.
	deadline := time.After(2 * time.Second)
	closed := false
	for !closed {
		select {
		case _, ok := <-slow:
			if !ok {
				closed = true
			}
		case <-deadline:
			t.Fatal("slow client was never evicted")
		}
	}

	if h.Len() != 1 {
		t.Fatalf("Len = %d after eviction, want 1 (the fast client)", h.Len())
	}

	// The fast client saw everything, which is the point: one stalled reader
	// must not cost the others any updates.
	for i := range 5 {
		if got := string(recv(t, drained)); got != strconv.Itoa(i) {
			t.Fatalf("fast client received %q, want %q", got, strconv.Itoa(i))
		}
	}

	h.Close()
	<-done
}

func TestCloseDisconnectsEverybodyAndIsIdempotent(t *testing.T) {
	h := newHub(t, 4)

	ch, cancel := h.Subscribe()
	h.Close()
	h.Close()

	if _, ok := <-ch; ok {
		t.Fatal("channel yielded a payload after Close, want it closed")
	}
	cancel()

	// Subscribing to a closed hub yields a closed channel rather than a
	// subscription that never fires.
	after, cancelAfter := h.Subscribe()
	defer cancelAfter()
	if _, ok := <-after; ok {
		t.Fatal("Subscribe on a closed hub yielded a payload, want a closed channel")
	}
}

func TestConcurrentSubscribeBroadcastUnsubscribe(t *testing.T) {
	h := newHub(t, 8)

	const workers = 16
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Publishers.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.Broadcast([]byte("snapshot"))
				}
			}
		}()
	}

	// Subscribers that come and go while the publishers hammer the hub.
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ch, cancel := h.Subscribe()
				select {
				case <-ch:
				case <-time.After(time.Millisecond):
				}
				cancel()
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	if h.Len() != 0 {
		t.Fatalf("Len = %d after every subscriber cancelled, want 0", h.Len())
	}
}
