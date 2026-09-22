package ami

import "testing"

func TestAsyncOutcomeCannotBeSwallowedByPendingAcknowledgement(t *testing.T) {
	c := New(Config{})
	sess := &session{pending: make(map[string]*pending), done: make(chan struct{})}
	p := newPending()
	sess.pending["call"] = p
	ack := &Message{}
	ack.Add("Response", "Success")
	ack.Add("ActionID", "call")
	outcome := NewEvent("OriginateResponse")
	outcome.Add("Response", "Failure")
	outcome.Add("ActionID", "call")
	outcome.Add("Reason", "5")
	// Both orders occur on real Asterisk. The pending action may still be
	// registered even after its acknowledgement was put on its channel.
	for _, firstOutcome := range []bool{false, true} {
		if firstOutcome {
			c.route(sess, outcome)
			c.route(sess, ack)
		} else {
			c.route(sess, ack)
			c.route(sess, outcome)
		}
		if got := <-p.ch; got != ack {
			t.Fatal("event consumed as command response")
		}
		select {
		case got := <-c.Events():
			if got != outcome {
				t.Fatal("wrong event")
			}
		default:
			t.Fatal("async outcome lost")
		}
	}
}
func TestListEventsStillRouteToListAction(t *testing.T) {
	c := New(Config{})
	sess := &session{pending: make(map[string]*pending), done: make(chan struct{})}
	p := newPending()
	p.list = true
	sess.pending["list"] = p
	event := NewEvent("ConfbridgeListComplete")
	event.Add("ActionID", "list")
	c.route(sess, event)
	select {
	case got := <-p.ch:
		if got != event {
			t.Fatal("wrong event")
		}
	default:
		t.Fatal("list completion lost")
	}
}
