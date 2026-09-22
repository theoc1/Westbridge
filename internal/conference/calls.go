package conference

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmalkin/westbridge/internal/ami"
)

// Call operation errors map to API 404, 409 and capacity responses.
var (
	ErrCallNotFound = errors.New("call is no longer in the list")
	ErrCallState    = errors.New("only failed calls can be retried")
	ErrCallLimit    = errors.New("too many calls; remove failed calls before dialling again")
)

// Call is a pending or failed outgoing attempt shown alongside the live roster.
type Call struct {
	ID         string    `json:"id"`
	Number     string    `json:"number"`
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	Cancelling bool      `json:"cancelling,omitempty"`
}

type callAttempt struct {
	Call
	op              sync.Mutex // serializes commands for this attempt, never held by event handlers
	cancelRequested bool
	timeout         bool
	finished        bool
	created         bool
	name            string
	dialReason      string
}

// Invite queues an independent asynchronous call. ActionID is also the uniqueid
// of the conference-facing Local channel; /n keeps that identity stable.
func (s *Service) Invite(ctx context.Context, number string) (string, error) {
	normalized, err := NormalizeNumber(number)
	if err != nil {
		return "", err
	}
	if !s.client.Connected() {
		return "", ami.ErrNotConnected
	}
	id := newActionID()
	attempt := &callAttempt{Call: Call{ID: id, Number: normalized, State: "dialing", CreatedAt: time.Now()}}
	attempt.op.Lock()
	defer attempt.op.Unlock()
	s.mu.Lock()
	if len(s.invites) >= 200 {
		s.mu.Unlock()
		return "", ErrCallLimit
	}
	s.invites[id] = attempt
	s.mu.Unlock()
	s.notify()
	action := ami.NewAction(actionOriginate)
	action.Add("ActionID", id)
	action.Add("Channel", fmt.Sprintf("Local/%s@%s/n", normalized, s.cfg.OriginateContext))
	action.Add("ChannelId", id)
	action.Add("OtherChannelId", id+"-dial")
	action.Add("Application", "ConfBridge")
	action.Add("Data", s.cfg.Room)
	action.Add("CallerID", s.cfg.OriginateCallerID)
	action.Add("Timeout", strconv.FormatInt(s.cfg.OriginateTimeout.Milliseconds(), 10))
	action.Add("Async", "true")
	_, err = s.client.Action(ctx, action)
	s.mu.Lock()
	if err != nil && !attempt.finished && attempt.State != "connected" {
		var rejection *ami.ActionError
		if errors.As(err, &rejection) {
			attempt.State = "failed"
			attempt.Reason = "Asterisk rejected the call"
			attempt.finished = true
		} else {
			// A lost acknowledgement does not prove that no call was placed. Keep the
			// attempt cancellable until events or a channel snapshot resolve it.
			attempt.Reason = "Checking call status…"
		}
	}
	s.mu.Unlock()
	if err != nil {
		s.log.Warn("conference: originate acknowledgement failed", "action_id", id, "error", err)
	}
	s.log.Info("conference: invite queued", "number", normalized, "action_id", id)
	s.notify()
	return id, nil
}

// CancelCall dismisses a failed row or requests Hangup on exactly this attempt.
// If the answer wins the race, Hangup still terminates that same Local channel.
func (s *Service) CancelCall(ctx context.Context, id string) error {
	s.mu.Lock()
	attempt := s.invites[id]
	s.mu.Unlock()
	if attempt == nil {
		return ErrCallNotFound
	}
	attempt.op.Lock()
	defer attempt.op.Unlock()
	s.mu.Lock()
	if s.invites[id] != attempt {
		s.mu.Unlock()
		return ErrCallNotFound
	}
	if attempt.State == "failed" {
		delete(s.invites, id)
		s.mu.Unlock()
		s.notify()
		return nil
	}
	if !s.client.Connected() {
		s.mu.Unlock()
		return ami.ErrNotConnected
	}
	attempt.cancelRequested = true
	s.mu.Unlock()
	s.notify()
	return s.hangupAttempt(ctx, attempt)
}

