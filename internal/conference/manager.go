package conference

import (
	"context"
	"sync"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
	"github.com/dmalkin/westbridge/internal/rooms"
)

// Provisioner translates room lifecycle operations into external-system effects.
type Provisioner interface {
	Entries(context.Context) (map[string]string, error)
	Ensure(context.Context, string, string) error
	Disable(context.Context, string, string) error
	Occupied(context.Context, string) (bool, error)
}

type roomClient struct {
	AMIClient
	events chan *ami.Message
	slots  chan struct{}
}

func (c *roomClient) Events() <-chan *ami.Message { return c.events }

type managedRoom struct {
	service  *Service
	client   *roomClient
	cancel   context.CancelFunc
	verified bool
}

// Manager owns room runtimes and the single event-stream consumer. Admission
// serializes grant/deletion changes with commands; no lock is held by the reader.
type Manager struct {
	Admission   sync.RWMutex
	client      AMIClient
	store       *rooms.Store
	provisioner Provisioner
	cfg         Config
	slots       chan struct{}
	mu          sync.Mutex
	runtimes    map[string]*managedRoom
	ctx         context.Context
	epoch       uint64
	wg          sync.WaitGroup
}

// NewManager constructs the multi-room coordinator without starting work.
func NewManager(client AMIClient, store *rooms.Store, p Provisioner, cfg Config) *Manager {
	return &Manager{client: client, store: store, provisioner: p, cfg: cfg.withDefaults(), runtimes: map[string]*managedRoom{}, slots: make(chan struct{}, 4)}
}

// Runtime returns a room's shared state, creating it before event dispatch starts.
func (m *Manager) Runtime(r rooms.Room) *Service {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.runtimes[r.ID]; existing != nil {
		return existing.service
	}
	c := &roomClient{AMIClient: m.client, events: make(chan *ami.Message, 256), slots: m.slots}
	cfg := m.cfg
	cfg.Room = r.Bridge
	cfg.AdmissionNumber = r.Number
	s := New(c, cfg)
	entry := &managedRoom{service: s, client: c}
	m.runtimes[r.ID] = entry
	if m.ctx != nil {
		m.start(entry)
	}
	return s
}
func (m *Manager) start(r *managedRoom) {
	ctx, cancel := context.WithCancel(m.ctx)
	r.cancel = cancel
	m.wg.Add(1)
	go func() { defer m.wg.Done(); r.service.Run(ctx) }()
}

// OnAMIStateChange resets each room's speaking state and triggers its resync.
func (m *Manager) OnAMIStateChange(up bool) {
	m.mu.Lock()
	m.epoch++
	defer m.mu.Unlock()
	for _, r := range m.runtimes {
		r.verified = false
		r.service.OnAMIStateChange(up)
	}
}

