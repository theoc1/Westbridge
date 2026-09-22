// Package conference holds the domain layer of the application: what the
// ConfBridge room currently looks like and the three operations the MVP
// exposes on it — list, kick and invite.
//
// Nothing in this package knows about HTTP or WebSockets. It talks to Asterisk
// through an AMI client and publishes full snapshots to subscribers, who are
// free to deliver them however they like.
package conference

import (
	"strings"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
)

// Participant is one channel currently bridged into the conference.
//
// The JSON tags are the wire contract shared with the frontend, so they must
// stay in sync with frontend/src/types.ts.
type Participant struct {
	// UniqueID is the channel's Asterisk unique identifier. It is the roster
	// key: unlike the channel name it is stable and never reused.
	UniqueID string `json:"uniqueid"`
	// Channel is the channel name, e.g. "PJSIP/1001-0000000a". ConfbridgeKick
	// addresses participants by name, so it has to be carried alongside the id.
	Channel      string    `json:"channel"`
	CallerIDNum  string    `json:"callerIdNum"`
	CallerIDName string    `json:"callerIdName"`
	Admin        bool      `json:"admin"`
	Muted        bool      `json:"muted"`
	JoinedAt     time.Time `json:"joinedAt,omitzero"`
}

// participantFrom builds a participant out of a ConfbridgeJoin or
// ConfbridgeList event. It reports false for a message that carries no
// identity, which is how a malformed or unexpected event is discarded.
//
// ConfbridgeList cannot tell when a channel joined: AnsweredTime measures
// the entire answered call. Leave JoinedAt unknown until a join is observed.
func participantFrom(m *ami.Message) (Participant, bool) {
	p := Participant{
		UniqueID:     m.Get("Uniqueid"),
		Channel:      m.Get("Channel"),
		CallerIDNum:  m.Get("CallerIDNum"),
		CallerIDName: m.Get("CallerIDName"),
		Admin:        amiBool(m.Get("Admin")),
		Muted:        amiBool(m.Get("Muted")),
	}
	if p.UniqueID == "" || p.Channel == "" {
		return Participant{}, false
	}
	return p, true
}

// amiBool reads Asterisk's boolean spelling. The manager interface is not
// consistent about it: ConfbridgeList says "Yes"/"No" while other events use
// "1"/"0" or "true"/"false".
func amiBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "true", "on", "1":
		return true
	default:
		return false
	}
}
