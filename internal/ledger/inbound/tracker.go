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
// fetch_info RPC. It is the go-xrpl analogue of rippled's InboundLedgers:
// the router registers each legacy acquisition via Track, and Tracker reads
// the acquisitions' own mutex-guarded state to build the snapshot — so it is
// safe to query from an RPC goroutine while the router drives acquisition
// from its own goroutine.
//
// Only the classic header + state + transaction acquisitions are tracked here;
// the replay delta / skip-list paths map to rippled's separate LedgerReplayer,
// which fetch_info does not cover.
type Tracker struct {
	mu        sync.Mutex
	active    map[[32]byte]*Ledger
	completed map[[32]byte]completedRecord
	failures  map[[32]byte]failureRecord
}

type failureRecord struct {
	snap Snapshot
	at   time.Time
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

// Find returns the in-flight acquisition for hash, or nil. Completed/failed
// acquisitions (already finalized via Remove, or not yet swept) are not
// returned, so callers route inbound data only to live acquisitions. Mirrors
// rippled InboundLedgers::find.
func (t *Tracker) Find(hash [32]byte) *Ledger {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active[hash]
}

// GetOrCreate returns the existing in-flight acquisition for hash, or registers
// a new one produced by factory (which must not block — peer I/O belongs to the
// caller, issued only when created is true). Mirrors rippled
// InboundLedgers::acquire's findCreate step. factory returning nil yields
// (nil,false).
func (t *Tracker) GetOrCreate(hash [32]byte, factory func() *Ledger) (l *Ledger, created bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing := t.active[hash]; existing != nil {
		return existing, false
	}
	l = factory()
	if l == nil {
		return nil, false
	}
	t.active[hash] = l
	return l, true
}

// Remove finalizes an in-flight acquisition: it records the terminal snapshot
// for fetch_info retention (completed window when complete, failure window
// otherwise) and drops it from the in-flight set. Idempotent — a no-op if the
// hash is not currently active.
func (t *Tracker) Remove(hash [32]byte, complete bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.active[hash]
	if l == nil {
		return
	}
	snap := l.Snapshot()
	now := time.Now()
	if complete {
		// The caller's verdict is authoritative — stamp the terminal flag so
		// the retained snapshot renders complete:true regardless of any race
		// on the acquisition's own state read (symmetric with the failure
		// branch below).
		snap.Complete = true
		snap.Failed = false
		t.completed[hash] = completedRecord{snap: snap, at: now}
	} else {
		snap.Failed = true
		snap.Complete = false
		t.failures[hash] = failureRecord{snap: snap, at: now}
	}
	delete(t.active, hash)
}

// ActiveTimedOut returns the in-flight acquisitions that have exceeded the
// acquisition timeout, for the router's maintenance reaper. The caller decides
// recovery and finalizes each via Remove.
func (t *Tracker) ActiveTimedOut() []*Ledger {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*Ledger
	for _, l := range t.active {
		if l.IsTimedOut() {
			out = append(out, l)
		}
	}
	return out
}

// Active returns every in-flight acquisition currently tracked. The router
// iterates these to attempt local completion from the fetch-pack cache
// (Ledger.CheckLocal), mirroring rippled's InboundLedgers::gotFetchPack which
// calls checkLocal on each live acquisition (InboundLedgers.cpp:359-380).
func (t *Tracker) Active() []*Ledger {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*Ledger, 0, len(t.active))
	for _, l := range t.active {
		out = append(out, l)
	}
	return out
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
// report have_header/have_state/have_transactions/peers and the needed_*_hashes
// for whichever tree is outstanding; completed entries report complete:true
// until their retention window elapses; recent failures report failed:true with
// the same per-tree fields (mirroring rippled's still-in-mLedgers getJson).
// Reconciling the active set (move completed to the retained set, demote
// failed/timed-out to failures) and expiring stale entries happens here.
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
			// A timed-out acquisition reports as failed for fetch_info (go-xrpl
			// reaps on first timeout); mark it before retaining the snapshot so
			// the failure entry mirrors rippled's still-in-mLedgers getJson.
			snap.Failed = true
			t.failures[hash] = failureRecord{snap: snap, at: now}
			delete(t.active, hash)
		default:
			live[acquisitionKey(snap.Seq, hash)] = AcquisitionJSON(snap)
		}
	}

	ret := make(map[string]any)

	for hash, rec := range t.failures {
		if now.Sub(rec.at) > reacquireInterval {
			delete(t.failures, hash)
			continue
		}
		ret[acquisitionKey(rec.snap.Seq, hash)] = AcquisitionJSON(rec.snap)
	}

	for hash, rec := range t.completed {
		if now.Sub(rec.at) > completedRetention {
			delete(t.completed, hash)
			continue
		}
		ret[acquisitionKey(rec.snap.Seq, hash)] = AcquisitionJSON(rec.snap)
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

// AcquisitionJSON mirrors rippled's InboundLedger::getJson
// (InboundLedger.cpp:1302-1349): hash and timeouts always; complete/failed/peers
// gated by state; and, once the header is in hand, have_state/have_transactions
// plus the needed_*_hashes arrays for whichever tree is still outstanding.
func AcquisitionJSON(snap Snapshot) map[string]any {
	entry := map[string]any{
		"hash":        fmt.Sprintf("%X", snap.Hash),
		"have_header": snap.HaveHeader,
		// go-xrpl reaps on the first timeout, so rippled's retry count is always
		// zero; emit it for wire-shape parity with InboundLedger::getJson.
		"timeouts": 0,
	}
	switch {
	case snap.Complete:
		entry["complete"] = true
	case snap.Failed:
		entry["failed"] = true
	default:
		// peers appears only while in flight, matching rippled's
		// !complete_ && !failed_ gate. Classic acquisition uses one source peer.
		entry["peers"] = snap.Peers
	}
	if snap.HaveHeader {
		entry["have_state"] = snap.HaveState
		entry["have_transactions"] = snap.HaveTransactions
		if !snap.HaveState {
			entry["needed_state_hashes"] = hashList(snap.NeededState)
		}
		if !snap.HaveTransactions {
			entry["needed_transaction_hashes"] = hashList(snap.NeededTx)
		}
	}
	return entry
}

func hashList(hs [][32]byte) []any {
	out := make([]any, 0, len(hs))
	for _, h := range hs {
		out = append(out, fmt.Sprintf("%X", h))
	}
	return out
}
