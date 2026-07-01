package adaptor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

func (r *Router) handleStatusChange(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeStatusChange, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode status_change", "error", err, "peer", msg.PeerID)
		return
	}
	sc, ok := decoded.(*message.StatusChange)
	if !ok {
		return
	}

	r.logger.Info("peer status change",
		"peer", msg.PeerID,
		"status", sc.NewStatus,
		"event", sc.NewEvent,
		"ledger_seq", sc.LedgerSeq,
		"needs_sync", r.adaptor.NeedsInitialSync(),
	)

	if sc.LedgerSeq > 0 {
		var peerHash [32]byte
		if len(sc.LedgerHash) == 32 {
			copy(peerHash[:], sc.LedgerHash)
		}
		var parentHash [32]byte
		haveParent := len(sc.LedgerHashPrevious) == 32
		if haveParent {
			copy(parentHash[:], sc.LedgerHashPrevious)
		}

		// Record the (seq, hash, parentHash) link so the forward-delta catch-up
		// knows the hash of closed+1 and its parent linkage. status_change is the
		// only gossip source that carries the parent hash (validations don't).
		if len(sc.LedgerHash) == 32 {
			r.recordSeqHash(sc.LedgerSeq, peerHash, parentHash, haveParent)
		}

		r.peersMu.Lock()
		r.peerStates[msg.PeerID] = &peerLedgerState{
			LedgerSeq:  sc.LedgerSeq,
			LedgerHash: peerHash,
		}
		r.peersMu.Unlock()

		// Surface the peer's reported LCL to the adaptor so the
		// engine's getNetworkLedger can consider it as a vote even
		// when no proposal has (yet) arrived from this peer.
		r.adaptor.UpdatePeerLCL(uint64(msg.PeerID), consensus.LedgerID(peerHash))

		// During initial sync, fetch the full ledger from the peer.
		// Don't adopt with synthetic headers — wait for real state data.
		// Routed through the bounded funnel so a fresh node also drives a
		// single target toward the tip instead of fanning out per status.
		if r.adaptor.NeedsInitialSync() && sc.LedgerSeq > 1 {
			r.ensureCatchupAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
			return
		}

		// When in Full mode and significantly behind (gap > 2), catch up toward
		// the network tip but stay in Full mode so we keep participating in
		// consensus.
		if r.adaptor.GetOperatingMode() == consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := r.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if sc.LedgerSeq > ourSeq+2 {
					r.logger.Warn("behind network while in Full mode, catching up",
						"our_seq", ourSeq,
						"peer_seq", sc.LedgerSeq,
						"gap", sc.LedgerSeq-ourSeq,
					)
					r.ensureCatchupAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		// While not in Full mode, keep catching up until we're within 1 ledger
		// of the network.
		if r.adaptor.GetOperatingMode() != consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := r.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if sc.LedgerSeq > ourSeq+1 {
					r.ensureCatchupAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		// Hash-divergence catch-up. A late-join node (or a node whose
		// consensus ran in isolation while disconnected) can end up at
		// the same seq as its peers but with a different ledger hash.
		// The seq-based branches above don't fire because ourSeq ==
		// peerSeq; we need to detect that our LCL hash differs from the
		// peer's and acquire theirs. Only fire if we're NOT already
		// acquiring that hash (startLedgerAcquisition dedupes internally
		// via the replayer / acquisition-registry guards, but checking
		// here saves a lookup in the hot path).
		svc := r.adaptor.LedgerService()
		if svc != nil && sc.LedgerSeq > 1 && len(sc.LedgerHash) == 32 {
			closed := svc.GetClosedLedger()
			if closed != nil {
				ourSeq := closed.Sequence()
				ourHash := closed.Hash()
				if ourSeq == sc.LedgerSeq && ourHash != peerHash {
					// Honour the single-acquisition cap like every other
					// catch-up arming site.
					if r.catchupInFlight() >= maxConcurrentCatchup {
						return
					}
					r.logger.Warn("ledger hash divergence at same seq, acquiring peer's ledger",
						"seq", sc.LedgerSeq,
						"our_hash", fmt.Sprintf("%x", ourHash[:8]),
						"peer_hash", fmt.Sprintf("%x", peerHash[:8]),
						"peer", msg.PeerID,
					)
					r.startLedgerAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					return
				}
			}
		}

		r.checkBehind(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
	}
}

// maxConcurrentCatchup bounds how many consensus-reason ledger acquisitions the
// gossip-driven catch-up may run at once. rippled's LedgerMaster::doAdvance
// drives a SINGLE needed target (the trusted-validated / preferred ledger)
// rather than arming one InboundLedger per gossiped status/validation; we mirror
// that with a hard cap. Kept as a named const so it can rise to 2 without
// touching the arming logic — ensureCatchupAcquisition and the retarget-on-
// complete path both compare against it and retarget (rather than add) once hit.
const maxConcurrentCatchup = 1

// maxForwardDeltaGap bounds how far behind the network tip the router walks
// forward one ledger at a time (replay-delta against the locally-held parent)
// before it prefers a single full-state jump-adopt. Within this gap a
// same-branch node closes the distance cheaply by applying each next tx set to
// its closed ledger — O(txs) per hop, which outruns the network's close cadence
// — so a serial forward walk converges. Beyond it a cold or far-behind start
// jumps straight to the validated tip rather than grinding a long serial walk.
// This mirrors rippled reserving InboundLedger full-state acquisition for the
// cold/forked case while LedgerMaster::doAdvance advances the forward chain in
// steady state.
const maxForwardDeltaGap = 128

// seqHashRetain bounds the seqHash table to a trailing window of ledger
// sequences so a long-running node never grows it unbounded. A drift of a few
// ledgers (the soak case) sits comfortably inside it.
const seqHashRetain = 256

// recordSeqHash records the network's hash — and, when a status_change revealed
// it, the parent hash — for a ledger sequence, learned from a trusted validation
// or peer gossip. It backs the forward-delta catch-up decision: the hash to
// acquire for closed+1 and the parent linkage that proves it descends from our
// closed ledger. The table is pruned to the most recent seqHashRetain sequences
// on insert. A later, fuller entry (parent hash from a status_change) upgrades an
// earlier hash-only entry rather than clobbering it.
func (r *Router) recordSeqHash(seq uint32, hash, parentHash [32]byte, haveParent bool) {
	if seq == 0 || hash == ([32]byte{}) {
		return
	}
	r.seqHashMu.Lock()
	defer r.seqHashMu.Unlock()

	e := r.seqHash[seq]
	e.hash = hash
	if haveParent {
		e.parentHash = parentHash
		e.haveParent = true
	}
	r.seqHash[seq] = e

	// The parent linkage also names seq-1's own hash. Seed it (without a
	// parent) when we don't already hold a fuller entry, so a same-branch check
	// against our closed seq can succeed even if no direct validation for it
	// arrived.
	if haveParent && seq > 1 {
		if pe, ok := r.seqHash[seq-1]; !ok || pe.hash == ([32]byte{}) {
			pe.hash = parentHash
			r.seqHash[seq-1] = pe
		}
	}

	if seq > r.seqHashMax {
		r.seqHashMax = seq
	}
	if len(r.seqHash) > seqHashRetain {
		r.pruneSeqHashLocked()
	}
}

// pruneSeqHashLocked drops entries older than the trailing seqHashRetain window.
// Caller holds seqHashMu.
func (r *Router) pruneSeqHashLocked() {
	if r.seqHashMax <= seqHashRetain {
		return
	}
	floor := r.seqHashMax - seqHashRetain
	for s := range r.seqHash {
		if s < floor {
			delete(r.seqHash, s)
		}
	}
}

// lookupSeqHash returns the recorded network view for a ledger sequence.
func (r *Router) lookupSeqHash(seq uint32) (ledgerHashEntry, bool) {
	r.seqHashMu.Lock()
	defer r.seqHashMu.Unlock()
	e, ok := r.seqHash[seq]
	return e, ok
}

// catchupInFlight reports how many consensus-reason ledger acquisitions are
// active, across both the legacy header+state fetchTracker and the replay-delta
// replayer. Generic (RPC-driven) acquisitions carry ReasonGeneric and are
// excluded so an arbitrary historical fetch never consumes a catch-up slot.
// This is the gate the catch-up funnel checks against maxConcurrentCatchup.
func (r *Router) catchupInFlight() int {
	return r.fetchTracker.CountReason(inbound.ReasonConsensus) + r.replayer.Count()
}

// recordCatchupTarget updates the single consensus catch-up target to (seq,
// hash, peerID) when seq is strictly higher than the target currently held.
// Older or equal tips are ignored so the router always drives toward the
// highest trusted tip it has seen — the go-xrpl analogue of rippled tracking a
// single preferred/needed ledger.
func (r *Router) recordCatchupTarget(seq uint32, hash [32]byte, peerID uint64) {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	if seq > r.catchup.seq {
		r.catchup = catchupTarget{seq: seq, hash: hash, peerID: peerID}
	}
}

// bestCatchupTarget returns the current highest recorded catch-up target.
func (r *Router) bestCatchupTarget() (seq uint32, hash [32]byte, peerID uint64) {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	return r.catchup.seq, r.catchup.hash, r.catchup.peerID
}

// armCatchupTowardTarget arms exactly one catch-up acquisition, but only while
// fewer than maxConcurrentCatchup consensus acquisitions are in flight and the
// recorded tip is still ahead of our closed ledger. It chooses between two
// strategies, mirroring rippled's LedgerMaster::doAdvance (forward chain walk)
// vs InboundLedger full-state acquisition (cold/forked start):
//
//   - Forward-delta step: when closed+1 is a known clean child of our closed
//     ledger and the tip is within maxForwardDeltaGap, acquire closed+1. Its
//     parent (our closed) is local, so startLedgerAcquisition selects the
//     bandwidth-cheap replay-delta path; the completion re-arms toward the next
//     closed+1, a serial forward walk that converges.
//   - Jump-adopt: otherwise (cold/far/forked) acquire the far validated tip
//     directly; its parent chain is absent, so the legacy full-state path plus
//     completeInboundLedger's gap>1 branch jumps the working ledger forward.
//
// Shared by ensureCatchupAcquisition (after recording a fresh tip) and the
// retarget-on-complete paths. startLedgerAcquisition's own dedup makes a
// redundant call a no-op.
func (r *Router) armCatchupTowardTarget() {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	if r.catchupInFlight() >= maxConcurrentCatchup {
		return
	}
	tSeq, tHash, tPeer := r.bestCatchupTarget()
	if tSeq == 0 {
		return
	}
	closed := svc.GetClosedLedgerIndex()
	if tSeq <= closed {
		return
	}

	if seq, hash, peer, ok := r.forwardDeltaStep(svc, closed, tSeq); ok {
		r.startLedgerAcquisition(seq, hash, peer)
		return
	}
	r.startLedgerAcquisition(tSeq, tHash, tPeer)
}

// forwardDeltaStep decides whether the next catch-up hop should be a forward
// one-ledger step against our locally-held closed ledger, returning the
// (seq, hash, peer) to acquire when so. It requires all of:
//
//   - a modest gap to the tip (tipSeq-closed <= maxForwardDeltaGap);
//   - a known network hash for closed+1 (the target to acquire); and
//   - proof that closed+1 descends from our closed ledger (same branch) —
//     closed+1's recorded parentHash equals our closed hash, or, when only a
//     validation (hash, no parent) populated closed+1, the recorded hash for
//     our own closed seq equals our closed hash. Those are equivalent parent
//     linkages: the parent of closed+1 IS the ledger at our closed seq.
//
// A missing target, divergent linkage (fork), gap beyond the bound, or absent
// linkage info all return ok=false, deferring to the jump-adopt fallback. This
// keeps steady-state same-branch drift on the cheap forward path while cold and
// forked starts resync via full-state acquisition.
func (r *Router) forwardDeltaStep(svc *service.Service, closed, tipSeq uint32) (seq uint32, hash [32]byte, peer uint64, ok bool) {
	if tipSeq-closed > maxForwardDeltaGap {
		return 0, [32]byte{}, 0, false
	}
	next := closed + 1
	entry, known := r.lookupSeqHash(next)
	if !known || entry.hash == ([32]byte{}) {
		return 0, [32]byte{}, 0, false
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return 0, [32]byte{}, 0, false
	}
	closedHash := closedLedger.Hash()

	sameBranch := false
	if entry.haveParent {
		sameBranch = entry.parentHash == closedHash
	} else if cEntry, okC := r.lookupSeqHash(closed); okC && cEntry.hash != ([32]byte{}) {
		sameBranch = cEntry.hash == closedHash
	}
	if !sameBranch {
		return 0, [32]byte{}, 0, false
	}
	return next, entry.hash, r.forwardStepPeer(next), true
}

// forwardStepPeer picks a peer to serve a forward-delta step: one reporting at
// or beyond the target seq, else the peer that advertised the current best tip.
// startLedgerAcquisition still falls back to the legacy header+state path (also
// a forward, gap-1 held adoption against our present parent) if that peer can't
// replay.
func (r *Router) forwardStepPeer(seq uint32) uint64 {
	if p, ok := r.selectAcquisitionPeer(seq); ok {
		return p
	}
	_, _, peer := r.bestCatchupTarget()
	return peer
}

// ensureCatchupAcquisition is the single funnel for gossip-driven consensus
// catch-up. It records (seq,hash,peerID) as the best target when strictly ahead
// of our closed ledger, then arms one acquisition toward the best target — but
// only while under the maxConcurrentCatchup cap. Once at the cap it just
// retargets (records the newer tip) and returns, so a stream of ever-higher
// gossiped tips no longer fans out into one acquisition per event. On
// completion, completeInboundLedger re-arms toward the latest recorded target,
// forming the bounded retarget loop that replaces the fan-out.
//
// Callers do their own eligibility gating first (e.g. the validated-tip gate in
// maybeAcquireFromValidation, the operating-mode gap checks in
// handleStatusChange). This helper only bounds CONCURRENCY, never eligibility.
func (r *Router) ensureCatchupAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	if seq == 0 || seq <= svc.GetClosedLedgerIndex() {
		return
	}
	r.recordCatchupTarget(seq, hash, peerID)
	r.armCatchupTowardTarget()
}

// startLedgerAcquisition picks the best available ledger-acquisition
// strategy for the given target. When we have the parent ledger locally
// and the peer advertises ledger-replay, the bandwidth-efficient
// replay-delta protocol is preferred (one request returns header + every
// tx blob); otherwise we fall back to the legacy mtGET_LEDGER
// header+state walk.
//
// This is currently the only driver of startReplayDeltaAcquisition: it
// handles a single target ledger per call. The Replayer coordinator
// supports concurrent acquisitions across many hashes, but the policy
// layer that walks a range (e.g., backward from a peer's tip via
// ParentHash) is a follow-up item.
func (r *Router) startLedgerAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	// Unified dedup across BOTH acquisition paths. A prior fix only
	// checked r.replayer.Has(hash); that still allowed the cross-path
	// race where two status changes at the same seq with different
	// hashes armed both a replay-delta AND a legacy acquisition
	// simultaneously, with adoption order then deciding which won. The
	// single-point-of-truth check is a deliberate narrowing: a tighter
	// guarantee that the same hash can't acquire through both paths.
	if r.isAcquiring(hash) {
		return
	}

	parent := r.adaptor.GetParentLedgerForReplay(seq)
	if parent != nil && r.adaptor.PeerSupportsReplay(peerID) {
		if err := r.startReplayDeltaAcquisition(seq, hash, peerID, parent); err == nil {
			return
		}
		// Fall through to the legacy path on issue failure.
	}
	r.startLedgerAcquisitionLegacy(seq, hash, peerID)
}