// Run routes all messages once, while reconciliation runs separately from events.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	for _, r := range m.runtimes {
		m.start(r)
	}
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			m.reconcile(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() {
		m.mu.Lock()
		for _, r := range m.runtimes {
			if r.cancel != nil {
				r.cancel()
			}
		}
		m.mu.Unlock()
		m.wg.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-m.client.Events():
			if msg == nil {
				continue
			}
			m.dispatch(msg)
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	if m.client.Connected() {
		m.cleanRegistry(ctx)
	}
	list, err := m.store.All(ctx)
	if err != nil {
		m.cfg.Logger.Error("room catalogue unavailable", "error", err)
		return
	}
	for _, room := range list {
		if ctx.Err() != nil {
			return
		}
		if room.State != "deleted" {
			m.Runtime(room)
		}
		if !m.client.Connected() {
			continue
		}
		func() {
			m.Admission.Lock()
			defer m.Admission.Unlock()
			current, err := m.store.Get(ctx, room.ID)
			if err != nil {
				return
			}
			room = current
			operation, stop := context.WithTimeout(ctx, 2*time.Second)
			defer stop()
			m.mu.Lock()
			epoch := m.epoch
			m.mu.Unlock()
			state := room.State
			switch room.State {
			case "deleted":
				err = m.provisioner.Disable(operation, room.Number, room.Bridge)
			case "deleting":
				err = m.provisioner.Disable(operation, room.Number, room.Bridge)
				if err == nil {
					var busy bool
					busy, err = m.busy(operation, room)
					if err == nil && !busy {
						state = "deleted"
					}
				}
			default:
				err = m.provisioner.Ensure(operation, room.Number, room.Bridge)
				if err == nil {
					state = "ready"
				}
			}
			m.mu.Lock()
			if runtime := m.runtimes[room.ID]; runtime != nil {
				runtime.verified = err == nil && state == "ready" && m.client.Connected() && epoch == m.epoch
			}
			m.mu.Unlock()
			if saveErr := m.store.Result(ctx, room, state, err); saveErr != nil {
				m.cfg.Logger.Error("room sync result failed", "error", saveErr)
			}
			if err != nil {
				m.cfg.Logger.Warn("room synchronization failed", "room", room.ID, "error", err)
			}
			if state == "deleted" && err == nil {
				m.mu.Lock()
				if r := m.runtimes[room.ID]; r != nil {
					if r.cancel != nil {
						r.cancel()
					}
					delete(m.runtimes, room.ID)
				}
				m.mu.Unlock()
			}
		}()
	}
}

func (m *Manager) busy(ctx context.Context, r rooms.Room) (bool, error) {
	if !m.client.Connected() {
		return false, ami.ErrNotConnected
	}
	occupied, err := m.provisioner.Occupied(ctx, r.Bridge)
	if err != nil {
		return false, err
	}
	if occupied {
		return true, nil
	}
	service := m.Runtime(r)
	snapshot, err := service.List(ctx)
	if err != nil {
		return false, err
	}
	if len(snapshot.Participants) > 0 {
		return true, nil
	}
	for _, call := range snapshot.Calls {
		if call.State != "failed" {
			return true, nil
		}
	}
	return false, nil
}

// Delete must be called while Admission is locked by the authorized caller.
func (m *Manager) Delete(ctx context.Context, r rooms.Room) error {
	if r.State == "deleted" {
		return rooms.ErrMissing
	}
	if r.State == "deleting" {
		return nil
	}
	busy, err := m.busy(ctx, r)
	if err != nil {
		return err
	}
	if busy {
		return rooms.ErrBusy
	}
	return m.store.Deleting(ctx, r.ID)
}

// Ready rejects new calls when provisioning or Asterisk availability is uncertain.
func (m *Manager) Ready(r rooms.Room) error {
	if !m.client.Connected() {
		return ami.ErrNotConnected
	}
	m.mu.Lock()
	runtime := m.runtimes[r.ID]
	verified := runtime != nil && runtime.verified
	m.mu.Unlock()
	if r.State != "ready" || !verified {
		return rooms.ErrNotReady
	}
	return nil
}

func (c *roomClient) ActionList(ctx context.Context, a *ami.Message, end string) ([]*ami.Message, error) {
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.AMIClient.ActionList(ctx, a, end)
}

func (m *Manager) dispatch(msg *ami.Message) {
	event, callEvent := ami.DecodeCallEvent(msg)
	m.mu.Lock()
	for _, r := range m.runtimes {
		match := msg.Get("Conference") == r.service.cfg.Room
		if callEvent {
			r.service.mu.Lock()
			match = match || r.service.invites[event.ID] != nil
			r.service.mu.Unlock()
		}
		if match {
			select {
			case r.client.events <- msg:
			default:
				m.cfg.Logger.Warn("conference: room event queue overflow", "room", r.service.cfg.Room)
			}
		}
	}
	m.mu.Unlock()
}

func (m *Manager) cleanRegistry(ctx context.Context) {
	m.Admission.Lock()
	defer m.Admission.Unlock()
	operation, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	entries, err := m.provisioner.Entries(operation)
	if err != nil {
		m.cfg.Logger.Warn("room registry scan failed", "error", err)
		return
	}
	list, err := m.store.All(operation)
	if err != nil {
		return
	}
	wanted := map[string]bool{}
	for _, room := range list {
		if room.State != "deleted" {
			wanted[room.Number] = true
		}
	}
	for number, bridge := range entries {
		if !wanted[number] {
			if err := m.provisioner.Disable(operation, number, bridge); err != nil {
				m.cfg.Logger.Warn("orphan room cleanup failed", "number", number, "error", err)
			}
		}
	}
}