// RetryCall creates a new independent attempt, serializing clicks across users.
func (s *Service) RetryCall(ctx context.Context, id string) (string, error) {
	s.mu.Lock()
	attempt := s.invites[id]
	s.mu.Unlock()
	if attempt == nil {
		return "", ErrCallNotFound
	}
	attempt.op.Lock()
	defer attempt.op.Unlock()
	s.mu.Lock()
	if s.invites[id] != attempt {
		s.mu.Unlock()
		return "", ErrCallNotFound
	}
	if attempt.State != "failed" {
		s.mu.Unlock()
		return "", ErrCallState
	}
	number := attempt.Number
	s.mu.Unlock()
	next, err := s.Invite(ctx, number)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	delete(s.invites, id)
	s.mu.Unlock()
	s.notify()
	return next, nil
}

// hangupAttempt is called with attempt.op held. Acknowledging an async Originate
// may precede channel allocation: "No such channel" then means retry, not success.
func (s *Service) hangupAttempt(ctx context.Context, attempt *callAttempt) error {
	action := ami.NewAction("Hangup")
	action.Add("Channel", attempt.ID)
	_, err := s.client.Action(ctx, action)
	if err == nil {
		return nil
	} // Wait for Hangup or channel reconciliation before removing.
	var actionErr *ami.ActionError
	if errors.As(err, &actionErr) && strings.Contains(strings.ToLower(actionErr.Response.Get("Message")), "no such channel") {
		s.mu.Lock()
		if s.invites[attempt.ID] == attempt && (attempt.finished || attempt.created || time.Since(attempt.CreatedAt) > s.cfg.OriginateTimeout+10*time.Second) {
			s.finishCancelledLocked(attempt)
		}
		s.mu.Unlock()
		s.notify()
		return nil
	}
	return err
}

func (s *Service) finishCancelledLocked(attempt *callAttempt) {
	if attempt.timeout {
		attempt.State = "failed"
		attempt.Reason = "No answer"
		attempt.cancelRequested = false
		attempt.finished = true
	} else {
		delete(s.invites, attempt.ID)
	}
}

func (s *Service) handleCallEvent(msg *ami.Message) {
	name := msg.EventName()
	id := msg.ActionID()
	if !strings.EqualFold(name, eventOriginateResponse) {
		id = msg.Get("Uniqueid")
		id = strings.TrimSuffix(id, "-dial")
	}
	s.mu.Lock()
	attempt := s.invites[id]
	if attempt == nil {
		s.mu.Unlock()
		return
	}
	changed := false
	switch {
	case strings.EqualFold(name, "Newchannel") && msg.Get("Uniqueid") == attempt.ID:
		attempt.created = true
	case strings.EqualFold(name, "DialEnd"):
		if reason := dialFailure(msg.Get("DialStatus")); reason != "" {
			attempt.dialReason = reason
			if attempt.State == "failed" {
				attempt.Reason = reason
				changed = true
			}
		}
	case strings.EqualFold(name, "NewConnectedLine"):
		if label := msg.Get("ConnectedLineName"); label != "" && label != "<unknown>" && label != "unknown" {
			attempt.name = label
			changed = true
		}
	case strings.EqualFold(name, eventOriginateResponse):
		if msg.IsSuccess() {
			attempt.created = true
			if attempt.State == "dialing" && !attempt.finished {
				attempt.Reason = "Answered; joining conference…"
				changed = true
			}
		} else if attempt.State != "connected" {
			attempt.finished = true
			if attempt.cancelRequested {
				s.finishCancelledLocked(attempt)
			} else {
				wasFailed := attempt.State == "failed"
				attempt.State = "failed"
				reason := attempt.dialReason
				if reason == "" {
					reason = originateFailure(msg.Get("Reason"))
				}
				if !wasFailed || reason != "Connection failed" || attempt.Reason == "" {
					attempt.Reason = reason
				}
				s.log.Warn("conference: invite failed", "number", attempt.Number, "action_id", id, "reason", attempt.Reason)
			}
			changed = true
		}
	case strings.EqualFold(name, "Hangup") && msg.Get("Uniqueid") == attempt.ID+"-dial":
		if reason := hangupFailure(msg.Get("Cause")); reason != "Connection failed" && msg.Get("Cause") != "16" {
			attempt.dialReason = reason
			if attempt.State == "failed" {
				attempt.Reason = reason
				changed = true
			}
		}
	case strings.EqualFold(name, "Hangup") && msg.Get("Uniqueid") == attempt.ID:
		attempt.finished = true
		if attempt.cancelRequested {
			s.finishCancelledLocked(attempt)
		} else if attempt.State == "connected" {
			delete(s.invites, id)
		} else if attempt.State != "failed" {
			attempt.State = "failed"
			attempt.Reason = attempt.dialReason
			if attempt.Reason == "" {
				attempt.Reason = hangupFailure(msg.Get("Cause"))
			}
		}
		changed = true
	}
	s.mu.Unlock()
	if strings.EqualFold(name, "Hangup") && msg.Get("Uniqueid") == id {
		s.roster.Remove(id)
	}
	if changed {
		s.notify()
	}
}

