package conference

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
)

// Errors the service returns to its callers. HTTP handlers map these onto
// status codes, so they are sentinels rather than free-form text.
var (
	// ErrParticipantNotFound means the uniqueid is not in the roster. Either
	// the caller is working from a stale view or the participant has just left.
	ErrParticipantNotFound = errors.New("conference: participant not found")

	// ErrInvalidNumber rejects a number that is not safe to dial.
	ErrInvalidNumber = errors.New("conference: invalid number")
)

// AMI event and action names, spelled once so a typo cannot go unnoticed in
// one branch of a switch.
const (
	actionConfbridgeList = "ConfbridgeList"
	actionConfbridgeKick = "ConfbridgeKick"
	actionOriginate      = "Originate"

	eventConfbridgeList         = "ConfbridgeList"
	eventConfbridgeListComplete = "ConfbridgeListComplete"
	eventConfbridgeJoin         = "ConfbridgeJoin"
	eventConfbridgeLeave        = "ConfbridgeLeave"
	eventConfbridgeStart        = "ConfbridgeStart"
	eventConfbridgeEnd          = "ConfbridgeEnd"
	eventOriginateResponse      = "OriginateResponse"
)

// AMIClient is the slice of *ami.Client the service depends on. Keeping it an
// interface documents the coupling and keeps the service constructible in
// isolation; the tests still run against a real client and a fake Asterisk.
type AMIClient interface {
	Action(ctx context.Context, msg *ami.Message) (*ami.Message, error)
	ActionList(ctx context.Context, msg *ami.Message, completeEvent string) ([]*ami.Message, error)
	Events() <-chan *ami.Message
	Connected() bool
}

// Config parameterises the service. Room and OriginateContext are mandatory.
type Config struct {
	// Room is the ConfBridge conference number this instance controls. Events
	// for any other conference are ignored.
	Room string
	// OriginateContext is the dialplan context an invited number is dialled
	// through.
	OriginateContext string
	// OriginateCallerID is presented to the invited party.
	OriginateCallerID string
	// OriginateTimeout bounds the dial attempt. AMI wants it in milliseconds;
	// the conversion happens here. Default 30s.
	OriginateTimeout time.Duration
	// ResyncInterval is how often the roster is rebuilt from scratch, so that
	// a dropped event cannot accumulate into drift. Default 30s.
	ResyncInterval time.Duration

	Logger *slog.Logger
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.OriginateTimeout <= 0 {
		out.OriginateTimeout = 30 * time.Second
	}
	if out.ResyncInterval <= 0 {
		out.ResyncInterval = 30 * time.Second
	}
	if out.OriginateCallerID == "" {
		out.OriginateCallerID = "Westbridge <0000>"
	}
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	return out
}

// Snapshot is the complete state of the conference at one instant. Every
// update the frontend receives is one of these: the roster is small enough
// that sending it whole removes an entire class of desync bugs.
type Snapshot struct {
	Room              string        `json:"room"`
	AsteriskConnected bool          `json:"asteriskConnected"`
	Participants      []Participant `json:"participants"`
}

// Service owns the roster and turns AMI traffic into snapshots.
//
// Because *ami.Client takes its state-change callback at construction time
// while the service needs the client, wire the two through a closure:
//
//	var svc *conference.Service
//	client := ami.New(ami.Config{
//		OnStateChange: func(up bool) { svc.OnAMIStateChange(up) },
//		// ...
//	})
//	svc = conference.New(client, conference.Config{ /* ... */ })
//	go client.Run(ctx)
//	go svc.Run(ctx)
type Service struct {
	client AMIClient
	cfg    Config
	log    *slog.Logger
	roster *Roster

	// state carries AMI connect/disconnect notifications from the client's
	// goroutine to Run. It is buffered and dropped into non-blockingly,
	// because the callback runs on the client's reader goroutine.
	state chan bool

	// pubMu serialises snapshot publication. Changes originate on both Run's
	// goroutine and on HTTP handlers, and without it two of them can capture
	// snapshots in one order and broadcast them in the other, leaving every
	// browser on the older view.
	pubMu sync.Mutex

	mu      sync.Mutex
	subs    map[uint64]func(Snapshot)
	nextSub uint64
	invites map[string]invite
}