// isAcquiring reports whether an acquisition — replay-delta or legacy
// — is currently in flight for the given ledger hash. Used as the
// single dedup entry point so a race between a replay-delta and a
// legacy acquisition for the same hash is impossible.
func (r *Router) isAcquiring(hash [32]byte) bool {
	if r.replayer.Has(hash) {
		return true
	}
	if r.fetchTracker.Find(hash) != nil {
		return true
	}
	return false
}

// startReplayDeltaAcquisition registers a new acquisition with the
// Replayer coordinator and issues the corresponding
// mtREPLAY_DELTA_REQUEST.
//
// Returns ErrAcquisitionExists if a request for the same hash is
// already in flight (caller should drop the duplicate), ErrCapacityFull
// if the coordinator is at cap (caller falls back to legacy), or the
// wire-send error if the request itself failed (coordinator slot is
// freed before returning so the caller can retry).
func (r *Router) startReplayDeltaAcquisition(seq uint32, hash [32]byte, peerID uint64, parent *ledger.Ledger) error {
	rd, err := r.replayer.Acquire(hash, peerID, parent)
	if err != nil {
		return err
	}
	_ = rd // retained in replayer; HandleResponse retrieves it on reply.
	r.logger.Info("starting replay delta acquisition",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	if err := r.adaptor.RequestReplayDelta(peerID, hash); err != nil {
		r.logger.Warn("failed to request replay delta from peer", "error", err)
		r.replayer.Abandon(hash)
		return err
	}
	return nil
}

// startLedgerAcquisitionLegacy requests the full ledger (header + state
// tree) from a peer using the legacy mtGET_LEDGER protocol. This is the
// fallback path when the parent isn't locally available or replay-delta
// verification fails.
//
// Callers that enter via startLedgerAcquisition already consult
// isAcquiring across both paths — but we still re-check here because
// maintenanceTick and the replay-delta fallback paths can enter
// directly, bypassing the unified entry point.
func (r *Router) startLedgerAcquisitionLegacy(seq uint32, hash [32]byte, peerID uint64) {
	// Safety net: if a replay-delta for the same hash is still
	// registered, don't start a legacy on top of it — one path is
	// always enough.
	if r.replayer.Has(hash) {
		return
	}

	_, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.New(hash, seq, peerID, r.logger, r.acquisitionOpts()...)
	})
	if !created {
		// Already acquiring this hash (consensus or a prior arm).
		return
	}

	r.logger.Info("starting ledger acquisition (legacy)",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)

	if err := r.adaptor.RequestLedgerBaseFromPeer(peerID, hash, seq); err != nil {
		r.logger.Warn("failed to request ledger base from peer", "error", err)
		r.fetchTracker.Remove(hash, false)
	}
}

