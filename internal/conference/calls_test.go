package conference

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
)

type callClient struct {
	mu        sync.Mutex
	actions   []*ami.Message
	action    func(*ami.Message) (*ami.Message, error)
	items     []*ami.Message
	connected bool
}

func (c *callClient) Connected() bool             { return c.connected }
func (c *callClient) EventSequence() uint64       { return 0 }
func (c *callClient) Events() <-chan *ami.Message { return nil }
func (c *callClient) Action(_ context.Context, a *ami.Message) (*ami.Message, error) {
	c.mu.Lock()
	c.actions = append(c.actions, a)
	fn := c.action
	c.mu.Unlock()
	if fn != nil {
		return fn(a)
	}
	return &ami.Message{}, nil
}
func (c *callClient) ActionList(_ context.Context, _ *ami.Message, _ string) ([]*ami.Message, error) {
	return c.items, nil
}
func newCallService() (*Service, *callClient) {
	c := &callClient{connected: true}
	return New(c, Config{Room: "1000", OriginateContext: "conference-out"}), c
}
func callEvent(name, id string, fields ...string) *ami.Message {
	m := ami.NewEvent(name)
	m.Add("Uniqueid", id)
	for i := 0; i < len(fields); i += 2 {
		m.Add(fields[i], fields[i+1])
	}
	return m
}
func callByID(t *testing.T, s *Service, id string) Call {
	t.Helper()
	for _, c := range s.Snapshot().Calls {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("call %s not found in %+v", id, s.Snapshot())
	return Call{}
}
func TestConcurrentCallsJoinAndIndependentFailures(t *testing.T) {
	s, c := newCallService()
	ctx := context.Background()
	first, err := s.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(s.Snapshot().Calls) != 2 {
		t.Fatal("attempts collapsed")
	}
	for _, a := range c.actions {
		if a.Get("ChannelId") != a.ActionID() || a.Get("OtherChannelId") != a.ActionID()+"-dial" || !strings.HasSuffix(a.Get("Channel"), "/n") {
			t.Fatal("unstable channel identity", a)
		}
	}
	s.handleEvent(callEvent("OriginateResponse", first, "ActionID", first, "Response", "Success"))
	if len(s.Snapshot().Participants) != 0 || callByID(t, s, first).State != "dialing" {
		t.Fatal("answer incorrectly treated as conference join")
	}
	s.handleEvent(callEvent("NewConnectedLine", first, "ConnectedLineName", "Bob"))
	s.handleEvent(callEvent("ConfbridgeJoin", first, "Conference", "1000", "Channel", "Local/1002;1", "CallerIDNum", "0000"))
	snap := s.Snapshot()
	if len(snap.Participants) != 1 || snap.Participants[0].CallerIDNum != "1002" || snap.Participants[0].CallerIDName != "Bob" || len(snap.Calls) != 1 {
		t.Fatal(snap)
	}
	s.handleEvent(callEvent("DialEnd", second+"-dial", "DialStatus", "BUSY"))
	s.handleEvent(callEvent("OriginateResponse", second, "ActionID", second, "Response", "Failure", "Reason", "0"))
	if failed := callByID(t, s, second); failed.State != "failed" || failed.Reason != "Busy" {
		t.Fatal(failed)
	}
	if len(s.Snapshot().Participants) != 1 {
		t.Fatal("other call affected")
	}
	third, err := s.RetryCall(ctx, second)
	if err != nil || third == second {
		t.Fatal(third, err)
	}
	if len(s.Snapshot().Calls) != 1 || callByID(t, s, third).State != "dialing" {
		t.Fatal(s.Snapshot())
	}
	if _, err = s.RetryCall(ctx, second); !errors.Is(err, ErrCallNotFound) {
		t.Fatal("duplicate retry", err)
	}
}
func TestCancelBeforeChannelAllocationAndAnswerRace(t *testing.T) {
	s, c := newCallService()
	ctx := context.Background()
	id, err := s.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	c.action = func(a *ami.Message) (*ami.Message, error) {
		if a.Get("Action") == "Hangup" {
			if a.Get("Channel") != id {
				t.Fatal("cancelled unrelated channel")
			}
			r := &ami.Message{}
			r.Add("Message", "No such channel")
			return nil, &ami.ActionError{Response: r}
		}
		return &ami.Message{}, nil
	}
	if err = s.CancelCall(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !callByID(t, s, id).Cancelling {
		t.Fatal("forgot cancellation before allocation")
	}
	s.handleEvent(callEvent("Newchannel", id, "Channel", "Local/1002;1"))
	s.handleEvent(callEvent("ConfbridgeJoin", id, "Conference", "1000", "Channel", "Local/1002;1"))
	if len(s.Snapshot().Participants) != 0 || !callByID(t, s, id).Cancelling {
		t.Fatal("answer undid cancellation")
	}
	c.action = func(_ *ami.Message) (*ami.Message, error) { return &ami.Message{}, nil }
	s.maintainCalls(ctx)
	if !callByID(t, s, id).Cancelling {
		t.Fatal("Hangup ack was treated as Hangup event")
	}
	s.handleEvent(callEvent("Hangup", id, "Cause", "16"))
	s.handleEvent(callEvent("OriginateResponse", id, "ActionID", id, "Response", "Failure", "Reason", "0"))
	if snap := s.Snapshot(); len(snap.Calls) != 0 || len(snap.Participants) != 0 {
		t.Fatal("cancelled call returned", snap)
	}
}
func TestFailedCallDismissAndUnknownAcknowledgement(t *testing.T) {
	s, c := newCallService()
	ctx := context.Background()
	c.action = func(_ *ami.Message) (*ami.Message, error) { return nil, context.DeadlineExceeded }
	id, err := s.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	if got := callByID(t, s, id); got.State != "dialing" {
		t.Fatal("uncertain action marked failed", got)
	}
	s.handleEvent(callEvent("OriginateResponse", id, "ActionID", id, "Response", "Failure", "Reason", "5"))
	if got := callByID(t, s, id); got.State != "failed" || got.Reason != "Busy" {
		t.Fatal(got)
	}
	c.connected = false
	if err = s.CancelCall(ctx, id); err != nil {
		t.Fatal("dismiss should work offline", err)
	}
	if len(s.Snapshot().Calls) != 0 {
		t.Fatal("not dismissed")
	}
}
func TestTimeoutResyncDoesNotHangUpMissedJoin(t *testing.T) {
	s, c := newCallService()
	ctx := context.Background()
	id, err := s.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.invites[id].CreatedAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	c.items = []*ami.Message{callEvent("ConfbridgeList", id, "Conference", "1000", "Channel", "Local/1002;1")}
	s.maintainCalls(ctx)
	if len(s.Snapshot().Participants) != 1 || len(s.Snapshot().Calls) != 0 {
		t.Fatal(s.Snapshot())
	}
	for _, a := range c.actions {
		if a.Get("Action") == "Hangup" {
			t.Fatal("hung up an already joined call")
		}
	}
	c.items = nil
	if _, err = s.List(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.invites) != 0 {
		t.Fatal("departed bookkeeping leaked")
	}
}
func TestTimeoutAndDisconnectedCalls(t *testing.T) {
	s, c := newCallService()
	ctx := context.Background()
	id, err := s.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.invites[id].CreatedAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	c.connected = false
	s.maintainCalls(ctx)
	if callByID(t, s, id).State != "dialing" {
		t.Fatal("invented outcome during disconnect")
	}
	c.connected = true
	c.action = func(_ *ami.Message) (*ami.Message, error) {
		r := &ami.Message{}
		r.Add("Message", "No such channel")
		return nil, &ami.ActionError{Response: r}
	}
	s.maintainCalls(ctx)
	if got := callByID(t, s, id); got.State != "failed" || got.Reason != "No answer" {
		t.Fatal(got)
	}
}
func TestRetrySerializedAcrossUsers(t *testing.T) {
	s, _ := newCallService()
	ctx := context.Background()
	id, err := s.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	s.handleEvent(callEvent("OriginateResponse", id, "ActionID", id, "Response", "Failure", "Reason", "5"))
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.RetryCall(ctx, id); out <- err }()
	}
	wg.Wait()
	close(out)
	successes := 0
	for err := range out {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrCallNotFound) {
			t.Fatal(err)
		}
	}
	if successes != 1 || len(s.Snapshot().Calls) != 1 {
		t.Fatal("duplicate redial", successes, s.Snapshot())
	}
}

func TestLateOutcomeRefinesEarlyHangup(t *testing.T) {
	for _, reason := range []struct{ code, want string }{{"5", "Busy"}, {"3", "No answer"}} {
		s, _ := newCallService()
		id, err := s.Invite(context.Background(), "1002")
		if err != nil {
			t.Fatal(err)
		}
		s.handleEvent(callEvent("Hangup", id, "Cause", "0"))
		s.handleEvent(callEvent("OriginateResponse", id, "ActionID", id, "Response", "Failure", "Reason", reason.code))
		if got := callByID(t, s, id); got.Reason != reason.want {
			t.Fatal(got)
		}
	}
}
