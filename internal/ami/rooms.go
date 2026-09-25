package ami

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type roomRegistryClient interface {
	callTransport
	ActionList(context.Context, *Message, string) ([]*Message, error)
}

// RoomRegistry implements the managed dialplan contract, isolated from lifecycle policy.
type RoomRegistry struct{ client roomRegistryClient }

// NewRoomRegistry uses the existing AMI connection.
func NewRoomRegistry(client roomRegistryClient) *RoomRegistry {
	return &RoomRegistry{client: client}
}

func (r *RoomRegistry) value(ctx context.Context, variable string) (string, error) {
	a := NewAction("Getvar")
	a.Add("Variable", variable)
	response, err := r.client.Action(ctx, a)
	if err != nil {
		return "", err
	}
	return response.Get("Value"), nil
}

// Ensure provisions one admission mapping and verifies it was applied.
func (r *RoomRegistry) Ensure(ctx context.Context, number, bridge string) error {
	version, err := r.value(ctx, "GLOBAL(WB_ROOM_CONTRACT)")
	if err != nil {
		return err
	}
	if version != "1" {
		return errors.New("managed room dialplan v1 is not installed")
	}
	a := NewAction("DBPut")
	a.Add("Family", "westbridge-rooms")
	a.Add("Key", number)
	a.Add("Val", bridge)
	if _, err = r.client.Action(ctx, a); err != nil {
		return err
	}
	value, err := r.entry(ctx, number)
	if err != nil {
		return err
	}
	if value != bridge {
		return errors.New("room registration verification failed")
	}
	return nil
}

// Disable closes admission. Missing entries are an idempotent success only after read-back.
func (r *RoomRegistry) Disable(ctx context.Context, number, bridge string) error {
	current, err := r.entry(ctx, number)
	if err != nil {
		return err
	}
	if current == "" || current != bridge {
		return nil
	} // A later room may reuse this number.
	a := NewAction("DBDel")
	a.Add("Family", "westbridge-rooms")
	a.Add("Key", number)
	_, actionErr := r.client.Action(ctx, a)
	value, err := r.entry(ctx, number)
	if err != nil {
		return err
	}
	if value != "" {
		if actionErr != nil {
			return actionErr
		}
		return errors.New("room admission remains enabled")
	}
	return nil
}

// Occupied includes channels admitted before gate closure and outgoing dial legs,
// not just participants that have already entered ConfBridge.
func (r *RoomRegistry) Occupied(ctx context.Context, bridge string) (bool, error) {
	value, err := r.value(ctx, "GROUP_COUNT("+bridge+"@westbridge)")
	if err != nil {
		return false, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("invalid admission count %q", value)
	}
	return n > 0, nil
}

func (r *RoomRegistry) entry(ctx context.Context, number string) (string, error) {
	a := NewAction("DBGet")
	a.Add("Family", "westbridge-rooms")
	a.Add("Key", number)
	events, err := r.client.ActionList(ctx, a, "DBGetComplete")
	if err != nil {
		var rejected *ActionError
		if errors.As(err, &rejected) && strings.Contains(strings.ToLower(rejected.Response.Get("Message")), "not found") {
			return "", nil
		}
		return "", err
	}
	for _, event := range events {
		if event.EventName() == "DBGetResponse" {
			return event.Get("Val"), nil
		}
	}
	return "", errors.New("missing registry response")
}

// Entries enumerates only this installation's owned admission namespace.
func (r *RoomRegistry) Entries(ctx context.Context) (map[string]string, error) {
	action := NewAction("DBGetTree")
	action.Add("Family", "westbridge-rooms")
	events, err := r.client.ActionList(ctx, action, "DBGetTreeComplete")
	if err != nil {
		return nil, err
	}
	entries := map[string]string{}
	for _, event := range events {
		if event.EventName() != "DBGetTreeResponse" {
			continue
		}
		number, ok := strings.CutPrefix(event.Get("Key"), "/westbridge-rooms/")
		if ok && number != "" && !strings.Contains(number, "/") {
			entries[number] = event.Get("Val")
		}
	}
	return entries, nil
}