// startHistoryBackfill records the next skipped ledger to backfill after a
// jump-adopt, bounded below by floor (the pre-jump closed seq — history at
// and below it is already contiguous). The walk is serial and backward: each
// ingested ledger's header names its parent, which becomes the next target;
// the maintenance tick arms the fetches.
func (r *Router) startHistoryBackfill(seq uint32, hash [32]byte, peerID uint64, floor uint32) {
	if seq == 0 || seq <= floor || hash == ([32]byte{}) {
		return
	}
	r.historyMu.Lock()
	r.history = catchupTarget{seq: seq, hash: hash, peerID: peerID}
	r.historyFloor = floor
	r.historyMu.Unlock()
}

// currentHistoryFloor returns the active backfill walk's lower bound.
func (r *Router) currentHistoryFloor() uint32 {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	return r.historyFloor
}

// armHistoryBackfill drives one backward history-backfill acquisition from
// the maintenance tick (rippled fetchForHistory from doAdvance). Locally-held
// ledgers advance the walk without a fetch; the walk ends at the recorded
// gap floor, the online-delete floor, or genesis. At most one ReasonHistory
// acquisition is in flight, and it never occupies the consensus catch-up
// slot.
func (r *Router) armHistoryBackfill() {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	r.historyMu.Lock()
	target := r.history
	floor := r.historyFloor
	r.historyMu.Unlock()
	if target.seq == 0 {
		return
	}
	for {
		if target.seq == 0 || target.seq <= floor || target.hash == ([32]byte{}) || r.belowFloor(target.seq) {
			r.historyMu.Lock()
			r.history = catchupTarget{}
			r.historyFloor = 0
			r.historyMu.Unlock()
			return
		}
		held, err := svc.GetLedgerByHash(target.hash)
		if err != nil || held == nil {
			break
		}
		target = catchupTarget{seq: target.seq - 1, hash: held.ParentHash(), peerID: target.peerID}
		r.historyMu.Lock()
		r.history = target
		r.historyMu.Unlock()
	}
	if r.fetchTracker.CountReason(inbound.ReasonHistory) >= 1 || r.isAcquiring(target.hash) {
		return
	}
	peer := target.peerID
	if p, ok := r.selectAcquisitionPeer(target.seq); ok {
		peer = p
	}
	if peer == 0 {
		return
	}
	r.startHistoryAcquisition(target.seq, target.hash, peer)
}

// startHistoryAcquisition requests a skipped historical ledger (header +
// state) over the legacy mtGET_LEDGER protocol as a ReasonHistory
// acquisition. Replay-delta doesn't apply: the walk is backward, so the
// parent is never locally available.
func (r *Router) startHistoryAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	if r.replayer.Has(hash) {
		return
	}
	_, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.NewHistory(hash, seq, peerID, r.logger, r.acquisitionOpts()...)
	})
	if !created {
		return
	}
	r.logger.Info("starting history backfill acquisition",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	if err := r.adaptor.RequestLedgerBaseFromPeer(peerID, hash, seq); err != nil {
		r.logger.Warn("failed to request ledger base from peer", "error", err)
		r.fetchTracker.Remove(hash, false)
	}
}

