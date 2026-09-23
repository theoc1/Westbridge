package conference

import "testing"

func TestTalkingLifecycle(t *testing.T) {
	s, c := newCallService()
	s.roster.Add(Participant{UniqueID: "one", Channel: "PJSIP/one", CallerIDName: "Alice"})
	emit := func(id, room, status string) {
		s.handleEvent(callEvent("ConfbridgeTalking", id, "Conference", room, "TalkingStatus", status))
	}
	talking := func() bool { p, _ := s.roster.Get("one"); return p.Talking }
	updates := 0
	unsub := s.Subscribe(func(Snapshot) { updates++ })
	defer unsub()
	emit("one", "other", "on")
	emit("missing", "1000", "on")
	emit("one", "1000", "invalid")
	if talking() || s.roster.Len() != 1 || updates != 1 {
		t.Fatal("invalid event changed roster")
	}
	emit("one", "1000", "on")
	if !talking() || updates != 2 {
		t.Fatal("start not published")
	}
	emit("one", "1000", "on")
	if updates != 2 {
		t.Fatal("duplicate event published")
	}
	// ConfbridgeList does not include talking state; resync must retain it.
	s.roster.Replace([]Participant{{UniqueID: "one", Channel: "PJSIP/one", CallerIDName: "Alice"}})
	if !talking() {
		t.Fatal("resync interrupted speaking")
	}
	emit("one", "1000", "off")
	if talking() {
		t.Fatal("stop ignored")
	}
	emit("one", "1000", "on")
	s.handleEvent(callEvent("ConfbridgeMute", "one", "Conference", "1000"))
	if talking() {
		t.Fatal("mute retained speaking")
	}
	emit("one", "1000", "on")
	if talking() {
		t.Fatal("muted participant marked speaking")
	}
	s.handleEvent(callEvent("ConfbridgeUnmute", "one", "Conference", "1000"))
	if talking() {
		t.Fatal("unmute restored stale speech")
	}
	emit("one", "1000", "on")
	c.connected = false
	s.OnAMIStateChange(false)
	if talking() {
		t.Fatal("disconnect retained speech")
	}
	emit("one", "1000", "on")
	if talking() {
		t.Fatal("offline event applied")
	}
	c.connected = true
	s.OnAMIStateChange(true)
	emit("one", "1000", "on")
	s.roster.Remove("one")
	emit("one", "1000", "off")
	if s.roster.Len() != 0 {
		t.Fatal("late event recreated participant")
	}
}

func TestTalkingEventsSurviveRosterBoundary(t *testing.T) {
	s, _ := newCallService()
	s.roster.Add(Participant{UniqueID: "one", Channel: "PJSIP/one"})
	s.roster.SetTalking("one", true)
	s.ignoreThrough = 10
	event := callEvent("ConfbridgeTalking", "one", "Conference", "1000", "TalkingStatus", "off")
	event.Sequence = 9
	s.handleEvent(event)
	if p, _ := s.roster.Get("one"); p.Talking {
		t.Fatal("roster boundary discarded silence event")
	}
	s.talkingIgnoreThrough.Store(10)
	event = callEvent("ConfbridgeTalking", "one", "Conference", "1000", "TalkingStatus", "on")
	event.Sequence = 9
	s.handleEvent(event)
	if p, _ := s.roster.Get("one"); p.Talking {
		t.Fatal("old connection event restored speech")
	}
}
