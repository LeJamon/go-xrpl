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

// trackerSweepInterval matches rippled's shortest application sweep cadence
// while avoiding a full history scan on every router maintenance tick.
const trackerSweepInterval = 10 * time.Second

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
	clock     Clock
	nextSweep time.Time
	stopped   bool
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
	return NewTrackerWithClock(SystemClock)
}

// NewTrackerWithClock returns an empty Tracker driven by clock. A nil clock
// uses SystemClock.
func NewTrackerWithClock(clock Clock) *Tracker {
	if clock == nil {
		clock = SystemClock
	}
	now := clock.Now()
	return &Tracker{
		active:    make(map[[32]byte]*Ledger),
		completed: make(map[[32]byte]completedRecord),
		failures:  make(map[[32]byte]failureRecord),
		clock:     clock,
		nextSweep: now.Add(trackerSweepInterval),
	}
}

// Track registers an acquisition. The owner must finalize it with Remove.
func (t *Tracker) Track(l *Ledger) {
	if t == nil || l == nil {
		return
	}
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
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
	if t.stopped {
		return nil, false
	}
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
	l := t.Find(hash)
	if l == nil {
		return
	}
	t.RemoveExpectedWithSnapshot(l, l.Snapshot(), complete)
}

// RemoveWithSnapshot finalizes an acquisition with a snapshot prepared by the
// acquisition worker, avoiding a missing-node diagnostic walk on the Router.
func (t *Tracker) RemoveWithSnapshot(hash [32]byte, snap Snapshot, complete bool) {
	if t == nil {
		return
	}
	l := t.Find(hash)
	if l == nil {
		return
	}
	t.RemoveExpectedWithSnapshot(l, snap, complete)
}

// RemoveExpectedWithSnapshot finalizes ledger only if it is still the active
// acquisition for its hash. This makes worker-result retirement atomic with
// the identity check.
func (t *Tracker) RemoveExpectedWithSnapshot(l *Ledger, snap Snapshot, complete bool) bool {
	if t == nil || l == nil {
		return false
	}
	hash := l.Hash()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[hash] != l {
		return false
	}
	now := t.clock.Now()
	t.sweepIfDueLocked(now)
	t.removeLocked(hash, snap, complete, now)
	return true
}

// DiscardExpected removes a locally-aborted acquisition without recording a
// network failure or completion. Successfully persisted partial nodes remain
// reusable by the next acquisition.
func (t *Tracker) DiscardExpected(l *Ledger) bool {
	if t == nil || l == nil {
		return false
	}
	hash := l.Hash()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[hash] != l {
		return false
	}
	delete(t.active, hash)
	return true
}