// FetchInfo returns the inbound-ledger acquisition snapshot served by the
// fetch_info RPC. Safe to call from any goroutine.
func (r *Router) FetchInfo() map[string]any {
	return r.fetchTracker.Info()
}

// ClearFetchInfo resets the acquisition counters and recent-failure history,
// backing fetch_info's `clear` param.
func (r *Router) ClearFetchInfo() {
	r.fetchTracker.Clear()
}

// RequestLedger triggers (or joins) a generic acquisition of a ledger from
// peers, backing the ledger_request RPC. When hash is zero the target is
// resolved from the validated ledger's skip list, and a ReasonGeneric
// acquisition is started (or the in-flight one reused). started=true while
// an acquisition is in flight; (nil,false,false) when the target can't be
// resolved or no peer is available.
//
// reference distinguishes the two acquiring shapes: false when the
// snapshot is the target ledger itself; true when it is a 256-aligned
// reference ledger being fetched only to learn the target's hash.
//
// Safe to call from an RPC goroutine: the registry and each acquisition guard
// their own state.
func (r *Router) RequestLedger(hash [32]byte, seq uint32) (acquiring map[string]any, started, reference bool) {
	// Don't acquire history online-delete has reclaimed: rippled's
	// LedgerMaster::shouldAcquire refuses to fetch a missing ledger below
	// minimumOnline. Re-fetching it would only feed the rotator another
	// delete. Forward catch-up / validation acquisitions are above the
	// validated tip (≥ floor) so they never hit this gate.
	if seq != 0 && r.belowFloor(seq) {
		r.logger.Debug("ledger_request declined: below online-delete floor",
			"seq", seq, "floor", r.floor.MinimumOnline())
		return nil, false, false
	}
	if hash == ([32]byte{}) {
		if seq == 0 {
			return nil, false, false
		}
		svc := r.adaptor.LedgerService()
		if svc == nil {
			return nil, false, false
		}
		vl := svc.GetValidatedLedger()
		if vl == nil {
			return nil, false, false
		}
		h, ok, err := vl.HashOfSeq(seq)
		if err != nil {
			return nil, false, false
		}
		if !ok {
			// seq is past the rolling window and not 256-aligned, so its hash
			// isn't directly in the validated ledger. Resolve it through a
			// 256-aligned reference ledger whose hash IS enshrined in the skip
			// list.
			refIndex := getCandidateLedger(seq)
			refHash, refOK, err := vl.HashOfSeq(refIndex)
			if err != nil || !refOK {
				return nil, false, false
			}
			refLedger, err := svc.GetLedgerByHash(refHash)
			if err != nil || refLedger == nil {
				// We lack the reference ledger needed to learn the target's
				// hash — acquire it and report it as the in-flight reference.
				if snap, ok := r.startGenericAcquisition(refHash, refIndex); ok {
					return snap, true, true
				}
				return nil, false, false
			}
			h, ok, err = refLedger.HashOfSeq(seq)
			if err != nil || !ok {
				return nil, false, false
			}
		}
		hash = h
	}

	if snap, ok := r.startGenericAcquisition(hash, seq); ok {
		return snap, true, false
	}
	return nil, false, false
}

// startGenericAcquisition begins (or joins) a ReasonGeneric acquisition for
// hash, issuing a base fetch from a selected peer only when it creates a fresh
// one. Returns the acquisition snapshot, or ok=false when no peer is available
// or the initial fetch could not be issued. The fetchTracker's GetOrCreate is
// atomic, so a concurrent consensus catch-up arming the same hash is joined
// rather than duplicated.
func (r *Router) startGenericAcquisition(hash [32]byte, seq uint32) (map[string]any, bool) {
	if il := r.fetchTracker.Find(hash); il != nil {
		return inbound.AcquisitionJSON(il.Snapshot()), true
	}

	peerID, ok := r.selectAcquisitionPeer(seq)
	if !ok {
		return nil, false
	}

	il, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.NewGeneric(hash, seq, peerID, r.logger, r.acquisitionOpts()...)
	})
	if created {
		r.logger.Info("starting ledger acquisition (generic, ledger_request)",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
		)
		if err := r.adaptor.RequestLedgerBaseFromPeer(peerID, hash, seq); err != nil {
			r.logger.Warn("ledger_request: failed to request ledger base", "error", err)
			r.fetchTracker.Remove(hash, false)
			return nil, false
		}
	}
	return inbound.AcquisitionJSON(il.Snapshot()), true
}

// getCandidateLedger rounds seq up to the next multiple of 256 — the nearest
// ancestor whose hash is enshrined in the historical skip list and is therefore
// easy to resolve, then close enough (within 256) to hold seq's hash in its own
// rolling list.
func getCandidateLedger(seq uint32) uint32 {
	return (seq + 255) &^ 255
}

// selectAcquisitionPeer picks a connected peer to fetch a ledger from,
// preferring one whose reported ledger is at or beyond the target sequence
// (and therefore likely to hold it). When seq is unknown (0) or no peer is far
// enough along, it falls back to any connected peer. Returns (0,false) when no
// peer has reported a ledger state.
func (r *Router) selectAcquisitionPeer(seq uint32) (uint64, bool) {
	r.peersMu.RLock()
	defer r.peersMu.RUnlock()

	var fallback uint64
	var haveFallback bool
	for pid, st := range r.peerStates {
		if !haveFallback {
			fallback, haveFallback = uint64(pid), true
		}
		if seq == 0 || st.LedgerSeq >= seq {
			return uint64(pid), true
		}
	}
	return fallback, haveFallback
}

