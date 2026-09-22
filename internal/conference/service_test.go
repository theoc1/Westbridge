package conference_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
	"github.com/dmalkin/westbridge/internal/ami/amitest"
	"github.com/dmalkin/westbridge/internal/conference"
)

const testRoom = "1000"

// syncBuffer collects log output from the service's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// harness is a service wired to a real AMI client talking to a fake Asterisk.
type harness struct {
	srv   *amitest.Server
	svc   *conference.Service
	ctx   context.Context
	snaps chan conference.Snapshot
	logs  *syncBuffer
}

// newHarness brings up the fake, connects a client, and starts the service.
// The default ConfbridgeList handler reports an empty room, so a test that
// does not care about the roster is not disturbed by the startup resync.
func newHarness(t *testing.T, tune func(*conference.Config)) *harness {
	t.Helper()

	srv := amitest.NewServer(t)
	srv.Handle("ConfbridgeList", func(c *amitest.Conn, a *ami.Message) {
		c.Send(amitest.Error(a, "No active conferences."))
	})

	logs := &syncBuffer{}
	cfg := conference.Config{
		Room:              testRoom,
		OriginateContext:  "conference-out",
		OriginateCallerID: "Westbridge <0000>",
		OriginateTimeout:  30 * time.Second,
		ResyncInterval:    time.Hour, // resyncs are triggered explicitly in tests
		Logger:            slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if tune != nil {
		tune(&cfg)
	}

	// ami.Client takes its state callback at construction time while the
	// service needs the client, so the two are tied together with a closure.
	// The client is not running yet, so nothing can observe the nil.
	var svc *conference.Service
	client := ami.New(ami.Config{
		Addr:          srv.Addr(),
		Username:      srv.Username,
		Secret:        srv.Secret,
		DialTimeout:   2 * time.Second,
		ActionTimeout: 2 * time.Second,
		MinBackoff:    10 * time.Millisecond,
		MaxBackoff:    50 * time.Millisecond,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnStateChange: func(connected bool) { svc.OnAMIStateChange(connected) },
	})
	svc = conference.New(client, cfg)

	h := &harness{
		srv:   srv,
		svc:   svc,
		snaps: make(chan conference.Snapshot, 64),
		logs:  logs,
	}
	svc.Subscribe(func(s conference.Snapshot) {
		select {
		case h.snaps <- s:
		default:
			t.Error("snapshot channel overflowed")
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := client.Run(ctx); err != nil {
			t.Errorf("ami client Run: %v", err)
		}
	}()

	// The service must not start before the link is up, or its startup resync
	// would race the connection and make the tests flaky.
	waitFor(t, func() bool { return client.Connected() }, "AMI client to connect")

	go func() {
		defer wg.Done()
		svc.Run(ctx)
	}()

	// The connect that waitFor just observed queued a state change, and
	// handling it resyncs the roster before publishing. Waiting for a snapshot
	// that reports the link as up therefore also waits for the startup resync,
	// which would otherwise land mid-test and replace whatever the test set
	// up. Subscribe published a disconnected snapshot of its own before any of
	// this, so the predicate has to be the connected state and not "any".
	h.awaitSnapshot(t, "the service reports the AMI link as up", func(s conference.Snapshot) bool {
		return s.AsteriskConnected
	})

	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	return h
}

// waitFor polls cond until it holds or the test gives up.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// awaitSnapshot consumes published snapshots until one satisfies pred. Earlier
// snapshots are skipped rather than asserted on, because the startup state
// change publishes one before any test does anything.
func (h *harness) awaitSnapshot(t *testing.T, what string, pred func(conference.Snapshot) bool) conference.Snapshot {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case s := <-h.snaps:
			if pred(s) {
				return s
			}
		case <-timeout:
			t.Fatalf("timed out waiting for a snapshot where %s", what)
		}
	}
}

// capture replaces the handler for an action, recording every request and
// answering with a bare success.
func (h *harness) capture(action string) <-chan *ami.Message {
	got := make(chan *ami.Message, 8)
	h.srv.Handle(action, func(c *amitest.Conn, a *ami.Message) {
		got <- a
		c.Send(amitest.Success(a))
	})
	return got
}

// joinEvent builds a ConfbridgeJoin the way Asterisk emits it.
func joinEvent(room, uniqueID, channel, callerID string) *ami.Message {
	ev := ami.NewEvent("ConfbridgeJoin")
	ev.Add("Conference", room)
	ev.Add("BridgeUniqueid", "bridge-1")
	ev.Add("Channel", channel)
	ev.Add("Uniqueid", uniqueID)
	ev.Add("CallerIDNum", callerID)
	ev.Add("CallerIDName", "Caller "+callerID)
	ev.Add("Admin", "No")
	ev.Add("Muted", "No")
	return ev
}

func leaveEvent(room, uniqueID, channel string) *ami.Message {
	ev := ami.NewEvent("ConfbridgeLeave")
	ev.Add("Conference", room)
	ev.Add("Channel", channel)
	ev.Add("Uniqueid", uniqueID)
	return ev
}

// listItem is one ConfbridgeList event's payload as alternating key/values.
func listItem(room, uniqueID, channel, callerID, answered string) []string {
	return []string{
		"Conference", room,
		"Channel", channel,
		"Uniqueid", uniqueID,
		"CallerIDNum", callerID,
		"CallerIDName", "Caller " + callerID,
		"Admin", "No",
		"MarkedUser", "No",
		"Muted", "No",
		"AnsweredTime", answered,
	}
}

func TestServiceListPopulatesRoster(t *testing.T) {
	h := newHarness(t, nil)

	h.srv.HandleList("ConfbridgeList", "ConfbridgeList", "ConfbridgeListComplete",
		// "b" has been in the room longer than "a", so it must sort first.
		listItem(testRoom, "a", "PJSIP/1001-1", "1001", "5"),
		listItem(testRoom, "b", "PJSIP/1002-1", "1002", "30"),
		// A different conference on the same Asterisk must not leak in.
		listItem("2000", "c", "PJSIP/1003-1", "1003", "5"),
	)

	snap, err := h.svc.List(h.ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if snap.Room != testRoom {
		t.Errorf("Room = %q, want %q", snap.Room, testRoom)
	}
	if !snap.AsteriskConnected {
		t.Error("AsteriskConnected = false while the link is up")
	}
	if !equalIDs(snap.Participants, "a", "b") {
		t.Fatalf("participants = %v, want [a b]", ids(snap.Participants))
	}

	first := snap.Participants[1]
	if first.Channel != "PJSIP/1002-1" || first.CallerIDNum != "1002" || first.CallerIDName != "Caller 1002" {
		t.Errorf("participant fields not parsed: %+v", first)
	}
	if first.Admin || first.Muted {
		t.Errorf("Admin/Muted = %v/%v, want false/false", first.Admin, first.Muted)
	}
	if !first.JoinedAt.IsZero() {
		t.Errorf("JoinedAt = %v, want unknown for a snapshot", first.JoinedAt)
	}
}

func TestServiceListOfEmptyConferenceIsNotAnError(t *testing.T) {
	h := newHarness(t, nil) // the default handler answers "No active conferences."

	snap, err := h.svc.List(h.ctx)
	if err != nil {
		t.Fatalf("List of an empty conference: %v", err)
	}
	if len(snap.Participants) != 0 {
		t.Fatalf("participants = %v, want none", ids(snap.Participants))
	}
}

func TestServiceListPropagatesRealErrors(t *testing.T) {
	h := newHarness(t, nil)
	h.srv.Handle("ConfbridgeList", func(c *amitest.Conn, a *ami.Message) {
		c.Send(amitest.Error(a, "Permission denied"))
	})

	if _, err := h.svc.List(h.ctx); err == nil {
		t.Fatal("List: want an error for a rejected action")
	}
}

func TestServiceTracksJoinAndLeave(t *testing.T) {
	h := newHarness(t, nil)

	h.srv.Broadcast(joinEvent(testRoom, "a", "PJSIP/1001-1", "1001"))
	snap := h.awaitSnapshot(t, "the participant has joined", func(s conference.Snapshot) bool {
		return len(s.Participants) == 1
	})
	if snap.Participants[0].UniqueID != "a" {
		t.Fatalf("joined participant = %+v", snap.Participants[0])
	}

	h.srv.Broadcast(leaveEvent(testRoom, "a", "PJSIP/1001-1"))
	h.awaitSnapshot(t, "the participant has left", func(s conference.Snapshot) bool {
		return len(s.Participants) == 0
	})
}

func TestServiceIgnoresOtherConferences(t *testing.T) {
	h := newHarness(t, nil)

	h.srv.Broadcast(joinEvent("2000", "other", "PJSIP/9999-1", "9999"))
	h.srv.Broadcast(joinEvent(testRoom, "a", "PJSIP/1001-1", "1001"))

	// Waiting for the second join proves the first one was processed, so an
	// empty-but-for-"a" roster here is a real assertion and not a race.
	snap := h.awaitSnapshot(t, "our own participant has joined", func(s conference.Snapshot) bool {
		return len(s.Participants) > 0
	})
	if !equalIDs(snap.Participants, "a") {
		t.Fatalf("participants = %v, want only [a]", ids(snap.Participants))
	}
}

func TestServiceClearsRosterWhenConferenceEnds(t *testing.T) {
	h := newHarness(t, nil)

	h.srv.Broadcast(joinEvent(testRoom, "a", "PJSIP/1001-1", "1001"))
	h.awaitSnapshot(t, "the participant has joined", func(s conference.Snapshot) bool {
		return len(s.Participants) == 1
	})

	end := ami.NewEvent("ConfbridgeEnd")
	end.Add("Conference", testRoom)
	h.srv.Broadcast(end)

	h.awaitSnapshot(t, "the roster is empty", func(s conference.Snapshot) bool {
		return len(s.Participants) == 0
	})
}

func TestServiceKick(t *testing.T) {
	h := newHarness(t, nil)
	kicks := h.capture("ConfbridgeKick")

	h.srv.Broadcast(joinEvent(testRoom, "a", "PJSIP/1001-1", "1001"))
	h.awaitSnapshot(t, "the participant has joined", func(s conference.Snapshot) bool {
		return len(s.Participants) == 1
	})

	if err := h.svc.Kick(h.ctx, "a"); err != nil {
		t.Fatalf("Kick: %v", err)
	}

	select {
	case action := <-kicks:
		if got := action.Get("Conference"); got != testRoom {
			t.Errorf("Conference = %q, want %q", got, testRoom)
		}
		// ConfbridgeKick addresses the channel by name, not by uniqueid.
		if got := action.Get("Channel"); got != "PJSIP/1001-1" {
			t.Errorf("Channel = %q, want %q", got, "PJSIP/1001-1")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no ConfbridgeKick reached Asterisk")
	}

	// The kick removes the participant immediately rather than waiting for the
	// ConfbridgeLeave event.
	if snap := h.svc.Snapshot(); len(snap.Participants) != 0 {
		t.Fatalf("participants after kick = %v, want none", ids(snap.Participants))
	}
}

func TestServiceKickUnknownParticipant(t *testing.T) {
	h := newHarness(t, nil)
	kicks := h.capture("ConfbridgeKick")

	err := h.svc.Kick(h.ctx, "nobody")
	if !errors.Is(err, conference.ErrParticipantNotFound) {
		t.Fatalf("Kick of an unknown uniqueid: error = %v, want ErrParticipantNotFound", err)
	}

	select {
	case action := <-kicks:
		t.Fatalf("an unknown uniqueid reached Asterisk: %v", action)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServiceKickReportsAsteriskFailure(t *testing.T) {
	h := newHarness(t, nil)
	h.srv.Handle("ConfbridgeKick", func(c *amitest.Conn, a *ami.Message) {
		c.Send(amitest.Error(a, "No Conference by that name found."))
	})

	h.srv.Broadcast(joinEvent(testRoom, "a", "PJSIP/1001-1", "1001"))
	h.awaitSnapshot(t, "the participant has joined", func(s conference.Snapshot) bool {
		return len(s.Participants) == 1
	})

	if err := h.svc.Kick(h.ctx, "a"); err == nil {
		t.Fatal("Kick: want an error when Asterisk rejects the action")
	}
	if len(h.svc.Snapshot().Participants) != 1 {
		t.Fatal("a failed kick removed the participant from the roster")
	}
}

func TestServiceInvite(t *testing.T) {
	h := newHarness(t, func(c *conference.Config) {
		c.OriginateContext = "conference-out"
		c.OriginateCallerID = "Westbridge <0000>"
		c.OriginateTimeout = 20 * time.Second
	})
	originates := h.capture("Originate")

	actionID, err := h.svc.Invite(h.ctx, "+7 (999) 123-45-67")
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}

	select {
	case action := <-originates:
		want := map[string]string{
			"Channel":     "Local/+79991234567@conference-out/n",
			"Application": "ConfBridge",
			"Data":        testRoom,
			"CallerID":    "Westbridge <0000>",
			// AMI wants milliseconds, not seconds.
			"Timeout": "20000",
			// A synchronous Originate would block the single AMI connection
			// for the whole dial timeout.
			"Async":          "true",
			"ActionID":       actionID,
			"ChannelId":      actionID,
			"OtherChannelId": actionID + "-dial",
		}
		for key, wantValue := range want {
			if got := action.Get(key); got != wantValue {
				t.Errorf("Originate %s = %q, want %q", key, got, wantValue)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no Originate reached Asterisk")
	}
}

func TestServiceInviteRejectsUnsafeNumber(t *testing.T) {
	h := newHarness(t, nil)
	originates := h.capture("Originate")

	if _, err := h.svc.Invite(h.ctx, "1002@internal"); !errors.Is(err, conference.ErrInvalidNumber) {
		t.Fatalf("Invite: error = %v, want ErrInvalidNumber", err)
	}

	select {
	case action := <-originates:
		t.Fatalf("an unsafe number reached Asterisk: %v", action)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestServiceLogsOriginateOutcome(t *testing.T) {
	h := newHarness(t, nil)
	h.capture("Originate")

	actionID, err := h.svc.Invite(h.ctx, "1002")
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}

	// Asterisk reports the outcome of an async Originate much later, as an
	// event carrying the original ActionID.
	resp := ami.NewEvent("OriginateResponse")
	resp.Add("ActionID", actionID)
	resp.Add("Response", "Failure")
	resp.Add("Channel", "Local/1002@conference-out")
	resp.Add("Reason", "3")
	h.srv.Broadcast(resp)

	waitFor(t, func() bool {
		return strings.Contains(h.logs.String(), "invite failed")
	}, "the failed invite to be logged")

	logged := h.logs.String()
	if !strings.Contains(logged, "1002") || !strings.Contains(logged, actionID) {
		t.Fatalf("the invite failure log is missing the number or the action id:\n%s", logged)
	}
}

func TestServiceResyncsAfterReconnect(t *testing.T) {
	h := newHarness(t, nil)

	var mu sync.Mutex
	items := [][]string{listItem(testRoom, "a", "PJSIP/1001-1", "1001", "5")}
	h.srv.Handle("ConfbridgeList", func(c *amitest.Conn, a *ami.Message) {
		mu.Lock()
		current := items
		mu.Unlock()

		c.Send(amitest.Success(a, "Message", "Confbridge user list will follow"))
		for _, item := range current {
			ev := ami.NewEvent("ConfbridgeList")
			ev.Add("ActionID", a.ActionID())
			for i := 0; i < len(item); i += 2 {
				ev.Add(item[i], item[i+1])
			}
			c.Send(ev)
		}
		done := ami.NewEvent("ConfbridgeListComplete")
		done.Add("ActionID", a.ActionID())
		c.Send(done)
	})

	if _, err := h.svc.List(h.ctx); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !equalIDs(h.svc.Snapshot().Participants, "a") {
		t.Fatalf("participants = %v, want [a]", ids(h.svc.Snapshot().Participants))
	}

	// While the link is down the room changes completely. The roster must end
	// up matching Asterisk, with no stale entry left behind.
	mu.Lock()
	items = [][]string{listItem(testRoom, "b", "PJSIP/1002-1", "1002", "5")}
	mu.Unlock()

	h.srv.DropConns()

	h.awaitSnapshot(t, "the roster has been rebuilt after the reconnect", func(s conference.Snapshot) bool {
		return s.AsteriskConnected && equalIDs(s.Participants, "b")
	})
}

func TestServiceReportsDisconnectedState(t *testing.T) {
	h := newHarness(t, nil)

	h.srv.Close()
	h.awaitSnapshot(t, "the AMI link is reported as down", func(s conference.Snapshot) bool {
		return !s.AsteriskConnected
	})
}
