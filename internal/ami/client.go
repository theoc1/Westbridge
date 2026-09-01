package ami

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Errors returned by the client. They are all sentinel values so that callers
// can branch on them with errors.Is.
var (
	// ErrNotConnected is returned by an action issued while the AMI link is
	// down. The caller decides whether to retry; the client itself keeps
	// reconnecting in the background either way.
	ErrNotConnected = errors.New("ami: not connected to Asterisk")

	// ErrDisconnected is returned to every action that was still in flight
	// when the connection dropped, so that no caller is left hanging until
	// its context expires.
	ErrDisconnected = errors.New("ami: connection lost while the action was in flight")

	// ErrLoginFailed means Asterisk rejected the credentials. This is the one
	// failure Run does not retry: bad credentials never fix themselves, and
	// reconnecting in a loop would only hammer the manager with failed logins.
	ErrLoginFailed = errors.New("ami: login rejected")
)

// ActionError reports an action that reached Asterisk and came back with
// "Response: Error". The full response is kept so callers can inspect fields
// beyond the human-readable message.
type ActionError struct {
	Action   string
	Response *Message
}

func (e *ActionError) Error() string {
	if msg := e.Response.Get("Message"); msg != "" {
		return fmt.Sprintf("ami: action %q failed: %s", e.Action, msg)
	}
	return fmt.Sprintf("ami: action %q failed: %s", e.Action, e.Response.Get("Response"))
}

// Config describes how to reach Asterisk and how the client should behave.
// Every duration and size has a sane default, so only Addr, Username and
// Secret are mandatory.
type Config struct {
	// Addr is the AMI TCP endpoint, e.g. "127.0.0.1:5038".
	Addr string
	// Username and Secret are the manager.conf credentials.
	Username string
	Secret   string

	// DialTimeout bounds the TCP connect, the banner read and the login
	// exchange. Default 10s.
	DialTimeout time.Duration
	// ActionTimeout is applied to actions whose context carries no deadline
	// of its own. Default 10s.
	ActionTimeout time.Duration

	// MinBackoff and MaxBackoff bound the exponential reconnect delay.
	// Defaults are 1s and 30s.
	MinBackoff time.Duration
	MaxBackoff time.Duration

	// EventBuffer sizes the unsolicited-event channel. Default 256.
	EventBuffer int

	// Logger receives connection lifecycle and dropped-event messages.
	// Default slog.Default().
	Logger *slog.Logger

	// OnStateChange, when set, is called on every connect and disconnect.
	// It runs on the client's own goroutine and must not block.
	OnStateChange func(connected bool)
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.DialTimeout <= 0 {
		out.DialTimeout = 10 * time.Second
	}
	if out.ActionTimeout <= 0 {
		out.ActionTimeout = 10 * time.Second
	}
	if out.MinBackoff <= 0 {
		out.MinBackoff = time.Second
	}
	if out.MaxBackoff <= 0 {
		out.MaxBackoff = 30 * time.Second
	}
	if out.MaxBackoff < out.MinBackoff {
		out.MaxBackoff = out.MinBackoff
	}
	if out.EventBuffer <= 0 {
		out.EventBuffer = 256
	}
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	return out
}

// Client is a single persistent AMI connection multiplexing request/response
// actions, list-style actions and the unsolicited event stream.
//
// Exactly one goroutine — the one running Run — reads from the socket. Every
// other goroutine interacts with it through channels, so Client is safe for
// concurrent use.
type Client struct {
	cfg Config
	log *slog.Logger

	events chan *Message
	prefix string
	seq    atomic.Uint64

	mu   sync.Mutex
	sess *session
}

// New returns a client that is not connected yet; call Run to bring the link
// up and keep it up.
func New(cfg Config) *Client {
	resolved := cfg.withDefaults()
	return &Client{
		cfg:    resolved,
		log:    resolved.Logger,
		events: make(chan *Message, resolved.EventBuffer),
		prefix: randomPrefix(),
	}
}

// Events returns the stream of unsolicited events: everything Asterisk sends
// that is not the reply to an action currently in flight.
//
// The channel is buffered. A consumer that falls behind loses events — they
// are dropped with a warning rather than stalling the reader — which is safe
// here because the roster is rebuilt from a full ConfbridgeList on a timer.
func (c *Client) Events() <-chan *Message { return c.events }

// Connected reports whether the AMI link is currently up and logged in.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess != nil
}

