package ami

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dmalkin/westbridge/internal/telephony"
)

type callTransport interface {
	Action(context.Context, *Message) (*Message, error)
	Connected() bool
}

// CallController owns the Local-channel protocol for outgoing conference calls.
// It consumes no event stream and owns no goroutines; the existing dispatcher
// passes messages through DecodeCallEvent in their original order.
type CallController struct {
	client callTransport
	cfg    CallConfig
}

type CallConfig struct {
	Room     string
	Context  string
	CallerID string
	Timeout  time.Duration
}

func NewCallController(client callTransport, cfg CallConfig) *CallController {
	return &CallController{client: client, cfg: cfg}
}

func (c *CallController) Connected() bool { return c.client.Connected() }

func (c *CallController) Originate(ctx context.Context, id, number string) error {
	action := NewAction("Originate")
	action.Add("ActionID", id)
	action.Add("Channel", fmt.Sprintf("Local/%s@%s/n", number, c.cfg.Context))
	action.Add("ChannelId", id)
	action.Add("OtherChannelId", id+"-dial")
	action.Add("Application", "ConfBridge")
	action.Add("Data", c.cfg.Room)
	action.Add("CallerID", c.cfg.CallerID)
	action.Add("Timeout", strconv.FormatInt(c.cfg.Timeout.Milliseconds(), 10))
	action.Add("Async", "true")
	_, err := c.client.Action(ctx, action)
	var rejection *ActionError
	if errors.As(err, &rejection) {
		return fmt.Errorf("%w: %w", telephony.ErrRejected, err)
	}
	return err // Missing acknowledgement leaves the outcome unknown.
}

func (c *CallController) Hangup(ctx context.Context, id string) error {
	action := NewAction("Hangup")
	action.Add("Channel", id)
	_, err := c.client.Action(ctx, action)
	var rejection *ActionError
	if errors.As(err, &rejection) && strings.Contains(strings.ToLower(rejection.Response.Get("Message")), "no such channel") {
		return fmt.Errorf("%w: %w", telephony.ErrChannelNotFound, err)
	}
	return err
}

// DecodeCallEvent translates both Local legs into facts about one attempt.
// The main leg ending terminates the call; the dial leg only refines its cause.
func DecodeCallEvent(msg *Message) (telephony.Event, bool) {
	id := strings.TrimSuffix(msg.Get("Uniqueid"), "-dial")
	e := telephony.Event{ID: id}
	switch strings.ToLower(msg.EventName()) {
	case "newchannel":
		if msg.Get("Uniqueid") != id {
			return e, false
		}
		e.Kind = telephony.ChannelObserved
	case "dialend":
		e.Kind = telephony.FailureObserved
		e.Failure = dialFailure(msg.Get("DialStatus"))
		if e.Failure == "" {
			return e, false
		}
	case "newconnectedline":
		e.Kind = telephony.NameUpdated
		e.Name = msg.Get("ConnectedLineName")
		if e.Name == "" || e.Name == "<unknown>" || e.Name == "unknown" {
			return e, false
		}
	case "originateresponse":
		e.ID = msg.ActionID()
		if msg.IsSuccess() {
			e.Kind = telephony.Answered
		} else {
			e.Kind = telephony.OriginationFailed
			e.Failure = originateFailure(msg.Get("Reason"))
		}
	case "hangup":
		e.Failure = hangupFailure(msg.Get("Cause"))
		if msg.Get("Uniqueid") == id {
			e.Kind = telephony.Ended
		} else {
			e.Kind = telephony.FailureObserved
			if e.Failure == telephony.Unknown || msg.Get("Cause") == "16" {
				return e, false
			}
		}
	default:
		return e, false
	}
	return e, e.ID != ""
}

func dialFailure(status string) telephony.Failure {
	switch strings.ToUpper(status) {
	case "BUSY":
		return telephony.Busy
	case "NOANSWER":
		return telephony.NoAnswer
	case "CHANUNAVAIL":
		return telephony.Unavailable
	case "CONGESTION":
		return telephony.Congestion
	case "CANCEL":
		return telephony.Cancelled
	case "DONTCALL", "TORTURE", "INVALIDARGS":
		return telephony.Rejected
	default:
		return ""
	}
}
func originateFailure(reason string) telephony.Failure {
	switch reason {
	case "1":
		return telephony.NoAnswer
	case "3":
		return telephony.NoAnswer
	case "5":
		return telephony.Busy
	case "8":
		return telephony.Congestion
	default:
		return telephony.Unknown
	}
}
func hangupFailure(cause string) telephony.Failure {
	switch cause {
	case "17":
		return telephony.Busy
	case "18", "19", "16":
		return telephony.NoAnswer
	case "21":
		return telephony.Rejected
	case "1", "3", "20", "27":
		return telephony.Unavailable
	case "34", "38", "41", "42", "44", "47":
		return telephony.Congestion
	default:
		return telephony.Unknown
	}
}
