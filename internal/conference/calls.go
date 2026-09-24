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
	callModel
	op sync.Mutex // Serializes commands, never held by event handlers.
}

// applyCallLocked applies a pure transition and handles active-list ownership.
// The caller holds s.mu and performs returned I/O effects after unlocking.
func (s *Service) applyCallLocked(attempt *callAttempt, input callInput) callEffects {
	next, effects := transitionCall(attempt.callModel, input)
	attempt.callModel = next
	if next.Phase == callEnded {
		delete(s.invites, next.ID)
	}
	return effects
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
	attempt := &callAttempt{callModel: callModel{ID: id, Number: normalized, Phase: callDialing, CreatedAt: time.Now()}}
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
	if err != nil {
		kind := callAcknowledgementUnknown
		if errors.Is(err, telephony.ErrRejected) {
			kind = callRejected
		}
		s.applyCallLocked(attempt, callInput{Kind: kind})
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
	if attempt.Phase == callFailed {
		s.applyCallLocked(attempt, callInput{Kind: callCancel})
		s.mu.Unlock()
		s.notify()
		return nil
	}
	if !s.calls.Connected() {
		s.mu.Unlock()
		return telephony.ErrUnavailable
	}
	effects := s.applyCallLocked(attempt, callInput{Kind: callCancel})
	s.mu.Unlock()
	s.notify()
	if effects.Hangup {
		return s.hangupAttempt(ctx, attempt)
	}
	return nil
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
	if attempt.Phase != callFailed {
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
		if s.invites[attempt.ID] == attempt {
			s.applyCallLocked(attempt, callInput{Kind: callChannelMissing, Now: time.Now(), Timeout: s.cfg.OriginateTimeout})
		}
		s.mu.Unlock()
		s.notify()
		return nil
	}
	return err
}

func (s *Service) handleCallEvent(event telephony.Event) {
	s.mu.Lock()
	attempt := s.invites[event.ID]
	if attempt == nil {
		s.mu.Unlock()
		return
	}
	before := attempt.callModel
	effects := s.applyCallLocked(attempt, callInput{Kind: callTransportEvent, Event: event})
	changed := before != attempt.callModel
	if event.Kind == telephony.OriginationFailed && attempt.Phase == callFailed {
		s.log.Warn("conference: invite failed", "number", attempt.Number, "action_id", event.ID, "reason", attempt.view().Reason)
	}
	s.mu.Unlock()
	if effects.RemoveParticipant {
		s.roster.Remove(event.ID)
	}
	if changed || effects.RemoveParticipant {
		s.notify()
	}
}

func (s *Service) reconcileJoined(participants []Participant) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, p := range participants {
		if attempt := s.invites[p.UniqueID]; attempt != nil {
			before := attempt.callModel
			s.applyCallLocked(attempt, callInput{Kind: callJoined})
			changed = changed || before != attempt.callModel
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
		if attempt.pending() || attempt.stopping() {
			attempts = append(attempts, attempt)
		}
	}
	s.mu.Unlock()
	// Use one time boundary so a deadline crossed during this pass cannot
	// trigger a timeout without the preceding roster check.
	now := time.Now()
	needSnapshot := false
	s.mu.Lock()
	for _, attempt := range attempts {
		if attempt.needsReconcile(now, s.cfg.OriginateTimeout) {
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
		effects := callEffects{}
		if s.invites[attempt.ID] == attempt {
			effects = s.applyCallLocked(attempt, callInput{Kind: callMaintenance, Now: now, Timeout: s.cfg.OriginateTimeout})
		}
		s.mu.Unlock()
		if effects.Hangup {
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
		if !present[id] {
			s.applyCallLocked(attempt, callInput{Kind: callAbsentFromConference})
		}
	}
}
