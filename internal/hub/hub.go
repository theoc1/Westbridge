// Package hub is the fan-out between the conference service and the browsers
// watching it. One goroutine publishes a snapshot; every connected WebSocket
// gets a copy.
//
// The hub deals in opaque []byte payloads rather than domain types: the
// snapshot is marshalled once by the publisher instead of once per client, and
// the package stays free of any dependency on conference or web.
package hub

import (
	"log/slog"
	"sync"
)

// DefaultBufferSize is how many payloads a client may fall behind by before it
// is evicted. Snapshots are small and each one supersedes the last, so a deep
// queue would only serve stale data to a client that cannot keep up.
const DefaultBufferSize = 8

// Hub broadcasts payloads to a set of subscribers. It is safe for concurrent
// use.
type Hub struct {
	log        *slog.Logger
	bufferSize int

	mu      sync.Mutex
	clients map[uint64]*client
	nextID  uint64
	last    []byte
	closed  bool
}

type client struct {
	id   uint64
	ch   chan []byte
	once sync.Once
}

// Config tunes the hub. Both fields have defaults.
type Config struct {
	// BufferSize is the per-client queue depth. Default DefaultBufferSize.
	BufferSize int
	// Logger records evictions. Default slog.Default().
	Logger *slog.Logger
}

// New returns an empty hub.
func New(cfg Config) *Hub {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultBufferSize
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Hub{
		log:        cfg.Logger,
		bufferSize: cfg.BufferSize,
		clients:    make(map[uint64]*client),
	}
}

// Subscribe registers a new client and returns the channel it should read
// from, plus a function that unsubscribes it. The channel is closed when the
// client is unsubscribed, evicted for falling behind, or the hub is closed, so
// a reader can simply range over it.
//
// The most recent payload, if there is one, is already queued on the returned
// channel: a browser that connects between two snapshots should not have to
// wait for the next one to render something.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c := &client{id: h.nextID, ch: make(chan []byte, h.bufferSize)}
	h.nextID++

	if h.closed {
		// Nothing will ever be published; hand back a closed channel rather
		// than a subscription that silently never fires.
		c.close()
		return c.ch, func() {}
	}

	h.clients[c.id] = c
	if h.last != nil {
		c.ch <- h.last
	}

	return c.ch, func() { h.remove(c.id) }
}

// Broadcast sends payload to every subscriber and remembers it as the state a
// future subscriber starts from.
//
// A client whose buffer is full is dropped: the broadcaster is the service's
// only goroutine, and blocking it on one stalled browser would freeze the
// roster for everybody.
func (h *Hub) Broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}
	h.last = payload

	for id, c := range h.clients {
		select {
		case c.ch <- payload:
		default:
			h.log.Warn("hub: dropping a client that is not keeping up", "client", id)
			delete(h.clients, id)
			c.close()
		}
	}
}

// Len is the number of current subscribers.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Close disconnects every subscriber and makes subsequent broadcasts no-ops.
// It is idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}
	h.closed = true
	for id, c := range h.clients {
		delete(h.clients, id)
		c.close()
	}
}

// remove unsubscribes a client, tolerating a client that was already evicted.
func (h *Hub) remove(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	c, ok := h.clients[id]
	if !ok {
		return
	}
	delete(h.clients, id)
	c.close()
}

// close shuts the client's channel exactly once, so that an eviction racing an
// unsubscribe cannot close it twice.
func (c *client) close() { c.once.Do(func() { close(c.ch) }) }