// Run connects, logs in, and serves the connection until ctx is cancelled,
// reconnecting with exponential backoff after every failure. It returns nil
// when ctx is cancelled and ErrLoginFailed if the credentials are rejected.
func (c *Client) Run(ctx context.Context) error {
	backoff := c.cfg.MinBackoff

	for {
		if ctx.Err() != nil {
			return nil
		}

		connected, err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrLoginFailed) {
			c.log.Error("ami: login rejected, giving up", "addr", c.cfg.Addr, "error", err)
			return err
		}
		if connected {
			// The session did real work, so the next outage starts over
			// from the shortest delay instead of inheriting an old one.
			backoff = c.cfg.MinBackoff
			c.log.Warn("ami: connection lost", "addr", c.cfg.Addr, "error", err)
		} else {
			c.log.Warn("ami: connect failed", "addr", c.cfg.Addr, "error", err, "retry_in", backoff)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}

		if backoff *= 2; backoff > c.cfg.MaxBackoff {
			backoff = c.cfg.MaxBackoff
		}
	}
}

// runOnce owns exactly one TCP connection. The bool reports whether the
// session ever reached the logged-in state, which is what tells Run whether
// to reset the backoff.
func (c *Client) runOnce(ctx context.Context) (bool, error) {
	dialer := net.Dialer{Timeout: c.cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		return false, err
	}

	// Closing the socket is the only way to unblock a goroutine parked in
	// Decode, so cancellation is wired straight to Close.
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
		}
		_ = conn.Close()
	}()
	defer close(stop)

	dec := NewDecoder(conn)
	if err := conn.SetDeadline(time.Now().Add(c.cfg.DialTimeout)); err != nil {
		return false, err
	}

	banner, err := dec.ReadBanner()
	if err != nil {
		return false, fmt.Errorf("ami: read banner: %w", err)
	}

	sess := newSession(conn)
	if err := c.login(sess, dec); err != nil {
		return false, err
	}
	// The handshake deadline must not leak into the event stream, which is
	// idle for minutes at a time.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return false, err
	}

	c.setSession(sess)
	defer c.clearSession(sess)
	c.log.Info("ami: connected", "addr", c.cfg.Addr, "banner", banner)

	for {
		msg, err := dec.Decode()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = fmt.Errorf("ami: %w", io.EOF)
			}
			return true, err
		}
		c.route(sess, msg)
	}
}

func (c *Client) login(sess *session, dec *Decoder) error {
	action := NewAction("Login")
	action.Add("ActionID", c.nextActionID())
	action.Add("Username", c.cfg.Username)
	action.Add("Secret", c.cfg.Secret)
	action.Add("Events", "on")

	if err := sess.write(action); err != nil {
		return fmt.Errorf("ami: send login: %w", err)
	}

	for {
		msg, err := dec.Decode()
		if err != nil {
			return fmt.Errorf("ami: read login response: %w", err)
		}
		if !msg.IsResponse() {
			continue // Asterisk may emit FullyBooted before the reply
		}
		if !msg.IsSuccess() {
			return fmt.Errorf("%w: %s", ErrLoginFailed, msg.Get("Message"))
		}
		return nil
	}
}

// route hands a decoded packet either to the action waiting for it or to the
// unsolicited event stream.
func (c *Client) route(sess *session, msg *Message) {
	if id := msg.ActionID(); id != "" {
		if p := sess.lookup(id); p != nil {
			select {
			case p.ch <- msg:
			case <-p.done: // the caller gave up; nothing left to deliver to
			case <-sess.done:
			}
			return
		}
	}

	if !msg.IsEvent() {
		// A response whose caller already timed out. Nothing to do with it.
		c.log.Debug("ami: unmatched response", "action_id", msg.ActionID(), "response", msg.Get("Response"))
		return
	}

	select {
	case c.events <- msg:
	default:
		c.log.Warn("ami: event dropped, consumer is behind", "event", msg.EventName())
	}
}

// Action sends a single-response action and waits for its response.
//
// If msg carries no ActionID one is generated. When the context has no
// deadline, Config.ActionTimeout is applied.
func (c *Client) Action(ctx context.Context, msg *Message) (*Message, error) {
	resp, _, err := c.do(ctx, msg, "")
	return resp, err
}

// ActionList sends a list-style action such as ConfbridgeList and accumulates
// the events it produces until the terminator arrives.
//
// Asterisk answers these in three parts: an acknowledging response, N events
// carrying the payload, and a final completeEvent (e.g.
// "ConfbridgeListComplete"), all sharing the action's ActionID. The returned
// slice holds only the payload events; the acknowledgement and the terminator
// are consumed.
func (c *Client) ActionList(ctx context.Context, msg *Message, completeEvent string) ([]*Message, error) {
	_, items, err := c.do(ctx, msg, completeEvent)
	return items, err
}

