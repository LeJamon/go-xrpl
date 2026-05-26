package inbound

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// reacquireInterval bounds how long a failed acquisition is remembered, so
// fetch_info reports a recently-failed ledger before letting it expire.
// Mirrors rippled's kReacquireInterval expiry on InboundLedgers::mRecentFailures.
const reacquireInterval = 5 * time.Minute

// completedRetention bounds how long a finished acquisition keeps appearing in
// fetch_info before being dropped, mirroring rippled's ~1-minute mLedgers sweep
// window (InboundLedgers::sweep) during which getInfo still reports complete:true.
const completedRetention = time.Minute

// Tracker aggregates the in-flight classic ledger acquisitions and a short
// history of recent failures, producing the JSON snapshot served by the
// fetch_info RPC. It is the goXRPL analogue of rippled's InboundLedgers:
// the router registers each legacy acquisition via Track, and Tracker reads
// the acquisitions' own mutex-guarded state to build the snapshot — so it is
// safe to query from an RPC goroutine while the router drives acquisition
// from its own goroutine.
//
// Only the classic header+state acquisitions are tracked here; the replay
// delta / skip-list paths map to rippled's separate LedgerReplayer, which
// fetch_info does not cover.
type Tracker struct {
	mu        sync.Mutex
	active    map[[32]byte]*Ledger
	completed map[[32]byte]completedRecord
	failures  map[[32]byte]failureRecord
}

type failureRecord struct {
	seq uint32
	at  time.Time
}

type completedRecord struct {
	snap Snapshot
	at   time.Time
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{
		active:    make(map[[32]byte]*Ledger),
		completed: make(map[[32]byte]completedRecord),
		failures:  make(map[[32]byte]failureRecord),
	}
}

// Track registers an acquisition. Completed/failed/timed-out acquisitions are
// swept out lazily on the next Info call, so callers never need to untrack.
func (t *Tracker) Track(l *Ledger) {
	if t == nil || l == nil {
		return
	}
	t.mu.Lock()
	t.active[l.Hash()] = l
	t.mu.Unlock()
}

// Clear resets both the in-flight set and the recent-failure history,
// backing fetch_info's `clear` param (rippled InboundLedgers::clearFailures,
// which clears mRecentFailures and mLedgers).
func (t *Tracker) Clear() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.active = make(map[[32]byte]*Ledger)
	t.completed = make(map[[32]byte]completedRecord)
	t.failures = make(map[[32]byte]failureRecord)
	t.mu.Unlock()
}

// Info returns the fetch_info snapshot keyed by ledger sequence (decimal, when
// seq > 1) or hash, mirroring rippled InboundLedgers::getInfo. In-flight entries
// report have_header/have_state/peers/needed_state_hashes; completed entries
// report complete:true until their retention window elapses; recent failures
// report {"failed": true}. Reconciling the active set (move completed to the
// retained set, demote failed/timed-out to failures) and expiring stale
// completed/failure entries happens here.
func (t *Tracker) Info() map[string]any {
	if t == nil {
		return map[string]any{}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()

	// Reconcile the active set first, then assemble the result. Live entries
	// are written last so they take precedence over a same-key completed or
	// failure entry, matching rippled's getInfo which writes failures before
	// the mLedgers acquisitions that can overwrite them.
	live := make(map[string]map[string]any)
	for hash, l := range t.active {
		snap := l.Snapshot()
		switch {
		case snap.Complete:
			t.completed[hash] = completedRecord{snap: snap, at: now}
			delete(t.active, hash)
		case snap.Failed || snap.TimedOut:
			t.failures[hash] = failureRecord{seq: snap.Seq, at: now}
			delete(t.active, hash)
		default:
			live[acquisitionKey(snap.Seq, hash)] = acquisitionJSON(snap)
		}
	}

	ret := make(map[string]any)

	for hash, rec := range t.failures {
		if now.Sub(rec.at) > reacquireInterval {
			delete(t.failures, hash)
			continue
		}
		ret[acquisitionKey(rec.seq, hash)] = map[string]any{"failed": true}
	}

	for hash, rec := range t.completed {
		if now.Sub(rec.at) > completedRetention {
			delete(t.completed, hash)
			continue
		}
		ret[acquisitionKey(rec.snap.Seq, hash)] = acquisitionJSON(rec.snap)
	}

	for key, entry := range live {
		ret[key] = entry
	}

	return ret
}

// acquisitionKey mirrors rippled's getInfo keying: by sequence number when it
// is a real (post-genesis) sequence, otherwise by hash.
func acquisitionKey(seq uint32, hash [32]byte) string {
	if seq > 1 {
		return strconv.FormatUint(uint64(seq), 10)
	}
	return fmt.Sprintf("%X", hash)
}

func acquisitionJSON(snap Snapshot) map[string]any {
	entry := map[string]any{
		"hash":        fmt.Sprintf("%X", snap.Hash),
		"have_header": snap.HaveHeader,
		// goXRPL reaps on the first timeout, so rippled's retry count is always
		// zero; emit it for wire-shape parity with InboundLedger::getJson.
		"timeouts": 0,
	}
	if snap.Complete {
		entry["complete"] = true
	} else {
		// peers appears only while in flight, matching rippled's
		// !complete_ && !failed_ gate. Classic acquisition uses one source peer.
		entry["peers"] = 1
	}
	if snap.HaveHeader {
		entry["have_state"] = snap.HaveState
		if !snap.HaveState {
			needed := make([]any, 0, len(snap.NeededState))
			for _, h := range snap.NeededState {
				needed = append(needed, fmt.Sprintf("%X", h))
			}
			entry["needed_state_hashes"] = needed
		}
	}
	return entry
}
