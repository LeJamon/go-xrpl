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
	"github.com/LeJamon/go-xrpl/shamap"
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
		if peerHash == ([32]byte{}) {
			r.peersMu.Lock()
			delete(r.peerStates, msg.PeerID)
			r.peersMu.Unlock()
			r.adaptor.UpdatePeerLCL(uint64(msg.PeerID), consensus.LedgerID{})
			return
		}
		var parentHash [32]byte
		haveParent := len(sc.LedgerHashPrevious) == 32
		if haveParent {
			copy(parentHash[:], sc.LedgerHashPrevious)
		}

		// status_change is the only gossip source carrying the parent hash
		// (validations don't), so the forward-delta catch-up learns linkage here.
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
				if aheadByMoreThan(sc.LedgerSeq, ourSeq, 2) {
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
				if aheadByMoreThan(sc.LedgerSeq, ourSeq, 1) {
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

// maxConcurrentCatchup caps concurrent consensus-reason ledger acquisitions.
// rippled's LedgerMaster::doAdvance drives a SINGLE needed target rather than
// one InboundLedger per gossiped status/validation; the arming sites retarget
// (never add) once the cap is hit.
const maxConcurrentCatchup = 1

// maxForwardDeltaGap bounds how far behind the tip the router walks forward one
// ledger at a time (replay-delta against the held parent) before it prefers a
// single full-state jump-adopt. Within the gap a same-branch serial walk (O(txs)
// per hop) outruns the network's close cadence and converges; beyond it a cold
// or far-behind start jumps straight to the validated tip. Mirrors rippled
// reserving InboundLedger full-state acquisition for the cold/forked case.
const maxForwardDeltaGap = 128

// seqHashRetain bounds the seqHash table to a trailing window of sequences so a
// long-running node never grows it unbounded.
const seqHashRetain = 256

// recordSeqHash records the network's hash — and, from a status_change, the
// parent hash — for a ledger sequence, backing the forward-delta catch-up. A
// later fuller entry (with parent) upgrades an earlier hash-only one rather than
// clobbering it.
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

	// The parent linkage also names seq-1's own hash; seed it (parentless) so a
	// same-branch check against our closed seq succeeds without a direct
	// validation for it.
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

// catchupInFlight counts active consensus-reason acquisitions across both the
// legacy fetchTracker and the replay-delta replayer. Generic (RPC-driven)
// acquisitions are excluded so an arbitrary fetch never consumes a catch-up slot.
func (r *Router) catchupInFlight() int {
	return r.fetchTracker.CountReason(inbound.ReasonConsensus) + r.replayer.Count()
}

// recordCatchupTarget raises the single consensus catch-up target or refreshes
// its preferred peer when another peer advertises the same ledger.
func (r *Router) recordCatchupTarget(seq uint32, hash [32]byte, peerID uint64) {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	if seq > r.catchup.seq {
		r.catchup = catchupTarget{seq: seq, hash: hash, peerID: peerID}
	} else if seq == r.catchup.seq && hash == r.catchup.hash {
		r.catchup.peerID = peerID
	}
}

func (r *Router) invalidateCatchupPeer(peerID uint64) {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	if r.catchup.peerID == peerID {
		r.catchup.peerID = 0
	}
}

const catchupFailureCooldown = 5 * time.Minute

func (r *Router) markFailedCatchupAcquisition(hash [32]byte) {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	if r.catchupFailures == nil {
		r.catchupFailures = make(map[[32]byte]time.Time)
	}
	now := time.Now()
	for failedHash, retryAfter := range r.catchupFailures {
		if !now.Before(retryAfter) {
			delete(r.catchupFailures, failedHash)
		}
	}
	r.catchupFailures[hash] = now.Add(catchupFailureCooldown)
}

func (r *Router) catchupRetryBlocked(hash [32]byte, now time.Time) bool {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	retryAfter, ok := r.catchupFailures[hash]
	if ok && !now.Before(retryAfter) {
		delete(r.catchupFailures, hash)
		return false
	}
	return ok
}

// bestCatchupTarget returns the current highest recorded catch-up target.
func (r *Router) bestCatchupTarget() (seq uint32, hash [32]byte, peerID uint64) {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	return r.catchup.seq, r.catchup.hash, r.catchup.peerID
}

// armCatchupTowardTarget arms one catch-up acquisition while under the
// maxConcurrentCatchup cap and the tip is still ahead of closed. Two strategies,
// mirroring rippled's LedgerMaster::doAdvance forward walk vs InboundLedger
// full-state acquisition:
//
//   - Forward-delta step: closed+1 is a known clean child of closed and the tip
//     is within maxForwardDeltaGap → acquire closed+1 (parent is local, so the
//     replay-delta path is selected); completion re-arms the serial walk.
//   - Jump-adopt: otherwise (cold/far/forked) acquire the far validated tip
//     directly; the legacy full-state path plus completeInboundLedger's gap>1
//     branch jumps the working ledger forward.
func (r *Router) armCatchupTowardTarget() {
	r.armCatchupTowardTargetWithPeer(0)
}

func (r *Router) armConsensusCatchup() {
	if r.armPendingConsensusLedger() {
		return
	}
	r.armCatchupTowardTarget()
}

func (r *Router) armPendingConsensusLedger() bool {
	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()

	if r.activeConsensusLedger != ([32]byte{}) {
		if r.isAcquiring(r.activeConsensusLedger) {
			return true
		}
		r.activeConsensusLedger = [32]byte{}
	}
	hash := r.pendingConsensusLedger
	if hash == ([32]byte{}) {
		return false
	}
	if r.isAcquiring(hash) {
		r.pendingConsensusLedger = [32]byte{}
		return true
	}
	if r.catchupInFlight() >= maxConcurrentCatchup {
		return true
	}
	if svc := r.adaptor.LedgerService(); svc != nil {
		if held, err := svc.GetLedgerByHash(hash); err == nil && held != nil {
			r.pendingConsensusLedger = [32]byte{}
			return false
		}
	}

	peerID, _ := r.selectAcquisitionPeer(0)
	r.startLedgerAcquisitionLegacyLocked(0, hash, peerID)
	if r.fetchTracker.Find(hash) == nil {
		return true
	}
	r.activeConsensusLedger = hash
	r.pendingConsensusLedger = [32]byte{}
	return true
}

func (r *Router) armCatchupTowardTargetWithPeer(peerHint uint64) {
	if r.adaptor == nil {
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	if r.catchupInFlight() >= maxConcurrentCatchup {
		return
	}
	tSeq, tHash, _ := r.bestCatchupTarget()
	if tSeq == 0 {
		return
	}
	closed := svc.GetClosedLedgerIndex()
	if tSeq <= closed {
		return
	}

	if seq, hash, ok := r.forwardDeltaStep(svc, closed, tSeq); ok {
		peer, found := r.resolveAcquisitionPeer(seq, peerHint)
		if !found {
			return
		}
		r.startLedgerAcquisition(seq, hash, peer)
		return
	}
	peer, found := r.resolveAcquisitionPeer(tSeq, peerHint)
	if !found {
		return
	}
	r.startLedgerAcquisition(tSeq, tHash, peer)
}

// forwardDeltaStep returns the (seq, hash) for a forward one-ledger step
// against our held closed ledger when all hold, else ok=false (deferring to
// jump-adopt):
//
//   - gap to tip within maxForwardDeltaGap;
//   - a known network hash for closed+1; and
//   - closed+1 descends from our closed ledger (same branch) — either closed+1's
//     recorded parentHash equals our closed hash, or, when only a validation
//     populated closed+1, the recorded hash for our closed seq equals it. The
//     parent of closed+1 IS the ledger at our closed seq, so these are equivalent.
func (r *Router) forwardDeltaStep(svc *service.Service, closed, tipSeq uint32) (seq uint32, hash [32]byte, ok bool) {
	if tipSeq-closed > maxForwardDeltaGap {
		return 0, [32]byte{}, false
	}
	next := closed + 1
	entry, known := r.lookupSeqHash(next)
	if !known || entry.hash == ([32]byte{}) {
		return 0, [32]byte{}, false
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return 0, [32]byte{}, false
	}
	closedHash := closedLedger.Hash()

	sameBranch := false
	if entry.haveParent {
		sameBranch = entry.parentHash == closedHash
	} else if cEntry, okC := r.lookupSeqHash(closed); okC && cEntry.hash != ([32]byte{}) {
		sameBranch = cEntry.hash == closedHash
	}
	if !sameBranch {
		return 0, [32]byte{}, false
	}
	return next, entry.hash, true
}

// ensureCatchupAcquisition is the single funnel for gossip-driven consensus
// catch-up: record the best target, then arm one acquisition under the
// maxConcurrentCatchup cap. At the cap it only retargets, so a stream of
// ever-higher tips no longer fans out one acquisition per event; the completion
// path re-arms toward the latest target. Bounds CONCURRENCY only — callers do
// their own eligibility gating first.
func (r *Router) ensureCatchupAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	if seq == 0 || seq <= svc.GetClosedLedgerIndex() {
		return
	}
	peerID, _ = r.resolveAcquisitionPeer(seq, peerID)
	r.recordCatchupTarget(seq, hash, peerID)
	if il := r.fetchTracker.Find(hash); il != nil && il.Reason() == inbound.ReasonConsensus {
		r.refreshCatchupAcquisitionPeer(il, peerID)
		return
	}
	r.armCatchupTowardTargetWithPeer(peerID)
}

func (r *Router) refreshCatchupAcquisitionPeer(il *inbound.Ledger, peerID uint64) {
	if peerID == 0 || il.State() != inbound.StateWantBase || slices.Contains(il.Peers(), peerID) {
		return
	}
	r.requestLedgerBase(il, peerID, "failed to request ledger base from replacement peer")
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
	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()
	r.startLedgerAcquisitionLocked(seq, hash, peerID)
}

func (r *Router) startLedgerAcquisitionLocked(seq uint32, hash [32]byte, peerID uint64) {
	if r.catchupRetryBlocked(hash, time.Now()) {
		return
	}

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

	// Already held locally (built or just adopted): never re-download. Without
	// this latch a consensus retrigger refetches the just-adopted ledger in a
	// tight loop, flooding peers past rippled's resource drop threshold.
	if svc := r.adaptor.LedgerService(); svc != nil {
		if l, err := svc.GetLedgerByHash(hash); err == nil && l != nil {
			return
		}
	}

	parent := r.adaptor.GetParentLedgerForReplay(seq)
	if parent != nil && r.adaptor.PeerSupportsReplay(peerID) {
		if err := r.startReplayDeltaAcquisition(seq, hash, peerID, parent); err == nil {
			return
		}
		// Fall through to the legacy path on issue failure.
	}
	r.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
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
	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()
	r.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
}

func (r *Router) startLedgerAcquisitionLegacyLocked(seq uint32, hash [32]byte, peerID uint64) {
	// Safety net: if a replay-delta for the same hash is still
	// registered, don't start a legacy on top of it — one path is
	// always enough.
	if r.replayer.Has(hash) {
		return
	}

	il, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
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

	r.seedAcquisitionPeers(il)
	requested := false
	for _, candidate := range il.Peers() {
		if r.requestLedgerBaseFromPeer(il, candidate, "failed to request ledger base from peer") {
			requested = true
		}
	}
	if !requested {
		r.requestLedgerBase(il, 0, "failed to request ledger base from peer")
	}
}

// requestConsensusLedger gives the consensus engine's exact wrong-LCL priority
// over speculative catch-up work, then coalesces later preference changes behind
// that exact acquisition. Hash-only sequence zero is resolved from the verified
// header in GotBase.
func (r *Router) requestConsensusLedger(id consensus.LedgerID) error {
	hash := [32]byte(id)
	if hash == ([32]byte{}) {
		return nil
	}

	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()

	if r.activeConsensusLedger != ([32]byte{}) {
		if r.isAcquiring(r.activeConsensusLedger) {
			if r.activeConsensusLedger != hash {
				r.pendingConsensusLedger = hash
			}
			return nil
		}
		r.activeConsensusLedger = [32]byte{}
	}
	if r.fetchTracker.Find(hash) != nil || r.replayer.Has(hash) {
		r.activeConsensusLedger = hash
		if r.pendingConsensusLedger == hash {
			r.pendingConsensusLedger = [32]byte{}
		}
		return nil
	}
	for _, active := range r.fetchTracker.Active() {
		if active.Reason() != inbound.ReasonConsensus {
			continue
		}
		if r.fetchTracker.DiscardExpected(active) {
			if lane := r.currentAcquisitionWork(); lane != nil {
				lane.cancelLedger(active)
			}
			r.retireAcquisitionStore(context.TODO(), active)
		}
	}
	for _, activeHash := range r.replayer.InFlight() {
		r.replayer.Abandon(activeHash)
	}

	peerID, _ := r.selectAcquisitionPeer(0)
	r.startLedgerAcquisitionLegacyLocked(0, hash, peerID)
	if r.fetchTracker.Find(hash) == nil {
		r.pendingConsensusLedger = hash
	} else {
		r.activeConsensusLedger = hash
		if r.pendingConsensusLedger == hash {
			r.pendingConsensusLedger = [32]byte{}
		}
	}
	return nil
}

const acquisitionPeerStart = 5

func (r *Router) seedAcquisitionPeers(il *inbound.Ledger) {
	peers := il.Peers()
	remaining := acquisitionPeerStart - len(peers)
	if remaining <= 0 {
		return
	}
	for _, peerID := range r.adaptor.SelectLedgerPeers(il.Hash(), il.Seq(), peers, remaining) {
		il.AddPeerBounded(peerID, acquisitionPeerStart)
	}
}

func (r *Router) requestLedgerBase(il *inbound.Ledger, peerID uint64, logMessage string) bool {
	excluded := make(map[uint64]struct{})
	for {
		if peerID == 0 {
			var ok bool
			peerID, ok = r.selectAcquisitionPeerExcluding(il.Seq(), excluded)
			if !ok {
				return false
			}
		}

		if r.requestLedgerBaseFromPeer(il, peerID, logMessage) {
			return true
		}
		excluded[peerID] = struct{}{}
		peerID = 0
	}
}

func (r *Router) requestLedgerBaseFromPeer(il *inbound.Ledger, peerID uint64, logMessage string) bool {
	il.AddPeer(peerID)
	err := r.adaptor.RequestLedgerBaseFromPeer(peerID, il.Hash(), il.Seq(), il.Timeouts() > 0)
	if err == nil {
		return true
	}
	r.logger.Warn(logMessage, "error", err, "peer", peerID)
	if !errors.Is(err, peermanagement.ErrPeerNotFound) &&
		!errors.Is(err, peermanagement.ErrConnectionClosed) {
		return true
	}
	il.RemovePeer(peerID)
	r.HandlePeerDisconnect(peermanagement.PeerID(peerID))
	return false
}

func (r *Router) invalidateHistoryPeer(peerID uint64) {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	if r.history.peerID == peerID {
		r.history.peerID = 0
	}
}

// startHistoryBackfill records the next skipped ledger to backfill after a
// jump-adopt, bounded below by floor (the pre-jump closed seq — already
// contiguous). The walk is serial and backward, each header naming its parent;
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

// armHistoryBackfill drives one backward history-backfill acquisition from the
// maintenance tick (rippled fetchForHistory from doAdvance). Locally-held
// ledgers advance the walk without a fetch; it ends at the gap floor, the
// online-delete floor, or genesis. At most one ReasonHistory acquisition runs,
// never in the consensus catch-up slot.
func (r *Router) armHistoryBackfill() {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	if targetSeq, _, _ := r.bestCatchupTarget(); targetSeq > svc.GetClosedLedgerIndex() {
		return
	}
	if svc.GetValidatedLedgerIndex() != svc.GetClosedLedgerIndex() {
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
	peer, ok := r.resolveAcquisitionPeer(target.seq, target.peerID)
	if !ok {
		return
	}
	r.startHistoryAcquisition(target.seq, target.hash, peer)
}

// startHistoryAcquisition requests a skipped historical ledger (header + state)
// over legacy mtGET_LEDGER as a ReasonHistory acquisition. Replay-delta doesn't
// apply: the walk is backward, so the parent is never locally available.
func (r *Router) startHistoryAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	if r.replayer.Has(hash) {
		return
	}
	il, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
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
	r.requestLedgerBase(il, peerID, "failed to request history ledger base from peer")
}

// FetchInfo returns the inbound-ledger acquisition snapshot served by the
// fetch_info RPC. Safe to call from any goroutine.
func (r *Router) FetchInfo() map[string]any {
	return r.fetchTracker.Info()
}

// ClearFetchInfo resets the acquisition counters and recent-failure history,
// backing fetch_info's `clear` param.
func (r *Router) ClearFetchInfo() {
	for _, ledger := range r.fetchTracker.Clear() {
		if lane := r.currentAcquisitionWork(); lane != nil {
			lane.cancelLedger(ledger)
		}
		r.retireAcquisitionStore(context.TODO(), ledger)
	}
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

	peerID, _ := r.selectAcquisitionPeer(seq)

	il, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.NewGeneric(hash, seq, peerID, r.logger, r.acquisitionOpts()...)
	})
	if created {
		r.logger.Info("starting ledger acquisition (generic, ledger_request)",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
		)
		if peerID != 0 {
			r.requestLedgerBase(il, peerID, "ledger_request: failed to request ledger base")
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
func (r *Router) resolveAcquisitionPeer(seq uint32, preferred uint64) (uint64, bool) {
	if preferred != 0 && (r.peerSessions == nil || r.peerSessions.IsPeerConnected(peermanagement.PeerID(preferred))) {
		return preferred, true
	}
	return r.selectAcquisitionPeer(seq)
}

func (r *Router) selectAcquisitionPeer(seq uint32) (uint64, bool) {
	return r.selectAcquisitionPeerExcluding(seq, nil)
}

func (r *Router) selectAcquisitionPeerExcluding(seq uint32, excluded map[uint64]struct{}) (uint64, bool) {
	r.peersMu.RLock()
	type candidate struct {
		id  uint64
		seq uint32
	}
	candidates := make([]candidate, 0, len(r.peerStates))
	for pid, st := range r.peerStates {
		candidates = append(candidates, candidate{id: uint64(pid), seq: st.LedgerSeq})
	}
	r.peersMu.RUnlock()

	var fallback uint64
	var haveFallback bool
	for _, peer := range candidates {
		if _, skip := excluded[peer.id]; skip {
			continue
		}
		if r.peerSessions != nil && !r.peerSessions.IsPeerConnected(peermanagement.PeerID(peer.id)) {
			continue
		}
		if !haveFallback {
			fallback, haveFallback = peer.id, true
		}
		if seq == 0 || peer.seq >= seq {
			return peer.id, true
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
		// Closed advanced: continue the serial forward walk toward the new
		// closed+1 (or jump-adopt if still far). On a stash closed didn't move,
		// so the parent chase above drives progress instead.
		r.armConsensusCatchup()
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
	// Record the hash for this seq regardless of the acquire gate below: the
	// forward-delta decision needs it for closed+1 and for our own closed seq
	// (same-branch check). Validations carry no parent hash.
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
	// majority validated another. The forward funnel below never fetches behind
	// closed, so acquire it directly — without it the validation trie can never
	// place the majority branch (rippled RCLValidationsAdaptor::acquire).
	if v.LedgerSeq <= svc.GetClosedLedgerIndex() {
		if r.catchupInFlight() >= maxConcurrentCatchup {
			return
		}
		r.startLedgerAcquisition(v.LedgerSeq, hash, originPeer)
		return
	}
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

	// At the cap, record the target; the maintenance-tick re-arm drives it once
	// the slot frees.
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
	// At the cap, skip: the maintenance-tick re-arm toward the recorded tip
	// jump-adopts past the stash at gap > 1.
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
	networkSeq := peerSeq
	r.peersMu.RLock()
	for _, state := range r.peerStates {
		if state.LedgerSeq > networkSeq {
			networkSeq = state.LedgerSeq
		}
	}
	r.peersMu.RUnlock()
	if targetSeq, _, _ := r.bestCatchupTarget(); targetSeq > networkSeq {
		networkSeq = targetSeq
	}

	// If we're caught up (gap ≤ 1) and not yet Full, transition to Full
	// only if our LCL hash matches what the majority of peers report.
	if !aheadByMoreThan(networkSeq, ourSeq, 1) {
		if r.adaptor.GetOperatingMode() == consensus.OpModeTracking {
			validatedSeq := svc.GetValidatedLedgerIndex()
			if !aheadByMoreThan(networkSeq, validatedSeq, 1) && r.ourLCLMatchesPeers() {
				r.logger.Info("caught up with network, transitioning to Full",
					"our_seq", ourSeq,
					"peer_seq", networkSeq,
				)
				r.adaptor.SetOperatingMode(consensus.OpModeFull)
			} else {
				r.logger.Info("caught up but validated LCL is not aligned, staying in Tracking",
					"our_seq", ourSeq,
					"validated_seq", validatedSeq,
					"peer_seq", networkSeq,
				)
			}
		}
		return
	}
	if !aheadByMoreThan(peerSeq, ourSeq, 1) {
		return
	}

	r.logger.Info("behind network, driving catch-up toward peer tip",
		"our_seq", ourSeq,
		"peer_seq", peerSeq,
		"gap", peerSeq-ourSeq,
		"peer", peerID,
	)

	// Funnel through the bounded catch-up. Both acquisition paths install their
	// own state machines, so responses have a live consumer; a bare mtGET_LEDGER
	// broadcast would arrive with none and drop.
	r.ensureCatchupAcquisition(peerSeq, peerHash, peerID)
}

func aheadByMoreThan(seq, base, distance uint32) bool {
	return seq > base && seq-base > distance
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
	if len(ld.LedgerHash) != 32 {
		r.logger.Warn("invalid ledger_data ledger hash", "peer", msg.PeerID, "length", len(ld.LedgerHash))
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-hash")
		return
	}
	if ld.InfoType < message.LedgerInfoBase || ld.InfoType > message.LedgerInfoTsCandidate {
		r.logger.Warn("invalid ledger_data info type", "peer", msg.PeerID, "info_type", ld.InfoType)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-type")
		return
	}
	if (ld.InfoType == message.LedgerInfoTsCandidate && ld.LedgerSeq != 0) ||
		(ld.InfoType != message.LedgerInfoTsCandidate && r.invalidFutureLedgerSequence(ld.LedgerSeq)) {
		r.logger.Warn("invalid ledger_data ledger sequence", "peer", msg.PeerID, "seq", ld.LedgerSeq)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-sequence")
		return
	}
	if ld.Error != message.ReplyErrorNone &&
		(ld.Error < message.ReplyErrorNoLedger || ld.Error > message.ReplyErrorBadRequest) {
		r.logger.Warn("invalid ledger_data reply error", "peer", msg.PeerID, "error", ld.Error)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-error")
		return
	}
	if err := inbound.ValidateReplyNodeCount(ld.Nodes); err != nil {
		r.logger.Warn("invalid ledger_data node count", "error", err, "peer", msg.PeerID)
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-count")
		return
	}
	if ld.InfoType == message.LedgerInfoAsNode || ld.InfoType == message.LedgerInfoTxNode {
		for _, node := range ld.Nodes {
			if len(node.NodeData) == 0 {
				r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-node")
				return
			}
			if _, err := shamap.ParseNodeID(node.NodeID); err != nil {
				r.adaptor.IncPeerBadData(uint64(msg.PeerID), "ledger-data-node")
				return
			}
		}
	}

	// A reply carrying a request_cookie answers a GetLedger we relayed on
	// another peer's behalf. Route it back to the original requester named
	// by the cookie and do not consume it locally. Mirrors rippled
	// onMessage(TMLedgerData).
	if ld.HasRequestCookie() {
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
	}
	if il == nil && ld.InfoType == message.LedgerInfoAsNode {
		r.cacheStaleStateNodes(ld)
	}
}

func (r *Router) cacheStaleStateNodes(ld *message.LedgerData) {
	now := time.Now()
	for _, node := range ld.Nodes {
		if len(node.NodeID) == 0 || len(node.NodeData) == 0 {
			return
		}
		entry, err := shamap.FlushEntryFromWire(node.NodeData, ld.LedgerSeq, shamap.TypeState)
		if err != nil {
			return
		}
		r.fetchPacks.add(entry.Hash, entry.Data, now)
	}
}

// handleInboundLedgerData feeds LedgerData to the given InboundLedger
// acquisition (already matched by hash in handleLedgerData). Returns true if
// the data was consumed by the acquisition.
func (r *Router) handleInboundLedgerData(il *inbound.Ledger, ld *message.LedgerData, peerID uint64) bool {
	if il == nil {
		return false
	}
	if lane := r.currentAcquisitionWork(); lane != nil {
		switch ld.InfoType {
		case message.LedgerInfoBase, message.LedgerInfoAsNode, message.LedgerInfoTxNode:
			if lane.submit(il, acquisitionWorkEvent{
				kind: acquisitionWorkData, data: ld, peerID: peerID,
			}) {
				return true
			}
			r.logger.Warn("inbound ledger reply deferred: acquisition worker saturated",
				"peer", peerID, "seq", il.Seq(), "info_type", ld.InfoType)
			return true
		}
	}

	switch ld.InfoType {
	case message.LedgerInfoBase:
		if len(ld.Nodes) < 2 {
			r.logger.Debug("inbound ledger: response has < 2 nodes", "nodes", len(ld.Nodes))
			if r.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), false) {
				r.retireAcquisitionStore(context.TODO(), il)
			}
			return true
		}
		if err := il.GotBase(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotBase failed", "error", err)
			r.adaptor.IncPeerBadData(peerID, "ledger-data-base")
			if r.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), false) {
				r.retireAcquisitionStore(context.TODO(), il)
			}
			return true
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
		useful, err := il.GotStateNodesUseful(ld.Nodes)
		if err != nil {
			r.logger.Warn("inbound ledger: GotStateNodes failed", "error", err)
			r.adaptor.IncPeerBadData(peerID, "ledger-data-state")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true
		}

		if useful > 0 {
			r.requestMissingAcquisitionNodes(il, peerID)
		}
		return true

	case message.LedgerInfoTxNode:
		useful, err := il.GotTransactionNodesUseful(ld.Nodes)
		if err != nil {
			r.logger.Warn("inbound ledger: GotTransactionNodes failed", "error", err)
			r.adaptor.IncPeerBadData(peerID, "ledger-data-tx")
			return true
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true
		}

		if useful > 0 {
			r.requestMissingAcquisitionNodes(il, peerID)
		}
		return true
	}

	return false
}

// requestMissingAcquisitionNodes asks for the acquisition's outstanding nodes,
// finishing account state before requesting the transaction tree.
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
	// target != 0 is the reply path (re-request to the answering peer), throttled
	// against nodes already requested this timer interval so a reply can't
	// re-request outstanding nodes at RTT rate; target == 0 is the timeout
	// fan-out, which bypasses that throttle to still reach everyone.
	isReply := target != 0
	queryDepth := uint32(0)
	if isReply {
		queryDepth = 1
	}
	stateIDs, txIDs, complete, err := il.CollectMissingRequestContext(context.Background(), isReply)
	if err != nil {
		r.logger.Warn("inbound ledger: failed to collect missing nodes", "error", err)
		return
	}
	if complete {
		r.completeInboundLedger(il)
		return
	}
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
			if err := r.adaptor.RequestStateNodes(peerID, hash, stateIDs, queryDepth, indirect); err != nil {
				r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
			}
		}
		if len(txIDs) > 0 {
			if err := r.adaptor.RequestTransactionNodes(peerID, hash, txIDs, queryDepth, indirect); err != nil {
				r.logger.Warn("inbound ledger: failed to request tx nodes", "error", err)
			}
		}
	}
}

