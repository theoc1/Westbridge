package ami

import (
	"context"
	"errors"
	"testing"
)

type registryTransport struct {
	values   map[string]string
	failRead bool
}

func (*registryTransport) Connected() bool { return true }
func (r *registryTransport) Action(_ context.Context, a *Message) (*Message, error) {
	out := &Message{}
	switch a.Get("Action") {
	case "DBPut":
		r.values[a.Get("Key")] = a.Get("Val")
	case "DBDel":
		delete(r.values, a.Get("Key"))
	case "Getvar":
		switch a.Get("Variable") {
		case "GLOBAL(WB_ROOM_CONTRACT)":
			out.Add("Value", "1")
		default:
			out.Add("Value", "0")
		}
	}
	return out, nil
}
func (r *registryTransport) ActionList(_ context.Context, a *Message, end string) ([]*Message, error) {
	if a.Get("Action") != "DBGet" || end != "DBGetComplete" {
		return nil, errors.New("incorrect registry read")
	}
	if r.failRead {
		return nil, context.DeadlineExceeded
	}
	if value := r.values[a.Get("Key")]; value != "" {
		event := NewEvent("DBGetResponse")
		event.Add("Val", value)
		return []*Message{event}, nil
	}
	response := &Message{}
	response.Add("Message", "Database entry not found")
	return nil, &ActionError{Response: response}
}
func TestRoomRegistryVerification(t *testing.T) {
	transport := &registryTransport{values: map[string]string{}}
	r := NewRoomRegistry(transport)
	ctx := context.Background()
	if err := r.Ensure(ctx, "7000", "wb-a"); err != nil {
		t.Fatal(err)
	}
	transport.failRead = true
	if err := r.Ensure(ctx, "7001", "wb-b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("accepted unverified write", err)
	}
	transport.failRead = false
	if err := r.Disable(ctx, "7000", "wb-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Disable(ctx, "7000", "wb-a"); err != nil {
		t.Fatal("non-idempotent deletion", err)
	}
	if err := r.Disable(ctx, "7001", "old-bridge"); err != nil {
		t.Fatal(err)
	}
	if transport.values["7001"] != "wb-b" {
		t.Fatal("other room removed")
	}
}