// handleReplayDeltaResponse verifies an inbound mtREPLAY_DELTA_RESPONSE
// against its matching in-flight acquisition (routed by ledger hash)
// and adopts the resulting ledger. On verification or apply failure the
// acquisition is abandoned and the legacy path is started for the same
// target. Unsolicited/stale responses (no matching acquisition) are
// silently dropped — a normal race when a peer batch-forwards replies
// after we've already moved on.
func (r *Router) handleReplayDeltaResponse(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeReplayDeltaResponse, msg.Payload)
	if err != nil {
		r.logger.Debug("failed to decode replay delta response", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "replay-delta-resp-decode")
		return
	}
	resp, ok := decoded.(*message.ReplayDeltaResponse)
	if !ok || resp == nil {
		return
	}

	rd, err := r.replayer.HandleResponse(resp)
	if errors.Is(err, inbound.ErrNoMatchingAcquisition) {
		// Stale or unsolicited — drop silently without charging the
		// peer. A misbehaving peer sending genuinely bogus data would
		// fail its ACTIVE acquisition's verifier (branch below), which
		// IS attributed via IncPeerBadData.
		r.logger.Debug("replay delta response with no matching acquisition",
			"peer", msg.PeerID)
		return
	}
	if err != nil {
		// Verification failed. rd is still registered in the Replayer so
		// we can read its provenance before abandoning the slot.
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := rd.PeerID()
		r.replayer.Abandon(hash)
		r.logger.Warn("replay delta verification failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.adaptor.IncPeerBadData(peerID, "replay-delta-verify")
		r.startLedgerAcquisitionLegacy(seq, hash, peerID)
		return
	}

	// GotResponse verified the header hash and the tx-map root. Apply
	// re-derives the post-state by replaying every tx through the
	// engine against a mutable copy of the parent's state, then
	// verifies the resulting AccountHash matches the target header —
	// the only proof we have that our engine produced the right state.
	// Without this step the adopted ledger would carry the parent's
	// stale state map, breaking consensus on the next round.
	parent := rd.Parent()
	engineCfg := r.adaptor.EngineConfigForReplay(parent)
	derived, err := rd.Apply(engineCfg)
	if err != nil {
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := rd.PeerID()
		r.replayer.Abandon(hash)
		// DO NOT charge the peer here. GotResponse already verified the
		// peer's header hash and tx-map root; a subsequent Apply failure
		// means OUR engine produced a divergent AccountHash — an engine
		// bug, not peer misbehavior. Charging here would wrongly evict
		// honest peers for our bugs.
		r.logger.Error("ENGINE DIVERGENCE: replay delta apply failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.startLedgerAcquisitionLegacy(seq, hash, peerID)
		return
	}
	peerID := rd.PeerID()
	r.replayer.Complete(rd.Hash())
	if err := r.adoptVerifiedLedger(derived, peerID); err != nil {
		r.logger.Warn("failed to adopt replay-delta ledger", "error", err)
	}
}

// adoptVerifiedLedger commits a ledger reconstructed from a verified replay
// delta, installing the peer-provided tx-blob tree alongside the state map.
// Routes through SubmitHeldAdoption so out-of-order arrivals are stashed by
// awaited parent seq; on stash we arm a backward-chain acquisition for the
// parent.
func (r *Router) adoptVerifiedLedger(l *ledger.Ledger, peerID uint64) error {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return errors.New("no ledger service")
	}
	hdr := l.Header()
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot state map: %w", err)
	}
	// Pass the verified tx map through so the adopted ledger carries
	// real transactions — without this, tx/tx_history/account_tx RPCs
	// can't answer for replay-delta-adopted ledgers and we can't
	// re-serve the replay-delta to other peers.
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot tx map: %w", err)
	}
	// context.TODO: adoptVerifiedLedger is reached from a peer-message
	// handler stack that does not currently carry a context. Threading
	// one through the message-dispatch chain is tracked separately from
	// this issue (#185).
	res, err := svc.SubmitHeldAdoption(context.TODO(), &hdr, stateMap, txMap)
	if err != nil {
		return fmt.Errorf("adopt with state: %w", err)
	}
	if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		r.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	r.logger.Info("adopted ledger via replay delta",
		"seq", hdr.LedgerIndex,
		"hash", fmt.Sprintf("%x", hdr.Hash[:8]),
	)
	// Notify the consensus engine so it can flip out of
	// ModeWrongLedger via Engine.OnLedger. Without this, the engine
	// remains stuck in wrongLedger indefinitely after a successful
	// inbound acquisition.
	if r.engine != nil {
		if err := r.engine.OnLedger(consensus.LedgerID(hdr.Hash), nil); err != nil {
			r.logger.Debug("engine rejected adopted ledger", "error", err, "seq", hdr.LedgerIndex)
		}
	}
	if res.Stashed {
		r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
	} else {
		// The forward-delta step advanced our closed ledger. Continue the
		// serial forward walk: re-arm toward the new closed+1 (or jump-adopt if
		// still far). Skipped on a stash, where closed didn't move and the
		// parent chase above drives progress instead.
		r.armCatchupTowardTarget()
	}
	return nil
}

// maybeAcquireFromValidation arms inbound acquisition for a ledger attested
// by a single TRUSTED validation, before the hash reaches quorum. It is the
// non-quorum counterpart to armValidationStashAcquisition, acquiring the
// ledger on EVERY trusted current validation when we don't already have it
// — quorum is not required. With only the quorum-gated path, a node below
// quorum (3 of 4 trusted validators on the network tip) never fetched that
// tip and stalled in the wrongLedger chase loop.
//
// This only ACQUIRES. Advancing validatedLedger still flows through the
// quorum gate (onFullyValidated → SetValidatedLedger), so a sub-quorum
// fetch cannot move our validated tip and carries no state-divergence
// risk; it just makes the ledger locally available so the node can rejoin
// consensus on the network's chain instead of holding no position.
func (r *Router) maybeAcquireFromValidation(v *consensus.Validation, originPeer uint64) {
	if v == nil || v.LedgerSeq == 0 {
		return
	}
	// Only trusted validators steer chain selection.
	if !r.adaptor.IsTrusted(v.NodeID) {
		return
	}
	// Record the network's hash for this seq regardless of the acquire gate
	// below: the forward-delta decision needs the hash for closed+1 AND — to
	// confirm the same branch — for our own closed seq, which can sit at or
	// below the validated tip. Validations carry no parent hash.
	r.recordSeqHash(v.LedgerSeq, [32]byte(v.LedgerID), [32]byte{}, false)

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	// Gate on the VALIDATED tip, never the closed/built tip — same rationale
	// as armValidationStashAcquisition: a node that ran its closed chain
	// ahead would otherwise skip the acquire and stay stuck on the wrong
	// chain.
	if v.LedgerSeq <= svc.GetValidatedLedgerIndex() {
		return
	}
	hash := [32]byte(v.LedgerID)
	// Already have it (built or adopted) — nothing to fetch.
	if l, err := svc.GetLedgerByHash(hash); err == nil && l != nil {
		return
	}
	// A trusted tip AT OR BELOW our closed tip on a chain we don't hold is a
	// consensus-island signature: we ran ahead on our own branch while the
	// majority validated another. The forward funnel below never fetches
	// behind closed, so acquire it directly (rippled acquires the ledger of
	// every unresolvable trusted validation, RCLValidationsAdaptor::acquire)
	// — without it the validation trie can never place the majority branch.
	if v.LedgerSeq <= svc.GetClosedLedgerIndex() {
		if r.catchupInFlight() >= maxConcurrentCatchup {
			return
		}
		r.startLedgerAcquisition(v.LedgerSeq, hash, originPeer)
		return
	}
	// Record this trusted tip and arm one bounded acquisition toward the best
	// target. Many validations naming the same (or ever-higher) tip no longer
	// fan out into an acquisition each: the funnel retargets under the cap and
	// the completion path re-arms toward the latest tip.
	r.ensureCatchupAcquisition(v.LedgerSeq, hash, originPeer)
}

