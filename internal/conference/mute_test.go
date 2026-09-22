package conference

import (
	"context"
	"errors"
	"testing"

	"github.com/dmalkin/westbridge/internal/ami"
)

func TestMuteActionsAndEvents(t *testing.T) {
	s, c := newCallService()
	s.roster.Add(Participant{UniqueID: "one", Channel: "PJSIP/1002-000001", CallerIDName: "Alice"})
	original, _ := s.roster.Get("one")
	notifications := 0
	unsub := s.Subscribe(func(Snapshot) { notifications++ })
	defer unsub()
	for _, muted := range []bool{true, false} {
		name := "ConfbridgeMute"
		if !muted {
			name = "ConfbridgeUnmute"
		}
		c.items = []*ami.Message{callEvent("ConfbridgeList", "one", "Conference", "1000", "Channel", original.Channel, "CallerIDName", "Alice", "Muted", map[bool]string{true: "Yes", false: "No"}[muted])}
		if err := s.SetMuted(context.Background(), "one", muted); err != nil {
			t.Fatal(err)
		}
		a := c.actions[len(c.actions)-1]
		if a.Get("Action") != name || a.Get("Channel") != original.Channel || a.Get("Conference") != "1000" {
			t.Fatal(a)
		}
		p, _ := s.roster.Get("one")
		if p.Muted != muted || p.JoinedAt != original.JoinedAt {
			t.Fatal(p)
		}
	}
	s.handleEvent(callEvent("ConfbridgeMute", "one", "Conference", "other"))
	if p, _ := s.roster.Get("one"); p.Muted {
		t.Fatal("foreign room affected participant")
	}
	s.handleEvent(callEvent("ConfbridgeMute", "one", "Conference", "1000"))
	if p, _ := s.roster.Get("one"); !p.Muted || p.CallerIDName != "Alice" {
		t.Fatal(p)
	}
	s.handleEvent(callEvent("ConfbridgeUnmute", "one", "Conference", "1000"))
	if p, _ := s.roster.Get("one"); p.Muted {
		t.Fatal(p)
	}
	if notifications < 5 {
		t.Fatal("updates not published", notifications)
	}
	s.roster.Remove("one")
	s.handleEvent(callEvent("ConfbridgeMute", "one", "Conference", "1000"))
	if s.roster.Len() != 0 {
		t.Fatal("late event recreated departed participant")
	}
	before := len(c.actions)
	if err := s.SetMuted(context.Background(), "one", true); !errors.Is(err, ErrParticipantNotFound) || len(c.actions) != before {
		t.Fatal(err)
	}
}

func TestMuteFailureDoesNotChangeState(t *testing.T) {
	s, c := newCallService()
	s.roster.Add(Participant{UniqueID: "one", Channel: "PJSIP/1002-000001"})
	c.action = func(*ami.Message) (*ami.Message, error) { return nil, ami.ErrDisconnected }
	if err := s.SetMuted(context.Background(), "one", true); !errors.Is(err, ami.ErrDisconnected) {
		t.Fatal(err)
	}
	if p, _ := s.roster.Get("one"); p.Muted {
		t.Fatal("failed action changed state")
	}
}