// invite remembers an Originate in flight so its asynchronous outcome can be
// logged against the number that was dialled.
type invite struct {
	number string
	at     time.Time
}

// New returns a service that is not running yet; call Run to start consuming
// events.
func New(client AMIClient, cfg Config) *Service {
	resolved := cfg.withDefaults()
	return &Service{
		client:  client,
		cfg:     resolved,
		log:     resolved.Logger,
		roster:  NewRoster(),
		state:   make(chan bool, 8),
		subs:    make(map[uint64]func(Snapshot)),
		invites: make(map[string]invite),
	}
}

// Snapshot returns the current state without touching Asterisk.
func (s *Service) Snapshot() Snapshot {
	return Snapshot{
		Room:              s.cfg.Room,
		AsteriskConnected: s.client.Connected(),
		Participants:      s.roster.Snapshot(),
	}
}

// Subscribe registers fn to receive a snapshot on every roster change and on
// every AMI state change, and returns a function that unsubscribes it. fn is
// called once with the current state before Subscribe returns, so a subscriber
// never has to handle a "no snapshot yet" state and no update can slip through
// between reading the state and subscribing to it.
//
// fn must not block: it runs on whichever goroutine caused the change, and one
// slow subscriber would hold up every other one.
func (s *Service) Subscribe(fn func(Snapshot)) func() {
	// pubMu before mu, the same order as notify, and held across the initial
	// call so it cannot be overtaken by a concurrent publication.
	s.pubMu.Lock()
	defer s.pubMu.Unlock()

	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	s.mu.Unlock()

	fn(s.Snapshot())

	return func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

// OnAMIStateChange is the callback to hand to ami.Config.OnStateChange. It
// never blocks: the client's reader goroutine is calling it.
func (s *Service) OnAMIStateChange(connected bool) {
	select {
	case s.state <- connected:
	default:
		s.log.Warn("conference: dropped an AMI state notification", "connected", connected)
	}
}

// Run consumes the AMI event stream until ctx is cancelled, resyncing the
// roster on every reconnect and on the configured interval. Nothing that
// happens on the AMI link is fatal to the service, so it has no error to
// report: it either runs or the caller cancelled it.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.ResyncInterval)
	defer ticker.Stop()

	// The link may already be up by the time Run starts, in which case no
	// state change is coming and the roster would stay empty until the first
	// tick.
	if s.client.Connected() {
		s.resync(ctx)
	}

	events := s.client.Events()
	for {
		select {
		case <-ctx.Done():
			return

		case msg := <-events:
			s.handleEvent(msg)

		case connected := <-s.state:
			if connected {
				// Anything missed while the link was down is corrected by
				// rebuilding the roster from scratch.
				s.resync(ctx)
			}
			s.notify()

		case <-ticker.C:
			s.pruneInvites()
			if s.client.Connected() {
				s.resync(ctx)
			}
		}
	}
}

// List rebuilds the roster from a ConfbridgeList and returns the result.
func (s *Service) List(ctx context.Context) (Snapshot, error) {
	action := ami.NewAction(actionConfbridgeList)
	action.Add("Conference", s.cfg.Room)

	events, err := s.client.ActionList(ctx, action, eventConfbridgeListComplete)
	if err != nil {
		// An empty or not-yet-created conference is an error on the wire but
		// not an error here: it simply means nobody is in the room.
		if isNoSuchConference(err) {
			if s.roster.Clear() {
				s.notify()
			}
			return s.Snapshot(), nil
		}
		return Snapshot{}, err
	}

	participants := make([]Participant, 0, len(events))
	for _, ev := range events {
		if !strings.EqualFold(ev.EventName(), eventConfbridgeList) {
			continue
		}
		if !s.isOurRoom(ev) {
			continue
		}
		if p, ok := participantFrom(ev); ok {
			participants = append(participants, p)
		}
	}

	if s.roster.Replace(participants) {
		s.notify()
	}
	return s.Snapshot(), nil
}

