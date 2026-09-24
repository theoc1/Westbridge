package conference

import (
	"testing"
	"time"

	"github.com/dmalkin/westbridge/internal/telephony"
)

func TestCallModelSequences(t *testing.T) {
	now := time.Unix(1000, 0)
	event := func(k telephony.EventKind, f telephony.Failure) callInput {
		return callInput{Kind: callTransportEvent, Event: telephony.Event{Kind: k, Failure: f}}
	}
	type step struct {
		input   callInput
		phase   callPhase
		stop    stopReason
		hangup  bool
		failure telephony.Failure
	}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"normal lifecycle", []step{
			{input: event(telephony.Answered, ""), phase: callJoining},
			{input: callInput{Kind: callJoined}, phase: callConnected},
			{input: event(telephony.Ended, telephony.NoAnswer), phase: callEnded},
			{input: callInput{Kind: callJoined}, phase: callEnded},
		}},
		{"cancel before allocation and late answer", []step{
			{input: callInput{Kind: callCancel}, phase: callDialing, stop: stopByUser, hangup: true},
			{input: callInput{Kind: callChannelMissing, Now: now, Timeout: 30 * time.Second}, phase: callDialing, stop: stopByUser},
			{input: event(telephony.Answered, ""), phase: callJoining, stop: stopByUser},
			{input: callInput{Kind: callJoined}, phase: callConnected, stop: stopByUser},
			{input: callInput{Kind: callMaintenance, Now: now}, phase: callConnected, stop: stopByUser, hangup: true},
			{input: event(telephony.Ended, telephony.NoAnswer), phase: callEnded, stop: stopByUser},
		}},
		{"uncertain acknowledgement then join", []step{
			{input: callInput{Kind: callAcknowledgementUnknown}, phase: callDialing},
			{input: callInput{Kind: callJoined}, phase: callConnected},
			{input: callInput{Kind: callAcknowledgementUnknown}, phase: callConnected},
			{input: event(telephony.OriginationFailed, telephony.Unknown), phase: callConnected},
		}},
		{"timeout after reconciliation", []step{
			{input: callInput{Kind: callMaintenance, Now: now.Add(time.Minute), Timeout: 30 * time.Second}, phase: callDialing, stop: stopByTimeout, hangup: true},
			{input: callInput{Kind: callChannelMissing, Now: now.Add(time.Minute), Timeout: 30 * time.Second}, phase: callFailed, failure: telephony.NoAnswer},
			{input: callInput{Kind: callCancel}, phase: callEnded, failure: telephony.NoAnswer},
		}},
		{"missed join recovered before timeout", []step{
			{input: callInput{Kind: callJoined}, phase: callConnected},
			{input: callInput{Kind: callMaintenance, Now: now.Add(time.Minute), Timeout: 30 * time.Second}, phase: callConnected},
			{input: callInput{Kind: callAbsentFromConference}, phase: callEnded},
		}},
		{"late failure details and stale answer", []step{
			{input: event(telephony.Ended, telephony.Unknown), phase: callFailed, failure: telephony.Unknown},
			{input: event(telephony.FailureObserved, telephony.Busy), phase: callFailed, failure: telephony.Busy},
			{input: event(telephony.OriginationFailed, telephony.Unknown), phase: callFailed, failure: telephony.Busy},
			{input: event(telephony.Answered, ""), phase: callFailed, failure: telephony.Busy},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := callModel{ID: "attempt", Number: "1002", CreatedAt: now}
			for i, step := range tc.steps {
				var effects callEffects
				m, effects = transitionCall(m, step.input)
				if m.Phase != step.phase || m.Stop != step.stop || m.Failure != step.failure || effects.Hangup != step.hangup {
					t.Fatalf("step %d: model=%+v effects=%+v", i, m, effects)
				}
			}
		})
	}
}

func TestCallModelViewAndIdempotency(t *testing.T) {
	m := callModel{ID: "a", Number: "1002", CreatedAt: time.Unix(1000, 0)}
	input := callInput{Kind: callTransportEvent, Event: telephony.Event{Kind: telephony.Answered}}
	joined, _ := transitionCall(m, input)
	duplicate, _ := transitionCall(joined, input)
	if duplicate != joined {
		t.Fatal("duplicate answer changed state")
	}
	if got := joined.view(); got.State != "dialing" || got.Reason != "Answered; joining conference…" || got.Cancelling {
		t.Fatal(got)
	}
	cancelled, effects := transitionCall(joined, callInput{Kind: callCancel})
	if !effects.Hangup || !cancelled.view().Cancelling {
		t.Fatal("missing cancellation")
	}
	// Acknowledging the Hangup needs no transition: only an actual termination
	// or sufficiently authoritative missing-channel result completes cancellation.
	if cancelled.Phase != callJoining {
		t.Fatal("command completed call prematurely")
	}
	ended, effects := transitionCall(cancelled, callInput{Kind: callTransportEvent, Event: telephony.Event{Kind: telephony.Ended}})
	if ended.Phase != callEnded || !effects.RemoveParticipant {
		t.Fatal("termination did not remove participant")
	}
}
