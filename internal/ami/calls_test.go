package ami

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dmalkin/westbridge/internal/telephony"
)

type callTransportStub struct {
	last *Message
	err  error
}

func (s *callTransportStub) Connected() bool { return true }
func (s *callTransportStub) Action(_ context.Context, m *Message) (*Message, error) {
	s.last = m
	return nil, s.err
}

func TestCallControllerProtocol(t *testing.T) {
	transport := &callTransportStub{}
	controller := NewCallController(transport, CallConfig{Room: "1000", Context: "out", CallerID: "Bridge", Timeout: 30 * time.Second})
	if err := controller.Originate(context.Background(), "attempt", "1002"); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"Action": "Originate", "ActionID": "attempt", "Channel": "Local/1002@out/n", "ChannelId": "attempt", "OtherChannelId": "attempt-dial", "Application": "ConfBridge", "Data": "1000", "CallerID": "Bridge", "Timeout": "30000", "Async": "true"} {
		if got := transport.last.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	response := &Message{}
	response.Add("Message", "No such channel")
	transport.err = &ActionError{Response: response}
	if err := controller.Hangup(context.Background(), "attempt"); !errors.Is(err, telephony.ErrChannelNotFound) {
		t.Fatal(err)
	}
	if transport.last.Get("Channel") != "attempt" {
		t.Fatal("wrong channel identity")
	}
	if err := controller.Originate(context.Background(), "attempt", "1002"); !errors.Is(err, telephony.ErrRejected) {
		t.Fatal(err)
	}
	transport.err = context.DeadlineExceeded
	if err := controller.Originate(context.Background(), "attempt", "1002"); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, telephony.ErrRejected) {
		t.Fatal("uncertain outcome treated as rejection", err)
	}
}

func TestDecodeCallEvent(t *testing.T) {
	for _, tc := range []struct {
		name, id, key, value string
		kind                 telephony.EventKind
		failure              telephony.Failure
		accepted             bool
	}{
		{"Newchannel", "a", "", "", telephony.ChannelObserved, "", true},
		{"Newchannel", "a-dial", "", "", 0, "", false},
		{"DialEnd", "a-dial", "DialStatus", "BUSY", telephony.FailureObserved, telephony.Busy, true},
		{"DialEnd", "a-dial", "DialStatus", "ANSWER", 0, "", false},
		{"Hangup", "a-dial", "Cause", "17", telephony.FailureObserved, telephony.Busy, true},
		{"Hangup", "a-dial", "Cause", "16", 0, "", false},
		{"Hangup", "a", "Cause", "16", telephony.Ended, telephony.NoAnswer, true},
		{"OriginateResponse", "unrelated", "Response", "Success", telephony.Answered, "", true},
		{"OriginateResponse", "unrelated", "Reason", "5", telephony.OriginationFailed, telephony.Busy, true},
		{"NewConnectedLine", "a-dial", "ConnectedLineName", "Bob", telephony.NameUpdated, "", true},
		{"NewConnectedLine", "a", "ConnectedLineName", "<unknown>", 0, "", false},
	} {
		t.Run(tc.name+tc.id+tc.value, func(t *testing.T) {
			m := NewEvent(tc.name)
			m.Add("Uniqueid", tc.id)
			m.Add("ActionID", "a")
			m.Add(tc.key, tc.value)
			e, ok := DecodeCallEvent(m)
			if ok != tc.accepted {
				t.Fatalf("accepted=%v", ok)
			}
			if ok && (e.ID != "a" || e.Kind != tc.kind || e.Failure != tc.failure) {
				t.Fatalf("unexpected event: %+v", e)
			}
		})
	}
}

func TestManagedOriginateUsesAdmissionGate(t *testing.T) {
	transport := &callTransportStub{}
	controller := NewCallController(transport, CallConfig{Room: "wb-room", AdmissionNumber: "7000", Context: "conference-out"})
	if err := controller.Originate(context.Background(), "attempt", "1002"); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"Context": "westbridge-join", "Exten": "7000", "Priority": "1", "Application": "", "Data": "", "ChannelId": "attempt", "OtherChannelId": "attempt-dial"} {
		if got := transport.last.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