// Kick removes a participant from the conference by channel uniqueid.
//
// ConfbridgeKick addresses participants by channel name, so the name is
// resolved from the roster first; an unknown uniqueid never reaches Asterisk.
func (s *Service) Kick(ctx context.Context, uniqueID string) error {
	p, ok := s.roster.Get(uniqueID)
	if !ok {
		return fmt.Errorf("%w: %q", ErrParticipantNotFound, uniqueID)
	}

	action := ami.NewAction(actionConfbridgeKick)
	action.Add("Conference", s.cfg.Room)
	action.Add("Channel", p.Channel)

	if _, err := s.client.Action(ctx, action); err != nil {
		return fmt.Errorf("kick %s: %w", p.Channel, err)
	}

	s.log.Info("conference: kicked participant",
		"room", s.cfg.Room, "channel", p.Channel, "uniqueid", uniqueID)

	// ConfbridgeLeave will say the same thing in a moment, but removing the
	// participant now keeps the UI responsive and the removal is idempotent.
	if _, removed := s.roster.Remove(uniqueID); removed {
		s.notify()
	}
	return nil
}

// Invite dials number and drops the answered call into the conference. It
// returns the ActionID correlating the request with the OriginateResponse
// event that reports the outcome.
//
// The Originate is asynchronous on purpose: the application holds a single AMI
// connection, and a synchronous Originate would block it for the whole dial
// timeout.
func (s *Service) Invite(ctx context.Context, number string) (string, error) {
	normalized, err := NormalizeNumber(number)
	if err != nil {
		return "", err
	}

	actionID := newActionID()
	action := ami.NewAction(actionOriginate)
	action.Add("ActionID", actionID)
	action.Add("Channel", fmt.Sprintf("Local/%s@%s", normalized, s.cfg.OriginateContext))
	action.Add("Application", "ConfBridge")
	action.Add("Data", s.cfg.Room)
	action.Add("CallerID", s.cfg.OriginateCallerID)
	action.Add("Timeout", strconv.FormatInt(s.cfg.OriginateTimeout.Milliseconds(), 10))
	action.Add("Async", "true")

	s.mu.Lock()
	s.invites[actionID] = invite{number: normalized, at: time.Now()}
	s.mu.Unlock()

	if _, err := s.client.Action(ctx, action); err != nil {
		s.mu.Lock()
		delete(s.invites, actionID)
		s.mu.Unlock()
		return "", fmt.Errorf("invite %s: %w", normalized, err)
	}

	s.log.Info("conference: invite queued",
		"room", s.cfg.Room, "number", normalized, "action_id", actionID)
	return actionID, nil
}

// handleEvent applies one unsolicited AMI event to the roster.
func (s *Service) handleEvent(msg *ami.Message) {
	name := msg.EventName()

	// OriginateResponse is the only event we care about that is not scoped to
	// a conference, so it is matched before the room filter.
	if strings.EqualFold(name, eventOriginateResponse) {
		s.handleOriginateResponse(msg)
		return
	}

	switch {
	case strings.EqualFold(name, eventConfbridgeJoin),
		strings.EqualFold(name, eventConfbridgeLeave),
		strings.EqualFold(name, eventConfbridgeStart),
		strings.EqualFold(name, eventConfbridgeEnd):
	default:
		return
	}

	if !s.isOurRoom(msg) {
		return
	}

	switch {
	case strings.EqualFold(name, eventConfbridgeJoin):
		p, ok := participantFrom(msg)
		if !ok {
			s.log.Warn("conference: ignoring a join event with no channel identity", "event", msg.String())
			return
		}
		if s.roster.Add(p) {
			s.log.Info("conference: participant joined",
				"room", s.cfg.Room, "channel", p.Channel, "caller_id", p.CallerIDNum)
			s.notify()
		}

	case strings.EqualFold(name, eventConfbridgeLeave):
		if p, removed := s.roster.Remove(msg.Get("Uniqueid")); removed {
			s.log.Info("conference: participant left",
				"room", s.cfg.Room, "channel", p.Channel, "caller_id", p.CallerIDNum)
			s.notify()
		}

	case strings.EqualFold(name, eventConfbridgeStart):
		// The room has just been created, so whatever we remembered about a
		// previous incarnation of it is gone.
		s.log.Info("conference: started", "room", s.cfg.Room)
		if s.roster.Clear() {
			s.notify()
		}

	case strings.EqualFold(name, eventConfbridgeEnd):
		s.log.Info("conference: ended", "room", s.cfg.Room)
		if s.roster.Clear() {
			s.notify()
		}
	}
}

