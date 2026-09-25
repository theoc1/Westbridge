package ami

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func decodeAll(t *testing.T, wire string) []*Message {
	t.Helper()
	d := NewDecoder(strings.NewReader(wire))
	var out []*Message
	for {
		msg, err := d.Decode()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		out = append(out, msg)
	}
}

func TestMessageWriteTo(t *testing.T) {
	m := NewAction("Originate")
	m.Add("Channel", "Local/1002@conference-out")
	m.Add("Variable", "a=1")
	m.Add("Variable", "b=2")

	want := "Action: Originate\r\n" +
		"Channel: Local/1002@conference-out\r\n" +
		"Variable: a=1\r\n" +
		"Variable: b=2\r\n" +
		"\r\n"

	var b strings.Builder
	n, err := m.WriteTo(&b)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if b.String() != want {
		t.Errorf("wire format:\n got %q\nwant %q", b.String(), want)
	}
	if n != int64(len(want)) {
		t.Errorf("WriteTo returned n = %d, want %d", n, len(want))
	}
	if m.String() != want {
		t.Errorf("String() = %q, want %q", m.String(), want)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	m := NewAction("Originate")
	m.Add("Variable", "a=1")
	m.Add("Variable", "b=2")
	m.Add("Empty", "")
	m.Add("", "--END COMMAND--")

	var b strings.Builder
	if _, err := m.WriteTo(&b); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	got := decodeAll(t, b.String())
	if len(got) != 1 {
		t.Fatalf("decoded %d packets, want 1", len(got))
	}
	if len(got[0].Fields) != len(m.Fields) {
		t.Fatalf("decoded fields = %#v, want %#v", got[0].Fields, m.Fields)
	}
	for i, f := range m.Fields {
		if got[0].Fields[i] != f {
			t.Errorf("field %d = %#v, want %#v", i, got[0].Fields[i], f)
		}
	}
}

func TestMessageMultiValueKeys(t *testing.T) {
	m := &Message{}
	m.Add("Variable", "a=1")
	m.Add("variable", "b=2")
	m.Add("VARIABLE", "c=3")

	if got := m.GetAll("Variable"); len(got) != 3 || got[0] != "a=1" || got[2] != "c=3" {
		t.Errorf("GetAll = %#v, want all three values in order", got)
	}
	if got := m.Get("VaRiAbLe"); got != "a=1" {
		t.Errorf("Get = %q, want the first value %q", got, "a=1")
	}
	if got := m.GetAll("Missing"); got != nil {
		t.Errorf("GetAll(missing) = %#v, want nil", got)
	}
	if m.Get("Missing") != "" || m.Has("Missing") {
		t.Error("missing key reported as present")
	}
}

func TestMessageSet(t *testing.T) {
	m := &Message{}
	m.Add("Action", "Login")
	m.Add("Variable", "a=1")
	m.Add("Secret", "old")
	m.Add("variable", "b=2")

	m.Set("VARIABLE", "only=1")
	m.Set("ActionID", "42")

	want := []Field{
		{"Action", "Login"},
		{"VARIABLE", "only=1"},
		{"Secret", "old"},
		{"ActionID", "42"},
	}
	if len(m.Fields) != len(want) {
		t.Fatalf("fields = %#v, want %#v", m.Fields, want)
	}
	for i, f := range want {
		if m.Fields[i] != f {
			t.Errorf("field %d = %#v, want %#v", i, m.Fields[i], f)
		}
	}
}

func TestMessageHelpers(t *testing.T) {
	tests := []struct {
		name       string
		wire       string
		isResponse bool
		isEvent    bool
		isSuccess  bool
		eventName  string
		actionID   string
	}{
		{
			name:       "response",
			wire:       "Response: Success\r\nActionID: 7\r\nMessage: Authentication accepted\r\n\r\n",
			isResponse: true,
			isSuccess:  true,
			actionID:   "7",
		},
		{
			name:       "error response",
			wire:       "Response: Error\r\nMessage: Authentication failed\r\n\r\n",
			isResponse: true,
		},
		{
			name:      "list event",
			wire:      "Event: ConfbridgeList\r\nActionID: 7\r\nConference: 1000\r\n\r\n",
			isEvent:   true,
			eventName: "ConfbridgeList",
			actionID:  "7",
		},
		{
			name:      "unsolicited event",
			wire:      "event: ConfbridgeJoin\r\nConference: 1000\r\n\r\n",
			isEvent:   true,
			eventName: "ConfbridgeJoin",
		},
		{
			name:       "goodbye counts as success",
			wire:       "Response: Goodbye\r\n\r\n",
			isResponse: true,
			isSuccess:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := decodeAll(t, tc.wire)
			if len(msgs) != 1 {
				t.Fatalf("decoded %d packets, want 1", len(msgs))
			}
			m := msgs[0]
			if m.IsResponse() != tc.isResponse {
				t.Errorf("IsResponse = %v, want %v", m.IsResponse(), tc.isResponse)
			}
			if m.IsEvent() != tc.isEvent {
				t.Errorf("IsEvent = %v, want %v", m.IsEvent(), tc.isEvent)
			}
			if m.IsSuccess() != tc.isSuccess {
				t.Errorf("IsSuccess = %v, want %v", m.IsSuccess(), tc.isSuccess)
			}
			if m.EventName() != tc.eventName {
				t.Errorf("EventName = %q, want %q", m.EventName(), tc.eventName)
			}
			if m.ActionID() != tc.actionID {
				t.Errorf("ActionID = %q, want %q", m.ActionID(), tc.actionID)
			}
		})
	}
}

