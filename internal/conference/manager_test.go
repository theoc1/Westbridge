package conference

import (
	"context"
	"errors"
	"testing"

	"github.com/dmalkin/westbridge/internal/database"
	"github.com/dmalkin/westbridge/internal/rooms"
)

type registryFake struct {
	enabled map[string]string
	busy    bool
	fail    error
}

func (r *registryFake) Ensure(_ context.Context, n, b string) error { r.enabled[n] = b; return r.fail }
func (r *registryFake) Disable(_ context.Context, n, b string) error {
	if r.enabled[n] == b {
		delete(r.enabled, n)
	}
	return r.fail
}
func (r *registryFake) Occupied(context.Context, string) (bool, error) { return r.busy, r.fail }

func TestManagerReconcileAndDeletionRace(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store := rooms.New(db, 7000, 7999)
	ctx := context.Background()
	a, _ := store.Create(ctx, "A", "7000")
	b, _ := store.Create(ctx, "B", "7001")
	registry := &registryFake{enabled: map[string]string{}, fail: context.DeadlineExceeded}
	manager := NewManager(&callClient{connected: true}, store, registry, Config{OriginateContext: "out"})
	manager.reconcile(ctx)
	current, _ := store.Get(ctx, a.ID)
	if current.State != "pending" || current.Error == "" {
		t.Fatal("lost ack marked ready", current)
	}
	registry.fail = nil
	manager.reconcile(ctx)
	current, _ = store.Get(ctx, a.ID)
	if current.State != "ready" || len(registry.enabled) != 2 {
		t.Fatal(current)
	}
	registry.busy = true
	if err = manager.Delete(ctx, current); !errors.Is(err, rooms.ErrBusy) {
		t.Fatal(err)
	}
	registry.busy = false
	if err = manager.Delete(ctx, current); err != nil {
		t.Fatal(err)
	}
	registry.busy = true // channel admitted immediately before admission closes
	manager.reconcile(ctx)
	current, _ = store.Get(ctx, a.ID)
	if current.State != "deleting" || registry.enabled[a.Number] != "" {
		t.Fatal("racing join lost", current)
	}
	// Pending deletion must survive a coordinator restart.
	manager = NewManager(&callClient{connected: true}, store, registry, Config{})
	registry.busy = false
	manager.reconcile(ctx)
	current, _ = store.Get(ctx, a.ID)
	if current.State != "deleted" {
		t.Fatal(current)
	}
	if registry.enabled[b.Number] != b.Bridge {
		t.Fatal("other room changed")
	}
	// Recreate the coordinator: tombstones must disable stray external mappings.
	registry.enabled[a.Number] = a.Bridge
	NewManager(&callClient{connected: true}, store, registry, Config{}).reconcile(ctx)
	if registry.enabled[a.Number] != "" {
		t.Fatal("deleted room resurrected")
	}
}

func TestManagerRoutesRoomAndAttemptEvents(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store := rooms.New(db, 7000, 7999)
	ctx := context.Background()
	a, _ := store.Create(ctx, "A", "7000")
	b, _ := store.Create(ctx, "B", "7001")
	manager := NewManager(&callClient{connected: true}, store, &registryFake{enabled: map[string]string{}}, Config{OriginateContext: "out"})
	first, second := manager.Runtime(a), manager.Runtime(b)
	one, err := first.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.Invite(ctx, "1002")
	if err != nil {
		t.Fatal(err)
	}
	manager.dispatch(callEvent("OriginateResponse", one, "ActionID", one, "Response", "Success"))
	event := <-manager.runtimes[a.ID].client.events
	first.handleEvent(event)
	select {
	case <-manager.runtimes[b.ID].client.events:
		t.Fatal("answer leaked into other room")
	default:
	}
	manager.dispatch(callEvent("ConfbridgeJoin", one, "Conference", a.Bridge, "Channel", "Local/a"))
	first.handleEvent(<-manager.runtimes[a.ID].client.events)
	manager.dispatch(callEvent("ConfbridgeTalking", one, "Conference", a.Bridge, "TalkingStatus", "on"))
	first.handleEvent(<-manager.runtimes[a.ID].client.events)
	if len(first.Snapshot().Participants) != 1 || !first.Snapshot().Participants[0].Talking {
		t.Fatal(first.Snapshot())
	}
	if len(second.Snapshot().Participants) != 0 || callByID(t, second, two).State != "dialing" {
		t.Fatal(second.Snapshot())
	}
	manager.dispatch(callEvent("OriginateResponse", two, "ActionID", two, "Response", "Failure", "Reason", "5"))
	second.handleEvent(<-manager.runtimes[b.ID].client.events)
	if callByID(t, second, two).Reason != "Busy" {
		t.Fatal(second.Snapshot())
	}
}

func (r *registryFake) Entries(context.Context) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range r.enabled {
		out[k] = v
	}
	return out, r.fail
}
