// Package telephony defines transport-independent outgoing call operations and facts.
package telephony

import "errors"

// Operation errors distinguish unavailable transport, rejection and missing calls.
var (
	ErrUnavailable     = errors.New("telephony: unavailable")
	ErrRejected        = errors.New("telephony: call rejected")
	ErrChannelNotFound = errors.New("telephony: channel not found")
)

// EventKind describes a transport-independent fact about a call.
type EventKind uint8

// Call facts emitted by an integration adapter.
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

// Failure is a normalized reason for an unsuccessful call.
type Failure string

// Failure reasons shared across call integrations and application consumers.
const (
	Unknown     Failure = "Connection failed"
	Busy        Failure = "Busy"
	NoAnswer    Failure = "No answer"
	Unavailable Failure = "Unavailable"
	Congestion  Failure = "Network congestion"
	Cancelled   Failure = "Call cancelled"
	Rejected    Failure = "Call rejected"
)