// armValidationStashAcquisition arms inbound acquisition for a (seq, hash)
// that SetValidatedLedger stashed. Prefers a peer advertising LCL >= seq,
// falls back to any tracked peer.
func (r *Router) armValidationStashAcquisition(seq uint32, hash [32]byte) {
	defer func() {
		if rv := recover(); rv != nil {
			r.logger.Error("armValidationStashAcquisition panic recovered",
				"seq", seq,
				"hash", fmt.Sprintf("%x", hash[:8]),
				"panic", rv,
			)
		}
	}()
	if seq == 0 {
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	// Skip only when seq is at or below the last *validated* ledger.
	// Gating on the closed-ledger index instead silently swallowed
	// recovery for a node that had run ahead on a private chain: when the
	// validation tracker observed quorum on canonical seq=N with a
	// different hash than our local seq=N, the acquire was skipped because
	// closedSeq >> validatedSeq, leaving us stuck on the private chain
	// forever.
	if seq <= svc.GetValidatedLedgerIndex() {
		return
	}

	// Walk peers in ID order so the chosen peer (and the emitted log)
	// is reproducible across runs. Any peer with the hash can serve it.
	r.peersMu.RLock()
	peerIDs := make([]peermanagement.PeerID, 0, len(r.peerStates))
	for pid := range r.peerStates {
		peerIDs = append(peerIDs, pid)
	}
	slices.Sort(peerIDs)
	var (
		preferredPeerID uint64
		fallbackPeerID  uint64
	)
	for _, pid := range peerIDs {
		st := r.peerStates[pid]
		if fallbackPeerID == 0 {
			fallbackPeerID = uint64(pid)
		}
		if st != nil && st.LedgerSeq >= seq {
			preferredPeerID = uint64(pid)
			break
		}
	}
	r.peersMu.RUnlock()
	if preferredPeerID == 0 {
		preferredPeerID = fallbackPeerID
	}
	if preferredPeerID == 0 {
		return
	}

	// Honour the single-acquisition cap; the maintenance-tick re-arm drives
	// the recorded target once the slot frees.
	if r.catchupInFlight() >= maxConcurrentCatchup {
		r.recordCatchupTarget(seq, hash, preferredPeerID)
		return
	}
	r.logger.Info("arming acquisition for stashed validation",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"preferred_peer", preferredPeerID,
	)
	r.startLedgerAcquisition(seq, hash, preferredPeerID)
}

// armParentAcquisition fires a backward-chain acquisition for the parent of
// a stashed held-adoption candidate. Skips at-or-below closed (already
// adopted or fork-dropped).
func (r *Router) armParentAcquisition(svc *service.Service, parentSeq uint32, parentHash [32]byte, preferredPeerID uint64) {
	if parentSeq == 0 {
		return
	}
	if parentSeq <= svc.GetClosedLedgerIndex() {
		return
	}
	// Honour the single-acquisition cap; a skipped parent chase is superseded
	// by the maintenance-tick re-arm toward the recorded tip (which jump-adopts
	// past the stash at gap > 1).
	if r.catchupInFlight() >= maxConcurrentCatchup {
		return
	}
	r.logger.Info("arming backward-chain acquisition for stashed held-adoption parent",
		"parent_seq", parentSeq,
		"parent_hash", fmt.Sprintf("%x", parentHash[:8]),
		"preferred_peer", preferredPeerID,
	)
	r.startLedgerAcquisition(parentSeq, parentHash, preferredPeerID)
}

// checkBehind decides what to do based on how far behind a peer
// reports. Two outcomes:
//
//   - peerSeq <= ourSeq+1: we're caught up. If still in Tracking and
//     our LCL hash matches peers' majority, transition to Full.
//     Otherwise stay in Tracking — the hash-mismatch branch in
//     handleStatusChange will have already fired the right acquisition.
//   - peerSeq > ourSeq+1: we're behind by more than one ledger. Arm a
//     single acquisition for the peer's tip. Subsequent status changes
//     from peers will chain more acquisitions forward as we adopt each
//     ledger and ourSeq advances.
//
// Only one acquisition fires per call. A faster "range walk" that
// issues concurrent requests for every seq between ourLCL+1 and
// peerSeq would need the intermediate ledger hashes, which we don't
// know until each acquired header reveals its ParentHash; we rely on
// forward status gossip instead. Replayer already supports concurrent
// in-flight acquisitions, so switching to backward-walk later is a
// localized change in this function.
func (r *Router) checkBehind(peerSeq uint32, peerHash [32]byte, peerID uint64) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	ourSeq := svc.GetClosedLedgerIndex()

	// If we're caught up (gap ≤ 1) and not yet Full, transition to Full
	// only if our LCL hash matches what the majority of peers report.
	if peerSeq <= ourSeq+1 {
		if r.adaptor.GetOperatingMode() == consensus.OpModeTracking {
			if r.ourLCLMatchesPeers() {
				r.logger.Info("caught up with network, transitioning to Full",
					"our_seq", ourSeq,
					"peer_seq", peerSeq,
				)
				r.adaptor.SetOperatingMode(consensus.OpModeFull)
			} else {
				r.logger.Info("caught up but LCL hash differs, staying in Tracking",
					"our_seq", ourSeq,
					"peer_seq", peerSeq,
				)
			}
		}
		return
	}

	r.logger.Info("behind network, driving catch-up toward peer tip",
		"our_seq", ourSeq,
		"peer_seq", peerSeq,
		"gap", peerSeq-ourSeq,
		"peer", peerID,
	)

	// Funnel through the bounded catch-up: record the tip and arm at most one
	// acquisition toward the best target. Both acquisition paths (replay-delta
	// or legacy) install their own state machines, so responses have a live
	// consumer; a bare mtGET_LEDGER broadcast would arrive with none and drop.
	r.ensureCatchupAcquisition(peerSeq, peerHash, peerID)
}

// ourLCLMatchesPeers checks if our closed ledger hash matches what the
// majority of tracked peers report. Returns true if we have no peer data
// (to avoid blocking startup).
func (r *Router) ourLCLMatchesPeers() bool {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return true
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return true
	}
	ourHash := closedLedger.Hash()
	ourSeq := svc.GetClosedLedgerIndex()

	r.peersMu.RLock()
	defer r.peersMu.RUnlock()

	if len(r.peerStates) == 0 {
		return true
	}

	matching := 0
	total := 0
	for _, ps := range r.peerStates {
		if ps.LedgerSeq == ourSeq {
			total++
			if ps.LedgerHash == ourHash {
				matching++
			}
		}
	}

	// If no peers at our seq, allow transition (they may have advanced)
	if total == 0 {
		return true
	}

	return matching > total/2
}

func (r *Router) handleLedgerData(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeLedgerData, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode ledger_data", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-decode")
		return
	}
	ld, ok := decoded.(*message.LedgerData)
	if !ok {
		return
	}

	// A reply carrying a request_cookie answers a GetLedger we relayed on
	// another peer's behalf. Route it back to the original requester named
	// by the cookie and do not consume it locally. Mirrors rippled
	// onMessage(TMLedgerData).
	if ld.RequestCookie != 0 {
		r.routeRelayedLedgerData(ld, msg.PeerID)
		return
	}

	var il *inbound.Ledger
	if len(ld.LedgerHash) == 32 {
		var h [32]byte
		copy(h[:], ld.LedgerHash)
		il = r.fetchTracker.Find(h)
	}

	r.logger.Info("received ledger data",
		"peer", msg.PeerID,
		"seq", ld.LedgerSeq,
		"nodes", len(ld.Nodes),
		"itype", ld.InfoType,
		"has_inbound", il != nil,
	)

	// liTS_CANDIDATE response — feeds the engine via the tx-set path
	// (consensus-time only).
	if ld.InfoType == message.LedgerInfoTsCandidate {
		r.handleTxSetData(ld, uint64(msg.PeerID))
		return
	}

	if il != nil {
		if r.handleInboundLedgerData(il, ld, uint64(msg.PeerID)) {
			return
		}
		// If handleInboundLedgerData returned false (e.g. GotBase failed),
		// fall through to the legacy header-only adoption path
	}

	// During initial sync, try to adopt the ledger header from peers
	if ld.InfoType == message.LedgerInfoBase && len(ld.Nodes) > 0 && r.adaptor.NeedsInitialSync() {
		headerData := ld.Nodes[0].NodeData
		if err := r.adaptor.AdoptLedgerFromHeader(headerData); err != nil {
			r.logger.Debug("failed to adopt ledger header", "error", err, "peer", msg.PeerID)
		} else {
			r.logger.Info("adopted ledger from peer",
				"seq", ld.LedgerSeq,
				"peer", msg.PeerID,
			)
			return
		}
	}

	if len(ld.LedgerHash) == 32 {
		var ledgerID consensus.LedgerID
		copy(ledgerID[:], ld.LedgerHash)

		var payload []byte
		for _, node := range ld.Nodes {
			payload = append(payload, node.NodeData...)
		}

		if err := r.engine.OnLedger(ledgerID, payload); err != nil {
			r.logger.Debug("engine rejected ledger data", "error", err, "peer", msg.PeerID)
		}
	}
}