func (r *Router) requestMissingAcquisitionNodesFromAddedPeer(il *inbound.Ledger, peerID uint64) {
	stateIDs, txIDs := il.CollectMissingRequest(true)
	if len(stateIDs) == 0 && len(txIDs) == 0 {
		return
	}
	hash := il.Hash()
	indirect := il.Timeouts() > 0
	if len(stateIDs) > 0 {
		if err := r.adaptor.RequestStateNodes(peerID, hash, stateIDs, 0, indirect); err != nil {
			r.logger.Warn("inbound ledger: failed to request state nodes from added peer", "error", err)
		}
	}
	if len(txIDs) > 0 {
		if err := r.adaptor.RequestTransactionNodes(peerID, hash, txIDs, 0, indirect); err != nil {
			r.logger.Warn("inbound ledger: failed to request tx nodes from added peer", "error", err)
		}
	}
}

// escalateAcquisition runs one no-progress escalation rung for a stalled
// acquisition, mirroring rippled InboundLedger::onTimer's !wasProgress branch:
// try to complete locally from the fetch-pack cache, broaden the source-peer
// set, re-request the missing nodes (timer-driven, so a silent peer cannot
// stall it), arm a one-shot fetch-pack, and once aggressive ask the peer set
// for the missing nodes by content hash.
func (r *Router) escalateAcquisition(il *inbound.Ledger, now time.Time) bool {
	if il.State() == inbound.StateWantBase {
		r.broadenAcquisitionPeers(il)
		r.requestAcquisitionBase(il)
		return false
	}
	if r.currentAcquisitionWork() != nil {
		existingPeers := il.Peers()
		addedPeers := r.broadenAcquisitionPeers(il)
		r.tryFetchPackEscalation(il)
		fetch := func(h [32]byte) ([]byte, bool) { return r.fetchPacks.get(h, time.Now()) }
		queued := r.submitAcquisitionWork(il, acquisitionWorkEvent{
			kind: acquisitionWorkTimer, fetch: fetch, peers: existingPeers, added: addedPeers,
		})
		if !queued {
			r.logger.Warn("inbound ledger: timeout traversal deferred; acquisition worker saturated", "seq", il.Seq())
		}
		return queued
	}
	if il.CheckLocal(func(h [32]byte) ([]byte, bool) { return r.fetchPacks.get(h, now) }) && il.IsComplete() {
		r.completeInboundLedger(il)
		return false
	}
	r.requestMissingAcquisitionNodes(il, 0)
	for _, peerID := range r.broadenAcquisitionPeers(il) {
		r.requestMissingAcquisitionNodesFromAddedPeer(il, peerID)
	}
	r.tryFetchPackEscalation(il)
	r.requestAcquisitionNodesByHash(il)
	return false
}