func TestDecoderLineEndingTolerance(t *testing.T) {
	// Bare LF, mixed CRLF, extra spacing around the separator and stray blank
	// lines between packets all have to decode identically.
	wire := "Event: ConfbridgeJoin\nConference:   1000  \r\n\n" +
		"\r\n" +
		"Event: ConfbridgeLeave\r\nConference: 1000\r\n\r\n"

	msgs := decodeAll(t, wire)
	if len(msgs) != 2 {
		t.Fatalf("decoded %d packets, want 2", len(msgs))
	}
	if got := msgs[0].EventName(); got != "ConfbridgeJoin" {
		t.Errorf("first event = %q", got)
	}
	if got := msgs[0].Get("Conference"); got != "1000" {
		t.Errorf("Conference = %q, want %q (surrounding spaces trimmed)", got, "1000")
	}
	if got := msgs[1].EventName(); got != "ConfbridgeLeave" {
		t.Errorf("second event = %q", got)
	}
}

func TestDecoderBanner(t *testing.T) {
	wire := "Asterisk Call Manager/2.10.6\r\nResponse: Success\r\n\r\n"
	d := NewDecoder(strings.NewReader(wire))

	banner, err := d.ReadBanner()
	if err != nil {
		t.Fatalf("ReadBanner: %v", err)
	}
	if banner != "Asterisk Call Manager/2.10.6" {
		t.Errorf("banner = %q", banner)
	}

	msg, err := d.Decode()
	if err != nil {
		t.Fatalf("Decode after banner: %v", err)
	}
	if !msg.IsSuccess() {
		t.Errorf("packet after banner = %q, want a success response", msg)
	}
}

func TestDecoderBannerFromWrongService(t *testing.T) {
	d := NewDecoder(strings.NewReader("HTTP/1.1 400 Bad Request\r\n\r\n"))
	if _, err := d.ReadBanner(); !errors.Is(err, ErrNotAsterisk) {
		t.Fatalf("ReadBanner error = %v, want ErrNotAsterisk", err)
	}
}

func TestDecoderPacketTooLarge(t *testing.T) {
	t.Run("one long line", func(t *testing.T) {
		wire := "Event: Big\r\nData: " + strings.Repeat("x", 512) + "\r\n\r\n"
		d := NewDecoder(strings.NewReader(wire))
		d.MaxPacketSize = 128
		if _, err := d.Decode(); !errors.Is(err, ErrPacketTooLarge) {
			t.Fatalf("Decode error = %v, want ErrPacketTooLarge", err)
		}
	})

	t.Run("many short lines", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("Event: Big\r\n")
		for i := 0; i < 100; i++ {
			b.WriteString("Variable: x\r\n")
		}
		b.WriteString("\r\n")

		d := NewDecoder(strings.NewReader(b.String()))
		d.MaxPacketSize = 128
		if _, err := d.Decode(); !errors.Is(err, ErrPacketTooLarge) {
			t.Fatalf("Decode error = %v, want ErrPacketTooLarge", err)
		}
	})

	t.Run("under the cap", func(t *testing.T) {
		// Longer than bufio's internal buffer, so the reader has to stitch
		// several fragments together.
		value := strings.Repeat("y", 10000)
		msgs := decodeAll(t, "Event: Big\r\nData: "+value+"\r\n\r\n")
		if len(msgs) != 1 {
			t.Fatalf("decoded %d packets, want 1", len(msgs))
		}
		if got := msgs[0].Get("Data"); got != value {
			t.Errorf("Data length = %d, want %d", len(got), len(value))
		}
	})
}

