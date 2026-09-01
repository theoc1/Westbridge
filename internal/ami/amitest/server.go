// Package amitest provides a fake Asterisk Manager Interface server for tests.
//
// It speaks enough of the protocol to exercise a real client: it sends the
// banner, authenticates a Login action, dispatches subsequent actions to
// scripted handlers, and can push unsolicited events or drop the connection on
// demand so that reconnect paths can be tested.
package amitest

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
)

// DefaultBanner matches the greeting of a recent Asterisk release.
const DefaultBanner = "Asterisk Call Manager/9.0.0"

// Handler replies to one action on the connection that carried it. Handlers
// run on the connection's own goroutine, one action at a time.
type Handler func(c *Conn, action *ami.Message)

// Server is a fake AMI endpoint listening on a loopback port.
type Server struct {
	// Username and Secret are the credentials Login must present. They
	// default to "westbridge" / "secret".
	Username string
	Secret   string
	// Banner is sent verbatim before anything else.
	Banner string

	ln net.Listener
	wg sync.WaitGroup

	mu       sync.Mutex
	handlers map[string]Handler
	conns    []*Conn
	closed   bool

	accepted chan *Conn
}

// NewServer starts a fake server on 127.0.0.1 and registers cleanup with t.
func NewServer(t *testing.T) *Server {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("amitest: listen: %v", err)
	}

	s := &Server{
		Username: "westbridge",
		Secret:   "secret",
		Banner:   DefaultBanner,
		ln:       ln,
		handlers: make(map[string]Handler),
		accepted: make(chan *Conn, 16),
	}

	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.Close)
	return s
}

// Addr is the "host:port" the client should dial.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Handle registers the handler for an action name, matched case-insensitively.
// Registering "" sets the fallback used for actions with no handler.
func (s *Server) Handle(action string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[strings.ToLower(action)] = h
}

// HandleSuccess replies to action with "Response: Success" plus extra fields
// given as alternating key/value arguments.
func (s *Server) HandleSuccess(action string, kv ...string) {
	s.Handle(action, func(c *Conn, a *ami.Message) {
		c.Send(Success(a, kv...))
	})
}

// HandleList replies to a list-style action the way Asterisk does: an
// acknowledging response, one event per item, then the terminator event.
// Each item is a set of alternating key/value pairs; the Event and ActionID
// fields are filled in automatically.
func (s *Server) HandleList(action, itemEvent, completeEvent string, items ...[]string) {
	s.Handle(action, func(c *Conn, a *ami.Message) {
		c.Send(Success(a, "Message", itemEvent+" will follow"))
		for _, item := range items {
			ev := ami.NewEvent(itemEvent)
			ev.Add("ActionID", a.ActionID())
			addPairs(ev, item)
			c.Send(ev)
		}
		done := ami.NewEvent(completeEvent)
		done.Add("ActionID", a.ActionID())
		done.Add("ListItems", fmt.Sprint(len(items)))
		c.Send(done)
	})
}

// WaitConn blocks until a client has connected and logged in successfully.
func (s *Server) WaitConn(t *testing.T) *Conn {
	t.Helper()
	select {
	case c := <-s.accepted:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("amitest: timed out waiting for a client connection")
		return nil
	}
}

// Broadcast sends an unsolicited event to every live connection.
func (s *Server) Broadcast(m *ami.Message) {
	for _, c := range s.snapshotConns() {
		c.Send(m)
	}
}

// DropConns closes every live client connection without closing the listener,
// so the client under test has to reconnect.
func (s *Server) DropConns() {
	for _, c := range s.snapshotConns() {
		c.Close()
	}
}

// Close shuts the listener down and disconnects every client. It is safe to
// call more than once.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	_ = s.ln.Close()
	s.DropConns()
	s.wg.Wait()
}

func (s *Server) snapshotConns() []*Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Conn(nil), s.conns...)
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}

		c := &Conn{nc: nc}
		s.mu.Lock()
		s.conns = append(s.conns, c)
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(c)
		}()
	}
}

func (s *Server) serve(c *Conn) {
	defer c.Close()

	if _, err := fmt.Fprintf(c.nc, "%s\r\n", s.Banner); err != nil {
		return
	}

	dec := ami.NewDecoder(c.nc)
	authed := false

	for {
		action, err := dec.Decode()
		if err != nil {
			return
		}

		name := strings.ToLower(action.Get("Action"))
		if !authed {
			if name != "login" {
				c.Send(Error(action, "Authentication Required"))
				return
			}
			if action.Get("Username") != s.Username || action.Get("Secret") != s.Secret {
				c.Send(Error(action, "Authentication failed"))
				return
			}
			authed = true
			c.Send(Success(action, "Message", "Authentication accepted"))
			select {
			case s.accepted <- c:
			default:
			}
			continue
		}

		if name == "logoff" {
			c.Send(goodbye(action))
			return
		}

		s.dispatch(c, name, action)
	}
}

func (s *Server) dispatch(c *Conn, name string, action *ami.Message) {
	s.mu.Lock()
	h, ok := s.handlers[name]
	if !ok {
		h = s.handlers[""]
	}
	s.mu.Unlock()

	if h == nil {
		c.Send(Error(action, "Invalid/unknown command: "+action.Get("Action")))
		return
	}
	h(c, action)
}

// Conn is one accepted client connection.
type Conn struct {
	nc net.Conn

	mu     sync.Mutex
	closed bool
}

// Send writes a message to the client. Errors are ignored: a test server has
// nothing useful to do about a peer that went away.
func (c *Conn) Send(m *ami.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	_, _ = m.WriteTo(c.nc)
}

// Close drops the connection.
func (c *Conn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	_ = c.nc.Close()
}

// Success builds a success response correlated with action.
func Success(action *ami.Message, kv ...string) *ami.Message {
	m := &ami.Message{}
	m.Add("Response", "Success")
	if id := action.ActionID(); id != "" {
		m.Add("ActionID", id)
	}
	addPairs(m, kv)
	return m
}

// Error builds an error response correlated with action.
func Error(action *ami.Message, message string) *ami.Message {
	m := &ami.Message{}
	m.Add("Response", "Error")
	if id := action.ActionID(); id != "" {
		m.Add("ActionID", id)
	}
	m.Add("Message", message)
	return m
}

func goodbye(action *ami.Message) *ami.Message {
	m := &ami.Message{}
	m.Add("Response", "Goodbye")
	if id := action.ActionID(); id != "" {
		m.Add("ActionID", id)
	}
	return m
}

func addPairs(m *ami.Message, kv []string) {
	if len(kv)%2 != 0 {
		panic(errors.New("amitest: key/value arguments must come in pairs"))
	}
	for i := 0; i < len(kv); i += 2 {
		m.Add(kv[i], kv[i+1])
	}
}