// handleInboundLedgerData feeds LedgerData to the given InboundLedger
// acquisition (already matched by hash in handleLedgerData). Returns true if
// the data was consumed by the acquisition.
func (r *Router) handleInboundLedgerData(il *inbound.Ledger, ld *message.LedgerData, peerID uint64) bool {
	if il == nil {
		return false
	}

	switch ld.InfoType {
	case message.LedgerInfoBase:
		if len(ld.Nodes) < 2 {
			// Response doesn't include root nodes — can't do full acquisition.
			// Drop the acquisition and fall through to legacy adoption.
			r.logger.Debug("inbound ledger: response has < 2 nodes, falling back", "nodes", len(ld.Nodes))
			r.fetchTracker.Remove(il.Hash(), false)
			return false
		}
		if err := il.GotBase(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotBase failed, falling back", "error", err)
			r.adaptor.IncPeerBadData(peerID, "ledger-data-base")
			r.fetchTracker.Remove(il.Hash(), false)
			return false
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true
		}

		// Re-request the missing state and transaction nodes from the peer
		// that answered, mirroring rippled trigger(peer) on a reply.
		r.requestMissingAcquisitionNodes(il, peerID)
		return true

	case message.LedgerInfoAsNode:
		if err := il.GotStateNodes(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotStateNodes failed", "error", err)
			r.adaptor.IncPeerBadData(peerID, "ledger-data-state")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true
		}

		r.requestMissingAcquisitionNodes(il, peerID)
		return true

	case message.LedgerInfoTxNode:
		if err := il.GotTransactionNodes(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotTransactionNodes failed", "error", err)
			r.adaptor.IncPeerBadData(peerID, "ledger-data-tx")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true
		}

		r.requestMissingAcquisitionNodes(il, peerID)
		return true
	}

	return false
}

// requestMissingAcquisitionNodes asks for the acquisition's outstanding
// account-state and transaction tree nodes, requesting both trees in parallel.
// When target is non-zero (the reply path) the re-request goes to just that
// peer — the one that answered — mirroring rippled's trigger(peer) on a reply;
// when target is zero (the no-progress timeout path) it fans out to every peer
// in the (possibly broadened) set, mirroring trigger(nullptr). Each call is a
// no-op for a tree already complete.
//
// Once the acquisition has timed out at least once we mark the requests
// indirect (query_type=qtINDIRECT) so peers relay them on our behalf,
// mirroring rippled's InboundLedger::trigger timeouts_ != 0 gate.
func (r *Router) requestMissingAcquisitionNodes(il *inbound.Ledger, target uint64) {
	indirect := il.Timeouts() > 0
	// target != 0 is the reply path (re-request to the peer that answered);
	// target == 0 is the no-progress timeout fan-out. The reply path is
	// throttled against nodes already requested this timer interval so a peer
	// reply cannot re-request the same outstanding nodes at RTT rate; the
	// timeout path bypasses that throttle so the fan-out still reaches everyone.
	isReply := target != 0
	stateIDs, txIDs := il.CollectMissingRequest(isReply)
	if len(stateIDs) == 0 && len(txIDs) == 0 {
		return
	}
	hash := il.Hash()
	peers := il.Peers()
	if target != 0 {
		peers = []uint64{target}
	}
	for _, peerID := range peers {
		if len(stateIDs) > 0 {
			if err := r.adaptor.RequestStateNodes(peerID, hash, stateIDs, indirect); err != nil {
				r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
			}
		}
		if len(txIDs) > 0 {
			if err := r.adaptor.RequestTransactionNodes(peerID, hash, txIDs, indirect); err != nil {
				r.logger.Warn("inbound ledger: failed to request tx nodes", "error", err)
			}
		}
	}
}

// escalateAcquisition runs one no-progress escalation rung for a stalled
// acquisition, mirroring rippled InboundLedger::onTimer's !wasProgress branch:
// try to complete locally from the fetch-pack cache, broaden the source-peer
// set, re-request the missing nodes (timer-driven, so a silent peer cannot
// stall it), arm a one-shot fetch-pack, and once aggressive ask the peer set
// for the missing nodes by content hash.
func (r *Router) escalateAcquisition(il *inbound.Ledger, now time.Time) {
	if il.CheckLocal(func(h [32]byte) ([]byte, bool) { return r.fetchPacks.get(h, now) }) && il.IsComplete() {
		r.completeInboundLedger(il)
		return
	}
	r.broadenAcquisitionPeers(il)
	r.requestMissingAcquisitionNodes(il, 0)
	r.tryFetchPackEscalation(il)
	r.requestAcquisitionNodesByHash(il)
}

// acquisitionPeerBroaden bounds how many fresh peers a single no-progress
// escalation adds to a stalled acquisition's source set, mirroring rippled's
// peerCountAdd (InboundLedger::addPeers).
const acquisitionPeerBroaden = 3

// broadenAcquisitionPeers adds up to acquisitionPeerBroaden fresh peers that
// report the wanted ledger to a stalled acquisition's source set, mirroring
// rippled InboundLedger::addPeers. Without it a single silent source peer
// stalls the acquisition for its whole budget; with it the re-requests fan out
// to several peers that can actually answer. AddPeer dedups against the set and
// caps it, so an exhausted overlay or a full set simply adds fewer.
func (r *Router) broadenAcquisitionPeers(il *inbound.Ledger) {
	for _, peerID := range r.adaptor.PeersWithLedger(il.Hash(), il.Seq(), il.Peers(), acquisitionPeerBroaden) {
		il.AddPeer(peerID)
	}
}

// failInboundAcquisition reaps an acquisition whose retry budget is exhausted.
// For a consensus-driven acquisition it also tells the engine, so a node pinned
// in wrongLedger on an unacquirable ledger can drop to a recoverable resync
// rather than starving the ledger loop into a fatal watchdog abort (issue #985).
func (r *Router) failInboundAcquisition(il *inbound.Ledger) {
	hash := il.Hash()
	reason := il.Reason()
	r.logger.Warn("inbound ledger acquisition failed",
		"seq", il.Seq(),
		"hash", fmt.Sprintf("%x", hash[:8]),
		"timeouts", il.Timeouts(),
	)
	r.fetchTracker.Remove(hash, false)
	if reason == inbound.ReasonConsensus && r.engine != nil {
		r.engine.OnLedgerAcquireFailed(consensus.LedgerID(hash))
	}
}

