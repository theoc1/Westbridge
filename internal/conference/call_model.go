package conference

import (
	"time"

	"github.com/dmalkin/westbridge/internal/telephony"
)

type callPhase uint8

const (
	callDialing callPhase = iota
	callJoining
	callConnected
	callFailed
	callEnded
)

type stopReason uint8

const (
	stopNone stopReason = iota
	stopByUser
	stopByTimeout
)

// callModel contains facts and intent, not locks, clocks or transport operations.
// Stop is independent of Phase: an answer must not undo a pending cancellation.
type callModel struct {
	ID, Number  string
	CreatedAt   time.Time
	Phase       callPhase
	Stop        stopReason
	ChannelSeen bool
	Name        string
	Failure     telephony.Failure
	DialFailure telephony.Failure
	Checking    bool
}

type callInputKind uint8

const (
	callTransportEvent callInputKind = iota
	callAcknowledgementUnknown
	callRejected
	callCancel
	callJoined
	callAbsentFromConference
	callMaintenance // Only after a successful roster check if the deadline passed.
	callChannelMissing
)

type callInput struct {
	Kind    callInputKind
	Event   telephony.Event
	Now     time.Time
	Timeout time.Duration
}

type callEffects struct {
	Hangup            bool
	RemoveParticipant bool
}

func (m callModel) pending() bool  { return m.Phase == callDialing || m.Phase == callJoining }
func (m callModel) stopping() bool { return m.Stop != stopNone }
func (m callModel) overdue(now time.Time, timeout time.Duration) bool {
	return now.Sub(m.CreatedAt) > timeout+10*time.Second
}
func (m callModel) needsReconcile(now time.Time, timeout time.Duration) bool {
	return m.pending() && !m.stopping() && m.overdue(now, timeout)
}

// transitionCall is the single place that changes a call's lifecycle. Duplicate
// facts are harmless; late failure details can refine a failed attempt, while
// only a confirmed conference join can recover it into Connected.
func transitionCall(m callModel, in callInput) (callModel, callEffects) {
	effects := callEffects{}
	if m.Phase == callEnded {
		return m, effects
	}
	finishStop := func() {
		if m.Stop == stopByTimeout {
			m.Phase, m.Failure, m.Stop = callFailed, telephony.NoAnswer, stopNone
		} else {
			m.Phase = callEnded
		}
	}
	fail := func(reason telephony.Failure) {
		if m.DialFailure != "" {
			reason = m.DialFailure
		}
		if m.Phase != callFailed || reason != telephony.Unknown || m.Failure == "" {
			m.Failure = reason
		}
		m.Phase = callFailed
	}
	switch in.Kind {
	case callAcknowledgementUnknown:
		if m.pending() {
			m.Checking = true
		}
	case callRejected:
		if m.pending() {
			m.Phase, m.Failure = callFailed, telephony.Failure("Asterisk rejected the call")
		}
	case callCancel:
		if m.Phase == callFailed {
			m.Phase = callEnded
		} else {
			// Preserve timeout policy if automatic cancellation is already underway.
			if m.Stop == stopNone {
				m.Stop = stopByUser
			}
			effects.Hangup = true
		}
	case callJoined:
		m.Phase, m.Failure, m.Checking = callConnected, "", false
	case callAbsentFromConference:
		if m.Phase == callConnected && !m.stopping() {
			m.Phase = callEnded
		}
	case callMaintenance:
		if m.needsReconcile(in.Now, in.Timeout) {
			m.Stop = stopByTimeout
		}
		effects.Hangup = m.stopping()
	case callChannelMissing:
		if m.stopping() && (m.ChannelSeen || m.Phase == callFailed || m.overdue(in.Now, in.Timeout)) {
			finishStop()
		}
	case callTransportEvent:
		e := in.Event
		switch e.Kind {
		case telephony.ChannelObserved:
			m.ChannelSeen = true
		case telephony.NameUpdated:
			m.Name = e.Name
		case telephony.FailureObserved:
			m.DialFailure = e.Failure
			if m.Phase == callFailed {
				m.Failure = e.Failure
			}
		case telephony.Answered:
			m.ChannelSeen = true
			if m.pending() {
				m.Phase, m.Checking = callJoining, false
			}
		case telephony.OriginationFailed:
			if m.Phase != callConnected {
				if m.stopping() {
					finishStop()
				} else {
					fail(e.Failure)
				}
			}
		case telephony.Ended:
			effects.RemoveParticipant = true
			if m.stopping() {
				finishStop()
			} else if m.Phase == callConnected {
				m.Phase = callEnded
			} else if m.Phase != callFailed {
				fail(e.Failure)
			}
		}
	}
	return m, effects
}

// view preserves the existing HTTP/WebSocket representation. Joining remains a
// yellow dialing row; Connected is rendered from the participant roster.
func (m callModel) view() Call {
	c := Call{ID: m.ID, Number: m.Number, CreatedAt: m.CreatedAt, State: "dialing"}
	switch m.Phase {
	case callJoining:
		c.Reason = "Answered; joining conference…"
	case callConnected:
		c.State = "connected"
	case callFailed:
		c.State, c.Reason = "failed", string(m.Failure)
	}
	if m.pending() && m.Checking {
		c.Reason = "Checking call status…"
	}
	if m.stopping() {
		c.State, c.Reason, c.Cancelling = "dialing", "Cancelling…", true
	}
	return c
}
