package conference

import (
	"sort"
	"sync"
	"time"
)

// Roster is the current membership of the conference, keyed by channel
// uniqueid. It is safe for concurrent use: the AMI event loop mutates it while
// HTTP handlers read snapshots off it.
type Roster struct {
	mu sync.RWMutex
	m  map[string]Participant
}

// NewRoster returns an empty roster.
func NewRoster() *Roster {
	return &Roster{m: make(map[string]Participant)}
}

// Add inserts or updates a participant and reports whether anything changed.
// A zero JoinedAt is stamped with the current time.
func (r *Roster) Add(p Participant) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev, existed := r.m[p.UniqueID]
	p.JoinedAt = r.resolveJoinedAt(p, prev, existed)
	if p.JoinedAt.IsZero() {
		p.JoinedAt = time.Now()
	}
	if existed && prev == p {
		return false
	}
	r.m[p.UniqueID] = p
	return true
}

// Remove drops a participant by uniqueid, reporting whether it was there.
func (r *Roster) Remove(uniqueID string) (Participant, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.m[uniqueID]
	if ok {
		delete(r.m, uniqueID)
	}
	return p, ok
}

// Get looks a participant up by uniqueid.
func (r *Roster) Get(uniqueID string) (Participant, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.m[uniqueID]
	return p, ok
}

// Replace swaps the whole membership for ps, which is what a ConfbridgeList
// resync does. It reports whether the result differs from what was there.
//
// A participant already present keeps the join time the roster already knows,
// because a join happens once. New participants keep their own JoinedAt;
// zero remains unknown when first discovered by a snapshot.
func (r *Roster) Replace(ps []Participant) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]Participant, len(ps))
	for _, p := range ps {
		if p.UniqueID == "" {
			continue
		}
		prev, existed := r.m[p.UniqueID]
		p.JoinedAt = r.resolveJoinedAt(p, prev, existed)
		next[p.UniqueID] = p
	}

	changed := len(next) != len(r.m)
	if !changed {
		for id, p := range next {
			if prev, ok := r.m[id]; !ok || prev != p {
				changed = true
				break
			}
		}
	}

	r.m = next
	return changed
}

// Clear empties the roster, reporting whether it held anything.
func (r *Roster) Clear() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.m) == 0 {
		return false
	}
	r.m = make(map[string]Participant)
	return true
}

// Len is the number of participants currently in the conference.
func (r *Roster) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.m)
}

// Snapshot returns a copy of the membership ordered by join time and then by
// uniqueid. The secondary key matters: two participants can share a join
// timestamp, and an unstable order would make rows jump between renders.
//
// The result is never nil, so it marshals to a JSON array rather than null.
func (r *Roster) Snapshot() []Participant {
	r.mu.RLock()
	out := make([]Participant, 0, len(r.m))
	for _, p := range r.m {
		out = append(out, p)
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if !out[i].JoinedAt.Equal(out[j].JoinedAt) {
			return out[i].JoinedAt.Before(out[j].JoinedAt)
		}
		return out[i].UniqueID < out[j].UniqueID
	})
	return out
}

// resolveJoinedAt implements the join-time rule described on Replace. It must
// be called with the lock held.
func (r *Roster) resolveJoinedAt(p, prev Participant, existed bool) time.Time {
	switch {
	case existed && !prev.JoinedAt.IsZero():
		return prev.JoinedAt
	case !p.JoinedAt.IsZero():
		return p.JoinedAt
	default:
		return time.Time{}
	}
}

// SetMuted updates only an existing participant, without recreating departed calls.
func (r *Roster) SetMuted(id string, muted bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.m[id]
	if !ok || p.Muted == muted {
		return false
	}
	p.Muted = muted
	r.m[id] = p
	return true
}