func (c *Client) do(ctx context.Context, msg *Message, completeEvent string) (*Message, []*Message, error) {
	sess := c.session()
	if sess == nil {
		return nil, nil, ErrNotConnected
	}

	id := msg.ActionID()
	if id == "" {
		id = c.nextActionID()
		msg.Set("ActionID", id)
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.ActionTimeout)
		defer cancel()
	}

	p := newPending()
	if !sess.register(id, p) {
		return nil, nil, ErrDisconnected
	}
	defer sess.unregister(id)

	if err := sess.write(msg); err != nil {
		return nil, nil, err
	}

	var (
		response *Message
		items    []*Message
	)

	// consume returns true once the action is complete.
	consume := func(m *Message) (bool, error) {
		if m.IsResponse() {
			response = m
			if !m.IsSuccess() {
				return true, &ActionError{Action: msg.Get("Action"), Response: m}
			}
			// A list action's response is only the acknowledgement; the
			// payload is still on its way.
			return completeEvent == "", nil
		}
		if completeEvent != "" && strings.EqualFold(m.EventName(), completeEvent) {
			return true, nil
		}
		items = append(items, m)
		return false, nil
	}

	for {
		select {
		case m := <-p.ch:
			done, err := consume(m)
			if done || err != nil {
				return response, items, err
			}

		case <-ctx.Done():
			return nil, nil, fmt.Errorf("ami: action %q: %w", msg.Get("Action"), ctx.Err())

		case <-sess.done:
			// The reader stopped, but whatever it had already delivered is
			// still queued and may well complete the action.
			for {
				select {
				case m := <-p.ch:
					done, err := consume(m)
					if done || err != nil {
						return response, items, err
					}
				default:
					return nil, nil, ErrDisconnected
				}
			}
		}
	}
}

func (c *Client) nextActionID() string {
	return fmt.Sprintf("%s-%d", c.prefix, c.seq.Add(1))
}

func (c *Client) session() *session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

func (c *Client) setSession(s *session) {
	c.mu.Lock()
	c.sess = s
	c.mu.Unlock()
	c.notify(true)
}

func (c *Client) clearSession(s *session) {
	c.mu.Lock()
	cleared := c.sess == s
	if cleared {
		c.sess = nil
	}
	c.mu.Unlock()

	// Closing releases every in-flight action with ErrDisconnected.
	s.close()

	if cleared {
		c.log.Info("ami: disconnected", "addr", c.cfg.Addr)
		c.notify(false)
	}
}

func (c *Client) notify(connected bool) {
	if c.cfg.OnStateChange != nil {
		c.cfg.OnStateChange(connected)
	}
}

// session is the state bound to one TCP connection: the socket, its write
// lock and the actions awaiting a reply on it.
type session struct {
	conn net.Conn

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]*pending
	closed  bool

	done      chan struct{}
	closeOnce sync.Once
}

func newSession(conn net.Conn) *session {
	return &session{
		conn:    conn,
		pending: make(map[string]*pending),
		done:    make(chan struct{}),
	}
}

func (s *session) write(msg *Message) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if _, err := msg.WriteTo(s.conn); err != nil {
		return fmt.Errorf("ami: write action: %w", err)
	}
	return nil
}

func (s *session) register(id string, p *pending) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.pending[id] = p
	return true
}

func (s *session) unregister(id string) {
	s.mu.Lock()
	p := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()

	if p != nil {
		p.release()
	}
}

func (s *session) lookup(id string) *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending[id]
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		close(s.done)
		_ = s.conn.Close()
	})
}

// pending is one action waiting for its reply. done is closed when the caller
// walks away, so the reader never blocks handing a message to a dead waiter.
type pending struct {
	ch   chan *Message
	done chan struct{}

	once sync.Once
}

func newPending() *pending {
	return &pending{
		ch:   make(chan *Message, 32),
		done: make(chan struct{}),
	}
}

func (p *pending) release() { p.once.Do(func() { close(p.done) }) }

// randomPrefix keeps ActionIDs from colliding across restarts, which matters
// because Asterisk may still be emitting events for a previous run's action.
func randomPrefix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "wb"
	}
	return hex.EncodeToString(b[:])
}