func (s *Service) reconcileJoined(participants []Participant) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, p := range participants {
		if attempt := s.invites[p.UniqueID]; attempt != nil && attempt.State != "connected" {
			attempt.State = "connected"
			attempt.Reason = ""
			changed = true
		}
	}
	return changed
}

func dialFailure(status string) string {
	switch strings.ToUpper(status) {
	case "BUSY":
		return "Busy"
	case "NOANSWER":
		return "No answer"
	case "CHANUNAVAIL":
		return "Unavailable"
	case "CONGESTION":
		return "Network congestion"
	case "CANCEL":
		return "Call cancelled"
	case "DONTCALL", "TORTURE", "INVALIDARGS":
		return "Call rejected"
	default:
		return ""
	}
}
func originateFailure(reason string) string {
	switch reason {
	case "1":
		return "No answer"
	case "3":
		return "No answer"
	case "5":
		return "Busy"
	case "8":
		return "Network congestion"
	default:
		return "Connection failed"
	}
}
func hangupFailure(cause string) string {
	switch cause {
	case "17":
		return "Busy"
	case "18", "19", "16":
		return "No answer"
	case "21":
		return "Call rejected"
	case "1", "3", "20", "27":
		return "Unavailable"
	case "34", "38", "41", "42", "44", "47":
		return "Network congestion"
	default:
		return "Connection failed"
	}
}

// maintainCalls resolves missed events and retries cancellations whose channels
// did not exist yet. A lost AMI link never invents a successful cancellation.
func (s *Service) maintainCalls(ctx context.Context) {
	if !s.client.Connected() {
		return
	}
	s.mu.Lock()
	attempts := make([]*callAttempt, 0)
	for _, attempt := range s.invites {
		if attempt.State == "dialing" || attempt.cancelRequested {
			attempts = append(attempts, attempt)
		}
	}
	s.mu.Unlock()
	needSnapshot := false
	s.mu.Lock()
	for _, attempt := range attempts {
		if time.Since(attempt.CreatedAt) > s.cfg.OriginateTimeout+10*time.Second && !attempt.cancelRequested {
			needSnapshot = true
			break
		}
	}
	s.mu.Unlock()
	if needSnapshot {
		listCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		_, err := s.List(listCtx)
		stop()
		if err != nil {
			return
		}
	}
	for _, attempt := range attempts {
		if !attempt.op.TryLock() {
			continue
		}
		s.mu.Lock()
		current := s.invites[attempt.ID] == attempt
		overdue := current && attempt.State == "dialing" && time.Since(attempt.CreatedAt) > s.cfg.OriginateTimeout+10*time.Second
		if overdue && !attempt.cancelRequested {
			attempt.cancelRequested = true
			attempt.timeout = true
		}
		cancel := current && attempt.cancelRequested
		s.mu.Unlock()
		if cancel {
			actionCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			if err := s.hangupAttempt(actionCtx, attempt); err != nil {
				s.log.Debug("conference: cancel retry", "error", err, "action_id", attempt.ID)
			}
			stop()
		}
		attempt.op.Unlock()
	}
}

// forgetDeparted removes connected bookkeeping after an authoritative resync.
func (s *Service) forgetDeparted(participants []Participant) {
	present := map[string]bool{}
	for _, p := range participants {
		present[p.UniqueID] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, attempt := range s.invites {
		if attempt.State == "connected" && !present[id] && !attempt.cancelRequested {
			delete(s.invites, id)
		}
	}
}
