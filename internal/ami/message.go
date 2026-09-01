// Package ami implements the Asterisk Manager Interface wire protocol and a
// client that speaks it over a single TCP connection.
//
// The wire format is plain text: a packet is a sequence of "Key: Value" lines
// terminated by CRLF, and the packet itself is terminated by an empty line.
// Keys may repeat within a packet ("Variable:" in an Originate action is the
// common case), so a message is an ordered list of fields rather than a map.
package ami

import (
	"fmt"
	"io"
	"strings"
)

// Field is a single "Key: Value" line. A Field with an empty Key represents a
// line that carries no colon at all, such as the "--END COMMAND--" marker that
// terminates the output of a "Response: Follows" packet; it is written back out
// verbatim so that decode/encode round-trips.
type Field struct {
	Key   string
	Value string
}

// Message is one AMI packet: an action, a response, or an event.
//
// The zero value is an empty message ready for use. Field order is preserved
// exactly as constructed or decoded, because Asterisk is order-sensitive for
// repeated keys such as Variable.
type Message struct {
	Fields []Field
}

// NewAction returns a message with a single "Action" field, the mandatory first
// field of every request sent to Asterisk.
func NewAction(name string) *Message {
	m := &Message{}
	m.Add("Action", name)
	return m
}

// Add appends a field, keeping any existing field with the same key. This is
// how repeated keys are built up.
func (m *Message) Add(key, value string) {
	m.Fields = append(m.Fields, Field{Key: key, Value: value})
}

// Set replaces every existing field matching key (case-insensitively) with a
// single field holding value, in the position of the first match. If no field
// matches, it appends.
func (m *Message) Set(key, value string) {
	out := m.Fields[:0]
	replaced := false
	for _, f := range m.Fields {
		if !strings.EqualFold(f.Key, key) {
			out = append(out, f)
			continue
		}
		if !replaced {
			out = append(out, Field{Key: key, Value: value})
			replaced = true
		}
	}
	m.Fields = out
	if !replaced {
		m.Add(key, value)
	}
}

// Get returns the value of the first field whose key matches case-insensitively,
// or "" when there is none. Asterisk's own capitalisation is not stable across
// versions, so callers must never compare keys byte for byte.
func (m *Message) Get(key string) string {
	for _, f := range m.Fields {
		if strings.EqualFold(f.Key, key) {
			return f.Value
		}
	}
	return ""
}

// GetAll returns the values of every field matching key, in packet order.
func (m *Message) GetAll(key string) []string {
	var out []string
	for _, f := range m.Fields {
		if strings.EqualFold(f.Key, key) {
			out = append(out, f.Value)
		}
	}
	return out
}

// Has reports whether the message carries at least one field with key.
func (m *Message) Has(key string) bool {
	for _, f := range m.Fields {
		if strings.EqualFold(f.Key, key) {
			return true
		}
	}
	return false
}

// IsResponse reports whether the message is a reply to an action.
func (m *Message) IsResponse() bool { return m.Has("Response") }

// IsEvent reports whether the message is an event. List-style actions reply
// with a response first and then deliver their payload as events, so a message
// can be one or the other but never both.
func (m *Message) IsEvent() bool { return m.Has("Event") }

// EventName returns the value of the Event field, or "" for a non-event.
func (m *Message) EventName() string { return m.Get("Event") }

// ActionID returns the correlation identifier shared by an action, its response
// and any events it produces. It is empty for unsolicited events.
func (m *Message) ActionID() string { return m.Get("ActionID") }

// IsSuccess reports whether the message is a response with a non-error status.
// Asterisk answers with "Success", and with "Goodbye" for Logoff.
func (m *Message) IsSuccess() bool {
	switch strings.ToLower(m.Get("Response")) {
	case "success", "goodbye", "follows":
		return true
	default:
		return false
	}
}

// WriteTo encodes the message in AMI wire format: one CRLF-terminated line per
// field, followed by the empty line that terminates the packet. It implements
// io.WriterTo.
func (m *Message) WriteTo(w io.Writer) (int64, error) {
	var b strings.Builder
	for _, f := range m.Fields {
		if f.Key == "" {
			b.WriteString(f.Value)
		} else {
			b.WriteString(f.Key)
			b.WriteString(": ")
			b.WriteString(f.Value)
		}
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")

	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// String renders the message in wire format, which is also its most readable
// form in a log line.
func (m *Message) String() string {
	var b strings.Builder
	if _, err := m.WriteTo(&b); err != nil {
		return fmt.Sprintf("ami.Message(error: %v)", err)
	}
	return b.String()
}
