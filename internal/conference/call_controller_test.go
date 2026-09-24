package conference

import (
	"context"
	"github.com/dmalkin/westbridge/internal/telephony"
	"testing"
)

type semanticCalls struct{ hangups []string }

func (*semanticCalls) Connected() bool                                 { return true }
func (*semanticCalls) Originate(context.Context, string, string) error { return nil }
func (c *semanticCalls) Hangup(_ context.Context, id string) error {
	c.hangups = append(c.hangups, id)
	return telephony.ErrChannelNotFound
}

// Exercise cancellation and a late answer without any AMI messages or actions.
func TestCallCancellationWithSemanticController(t *testing.T) {
	s, _ := newCallService()
	calls := &semanticCalls{}
	s.calls = calls
	id, err := s.Invite(context.Background(), "1002")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CancelCall(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	s.handleCallEvent(telephony.Event{ID: id, Kind: telephony.Answered})
	if !callByID(t, s, id).Cancelling {
		t.Fatal("late answer undid cancellation")
	}
	s.handleCallEvent(telephony.Event{ID: id, Kind: telephony.Ended, Failure: telephony.NoAnswer})
	if len(s.Snapshot().Calls) != 0 || len(calls.hangups) != 1 || calls.hangups[0] != id {
		t.Fatal("cancellation was not completed")
	}
}