func TestDecoderEOF(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		d := NewDecoder(strings.NewReader("Response: Success\r\n\r\n"))
		if _, err := d.Decode(); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if _, err := d.Decode(); !errors.Is(err, io.EOF) {
			t.Fatalf("Decode at end = %v, want io.EOF", err)
		}
	})

	t.Run("truncated packet", func(t *testing.T) {
		d := NewDecoder(strings.NewReader("Response: Success\r\nActionID: 1\r\n"))
		if _, err := d.Decode(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("Decode error = %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("truncated line", func(t *testing.T) {
		d := NewDecoder(strings.NewReader("Response: Suc"))
		if _, err := d.Decode(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("Decode error = %v, want io.ErrUnexpectedEOF", err)
		}
	})
}

func TestDecoderListSequence(t *testing.T) {
	// The shape every list-style action produces: a response, N events, then a
	// terminator, all sharing one ActionID.
	wire := "Response: Success\r\nActionID: 3\r\nEventList: start\r\n\r\n" +
		"Event: ConfbridgeList\r\nActionID: 3\r\nUniqueid: 1756.1\r\nChannel: PJSIP/1001-0000000a\r\n\r\n" +
		"Event: ConfbridgeList\r\nActionID: 3\r\nUniqueid: 1756.2\r\nChannel: PJSIP/1002-0000000b\r\n\r\n" +
		"Event: ConfbridgeListComplete\r\nActionID: 3\r\nListItems: 2\r\n\r\n"

	msgs := decodeAll(t, wire)
	if len(msgs) != 4 {
		t.Fatalf("decoded %d packets, want 4", len(msgs))
	}
	if !msgs[0].IsResponse() || msgs[0].IsEvent() {
		t.Errorf("first packet = %q, want a response", msgs[0])
	}
	for _, m := range msgs {
		if m.ActionID() != "3" {
			t.Errorf("packet %q lost its ActionID", m)
		}
	}
	if msgs[3].EventName() != "ConfbridgeListComplete" {
		t.Errorf("terminator = %q", msgs[3].EventName())
	}
}

// A newline inside a field would end the packet early and let the rest of the
// value be read as an action of its own, so WriteTo refuses it. Reaching this
// needs a value that survived telephony.NormalizeNumber, but the codec is the
// last place that can still tell, so it is the place that checks.
func TestMessageWriteToRejectsNewlines(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"LF in value", "Channel", "Local/1002@out\nAction: Command"},
		{"CR in value", "Channel", "Local/1002@out\rAction: Command"},
		{"LF in key", "Chan\nnel", "Local/1002@out"},
		{"CR in key", "Chan\rnel", "Local/1002@out"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewAction("Originate")
			m.Add(tt.key, tt.value)

			var b strings.Builder
			n, err := m.WriteTo(&b)
			if !errors.Is(err, ErrInvalidField) {
				t.Fatalf("WriteTo error = %v, want ErrInvalidField", err)
			}
			if n != 0 {
				t.Errorf("WriteTo wrote n = %d bytes, want 0", n)
			}
			if b.String() != "" {
				t.Errorf("WriteTo emitted %q, want nothing", b.String())
			}
			// String falls back to the error, which escapes the value, so
			// a log line can never show the injected action on a line of
			// its own.
			s := m.String()
			if !strings.HasPrefix(s, "ami.Message(error:") {
				t.Errorf("String() = %q, want the error form", s)
			}
			if strings.ContainsAny(s, "\r\n") {
				t.Errorf("String() = %q, want no raw newline", s)
			}
		})
	}
}
