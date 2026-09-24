package conference

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dmalkin/westbridge/internal/telephony"
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
	dialReason      telephony.Failure
}

// Invite queues an independent asynchronous call with a stable application ID.
func (s *Service) Invite(ctx context.Context, number string) (string, error) {
	normalized, err := NormalizeNumber(number)
	if err != nil {
		return "", err
	}
	if !s.calls.Connected() {
		return "", telephony.ErrUnavailable
	}
	id := newCallID()
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
	err = s.calls.Originate(ctx, id, normalized)
	s.mu.Lock()
	if err != nil && !attempt.finished && attempt.State != "connected" {
		if errors.Is(err, telephony.ErrRejected) {
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
// If the answer wins the race, Hangup still terminates that same call.
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
	if !s.calls.Connected() {
		s.mu.Unlock()
		return telephony.ErrUnavailable
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

// hangupAttempt is called with attempt.op held. Acknowledging an outgoing call
// may precede channel allocation: A missing channel then means retry, not success.
func (s *Service) hangupAttempt(ctx context.Context, attempt *callAttempt) error {
	err := s.calls.Hangup(ctx, attempt.ID)
	if err == nil {
		return nil
	} // Wait for termination or reconciliation before removing.
	if errors.Is(err, telephony.ErrChannelNotFound) {
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

func (s *Service) handleCallEvent(event telephony.Event) {
	id := event.ID
	s.mu.Lock()
	attempt := s.invites[id]
	if attempt == nil {
		s.mu.Unlock()
		return
	}
	changed := false
	switch event.Kind {
	case telephony.ChannelObserved:
		attempt.created = true
	case telephony.FailureObserved:
		attempt.dialReason = event.Failure
		if attempt.State == "failed" {
			attempt.Reason = string(event.Failure)
			changed = true
		}
	case telephony.NameUpdated:
		attempt.name = event.Name
		changed = true
	case telephony.Answered, telephony.OriginationFailed:
		if event.Kind == telephony.Answered {
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
					reason = event.Failure
				}
				if !wasFailed || reason != telephony.Unknown || attempt.Reason == "" {
					attempt.Reason = string(reason)
				}
				s.log.Warn("conference: invite failed", "number", attempt.Number, "action_id", id, "reason", attempt.Reason)
			}
			changed = true
		}
	case telephony.Ended:
		attempt.finished = true
		if attempt.cancelRequested {
			s.finishCancelledLocked(attempt)
		} else if attempt.State == "connected" {
			delete(s.invites, id)
		} else if attempt.State != "failed" {
			attempt.State = "failed"
			attempt.Reason = string(attempt.dialReason)
			if attempt.Reason == "" {
				attempt.Reason = string(event.Failure)
			}
		}
		changed = true
	}
	s.mu.Unlock()
	if event.Kind == telephony.Ended {
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

// maintainCalls resolves missed events and retries cancellations whose channels
// did not exist yet. A lost AMI link never invents a successful cancellation.
func (s *Service) maintainCalls(ctx context.Context) {
	if !s.calls.Connected() {
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
