// Copyright (c) 2024-2026. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package inbound

import (
	"errors"
	"log/slog"
	"sort"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// DefaultMaxInFlightReplays caps concurrent replay-delta acquisitions.
// Chosen to cover a catchup burst without monopolizing a peer's
// request-serving capacity. Does NOT mirror a specific rippled
// constant — rippled's MAX_TASKS=10 is for full LedgerReplayTasks
// (multi-ledger jobs), not per-ledger sub-acquisitions, and rippled
// has no explicit cap on in-flight LedgerDeltaAcquire instances. 16
// is our own tuning knob; bump it if catchup stalls with many peers.
const DefaultMaxInFlightReplays = 16

// Sentinel errors the Replayer returns so callers can distinguish
// duplicate/capacity/unmatched conditions without string-matching.
var (
	// ErrAcquisitionExists signals Acquire was called for a hash that
	// already has an in-flight acquisition. The caller is expected to
	// drop the duplicate rather than double-issue a wire request.
	ErrAcquisitionExists = errors.New("replay delta acquisition already in flight for this hash")

	// ErrCapacityFull signals Acquire was called while the coordinator
	// is at its maxInFlight cap. The caller should back off and retry
	// after some existing acquisition completes or is abandoned.
	ErrCapacityFull = errors.New("replay delta acquisition capacity full")

	// ErrNoMatchingAcquisition signals HandleResponse received a
	// response whose LedgerHash doesn't match any in-flight acquisition.
	// This is a normal race (a stale or unsolicited reply) and should
	// be dropped silently by the caller.
	ErrNoMatchingAcquisition = errors.New("no in-flight replay delta acquisition matches this response hash")
)

// TimedOutEntry is a compact summary of a timed-out acquisition. Lets
// the router re-issue via the legacy path (needs hash + seq + peerID)
// without retaining a reference to the *ReplayDelta past Abandon().
type TimedOutEntry struct {
	Hash   [32]byte
	Seq    uint32
	PeerID uint64
}

// Replayer coordinates multiple concurrent *ReplayDelta acquisitions,
// keyed by target ledger hash, under a shared concurrency cap. Mirrors
// rippled's LedgerReplayer: it holds a map<uint256, LedgerDeltaAcquire>
// and hands out slots up to maxInFlight. Transport-agnostic — the
// caller (the consensus router) issues the wire request via its own
// NetworkSender. This keeps the Replayer easy to unit-test and mirrors
// how inbound.Ledger is already layered against its transport.
type Replayer struct {
	mu          sync.Mutex
	inFlight    map[[32]byte]*ReplayDelta
	logger      *slog.Logger
	clock       Clock
	maxInFlight int
}

// NewReplayer returns a Replayer configured with the given concurrency
// cap. maxInFlight <= 0 defaults to DefaultMaxInFlightReplays.
// A nil logger defaults to slog.Default(); a nil clock defaults to
// SystemClock, matching the rest of this package.
func NewReplayer(logger *slog.Logger, clock Clock, maxInFlight int) *Replayer {
	if logger == nil {
		logger = slog.Default()
	}
	if clock == nil {
		clock = SystemClock
	}
	if maxInFlight <= 0 {
		maxInFlight = DefaultMaxInFlightReplays
	}
	return &Replayer{
		inFlight:    make(map[[32]byte]*ReplayDelta),
		logger:      logger,
		clock:       clock,
		maxInFlight: maxInFlight,
	}
}

// SetClock swaps the clock driving timeout decisions for subsequent
// Acquire calls. Does NOT retroactively change the clock used by
// already-in-flight acquisitions (a *ReplayDelta captures its clock at
// creation, matching inbound.Ledger's semantics). Intended for tests
// and DI; production callers never need this.
func (r *Replayer) SetClock(c Clock) {
	if c == nil {
		c = SystemClock
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = c
}

// Acquire arms a new *ReplayDelta for the ledger identified by hash,
// anchored on parent and asked from peerID. Returns:
//
//   - ErrAcquisitionExists: hash is already in flight. Caller should
//     drop the duplicate.
//   - ErrCapacityFull: we're at the maxInFlight cap. Caller should
//     back off.
//
// On success the returned *ReplayDelta is already registered in the
// in-flight map; the caller then issues the wire request via its own
// NetworkSender and, on wire-send failure, calls Abandon(hash) to free
// the slot. HandleResponse later resolves the response back to this
// acquisition via its ledger hash.
func (r *Replayer) Acquire(hash [32]byte, peerID uint64, parent *ledger.Ledger) (*ReplayDelta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.inFlight[hash]; exists {
		return nil, ErrAcquisitionExists
	}
	if len(r.inFlight) >= r.maxInFlight {
		return nil, ErrCapacityFull
	}

	rd := NewReplayDeltaWithClock(hash, peerID, parent, r.logger, r.clock)
	r.inFlight[hash] = rd
	return rd, nil
}

// HandleResponse routes resp to the matching in-flight acquisition
// (keyed by resp.LedgerHash), runs its GotResponse verifier, and
// returns the acquisition so the caller can drive Apply + adopt.
//
// Returns ErrNoMatchingAcquisition if no in-flight entry matches
// (stale/unsolicited reply). If verification fails inside GotResponse,
// returns the underlying error AND the still-registered *ReplayDelta
// so the caller can read its Hash/Seq/PeerID before calling Abandon
// to free the slot.
//
// Does NOT remove the entry from inFlight on success: the caller
// normally runs Apply and adopts, then calls Complete to finalize.
// This gives the caller a single, explicit point at which the slot is
// freed, making misbehavior attribution cleaner (a verification
// failure leaves the slot occupied until we explicitly abandon).
func (r *Replayer) HandleResponse(resp *message.ReplayDeltaResponse) (*ReplayDelta, error) {
	if resp == nil {
		return nil, ErrNoMatchingAcquisition
	}
	hash, ok := toHash32(resp.LedgerHash)
	if !ok {
		return nil, ErrNoMatchingAcquisition
	}

	r.mu.Lock()
	rd, exists := r.inFlight[hash]
	r.mu.Unlock()

	if !exists {
		return nil, ErrNoMatchingAcquisition
	}

	if err := rd.GotResponse(resp); err != nil {
		return rd, err
	}
	return rd, nil
}

// Complete removes the acquisition for hash from in-flight. Called
// after a successful adoption OR after an explicit caller-driven
// abandonment. No-op on unknown hash so callers can call it
// unconditionally at the end of a handle-response path.
func (r *Replayer) Complete(hash [32]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, hash)
}

// Abandon is a synonym for Complete. Kept as a separate call for
// readability at the caller site — "we're giving up on this
// acquisition" vs "we finished it" tells a clearer story.
func (r *Replayer) Abandon(hash [32]byte) {
	r.Complete(hash)
}

// InFlight returns a snapshot of hashes currently being acquired.
// Sorted by the seq of their parent (parent.Sequence()+1 per
// ReplayDelta.Seq) so iteration is stable in tests. Hashes whose
// parent is nil (shouldn't happen in production) sort to the front as
// seq 0.
func (r *Replayer) InFlight() [][32]byte {
	r.mu.Lock()
	type entry struct {
		hash [32]byte
		seq  uint32
	}
	entries := make([]entry, 0, len(r.inFlight))
	for h, rd := range r.inFlight {
		entries = append(entries, entry{hash: h, seq: rd.Seq()})
	}
	r.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
	out := make([][32]byte, len(entries))
	for i, e := range entries {
		out[i] = e.hash
	}
	return out
}

// TimedOut returns summaries of every acquisition whose IsTimedOut()
// reports true. Caller typically loops through the results and
// Abandons each one, re-issuing via the legacy path with the captured
// Seq + PeerID. Returning TimedOutEntry (rather than bare hashes)
// removes a round-trip to fetch Seq/PeerID after the slot is freed.
func (r *Replayer) TimedOut() []TimedOutEntry {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []TimedOutEntry
	for h, rd := range r.inFlight {
		if rd.IsTimedOut() {
			out = append(out, TimedOutEntry{
				Hash:   h,
				Seq:    rd.Seq(),
				PeerID: rd.PeerID(),
			})
		}
	}
	// Stable order for deterministic test assertions.
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// SubTaskTimedOut returns acquisitions whose current peer has gone
// silent past subTaskRetryInterval but are still within the outer
// budget and have retry attempts remaining. The caller rotates each
// to a new peer by calling ReplayDelta.NoteSubTaskRetry(newPeerID)
// and re-issuing the wire request. Separated from TimedOut so the
// router handles rotation vs. abandon distinctly.
func (r *Replayer) SubTaskTimedOut() []*ReplayDelta {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []*ReplayDelta
	for _, rd := range r.inFlight {
		if rd.IsSubTaskTimedOut() && !rd.IsTimedOut() && !rd.RetriesExhausted() {
			out = append(out, rd)
		}
	}
	// Stable order by seq for deterministic test assertions.
	sort.Slice(out, func(i, j int) bool { return out[i].Seq() < out[j].Seq() })
	return out
}

// Count returns the current number of in-flight acquisitions.
func (r *Replayer) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inFlight)
}

// Has reports whether the given hash is currently in flight. Useful
// for callers that want to cheap-skip a duplicate Acquire without
// incurring the ErrAcquisitionExists round-trip.
func (r *Replayer) Has(hash [32]byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.inFlight[hash]
	return ok
}
