package conference_test

import (
	"testing"
	"time"

	"github.com/dmalkin/westbridge/internal/conference"
)

func at(sec int) time.Time {
	return time.Date(2026, 9, 1, 10, 0, sec, 0, time.UTC)
}

func participant(id, channel string, joined time.Time) conference.Participant {
	return conference.Participant{
		UniqueID:    id,
		Channel:     channel,
		CallerIDNum: id,
		JoinedAt:    joined,
	}
}

func ids(ps []conference.Participant) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.UniqueID)
	}
	return out
}

func equalIDs(got []conference.Participant, want ...string) bool {
	have := ids(got)
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if have[i] != want[i] {
			return false
		}
	}
	return true
}

func TestRosterAddAndRemove(t *testing.T) {
	r := conference.NewRoster()

	if !r.Add(participant("a", "PJSIP/1001-1", at(1))) {
		t.Fatal("Add of a new participant reported no change")
	}
	if r.Add(participant("a", "PJSIP/1001-1", at(1))) {
		t.Fatal("Add of an identical participant reported a change")
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1", r.Len())
	}

	got, ok := r.Get("a")
	if !ok || got.Channel != "PJSIP/1001-1" {
		t.Fatalf("Get(a) = %+v, %v", got, ok)
	}

	if _, ok := r.Remove("missing"); ok {
		t.Fatal("Remove of an absent uniqueid reported success")
	}
	if _, ok := r.Remove("a"); !ok {
		t.Fatal("Remove of a present uniqueid reported failure")
	}
	if r.Len() != 0 {
		t.Fatalf("Len after Remove = %d, want 0", r.Len())
	}
}

func TestRosterAddStampsMissingJoinTime(t *testing.T) {
	r := conference.NewRoster()
	before := time.Now()
	r.Add(conference.Participant{UniqueID: "a", Channel: "PJSIP/1001-1"})

	got, _ := r.Get("a")
	if got.JoinedAt.Before(before) || got.JoinedAt.After(time.Now()) {
		t.Fatalf("JoinedAt = %v, want a timestamp taken during the call", got.JoinedAt)
	}
}

func TestRosterSnapshotIsSortedAndDetached(t *testing.T) {
	r := conference.NewRoster()
	r.Add(participant("c", "PJSIP/1003-1", at(30)))
	r.Add(participant("b", "PJSIP/1002-1", at(10)))
	// Same instant as "b": the uniqueid breaks the tie so the order is stable.
	r.Add(participant("a", "PJSIP/1001-1", at(10)))

	snap := r.Snapshot()
	if !equalIDs(snap, "a", "b", "c") {
		t.Fatalf("Snapshot order = %v, want [a b c]", ids(snap))
	}

	snap[0].Channel = "mutated"
	if got, _ := r.Get("a"); got.Channel != "PJSIP/1001-1" {
		t.Fatal("mutating a snapshot changed the roster")
	}
}

func TestRosterSnapshotIsNeverNil(t *testing.T) {
	if snap := conference.NewRoster().Snapshot(); snap == nil {
		t.Fatal("Snapshot of an empty roster is nil, which marshals to JSON null")
	}
}

func TestRosterReplaceKeepsKnownJoinTimes(t *testing.T) {
	r := conference.NewRoster()
	r.Add(participant("a", "PJSIP/1001-1", at(10)))

	// A resync re-derives "a"'s join time from AnsweredTime and gets a
	// slightly different answer; the roster must keep the one it already has.
	changed := r.Replace([]conference.Participant{
		participant("a", "PJSIP/1001-1", at(11)),
		participant("b", "PJSIP/1002-1", at(20)),
	})
	if !changed {
		t.Fatal("Replace that added a participant reported no change")
	}

	got, _ := r.Get("a")
	if !got.JoinedAt.Equal(at(10)) {
		t.Fatalf("JoinedAt after Replace = %v, want the original %v", got.JoinedAt, at(10))
	}
	if !equalIDs(r.Snapshot(), "a", "b") {
		t.Fatalf("Snapshot = %v, want [a b]", ids(r.Snapshot()))
	}
}

func TestRosterReplaceReportsNoChangeForIdenticalMembership(t *testing.T) {
	r := conference.NewRoster()
	ps := []conference.Participant{
		participant("a", "PJSIP/1001-1", at(10)),
		participant("b", "PJSIP/1002-1", at(20)),
	}
	r.Replace(ps)

	if r.Replace(ps) {
		t.Fatal("Replace with identical membership reported a change")
	}
	if !r.Replace(ps[:1]) {
		t.Fatal("Replace that dropped a participant reported no change")
	}
}

func TestRosterReplaceDropsEntriesWithoutIdentity(t *testing.T) {
	r := conference.NewRoster()
	r.Replace([]conference.Participant{
		participant("a", "PJSIP/1001-1", at(10)),
		{Channel: "PJSIP/1002-1"},
	})
	if !equalIDs(r.Snapshot(), "a") {
		t.Fatalf("Snapshot = %v, want [a]", ids(r.Snapshot()))
	}
}

func TestRosterClear(t *testing.T) {
	r := conference.NewRoster()
	if r.Clear() {
		t.Fatal("Clear of an empty roster reported a change")
	}
	r.Add(participant("a", "PJSIP/1001-1", at(10)))
	if !r.Clear() {
		t.Fatal("Clear of a populated roster reported no change")
	}
	if r.Len() != 0 {
		t.Fatalf("Len after Clear = %d, want 0", r.Len())
	}
}

func TestRosterConcurrentAccess(t *testing.T) {
	r := conference.NewRoster()
	done := make(chan struct{})

	for i := range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := range 200 {
				id := string(rune('a' + (i+j)%8))
				r.Add(participant(id, "PJSIP/"+id, at(j%60)))
				r.Get(id)
				r.Snapshot()
				if j%3 == 0 {
					r.Remove(id)
				}
			}
		}()
	}
	for range 4 {
		<-done
	}

	// The race detector is the real assertion here; this only confirms the
	// roster survived the hammering in a coherent state.
	if got, want := len(r.Snapshot()), r.Len(); got != want {
		t.Fatalf("Snapshot holds %d participants but Len reports %d", got, want)
	}
}
