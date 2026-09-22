package conference

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dmalkin/westbridge/internal/ami"
	"strings"
	"testing"
	"time"
)

type snapshotClient struct {
	sequence uint64
	items    []*ami.Message
	err      error
}

func (c *snapshotClient) Connected() bool                                            { return true }
func (c *snapshotClient) EventSequence() uint64                                      { return c.sequence }
func (c *snapshotClient) Events() <-chan *ami.Message                                { return nil }
func (c *snapshotClient) Action(context.Context, *ami.Message) (*ami.Message, error) { return nil, nil }
func (c *snapshotClient) ActionList(context.Context, *ami.Message, string) ([]*ami.Message, error) {
	return c.items, c.err
}
func rosterEvent(name, id string, seq uint64) *ami.Message {
	m := ami.NewEvent(name)
	m.Sequence = seq
	m.Add("Conference", "1000")
	m.Add("Uniqueid", id)
	m.Add("Channel", "PJSIP/"+id)
	return m
}
func TestResyncSkipsOlderEventsButAppliesNewEvents(t *testing.T) {
	for _, name := range []string{"ConfbridgeEnd", "ConfbridgeStart", "ConfbridgeLeave", "ConfbridgeJoin"} {
		t.Run(name, func(t *testing.T) {
			item := rosterEvent("ConfbridgeList", "new", 0)
			item.Add("Muted", "Yes")
			c := &snapshotClient{sequence: 10, items: []*ami.Message{item}}
			s := New(c, Config{Room: "1000"})
			if _, err := s.List(context.Background()); err != nil {
				t.Fatal(err)
			}
			s.handleEvent(rosterEvent(name, "new", 10))
			ps := s.Snapshot().Participants
			if len(ps) != 1 || ps[0].UniqueID != "new" || !ps[0].Muted {
				t.Fatalf("old event overwrote snapshot: %+v", ps)
			}
			s.handleEvent(rosterEvent("ConfbridgeLeave", "new", 11))
			if len(s.Snapshot().Participants) != 0 {
				t.Fatal("event after snapshot boundary was lost")
			}
		})
	}
}
func TestFailedResyncStillAppliesQueuedEvents(t *testing.T) {
	c := &snapshotClient{sequence: 10, err: errors.New("list failed")}
	s := New(c, Config{Room: "1000"})
	if _, err := s.List(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	s.handleEvent(rosterEvent("ConfbridgeJoin", "new", 9))
	if len(s.Snapshot().Participants) != 1 {
		t.Fatal("failed snapshot discarded queued join")
	}
}
func TestEmptyResyncSkipsOlderJoin(t *testing.T) {
	response := &ami.Message{}
	response.Add("Message", "No active conferences.")
	c := &snapshotClient{sequence: 10, err: &ami.ActionError{Response: response}}
	s := New(c, Config{Room: "1000"})
	if _, err := s.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.handleEvent(rosterEvent("ConfbridgeJoin", "old", 9))
	if len(s.Snapshot().Participants) != 0 {
		t.Fatal("old join resurrected participant")
	}
}
func TestSnapshotDoesNotMistakeCallAgeForJoinTime(t *testing.T) {
	item := rosterEvent("ConfbridgeList", "new", 0)
	item.Add("AnsweredTime", "600")
	c := &snapshotClient{items: []*ami.Message{item}}
	s := New(c, Config{Room: "1000"})
	snap, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Participants[0].JoinedAt.IsZero() {
		t.Fatal("invented join time from call age")
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "joinedAt") {
		t.Fatalf("unknown time must be omitted: %s", data)
	}
	// A real join gives a known time; future list snapshots preserve it.
	before := time.Now()
	s.handleEvent(rosterEvent("ConfbridgeJoin", "new", 1))
	joined := s.Snapshot().Participants[0].JoinedAt
	if joined.Before(before) || joined.After(time.Now()) {
		t.Fatalf("join not timestamped: %v", joined)
	}
	snap, err = s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Participants[0].JoinedAt.Equal(joined) {
		t.Fatal("resync changed observed join time")
	}
}