// inboundByHashBatch bounds how many missing-node content hashes a single
// by-hash escalation requests per tree. By-hash is a targeted divergent-path
// fallback, not a bulk-transfer path, so the set is kept small — matching
// rippled's getNeededHashes cap of 4 per tree (InboundLedger::neededStateHashes/
// neededTxHashes).
const inboundByHashBatch = 4

// requestAcquisitionNodesByHash performs the by-hash escalation rung: once the
// acquisition has gone aggressive it asks the peer set for the missing
// state/tx nodes by content hash (TMGetObjectByHash), the unambiguous fallback
// for a node on a divergent path that path-based requests cannot place. Replies
// are served from peers' node stores and routed back through the fetch-pack
// cache + CheckLocal placement.
func (r *Router) requestAcquisitionNodesByHash(il *inbound.Ledger) {
	state, tx := il.TakeByHashRequest(inboundByHashBatch)
	if len(state) == 0 && len(tx) == 0 {
		return
	}
	peers := il.Peers()
	hash := il.Hash()
	seq := il.Seq()
	r.sendNodesByHash(peers, hash, seq, state, message.ObjectTypeStateNode)
	r.sendNodesByHash(peers, hash, seq, tx, message.ObjectTypeTransactionNode)
}

// sendNodesByHash issues a TMGetObjectByHash query for the given node content
// hashes to every peer in the set.
func (r *Router) sendNodesByHash(peers []uint64, ledgerHash [32]byte, seq uint32, hashes [][32]byte, objType message.ObjectType) {
	if len(hashes) == 0 || len(peers) == 0 {
		return
	}
	objs := make([]message.IndexedObject, 0, len(hashes))
	for i := range hashes {
		h := hashes[i]
		objs = append(objs, message.IndexedObject{Hash: h[:], LedgerSeq: seq})
	}
	req := &message.GetObjectByHash{
		ObjType:    objType,
		Query:      true,
		Seq:        seq,
		LedgerHash: ledgerHash[:],
		Objects:    objs,
	}
	frame, err := encodeFrame(message.TypeGetObjects, req)
	if err != nil {
		r.logger.Debug("inbound ledger: encode by-hash request failed", "error", err)
		return
	}
	for _, peerID := range peers {
		if err := r.adaptor.SendToPeer(peerID, frame); err != nil {
			r.logger.Debug("inbound ledger: by-hash request send failed", "peer", peerID, "error", err)
		}
	}
	r.logger.Info("inbound ledger: requesting nodes by hash",
		"seq", seq,
		"hash", fmt.Sprintf("%x", ledgerHash[:4]),
		"count", len(hashes),
		"obj_type", objType,
		"peers", len(peers),
	)
}

// completeInboundLedger finalizes an InboundLedger acquisition and adopts the
// ledger. A ReasonGeneric acquisition (RPC-driven, ledger_request) is persisted
// for querying but does not flip operating mode or notify consensus, so an
// arbitrary historical fetch can't disturb the active chain.
func (r *Router) completeInboundLedger(il *inbound.Ledger) {
	r.fetchTracker.Remove(il.Hash(), true)

	h, stateMap, txMap, err := il.Result()
	if err != nil {
		r.logger.Warn("inbound ledger: failed to get result", "error", err)
		return
	}
	peerID := il.PeerID()

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// A history backfill is a store-only ingest below the closed tip: persist
	// and index the skipped ledger, then advance the serial backward walk to
	// its parent. It never touches operating mode or the consensus engine.
	if il.Reason() == inbound.ReasonHistory {
		if err = svc.AdoptLedgerWithState(context.TODO(), h, stateMap, txMap); err != nil {
			r.logger.Warn("inbound ledger: history backfill ingest failed",
				"error", err, "seq", h.LedgerIndex)
			return
		}
		r.startHistoryBackfill(h.LedgerIndex-1, h.ParentHash, peerID, r.currentHistoryFloor())
		return
	}

	// The acquisition fetches the header, state map, and transaction map; txMap
	// is nil only when the ledger has no transactions (empty tx tree), in which
	// case the service installs the genesis-shaped empty tx map.
	//
	// context.TODO: same as adoptVerifiedLedger — reached from a peer-message
	// handler stack with no plumbed context. See note there.
	//
	// A consensus acquisition that completes two or more ledgers ahead of our
	// working ledger is a catch-up jump: its parent chain is absent, and on a
	// busy network fresh ledgers close faster than a backward parent chase can
	// fill the gap, so stashing the tip and chasing parents never converges.
	// Adopt the acquired tip directly instead, jumping the working ledger
	// forward so consensus rejoins on the trusted-validation-preferred branch;
	// the skipped ledgers backfill off the critical path via the ReasonHistory
	// walk armed below. This mirrors rippled setFullLedger/checkAccept plus
	// fetchForHistory. The published validated pointer still only advances at
	// quorum (drainPendingLedgerValidation). Gap ≤ 1 (single-ledger catch-up,
	// whose parent is present) and generic RPC acquisitions keep the
	// held-adoption seam so out-of-order arrivals cascade in order.
	var res service.SubmitHeldAdoptionResult
	if preJumpClosed := svc.GetClosedLedgerIndex(); il.Reason() == inbound.ReasonConsensus && h.LedgerIndex > preJumpClosed+1 {
		if err = svc.AdoptLedgerWithState(context.TODO(), h, stateMap, txMap); err != nil {
			r.logger.Warn("inbound ledger: catch-up jump adopt failed",
				"error", err, "seq", h.LedgerIndex)
			return
		}
		r.startHistoryBackfill(h.LedgerIndex-1, h.ParentHash, peerID, preJumpClosed)
	} else if res, err = svc.SubmitHeldAdoption(context.TODO(), h, stateMap, txMap); err != nil {
		r.logger.Warn("inbound ledger: failed to adopt with state", "error", err)
		return
	}

	// A generic (RPC-driven) acquisition is now persisted and queryable; it
	// must not advance operating mode or feed consensus.
	if il.Reason() == inbound.ReasonGeneric {
		r.logger.Info("acquired ledger (generic) with full state from peer",
			"seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]),
		)
		if res.Stashed {
			r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
		}
		return
	}

	// Only upgrade to Tracking if still in a lower mode.
	// Never demote from Full — that would break consensus participation.
	if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		r.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	r.logger.Info("adopted ledger with full state from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
	)
	// Notify the consensus engine so it can flip out of
	// ModeWrongLedger via Engine.OnLedger, mirroring the replay-delta
	// path in adoptVerifiedLedger.
	if r.engine != nil {
		if err := r.engine.OnLedger(consensus.LedgerID(h.Hash), nil); err != nil {
			r.logger.Debug("engine rejected adopted ledger", "error", err, "seq", h.LedgerIndex)
		}
	}
	// Forward walk / retarget loop: this consensus acquisition (which the
	// fetchTracker.Remove above already cleared from the in-flight set) advanced
	// our closed ledger, either a gap-1 forward step or a jump-adopt. Re-arm the
	// next single acquisition — the next forward closed+1 when same-branch, else
	// a jump toward the highest trusted tip. Skipped on a stash: closed didn't
	// move, so the parent chase is what makes progress and re-arming would only
	// re-request the just-stashed hash.
	if res.Stashed {
		r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
	} else {
		r.armCatchupTowardTarget()
	}
}
