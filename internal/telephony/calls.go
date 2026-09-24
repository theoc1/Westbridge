// Package telephony defines transport-independent outgoing call operations and facts.
package telephony

import "errors"

var (
	ErrUnavailable     = errors.New("telephony: unavailable")
	ErrRejected        = errors.New("telephony: call rejected")
	ErrChannelNotFound = errors.New("telephony: channel not found")
)

type EventKind uint8

const (
	ChannelObserved EventKind = iota + 1
	FailureObserved
	NameUpdated
	Answered
	OriginationFailed
	Ended
)

// Event identifies the application attempt, never a transport-specific call leg.
type Event struct {
	ID      string
	Kind    EventKind
	Failure Failure
	Name    string
}

type Failure string

const (
	Unknown     Failure = "Connection failed"
	Busy        Failure = "Busy"
	NoAnswer    Failure = "No answer"
	Unavailable Failure = "Unavailable"
	Congestion  Failure = "Network congestion"
	Cancelled   Failure = "Call cancelled"
	Rejected    Failure = "Call rejected"
)