func (r *Router) requestAcquisitionBase(il *inbound.Ledger) {
	requested := false
	for _, peerID := range il.Peers() {
		if r.requestLedgerBaseFromPeer(il, peerID, "failed to retry ledger base request") {
			requested = true
		}
	}
	if requested {
		return
	}
	peerID, ok := r.selectAcquisitionPeer(il.Seq())
	if !ok {
		return
	}
	r.requestLedgerBase(il, peerID, "failed to retry ledger base request")
}

const acquisitionPeerBroaden = 3

// broadenAcquisitionPeers adds up to acquisitionPeerBroaden fresh peers to a
// stalled acquisition's source set. Known holders rank ahead of fallbacks.
func (r *Router) broadenAcquisitionPeers(il *inbound.Ledger) []uint64 {
	peers := il.Peers()
	limit := acquisitionPeerBroaden
	if len(peers) == 0 {
		limit = acquisitionPeerStart
	}
	var added []uint64
	for _, peerID := range r.adaptor.SelectLedgerPeers(il.Hash(), il.Seq(), peers, limit) {
		if il.AddPeer(peerID) {
			added = append(added, peerID)
		}
	}
	return added
}

// failInboundAcquisition reaps an acquisition whose retry budget is exhausted.
// For a consensus-driven acquisition it also tells the engine, so a node pinned
// in wrongLedger on an unacquirable ledger can drop to a recoverable resync
// rather than starving the ledger loop into a fatal watchdog abort (issue #985).
func (r *Router) failInboundAcquisition(il *inbound.Ledger) {
	r.failInboundAcquisitionWithSnapshot(il, il.Snapshot())
}