// handleOriginateResponse reports how an invite ended. The call itself shows
// up in the roster through ConfbridgeJoin; this exists so that a failed invite
// leaves a diagnosable trace instead of silence.
func (s *Service) handleOriginateResponse(msg *ami.Message) {
	id := msg.ActionID()

	s.mu.Lock()
	inv, ok := s.invites[id]
	delete(s.invites, id)
	s.mu.Unlock()

	if !ok {
		return // somebody else's Originate, or one we already reported on
	}

	if msg.IsSuccess() {
		s.log.Info("conference: invite answered",
			"number", inv.number, "channel", msg.Get("Channel"), "action_id", id)
		return
	}
	s.log.Warn("conference: invite failed",
		"number", inv.number,
		"reason", msg.Get("Reason"),
		"response", msg.Get("Response"),
		"action_id", id)
}

// resync rebuilds the roster, logging rather than propagating failures: it
// runs on a timer and on reconnect, where there is nobody to return an error to.
func (s *Service) resync(ctx context.Context) {
	if _, err := s.List(ctx); err != nil {
		s.log.Warn("conference: roster resync failed", "room", s.cfg.Room, "error", err)
	}
}

// pruneInvites forgets Originates whose OriginateResponse never arrived, so a
// long-running process cannot accumulate them.
func (s *Service) pruneInvites() {
	// Twice the dial timeout, floored at a minute, is long enough that a live
	// invite is never dropped early.
	ttl := 2 * s.cfg.OriginateTimeout
	if ttl < time.Minute {
		ttl = time.Minute
	}
	cutoff := time.Now().Add(-ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, inv := range s.invites {
		if inv.at.Before(cutoff) {
			s.log.Warn("conference: invite outcome never reported",
				"number", inv.number, "action_id", id)
			delete(s.invites, id)
		}
	}
}

// notify publishes the current snapshot to every subscriber.
//
// pubMu is held across the capture and the fan-out so that concurrent
// publications cannot deliver their snapshots out of order.
func (s *Service) notify() {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()

	snap := s.Snapshot()

	s.mu.Lock()
	subs := make([]func(Snapshot), 0, len(s.subs))
	for _, fn := range s.subs {
		subs = append(subs, fn)
	}
	s.mu.Unlock()

	for _, fn := range subs {
		fn(snap)
	}
}

// isOurRoom filters out events belonging to any other conference on the same
// Asterisk.
func (s *Service) isOurRoom(msg *ami.Message) bool {
	return msg.Get("Conference") == s.cfg.Room
}

// isNoSuchConference recognises Asterisk's way of saying the room is not up.
// It answers ConfbridgeList for an empty conference with an error response
// rather than an empty list, and the wording has changed between releases.
func isNoSuchConference(err error) bool {
	var actionErr *ami.ActionError
	if !errors.As(err, &actionErr) {
		return false
	}
	msg := strings.ToLower(actionErr.Response.Get("Message"))
	return strings.Contains(msg, "no active conferences") ||
		strings.Contains(msg, "no conference by that name") ||
		strings.Contains(msg, "conference not found")
}

// newActionID returns an identifier for an Originate. The service generates it
// instead of letting the client do so because the ActionID is the only link
// between the request and the OriginateResponse that arrives much later.
func newActionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("wb-invite-%d", time.Now().UnixNano())
	}
	return "wb-invite-" + hex.EncodeToString(b[:])
}