func (t *Tracker) removeLocked(hash [32]byte, snap Snapshot, complete bool, now time.Time) {
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

// CountReason returns how many in-flight acquisitions carry the given reason.
// The router uses it to bound concurrent consensus-driven catch-up
// acquisitions (ReasonConsensus) so a stream of gossiped tips can't fan out
// into one acquisition per event.
func (t *Tracker) CountReason(reason Reason) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, l := range t.active {
		if l.Reason() == reason {
			n++
		}
	}
	return n
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

// Clear resets both the in-flight set and the recent-failure history and
// returns the removed acquisitions for owner-side resource retirement.
func (t *Tracker) Clear() []*Ledger {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	active := t.clearLocked()
	t.mu.Unlock()
	return active
}

// Stop terminally drains the tracker and rejects later admissions. A stopped
// tracker belongs to a stopped Router and is not reusable.
func (t *Tracker) Stop() []*Ledger {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	t.stopped = true
	active := t.clearLocked()
	t.mu.Unlock()
	return active
}

func (t *Tracker) clearLocked() []*Ledger {
	active := make([]*Ledger, 0, len(t.active))
	for _, l := range t.active {
		active = append(active, l)
	}
	t.active = make(map[[32]byte]*Ledger)
	t.completed = make(map[[32]byte]completedRecord)
	t.failures = make(map[[32]byte]failureRecord)
	return active
}

// Sweep removes expired terminal acquisition history when its maintenance
// interval has elapsed.
func (t *Tracker) Sweep() {
	if t == nil {
		return
	}
	t.mu.Lock()
	now := t.clock.Now()
	t.sweepIfDueLocked(now)
	t.mu.Unlock()
}

func (t *Tracker) sweepIfDueLocked(now time.Time) {
	if t.stopped || now.Before(t.nextSweep) {
		return
	}
	t.nextSweep = now.Add(trackerSweepInterval)
	for hash, rec := range t.completed {
		if rec.at.Add(completedRetention).Before(now) {
			delete(t.completed, hash)
		}
	}
	for hash, rec := range t.failures {
		if !rec.at.Add(reacquireInterval).After(now) {
			delete(t.failures, hash)
		}
	}
}

// Info returns the fetch_info snapshot keyed by ledger sequence (decimal, when
// seq > 1) or hash, mirroring rippled InboundLedgers::getInfo. In-flight entries
// report have_header/have_state/have_transactions/peers and the latest cached
// needed_*_hashes after an outstanding tree has been scanned; completed entries
// report complete:true until their retention window elapses; recent failures
// report failed:true with the same per-tree fields.
// Terminal lifecycle belongs to the acquisition owner; Info only reads each
// acquisition's cached worker frontier and retained terminal records.
func (t *Tracker) Info() map[string]any {
	if t == nil {
		return map[string]any{}
	}

	t.mu.Lock()
	active := make(map[[32]byte]*Ledger, len(t.active))
	for hash, l := range t.active {
		active[hash] = l
	}
	ret := make(map[string]any)
	for hash, rec := range t.failures {
		ret[acquisitionKey(rec.snap.Seq, hash)] = AcquisitionJSON(rec.snap)
	}

	for hash, rec := range t.completed {
		ret[acquisitionKey(rec.snap.Seq, hash)] = AcquisitionJSON(rec.snap)
	}
	t.mu.Unlock()

	for hash, l := range active {
		snap := l.Snapshot()
		ret[acquisitionKey(snap.Seq, hash)] = AcquisitionJSON(snap)
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
// plus the needed_*_hashes arrays for outstanding trees whose frontier has been
// scanned at least once.
func AcquisitionJSON(snap Snapshot) map[string]any {
	entry := map[string]any{
		"hash":                          fmt.Sprintf("%X", snap.Hash),
		"have_header":                   snap.HaveHeader,
		"request_peers":                 snap.RequestPeers,
		"state_received_total":          snap.StateReceived,
		"state_useful_total":            snap.StateUseful,
		"tx_received_total":             snap.TxReceived,
		"tx_useful_total":               snap.TxUseful,
		"state_equal_subtrees_skipped":  snap.StateEqualSubtreesSkipped,
		"state_nodes_descended":         snap.StateNodesDescended,
		"state_durable_reads":           snap.StateDurableReads,
		"state_missing_discovered":      snap.StateMissingDiscovered,
		"state_verified_base_fallbacks": snap.StateVerifiedBaseFallbacks,
		// Live no-progress retry count, mirroring InboundLedger::getJson's
		// timeouts_ now that the acquisition runs a timer-driven retry loop.
		"timeouts": snap.Timeouts,
	}
	switch {
	case snap.Complete:
		entry["complete"] = true
	case snap.Failed:
		entry["failed"] = true
	default:
		// peers appears only while in flight, matching rippled's
		// !complete_ && !failed_ gate: the broadened source-peer set size.
		entry["peers"] = snap.Peers
	}
	if snap.HaveHeader {
		entry["have_state"] = snap.HaveState
		entry["have_transactions"] = snap.HaveTransactions
		if !snap.HaveState && snap.NeededState != nil {
			entry["needed_state_hashes"] = hashList(snap.NeededState)
		}
		if !snap.HaveTransactions && snap.NeededTx != nil {
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