func (r *Router) failInboundAcquisitionWithSnapshot(il *inbound.Ledger, snapshot inbound.Snapshot) {
	hash := il.Hash()
	reason := il.Reason()
	if !r.fetchTracker.RemoveExpectedWithSnapshot(il, snapshot, false) {
		return
	}
	r.retireAcquisitionStore(context.TODO(), il)
	r.logger.Warn("inbound ledger acquisition failed",
		"seq", il.Seq(),
		"hash", fmt.Sprintf("%x", hash[:8]),
		"timeouts", il.Timeouts(),
	)
	if reason == inbound.ReasonConsensus {
		r.markFailedCatchupAcquisition(hash)
	}
	if reason == inbound.ReasonConsensus && r.engine != nil {
		r.engine.OnLedgerAcquireFailed(consensus.LedgerID(hash))
	}
	if reason == inbound.ReasonConsensus {
		r.armConsensusCatchup()
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
		LedgerHash: ledgerHash[:],
		Objects:    objs,
	}
	frame, err := message.EncodeFrame(req)
	if err != nil {
		r.logger.Debug("inbound ledger: encode by-hash request failed", "error", err)
		return
	}
	for _, peerID := range peers {
		if err := r.adaptor.SendPriorityToPeer(peerID, frame); err != nil {
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
	if err := r.flushAcquisitionStore(context.TODO(), il); err != nil {
		r.logger.Warn("inbound ledger: verified-node persistence failed", "error", err, "seq", il.Seq())
		if r.fetchTracker.DiscardExpected(il) {
			r.retireAcquisitionStore(context.TODO(), il)
		}
		return
	}
	r.completeInboundLedgerReady(il)
}

func (r *Router) completeInboundLedgerReady(il *inbound.Ledger) {
	h, stateMap, txMap, err := il.Result()
	if err != nil {
		r.logger.Warn("inbound ledger: failed to get result", "error", err)
		if r.fetchTracker.DiscardExpected(il) {
			r.retireAcquisitionStore(context.TODO(), il)
		}
		return
	}
	if r.adaptor == nil {
		if r.fetchTracker.DiscardExpected(il) {
			r.retireAcquisitionStore(context.TODO(), il)
		}
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		if r.fetchTracker.DiscardExpected(il) {
			r.retireAcquisitionStore(context.TODO(), il)
		}
		return
	}
	if err = r.promoteAcquisitionStore(context.TODO(), il); err != nil {
		r.logger.Warn("inbound ledger: failed to promote persistence scope", "error", err, "seq", il.Seq())
		if r.fetchTracker.DiscardExpected(il) {
			r.retireAcquisitionStore(context.TODO(), il)
		}
		return
	}
	if !r.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), true) {
		r.retireAcquisitionStore(context.TODO(), il)
		return
	}
	peerID := il.PeerID()

	// A history backfill is a store-only ingest below the closed tip: persist,
	// then advance the backward walk to its parent. Never touches operating mode
	// or the consensus engine.
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
	// A consensus acquisition two or more ledgers ahead is a catch-up jump: its
	// parent chain is absent and fresh ledgers close faster than a backward
	// parent chase converges, so adopt the tip directly, jumping the working
	// ledger forward onto the trusted-validation-preferred branch; skipped
	// ledgers backfill off the critical path via the ReasonHistory walk. Mirrors
	// rippled setFullLedger/checkAccept plus fetchForHistory. The published
	// validated pointer still only advances at quorum. Gap ≤ 1 and generic RPC
	// acquisitions keep the held-adoption seam so out-of-order arrivals cascade.
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
	// Closed advanced (gap-1 step or jump-adopt): re-arm the next single
	// acquisition toward the best target. On a stash closed didn't move, so the
	// parent chase makes progress and re-arming would only re-request the stash.
	if res.Stashed {
		r.armParentAcquisition(svc, res.ParentSeq, res.ParentHash, peerID)
	} else {
		r.armConsensusCatchup()
	}
}
