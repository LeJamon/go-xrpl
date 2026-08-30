package adaptor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
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

	svc := r.adaptor.LedgerService()
	fastLoadProvisional := svc != nil && svc.IsFastLoadProvisional()
	r.logger.Info("peer status change",
		"peer", msg.PeerID,
		"status", sc.NewStatus,
		"event", sc.NewEvent,
		"ledger_seq", sc.LedgerSeq,
		"needs_sync", r.adaptor.NeedsInitialSync(),
		"fast_load_provisional", fastLoadProvisional,
	)

	if sc.NewEvent == message.NodeEventLostSync {
		r.peersMu.Lock()
		delete(r.peerStates, msg.PeerID)
		delete(r.peerStatusCandidates, msg.PeerID)
		r.peersMu.Unlock()
		r.invalidateCatchupPeer(uint64(msg.PeerID))
		r.invalidateHistoryPeer(uint64(msg.PeerID))
		r.adaptor.UpdatePeerLCL(uint64(msg.PeerID), consensus.LedgerID{})
		return
	}

	if sc.LedgerSeq > 0 {
		var peerHash [32]byte
		if len(sc.LedgerHash) == 32 {
			copy(peerHash[:], sc.LedgerHash)
		}
		if peerHash == ([32]byte{}) {
			r.peersMu.Lock()
			delete(r.peerStates, msg.PeerID)
			delete(r.peerStatusCandidates, msg.PeerID)
			r.peersMu.Unlock()
			r.invalidateCatchupPeer(uint64(msg.PeerID))
			r.adaptor.UpdatePeerLCL(uint64(msg.PeerID), consensus.LedgerID{})
			return
		}
		var parentHash [32]byte
		haveParent := len(sc.LedgerHashPrevious) == 32
		if haveParent {
			copy(parentHash[:], sc.LedgerHashPrevious)
		}

		admitted := r.admitPeerStatus(peerStatusCandidate{
			peerLedgerState: peerLedgerState{
				LedgerSeq:  sc.LedgerSeq,
				LedgerHash: peerHash,
				parentHash: parentHash,
				haveParent: haveParent,
			},
			peerID: msg.PeerID,
		})
		if len(admitted) == 0 {
			return
		}
		for _, status := range admitted {
			r.adaptor.UpdatePeerLCL(uint64(status.peerID), consensus.LedgerID(status.LedgerHash))
		}
		for _, status := range admitted {
			r.reconcilePeerSeqHash(status.LedgerSeq)
		}

		// During network startup or fast-load confirmation, acquire the
		// peer-preferred ledger. Don't adopt with synthetic headers — wait for
		// real state data.
		if r.needsStartupLedgerConfirmation() && sc.LedgerSeq > 1 {
			if r.peerLedgerIsPreferred(peerHash) ||
				r.isValidationCatchupTarget(sc.LedgerSeq, peerHash) {
				r.ensureCatchupAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
			}
			return
		}

		// A node materially behind the peer-preferred network tip must stop
		// advertising Full before it starts catch-up. Remaining Full would let a
		// validator keep proposing and issuing full validations on its stale LCL.
		if r.adaptor.GetOperatingMode() == consensus.OpModeFull && sc.LedgerSeq > 1 {
			svc := r.adaptor.LedgerService()
			if svc != nil {
				ourSeq := svc.GetClosedLedgerIndex()
				if aheadByMoreThan(sc.LedgerSeq, ourSeq, 2) {
					if !r.peerLedgerIsPreferred(peerHash) {
						return
					}
					leftFull := false
					if lcl, err := r.adaptor.GetLastClosedLedger(); err == nil && lcl != nil &&
						r.adaptor.networkLedgerDiffers(lcl, consensus.OpModeFull) {
						r.adaptor.SetOperatingMode(consensus.OpModeConnected)
						leftFull = true
					}
					r.logger.Warn("behind network; catching up",
						"our_seq", ourSeq,
						"peer_seq", sc.LedgerSeq,
						"gap", sc.LedgerSeq-ourSeq,
						"left_full", leftFull,
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
					if r.peerLedgerIsPreferred(peerHash) {
						r.ensureCatchupAcquisition(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
					}
					return
				}
			}
		}

		r.checkBehind(sc.LedgerSeq, peerHash, uint64(msg.PeerID))
	}
}

func (r *Router) admitPeerStatus(status peerStatusCandidate) []peerStatusCandidate {
	if r.isValidationCatchupTarget(status.LedgerSeq, status.LedgerHash) ||
		r.peerStatusWithinAnchor(status.LedgerSeq) {
		r.peersMu.Lock()
		delete(r.peerStatusCandidates, status.peerID)
		r.peerStates[status.peerID] = &peerLedgerState{
			LedgerSeq:  status.LedgerSeq,
			LedgerHash: status.LedgerHash,
			parentHash: status.parentHash,
			haveParent: status.haveParent,
		}
		r.peersMu.Unlock()
		return []peerStatusCandidate{status}
	}

	r.peersMu.Lock()
	r.peerStatusCandidates[status.peerID] = status
	matching := make([]peerStatusCandidate, 0, 2)
	for _, candidate := range r.peerStatusCandidates {
		if candidate.LedgerSeq == status.LedgerSeq && candidate.LedgerHash == status.LedgerHash {
			matching = append(matching, candidate)
		}
	}
	if len(matching) < 2 {
		r.peersMu.Unlock()
		return nil
	}
	for _, candidate := range matching {
		delete(r.peerStatusCandidates, candidate.peerID)
		r.peerStates[candidate.peerID] = &peerLedgerState{
			LedgerSeq:  candidate.LedgerSeq,
			LedgerHash: candidate.LedgerHash,
			parentHash: candidate.parentHash,
			haveParent: candidate.haveParent,
		}
	}
	r.peersMu.Unlock()
	return matching
}

func (r *Router) peerLedgerIsPreferred(hash [32]byte) bool {
	closed, err := r.adaptor.GetLastClosedLedger()
	if err != nil || closed == nil {
		return false
	}
	preferred := r.adaptor.preferredLCL(closed, r.adaptor.GetOperatingMode())
	return preferred != closed.ID() && preferred == consensus.LedgerID(hash)
}

func (r *Router) needsStartupLedgerConfirmation() bool {
	svc := r.adaptor.LedgerService()
	return svc != nil && (svc.NeedsInitialSync() || svc.IsFastLoadProvisional())
}

// maxConcurrentCatchup bounds the hash-keyed current-ledger acquisition set.
// Separate target hashes share the NodeStore and remain independently useful
// when the preferred ledger moves during a long state walk.
const maxConcurrentCatchup = 3

// Gossip-driven acquisition leaves one slot available for the exact ledger
// requested by consensus wrong-ledger recovery.
const maxConcurrentSpeculativeCatchup = maxConcurrentCatchup - 1

// A provisional fast-loaded ledger has a cold in-memory full-below cache. A
// second full-state acquisition would duplicate the same durable SHAMap scan
// and compete for storage bandwidth. Keep one pinned full-state jump in flight;
// newer trusted targets remain queued and are armed when it completes.
const maxConcurrentProvisionalFullStateCatchup = 1

// maxForwardDeltaGap bounds the initial recovery strategy and peer ancestry
// hints. A healthy frozen-pivot replay is governed by measured progress instead
// of its absolute distance from the moving tip.
const maxForwardDeltaGap = 128

const catchupLinkageGracePeriod = 2 * time.Second

// seqHashRetain bounds the ancestry retained behind the local or trusted
// frontier. Peer gossip is admitted only through maxForwardDeltaGap ahead.
const seqHashRetain = 2048

func (r *Router) recordSeqHash(seq uint32, hash, parentHash [32]byte, haveParent bool) {
	r.recordSeqHashFrom(seq, hash, parentHash, haveParent, seqHashSourceQuorum)
}

func (r *Router) recordValidationSeqHash(seq uint32, hash [32]byte) {
	r.recordSeqHashFrom(seq, hash, [32]byte{}, false, seqHashSourceValidation)
}

func (r *Router) recordAcquiredSeqHash(seq uint32, hash, parentHash [32]byte) {
	r.recordSeqHashFrom(seq, hash, parentHash, true, seqHashSourceAcquired)
}

func (r *Router) recordPeerSeqHash(seq uint32, hash, parentHash [32]byte, haveParent bool) {
	r.recordSeqHashFrom(seq, hash, parentHash, haveParent, seqHashSourcePeer)
}

func (r *Router) reconcilePeerSeqHash(seq uint32) {
	type peerEvidence struct {
		peerID     uint64
		hash       [32]byte
		parentHash [32]byte
		haveParent bool
	}
	r.peersMu.RLock()
	peers := make([]peerEvidence, 0, len(r.peerStates))
	hashSupport := make(map[[32]byte]int)
	for peerID, state := range r.peerStates {
		if state == nil || state.LedgerSeq != seq {
			continue
		}
		peers = append(peers, peerEvidence{
			peerID:     uint64(peerID),
			hash:       state.LedgerHash,
			parentHash: state.parentHash,
			haveParent: state.haveParent,
		})
		hashSupport[state.LedgerHash]++
	}
	r.peersMu.RUnlock()
	if len(peers) == 0 {
		return
	}

	entry, known := r.lookupSeqHash(seq)
	chosenHash := entry.hash
	chosenPreferred := false
	if !known || entry.source < seqHashSourceValidation {
		chosenHash = [32]byte{}
		for _, peer := range peers {
			if r.peerLedgerIsPreferred(peer.hash) {
				chosenHash = peer.hash
				chosenPreferred = true
				break
			}
		}
		if chosenHash == ([32]byte{}) {
			best := 0
			for hash, count := range hashSupport {
				if count > best || (count == best && bytes.Compare(hash[:], chosenHash[:]) > 0) {
					chosenHash, best = hash, count
				}
			}
		}
	}
	if chosenHash == ([32]byte{}) {
		return
	}

	parents := make(map[[32]byte]int)
	var peerID uint64
	for _, peer := range peers {
		if peer.hash != chosenHash {
			continue
		}
		if peerID == 0 || peer.peerID < peerID {
			peerID = peer.peerID
		}
		if peer.haveParent {
			parents[peer.parentHash]++
		}
	}
	if peerID == 0 {
		return
	}
	parentHash, haveParent := r.preferredPeerParent(seq, parents)
	r.recordPeerSeqHash(seq, chosenHash, parentHash, haveParent)
	if !haveParent {
		r.seqHashMu.Lock()
		entry = r.seqHash[seq]
		if entry.hash == chosenHash && entry.parentFrom == seqHashSourcePeer {
			entry.parentHash = [32]byte{}
			entry.haveParent = false
			entry.parentFrom = seqHashSourcePeer
			r.seqHash[seq] = entry
		}
		if len(parents) > 1 && seq > 1 {
			parent := r.seqHash[seq-1]
			if parent.source == seqHashSourcePeer {
				delete(r.seqHash, seq-1)
			}
		}
		r.seqHashMu.Unlock()
	}

	r.catchupMu.Lock()
	if chosenPreferred && r.catchup.source == catchupSourcePeer && r.catchup.seq == seq {
		r.catchup.hash = chosenHash
		r.catchup.peerID = peerID
	}
	r.catchupMu.Unlock()
}

func (r *Router) preferredPeerParent(seq uint32, support map[[32]byte]int) ([32]byte, bool) {
	if seq > 1 && r.adaptor != nil {
		if svc := r.adaptor.LedgerService(); svc != nil {
			if parent, err := svc.GetLedgerBySequence(seq - 1); err == nil && parent != nil {
				if support[parent.Hash()] > 0 {
					return parent.Hash(), true
				}
			}
		}
	}
	if len(support) == 1 {
		for parent := range support {
			return parent, true
		}
	}

	var chosen [32]byte
	best, second := 0, 0
	for parent, count := range support {
		if count > best || (count == best && bytes.Compare(parent[:], chosen[:]) > 0) {
			second = best
			best = count
			chosen = parent
		} else if count > second {
			second = count
		}
	}
	return chosen, best >= 2 && best > second
}

func (r *Router) recordSeqHashFrom(
	seq uint32,
	hash, parentHash [32]byte,
	haveParent bool,
	source seqHashSource,
) {
	if seq == 0 || hash == ([32]byte{}) {
		return
	}
	localAnchor := r.localSeqHashAnchor()

	r.seqHashMu.Lock()
	defer r.seqHashMu.Unlock()

	if localAnchor > r.seqHashAnchor {
		r.seqHashAnchor = localAnchor
	}
	if source >= seqHashSourceValidation && seq > r.seqHashAnchor {
		r.seqHashAnchor = seq
	}
	r.pruneSeqHashLocked()
	if source < seqHashSourceValidation && !seqHashWithinAnchor(seq, r.seqHashAnchor) {
		return
	}

	e := r.seqHash[seq]
	if e.hash != ([32]byte{}) && e.hash != hash {
		if source < e.source ||
			(source == e.source && source != seqHashSourcePeer && source != seqHashSourceQuorum) {
			return
		}
		e = ledgerHashEntry{}
	}
	e.hash = hash
	if source > e.source {
		e.source = source
	}
	if haveParent && (!e.haveParent || source > e.parentFrom ||
		(source == seqHashSourcePeer && e.parentFrom == seqHashSourcePeer)) {
		e.parentHash = parentHash
		e.haveParent = true
		e.parentFrom = source
	}
	r.seqHash[seq] = e

	// The parent linkage also names seq-1's own hash; seed it (parentless) so a
	// same-branch check against our closed seq succeeds without a direct
	// validation for it.
	if haveParent && seq > 1 {
		pe := r.seqHash[seq-1]
		if pe.hash == ([32]byte{}) || pe.hash == parentHash || source > pe.source ||
			(source == seqHashSourcePeer && pe.source == seqHashSourcePeer) {
			if pe.hash != ([32]byte{}) && pe.hash != parentHash {
				pe = ledgerHashEntry{}
			}
			pe.hash = parentHash
			if source > pe.source {
				pe.source = source
			}
			r.seqHash[seq-1] = pe
		}
	}

	r.pruneSeqHashLocked()
}

func (r *Router) localSeqHashAnchor() uint32 {
	if r.adaptor == nil {
		return 0
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return 0
	}
	anchor := svc.GetClosedLedgerIndex()
	if validated := svc.GetValidatedLedgerIndex(); validated > anchor {
		anchor = validated
	}
	return anchor
}

func (r *Router) peerStatusWithinAnchor(seq uint32) bool {
	localAnchor := r.localSeqHashAnchor()
	r.seqHashMu.Lock()
	defer r.seqHashMu.Unlock()
	if localAnchor > r.seqHashAnchor {
		r.seqHashAnchor = localAnchor
	}
	r.pruneSeqHashLocked()
	anchor := r.seqHashAnchor
	if anchor == 0 {
		return false
	}
	if seq >= anchor {
		return seq-anchor <= maxForwardDeltaGap
	}
	return anchor-seq <= maxForwardDeltaGap
}

func seqHashWithinAnchor(seq, anchor uint32) bool {
	if anchor == 0 {
		return false
	}
	floor := uint32(0)
	if anchor > seqHashRetain {
		floor = anchor - seqHashRetain
	}
	ceiling := anchor + maxForwardDeltaGap
	if ceiling < anchor {
		ceiling = ^uint32(0)
	}
	return seq >= floor && seq <= ceiling
}

func (r *Router) pruneSeqHashLocked() {
	for s := range r.seqHash {
		if !seqHashWithinAnchor(s, r.seqHashAnchor) {
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

func (r *Router) lookupSeqForHash(hash [32]byte) (uint32, bool) {
	r.seqHashMu.Lock()
	defer r.seqHashMu.Unlock()
	for seq, entry := range r.seqHash {
		if entry.hash == hash {
			return seq, true
		}
	}
	return 0, false
}

// recoveryAnchorReachesTarget proves that anchor is on the recorded parent
// chain of target. Missing linkage is treated as unknown rather than ancestry.
func (r *Router) recoveryAnchorReachesTarget(anchorSeq uint32, anchorHash, targetHash [32]byte) bool {
	targetSeq, ok := r.lookupSeqForHash(targetHash)
	if !ok || anchorSeq > targetSeq {
		return false
	}
	current := targetHash
	for seq := targetSeq; seq > anchorSeq; seq-- {
		entry, found := r.lookupSeqHash(seq)
		if !found || entry.hash != current || !entry.haveParent {
			return false
		}
		current = entry.parentHash
	}
	return current == anchorHash
}

// catchupInFlight counts active consensus-reason acquisitions across both the
// legacy fetchTracker and the replay-delta replayer. Generic (RPC-driven)
// acquisitions are excluded so an arbitrary fetch never consumes a catch-up slot.
func (r *Router) catchupInFlight() int {
	return r.fetchTracker.CountReason(inbound.ReasonConsensus) + r.replayer.Count()
}

func (r *Router) protectedCatchupInFlight() int {
	active := r.replayer.Count()
	for _, candidate := range r.fetchTracker.Active() {
		if candidate.Reason() == inbound.ReasonConsensus && !candidate.TransactionOnly() {
			active++
		}
	}
	return active
}

// recordCatchupTarget raises the single consensus catch-up target or refreshes
// its preferred peer when another peer advertises the same ledger.
func (r *Router) recordCatchupTarget(seq uint32, hash [32]byte, peerID uint64) {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	if r.catchup.source != catchupSourcePeer {
		if r.catchup.hash == hash {
			r.catchup.peerID = peerID
		}
		return
	}
	if seq > r.catchup.seq {
		r.catchup = catchupTarget{seq: seq, hash: hash, peerID: peerID}
	} else if seq == r.catchup.seq {
		r.catchup = catchupTarget{seq: seq, hash: hash, peerID: peerID}
	}
}

func (r *Router) recordValidationCatchupTarget(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	source catchupTargetSource,
) {
	r.catchupMu.Lock()
	previous := r.catchup
	// Trusted evidence replaces a peer-derived target at any sequence. Once
	// validation-driven, only a higher sequence or same-sequence quorum can
	// move the frontier.
	if r.catchup.source == catchupSourcePeer ||
		(source == catchupSourceQuorum && seq == r.catchup.seq) ||
		seq > r.catchup.seq {
		if peerID == 0 && previous.seq == seq && previous.hash == hash {
			peerID = previous.peerID
		}
		r.catchup = catchupTarget{seq: seq, hash: hash, peerID: peerID, source: source}
	} else if seq == r.catchup.seq && hash == r.catchup.hash {
		if peerID != 0 {
			r.catchup.peerID = peerID
		}
	}
	if previous.hash != ([32]byte{}) && previous.hash != r.catchup.hash &&
		previous.source != catchupSourcePeer && r.catchup.source != catchupSourcePeer {
		r.targetSuperseded.Add(1)
	}
	target := r.catchup
	r.catchupMu.Unlock()

	if target.source != catchupSourcePeer {
		r.promotePeerStatusCandidates(target.seq, target.hash)
	}
}

func (r *Router) promotePeerStatusCandidates(seq uint32, hash [32]byte) {
	r.peersMu.Lock()
	matching := make([]peerStatusCandidate, 0, 1)
	for peerID, candidate := range r.peerStatusCandidates {
		if candidate.LedgerSeq != seq || candidate.LedgerHash != hash {
			continue
		}
		matching = append(matching, candidate)
		delete(r.peerStatusCandidates, peerID)
		r.peerStates[peerID] = &peerLedgerState{
			LedgerSeq:  seq,
			LedgerHash: hash,
			parentHash: candidate.parentHash,
			haveParent: candidate.haveParent,
		}
	}
	r.peersMu.Unlock()

	for _, candidate := range matching {
		r.adaptor.UpdatePeerLCL(uint64(candidate.peerID), consensus.LedgerID(hash))
	}
	if len(matching) > 0 {
		r.reconcilePeerSeqHash(seq)
		r.recordCatchupTarget(seq, hash, uint64(matching[0].peerID))
	}
}

func (r *Router) isValidationCatchupTarget(seq uint32, hash [32]byte) bool {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	return r.catchup.source != catchupSourcePeer &&
		r.catchup.seq == seq &&
		r.catchup.hash == hash
}

func (r *Router) invalidateCatchupPeer(peerID uint64) {
	type peerTip struct {
		peerID uint64
		seq    uint32
		hash   [32]byte
	}
	r.peersMu.RLock()
	peers := make([]peerTip, 0, len(r.peerStates))
	for id, state := range r.peerStates {
		peers = append(peers, peerTip{peerID: uint64(id), seq: state.LedgerSeq, hash: state.LedgerHash})
	}
	r.peersMu.RUnlock()

	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	if r.catchup.peerID != peerID {
		return
	}
	if r.catchup.source != catchupSourcePeer {
		r.catchup.peerID = 0
		return
	}
	for _, peer := range peers {
		if peer.seq == r.catchup.seq && peer.hash == r.catchup.hash {
			r.catchup.peerID = peer.peerID
			return
		}
	}
	r.catchup = catchupTarget{}
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
	for {
		r.acquisitionMu.Lock()
		recovery := r.consensusRecovery
		if recovery.stepHash != ([32]byte{}) && r.isAcquiring(recovery.stepHash) {
			r.acquisitionMu.Unlock()
			return true
		}
		if recovery.targetHash != ([32]byte{}) && r.isAcquiring(recovery.targetHash) {
			r.consensusRecovery.stepHash = recovery.targetHash
			r.acquisitionMu.Unlock()
			return true
		}
		if recovery.stepHash != recovery.targetHash {
			r.consensusRecovery.stepHash = [32]byte{}
			recovery.stepHash = [32]byte{}
		}
		r.acquisitionMu.Unlock()

		hash := recovery.targetHash
		if hash == ([32]byte{}) {
			return false
		}
		svc := r.adaptor.LedgerService()
		if svc != nil {
			if held, err := svc.GetLedgerByHash(hash); err == nil && held != nil {
				if svc.NeedsInitialSync() || svc.IsFastLoadProvisional() {
					accepted, rearm := r.tryInitialLedgerSwitch(held.Sequence(), held.Hash())
					if accepted || !rearm {
						return true
					}
					return false
				}
				accepted, rearm := r.tryConsensusLedgerSwitch(held.Sequence(), held.Hash())
				if accepted || !rearm {
					return true
				}
				return false
			}
		}
		seq, known := r.lookupSeqForHash(hash)
		var nextSeq uint32
		var nextHash [32]byte
		var parent *ledger.Ledger
		var replay bool
		var discardAnchor bool
		if known {
			nextSeq, nextHash, parent, replay, discardAnchor = r.recoveryForwardStep(svc, seq, hash, recovery)
		}
		if discardAnchor {
			r.acquisitionMu.Lock()
			if r.consensusRecovery.targetHash == hash &&
				r.consensusRecovery.anchorHash == recovery.anchorHash &&
				r.consensusRecovery.anchorSeq == recovery.anchorSeq {
				r.consensusRecovery.anchorHash = [32]byte{}
				r.consensusRecovery.anchorSeq = 0
				recovery.anchorHash = [32]byte{}
				recovery.anchorSeq = 0
			}
			r.acquisitionMu.Unlock()
		}
		if replay && parent != nil {
			if _, replayPeerFound := r.resolveReplayPeer(parent.Sequence()+1, 0); !replayPeerFound &&
				r.tryArmStandardReplayPipeline(svc, parent, seq, hash, 0) {
				return true
			}
		}

		r.acquisitionMu.Lock()
		if r.consensusRecovery.targetHash != hash {
			r.acquisitionMu.Unlock()
			continue
		}
		if r.consensusRecovery.stepHash != ([32]byte{}) && r.isAcquiring(r.consensusRecovery.stepHash) {
			r.acquisitionMu.Unlock()
			return true
		}
		if r.isAcquiring(hash) {
			r.consensusRecovery.stepHash = hash
			r.acquisitionMu.Unlock()
			return true
		}
		acquisitionSeq := seq
		acquisitionHash := hash
		if replay {
			acquisitionSeq = nextSeq
			acquisitionHash = nextHash
		}
		if r.isAcquiring(acquisitionHash) {
			r.consensusRecovery.stepHash = acquisitionHash
			r.acquisitionMu.Unlock()
			return true
		}
		if !r.canAdmitCatchupLocked(acquisitionHash, maxConcurrentCatchup) {
			r.acquisitionMu.Unlock()
			return true
		}
		if replay {
			peer, found := r.resolveReplayPeer(nextSeq, 0)
			if found && r.startReplayDeltaAcquisition(nextSeq, nextHash, peer, parent) == nil {
				r.consensusRecovery.stepHash = nextHash
				r.acquisitionMu.Unlock()
				return true
			}
		}

		peerID, _ := r.selectAcquisitionPeer(acquisitionSeq)
		if replay && parent != nil {
			r.startLedgerReplayAcquisitionLegacyLocked(acquisitionSeq, acquisitionHash, peerID)
		} else {
			r.startLedgerAcquisitionLegacyLocked(acquisitionSeq, acquisitionHash, peerID)
		}
		started := r.fetchTracker.Find(acquisitionHash) != nil
		if started {
			r.consensusRecovery.stepHash = acquisitionHash
		}
		r.acquisitionMu.Unlock()
		return started
	}
}

func (r *Router) resolveReplayPeer(seq uint32, preferred uint64) (uint64, bool) {
	if peer, ok := r.resolveAcquisitionPeer(seq, preferred); ok && r.acquisition.PeerSupportsReplay(peer) {
		return peer, true
	}
	peers := r.acquisition.ReplayCapablePeersExcluding(nil, 1)
	if len(peers) == 0 {
		return 0, false
	}
	return peers[0], true
}

func (r *Router) recoveryForwardStep(
	svc *service.Service,
	targetSeq uint32,
	targetHash [32]byte,
	recovery consensusRecovery,
) (uint32, [32]byte, *ledger.Ledger, bool, bool) {
	if svc == nil {
		return 0, [32]byte{}, nil, false, recovery.anchorHash != ([32]byte{})
	}
	parent := svc.GetClosedLedger()
	if parent == nil || targetSeq <= parent.Sequence() {
		return 0, [32]byte{}, nil, false, recovery.anchorHash != ([32]byte{})
	}

	maxGap := uint32(maxForwardDeltaGap)
	discardAnchor := false
	if recovery.anchorHash != ([32]byte{}) {
		anchor, err := svc.GetLedgerByHash(recovery.anchorHash)
		anchorUsable := recovery.anchorSeq > parent.Sequence() && recovery.anchorSeq < targetSeq &&
			targetSeq-recovery.anchorSeq <= seqHashRetain &&
			r.recoveryAnchorReachesTarget(recovery.anchorSeq, recovery.anchorHash, targetHash) &&
			err == nil && anchor != nil && anchor.Sequence() == recovery.anchorSeq &&
			anchor.Hash() == recovery.anchorHash
		if anchorUsable {
			parent = anchor
			maxGap = seqHashRetain
		} else {
			discardAnchor = true
		}
	}
	if targetSeq-parent.Sequence() > maxGap {
		return 0, [32]byte{}, nil, false, discardAnchor
	}

	for parent.Sequence() < targetSeq {
		nextSeq := parent.Sequence() + 1
		entry, known := r.lookupSeqHash(nextSeq)
		if !known || entry.hash == ([32]byte{}) {
			return 0, [32]byte{}, nil, false, discardAnchor
		}
		if nextSeq == targetSeq && entry.hash != targetHash {
			return 0, [32]byte{}, nil, false, discardAnchor
		}
		parentHash := parent.Hash()
		if entry.haveParent {
			if entry.parentHash != parentHash {
				return 0, [32]byte{}, nil, false, discardAnchor
			}
		} else if current, ok := r.lookupSeqHash(parent.Sequence()); !ok || current.hash != parentHash {
			return 0, [32]byte{}, nil, false, discardAnchor
		}

		next, err := svc.GetLedgerByHash(entry.hash)
		if err != nil || next == nil {
			return nextSeq, entry.hash, parent, true, discardAnchor
		}
		if next.Sequence() != nextSeq || next.ParentHash() != parentHash {
			return 0, [32]byte{}, nil, false, discardAnchor
		}
		parent = next
	}
	return 0, [32]byte{}, nil, false, discardAnchor
}

func (r *Router) armCatchupTowardTargetWithPeer(peerHint uint64) {
	if r.adaptor == nil {
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	target := r.credibleCatchupFrontier()
	if peerHint == 0 && target.source == catchupSourceQuorum {
		peerHint = target.peerID
	}
	tSeq, tHash := target.seq, target.hash
	if tSeq == 0 {
		return
	}
	if r.continueFrozenPivotRecovery(tSeq, tHash, peerHint) {
		return
	}
	r.reconcileStandardReplayTarget(tSeq, tHash)
	closed := svc.GetClosedLedgerIndex()
	if tSeq <= closed {
		if target.source != catchupSourceQuorum {
			return
		}
		if held, err := svc.GetLedgerByHash(tHash); err == nil && held != nil {
			return
		}
		if r.protectedCatchupInFlight() >= maxConcurrentSpeculativeCatchup {
			return
		}
		peer, found := r.resolveAcquisitionPeer(tSeq, peerHint)
		if !found || r.isBuildingLedger(tSeq) {
			return
		}
		r.startLedgerAcquisition(tSeq, tHash, peer)
		return
	}
	if target.source == catchupSourcePeer &&
		!svc.NeedsInitialSync() && !svc.IsFastLoadProvisional() &&
		!aheadByMoreThan(tSeq, closed, 1) {
		return
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger != nil && tSeq-closed <= maxForwardDeltaGap &&
		r.recoveryAnchorReachesTarget(closedLedger.Sequence(), closedLedger.Hash(), tHash) {
		if _, replayPeerFound := r.resolveReplayPeer(closedLedger.Sequence()+1, peerHint); !replayPeerFound &&
			r.tryArmStandardReplayPipeline(svc, closedLedger, tSeq, tHash, peerHint) {
			return
		}
	}
	if r.protectedCatchupInFlight() >= maxConcurrentSpeculativeCatchup {
		return
	}

	if seq, hash, ok := r.forwardDeltaStep(svc, closed, tSeq); ok {
		peer, found := r.resolveAcquisitionPeer(seq, peerHint)
		if !found {
			return
		}
		if r.isBuildingLedger(seq) {
			return
		}
		r.startLedgerAcquisition(seq, hash, peer)
		return
	}
	if svc.IsFastLoadProvisional() && r.forwardLinkagePending(svc, closed, tSeq) {
		if r.withinCatchupLinkageGrace(closed, tSeq, tHash, time.Now()) {
			return
		}
	}
	peer, found := r.resolveAcquisitionPeer(tSeq, peerHint)
	if !found {
		return
	}
	if r.isBuildingLedger(tSeq) {
		return
	}
	r.beginFrozenPivotRecovery(tSeq, tHash, peer)
}

func (r *Router) withinCatchupLinkageGrace(
	closed, seq uint32,
	hash [32]byte,
	now time.Time,
) bool {
	r.catchupMu.Lock()
	defer r.catchupMu.Unlock()
	if r.linkageWait.closed != closed || r.linkageWait.since.IsZero() {
		r.linkageWait = catchupLinkageWait{
			closed: closed,
			seq:    seq,
			hash:   hash,
			since:  now,
		}
		return true
	}
	r.linkageWait.seq = seq
	r.linkageWait.hash = hash
	return now.Sub(r.linkageWait.since) < catchupLinkageGracePeriod
}

func (r *Router) forwardLinkagePending(svc *service.Service, closed, tipSeq uint32) bool {
	if tipSeq <= closed+1 || tipSeq-closed > maxForwardDeltaGap {
		return false
	}
	next, known := r.lookupSeqHash(closed + 1)
	if !known || next.hash == ([32]byte{}) {
		tip, tipKnown := r.lookupSeqHash(tipSeq)
		return !tipKnown || !tip.haveParent
	}
	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		return true
	}
	if next.haveParent {
		return false
	}
	current, known := r.lookupSeqHash(closed)
	return !known || current.hash == ([32]byte{})
}

// forwardDeltaStep returns the (seq, hash) for a forward one-ledger step
// against our held closed ledger when all hold, else ok=false (deferring to
// jump-adopt):
//
//   - gap to tip within maxForwardDeltaGap;
//   - a known network hash for closed+1; and
//   - closed+1 descends from our closed ledger (same branch) — either closed+1's
//     recorded parentHash equals our closed hash, or the recorded hash for our
//     closed seq equals it. A provisional fast load may also probe a parentless
//     closed+1 hash; replay verification rejects it before apply if its header
//     names a different parent.
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
	} else if svc.IsFastLoadProvisional() {
		sameBranch = true
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
	r.ensureCatchupAcquisitionWithPriority(seq, hash, peerID, catchupSourcePeer)
}

// ensureValidationCatchupAcquisition acquires each trusted-validation ledger
// without allowing a lagging validator to lower the preferred catch-up frontier.
func (r *Router) ensureValidationCatchupAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	r.ensureCatchupAcquisitionWithPriority(seq, hash, peerID, catchupSourceValidation)
}

func (r *Router) ensureCatchupAcquisitionWithPriority(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	source catchupTargetSource,
) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	if seq == 0 || seq <= svc.GetClosedLedgerIndex() {
		return
	}
	if held, err := svc.GetLedgerByHash(hash); err == nil && held != nil {
		return
	}
	peerID, _ = r.resolveAcquisitionPeer(seq, peerID)
	if source == catchupSourcePeer {
		r.recordCatchupTarget(seq, hash, peerID)
	} else {
		r.recordValidationCatchupTarget(seq, hash, peerID, source)
	}
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
// tx blob). When the peer does not support that extension, standard
// mtGET_LEDGER fetches only the header + transaction SHAMap and the same local
// replay verifier derives the child state. A full header+state walk is reserved
// for targets whose parent is not held locally.
//
// This is currently the only driver of startReplayDeltaAcquisition: it
// handles a single target ledger per call. The Replayer coordinator
// supports concurrent acquisitions across many hashes, but the policy
// layer that walks a range (e.g., backward from a peer's tip via
// ParentHash) is a follow-up item.
func (r *Router) startLedgerAcquisition(seq uint32, hash [32]byte, peerID uint64) bool {
	if seq != 0 && r.belowFloor(seq) {
		return false
	}
	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()
	if step := r.consensusRecovery.stepHash; step != ([32]byte{}) && step != hash && r.isAcquiring(step) {
		return false
	}
	if !r.canAdmitCatchupLocked(hash, maxConcurrentSpeculativeCatchup) {
		return false
	}
	r.startLedgerAcquisitionLocked(seq, hash, peerID)
	return r.isAcquiring(hash)
}

// canAdmitCatchupLocked atomically combines hash deduplication with capacity
// admission. Gossip and validation paths use the speculative limit so the exact
// WrongLedger target can always claim the final slot.
func (r *Router) canAdmitCatchupLocked(hash [32]byte, limit int) bool {
	if r.isAcquiring(hash) {
		return true
	}
	return r.protectedCatchupInFlight() < limit
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
	if parent != nil && r.acquisition.PeerSupportsReplay(peerID) {
		if err := r.startReplayDeltaAcquisition(seq, hash, peerID, parent); err == nil {
			return
		}
	}
	if parent != nil {
		r.startLedgerReplayAcquisitionLegacyLocked(seq, hash, peerID)
		return
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
	if err := r.acquisition.RequestReplayDelta(peerID, hash); err != nil {
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

// Caller holds acquisitionMu.
func (r *Router) startLedgerReplayAcquisitionLegacyLocked(seq uint32, hash [32]byte, peerID uint64) (*inbound.Ledger, bool) {
	if seq != 0 && r.belowFloor(seq) {
		return nil, false
	}
	if r.replayer.Has(hash) {
		return nil, false
	}
	if svc := r.adaptor.LedgerService(); svc != nil {
		if l, err := svc.GetLedgerByHash(hash); err == nil && l != nil {
			return nil, false
		}
	}

	opts := append(r.acquisitionOpts(), inbound.WithTransactionOnly())
	il, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.New(hash, seq, peerID, r.logger, opts...)
	})
	if !created {
		return il, false
	}

	r.logger.Info("starting ledger replay acquisition (standard tx-only)",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)

	r.seedAcquisitionPeers(il)
	requested := false
	for _, candidate := range il.Peers() {
		if r.requestLedgerBaseFromPeer(il, candidate, "failed to request replay ledger base from peer") {
			requested = true
		}
	}
	if !requested {
		r.requestLedgerBase(il, 0, "failed to request replay ledger base from peer")
	}
	return il, true
}

func (r *Router) startLedgerAcquisitionLegacyLocked(seq uint32, hash [32]byte, peerID uint64) {
	if r.catchupRetryBlocked(hash, time.Now()) {
		return
	}
	if seq != 0 && r.belowFloor(seq) {
		return
	}
	// Safety net: if a replay-delta for the same hash is still
	// registered, don't start a legacy on top of it — one path is
	// always enough.
	if r.replayer.Has(hash) {
		return
	}
	if !r.canAdmitProvisionalFullStateLocked(hash) {
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

// Caller holds acquisitionMu.
func (r *Router) canAdmitProvisionalFullStateLocked(hash [32]byte) bool {
	svc := r.adaptor.LedgerService()
	if svc == nil || !svc.IsFastLoadProvisional() {
		return true
	}
	if existing := r.fetchTracker.Find(hash); existing != nil {
		return true
	}
	active := 0
	for _, candidate := range r.fetchTracker.Active() {
		if candidate.Reason() == inbound.ReasonConsensus && !candidate.TransactionOnly() {
			active++
		}
	}
	return active < maxConcurrentProvisionalFullStateCatchup
}

func (r *Router) fallbackReplayAcquisition(seq uint32, hash [32]byte, peerID uint64) {
	r.fallbackReplayAcquisitionForTarget(seq, hash, peerID, standardReplayTarget{})
}

func (r *Router) fallbackStandardReplayAcquisition(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	expected standardReplayTarget,
) {
	r.fallbackReplayAcquisitionForTarget(seq, hash, peerID, expected)
}

func (r *Router) fallbackReplayAcquisitionForTarget(
	seq uint32,
	hash [32]byte,
	peerID uint64,
	expected standardReplayTarget,
) {
	r.acquisitionMu.Lock()

	target := r.consensusRecovery.targetHash
	if expected.hash != ([32]byte{}) {
		if target != ([32]byte{}) {
			if target != expected.hash {
				r.acquisitionMu.Unlock()
				return
			}
		} else {
			r.catchupMu.Lock()
			current := r.catchup.seq == expected.seq && r.catchup.hash == expected.hash
			r.catchupMu.Unlock()
			if !current {
				r.acquisitionMu.Unlock()
				return
			}
		}
	}
	if target != ([32]byte{}) && r.consensusRecovery.stepHash != hash && target != hash {
		r.acquisitionMu.Unlock()
		return
	}
	if target != ([32]byte{}) && target != hash {
		targetSeq, known := r.lookupSeqForHash(target)
		if !known {
			r.consensusRecovery.stepHash = [32]byte{}
			r.acquisitionMu.Unlock()
			r.armConsensusCatchup()
			return
		}
		nextSeq, nextHash, _, replay, _ := r.recoveryForwardStep(
			r.adaptor.LedgerService(),
			targetSeq,
			target,
			r.consensusRecovery,
		)
		if !replay || nextSeq != seq || nextHash != hash {
			r.consensusRecovery.stepHash = [32]byte{}
			r.acquisitionMu.Unlock()
			r.armConsensusCatchup()
			return
		}
	}
	if !r.canAdmitCatchupLocked(hash, maxConcurrentCatchup) {
		r.acquisitionMu.Unlock()
		return
	}
	r.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
	if target != ([32]byte{}) && r.fetchTracker.Find(hash) != nil {
		r.consensusRecovery.stepHash = hash
	}
	r.acquisitionMu.Unlock()
}

func (r *Router) requestConsensusLedger(id consensus.LedgerID) error {
	hash := [32]byte(id)
	if hash == ([32]byte{}) {
		return nil
	}

	r.acquisitionMu.Lock()
	r.consensusRecovery.targetHash = hash
	recovery := r.consensusRecovery
	step := r.consensusRecovery.stepHash
	pipelineStep := r.standardReplayOwnsLocked(step)
	if step != ([32]byte{}) && r.isAcquiring(step) && !pipelineStep {
		r.acquisitionMu.Unlock()
		return nil
	}
	pipelineTarget := r.standardReplayOwnsLocked(hash)
	if r.isAcquiring(hash) && !pipelineTarget {
		r.consensusRecovery.stepHash = hash
		r.acquisitionMu.Unlock()
		return nil
	}
	if !pipelineTarget {
		r.consensusRecovery.stepHash = [32]byte{}
	}
	r.acquisitionMu.Unlock()
	seq, _ := r.lookupSeqForHash(hash)
	if r.continueFrozenPivotRecovery(seq, hash, 0) {
		return nil
	}
	if svc := r.adaptor.LedgerService(); svc != nil && seq > svc.GetClosedLedgerIndex() {
		held, _ := svc.GetLedgerByHash(hash)
		provenReplay := held != nil && held.Sequence() == seq && held.Hash() == hash
		_, _, _, forwardReplay, discardAnchor := r.recoveryForwardStep(svc, seq, hash, recovery)
		provenReplay = provenReplay || forwardReplay
		if discardAnchor {
			r.acquisitionMu.Lock()
			if r.consensusRecovery.anchorSeq == recovery.anchorSeq &&
				r.consensusRecovery.anchorHash == recovery.anchorHash {
				r.consensusRecovery.anchorSeq = 0
				r.consensusRecovery.anchorHash = [32]byte{}
			}
			r.acquisitionMu.Unlock()
		}
		if !provenReplay {
			peerID, found := r.resolveAcquisitionPeer(seq, 0)
			if found && r.beginFrozenPivotRecovery(seq, hash, peerID) {
				return nil
			}
		}
	}
	r.reconcileStandardReplayTarget(seq, hash)
	r.armConsensusCatchup()
	return nil
}

const acquisitionPeerStart = 5

func (r *Router) seedAcquisitionPeers(il *inbound.Ledger) {
	peers := il.Peers()
	remaining := acquisitionPeerStart - len(peers)
	if remaining <= 0 {
		return
	}
	for _, peerID := range r.acquisition.SelectLedgerPeers(il.Hash(), il.Seq(), peers, remaining) {
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
	err := r.acquisition.RequestLedgerBaseFromPeer(peerID, il.Hash(), il.Seq(), il.Timeouts() > 0)
	if err == nil {
		return true
	}
	r.logger.Warn(logMessage, "error", err, "peer", peerID)
	if !isAcquisitionDisconnectError(err) {
		return true
	}
	il.RemovePeer(peerID)
	r.HandlePeerDisconnect(peermanagement.PeerID(peerID))
	return false
}

func isAcquisitionDisconnectError(err error) bool {
	return errors.Is(err, peermanagement.ErrPeerNotFound) ||
		errors.Is(err, peermanagement.ErrConnectionClosed)
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

func (r *Router) onLedgerSwitched(seq uint32, _ [32]byte, parentHash [32]byte, historyFloor uint32) {
	if seq == 0 {
		return
	}
	r.startHistoryBackfill(seq-1, parentHash, 0, historyFloor)
}

func (r *Router) onLedgerFullyValidated(seq uint32, hash [32]byte) {
	r.recordSeqHash(seq, hash, [32]byte{}, false)

	removed := make(map[[32]byte]struct{})
	var legacy []*inbound.Ledger

	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	for _, candidate := range r.fetchTracker.Active() {
		if candidate.Reason() != inbound.ReasonConsensus ||
			candidate.Seq() != seq || candidate.Hash() == hash {
			continue
		}
		if r.fetchTracker.DiscardExpected(candidate) {
			legacy = append(legacy, candidate)
			removed[candidate.Hash()] = struct{}{}
		}
	}
	for _, replayHash := range r.replayer.AbandonOtherAtSequence(seq, hash) {
		removed[replayHash] = struct{}{}
	}
	pipelineConflict := r.standardReplay.active &&
		((seq == r.standardReplay.anchorSeq && hash != r.standardReplay.anchorHash) ||
			(seq == r.standardReplay.targetSeq && hash != r.standardReplay.targetHash))
	if entry := r.standardReplay.entries[seq]; entry != nil && entry.hash != hash {
		pipelineConflict = true
	}
	var pipelineRetirement standardReplayRetirement
	if pipelineConflict {
		for _, pipelineEntry := range r.standardReplay.entries {
			removed[pipelineEntry.hash] = struct{}{}
		}
		pipelineRetirement = r.cancelStandardReplayPipelineLocked()
		legacy = append(legacy, pipelineRetirement.ledgers...)
		pipelineRetirement.ledgers = nil
	}
	if victim := r.obsoleteCatchupVictimLocked(seq); victim != nil && r.fetchTracker.DiscardExpected(victim) {
		legacy = append(legacy, victim)
		removed[victim.Hash()] = struct{}{}
	}
	if _, ok := removed[r.consensusRecovery.stepHash]; ok {
		r.consensusRecovery.stepHash = [32]byte{}
	}
	if _, ok := removed[r.consensusRecovery.targetHash]; ok {
		r.consensusRecovery = consensusRecovery{}
	}
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()

	r.recordValidationCatchupTarget(seq, hash, 0, catchupSourceQuorum)

	r.retireLegacyAcquisitions(legacy)
	r.retireStandardReplay(pipelineRetirement)
	if len(removed) > 0 {
		r.logger.Info("canceled acquisitions superseded by trusted validation quorum",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"canceled", len(removed),
		)
	}
	r.armValidatedLedgerAcquisition(seq, hash)
}

func (r *Router) obsoleteCatchupVictimLocked(targetSeq uint32) *inbound.Ledger {
	if r.protectedCatchupInFlight() < maxConcurrentSpeculativeCatchup {
		return nil
	}
	var victim *inbound.Ledger
	for _, candidate := range r.fetchTracker.Active() {
		seq := candidate.Seq()
		if candidate.Reason() != inbound.ReasonConsensus || candidate.TransactionOnly() ||
			seq == 0 || seq >= targetSeq || candidate.Hash() == r.consensusRecovery.targetHash ||
			candidate.Hash() == r.consensusRecovery.stepHash {
			continue
		}
		consecutive := candidate.ConsecutiveTimeouts()
		if consecutive < 2 && targetSeq-seq <= maxForwardDeltaGap {
			continue
		}
		if victim == nil || seq < victim.Seq() ||
			(seq == victim.Seq() && consecutive > victim.ConsecutiveTimeouts()) ||
			(seq == victim.Seq() && consecutive == victim.ConsecutiveTimeouts() &&
				hashLess(candidate.Hash(), victim.Hash())) {
			victim = candidate
		}
	}
	return victim
}

func hashLess(left, right [32]byte) bool {
	return bytes.Compare(left[:], right[:]) < 0
}

func (r *Router) completeHistoryBackfill(seq uint32, hash, parentHash [32]byte, peerID uint64) {
	if seq == 0 {
		return
	}
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	if r.history.seq != seq || r.history.hash != hash {
		return
	}
	r.history = catchupTarget{seq: seq - 1, hash: parentHash, peerID: peerID}
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
			if r.history == target && r.historyFloor == floor {
				r.history = catchupTarget{}
				r.historyFloor = 0
			}
			r.historyMu.Unlock()
			return
		}
		held, err := svc.GetLedgerByHash(target.hash)
		if err != nil || held == nil {
			break
		}
		hdr := held.Header()
		stateMap, err := held.StateMapSnapshot()
		if err != nil {
			r.logger.Warn("history backfill: snapshot held ledger state failed",
				"error", err, "seq", target.seq)
			return
		}
		txMap, err := held.TxMapSnapshot()
		if err != nil {
			r.logger.Warn("history backfill: snapshot held ledger transactions failed",
				"error", err, "seq", target.seq)
			return
		}
		if err = svc.IngestHistoricalLedgerWithState(r.lifecycleContext(), &hdr, stateMap, txMap); err != nil {
			r.logger.Warn("history backfill: held ledger ingest failed",
				"error", err, "seq", target.seq)
			return
		}
		next := catchupTarget{seq: target.seq - 1, hash: held.ParentHash(), peerID: target.peerID}
		r.historyMu.Lock()
		if r.history != target || r.historyFloor != floor {
			r.historyMu.Unlock()
			return
		}
		r.history = next
		r.historyMu.Unlock()
		target = next
	}
	r.historyMu.Lock()
	stillCurrent := r.history == target && r.historyFloor == floor
	r.historyMu.Unlock()
	if !stillCurrent {
		return
	}
	if r.fetchTracker.CountReason(inbound.ReasonHistory) >= 1 || r.isAcquiring(target.hash) {
		return
	}
	peer, ok := r.resolveAcquisitionPeer(target.seq, target.peerID)
	if !ok {
		return
	}
	r.historyMu.Lock()
	if r.history != target || r.historyFloor != floor {
		r.historyMu.Unlock()
		return
	}
	il := r.prepareHistoryAcquisition(target.seq, target.hash, peer)
	r.historyMu.Unlock()
	if il == nil {
		return
	}
	r.requestHistoryAcquisition(il, peer)
}

func (r *Router) prepareHistoryAcquisition(seq uint32, hash [32]byte, peerID uint64) *inbound.Ledger {
	if r.replayer.Has(hash) {
		return nil
	}
	il, created := r.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.NewHistory(hash, seq, peerID, r.logger, r.acquisitionOpts()...)
	})
	if !created {
		return nil
	}
	return il
}

// requestHistoryAcquisition requests a skipped historical ledger (header +
// state) over legacy mtGET_LEDGER. Replay-delta doesn't apply: the walk is
// backward, so the parent is never locally available.
func (r *Router) requestHistoryAcquisition(il *inbound.Ledger, peerID uint64) {
	hash := il.Hash()
	r.logger.Info("starting history backfill acquisition",
		"seq", il.Seq(),
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

// FastSyncMetrics returns the current bounded outcome counters.
func (r *Router) FastSyncMetrics() FastSyncMetrics {
	r.acquisitionMu.Lock()
	depth := len(r.standardReplay.entries)
	readyDepth := 0
	for _, entry := range r.standardReplay.entries {
		if !entry.readyAt.IsZero() {
			readyDepth++
		}
	}
	headSeq := uint32(0)
	blockedUs := uint64(0)
	if r.standardReplay.active {
		headSeq = r.standardReplay.anchorSeq + 1
		if !r.standardReplay.headBlockedAt.IsZero() {
			blockedUs = durationMicros(time.Since(r.standardReplay.headBlockedAt))
		}
	}
	targetSeq := r.standardReplay.targetSeq
	pivotSeq := r.standardReplay.pivotSeq
	preparedTailSeq := r.standardReplay.collectSeq
	generation := r.standardReplay.generation
	pivotHash := r.standardReplay.pivotHash
	pivotStartedAt := r.standardReplay.pivotStartedAt
	r.acquisitionMu.Unlock()
	pivotStateRate := uint64(0)
	if pivot := r.fetchTracker.Find(pivotHash); pivot != nil && !pivotStartedAt.IsZero() {
		if elapsed := time.Since(pivotStartedAt); elapsed > 0 {
			pivotStateRate = uint64(float64(pivot.Snapshot().StateUseful) / elapsed.Seconds())
		}
	}
	r.catchupMu.Lock()
	trustedHeadSeq := uint32(0)
	if r.catchup.source == catchupSourceQuorum {
		trustedHeadSeq = r.catchup.seq
	}
	r.catchupMu.Unlock()
	return FastSyncMetrics{
		CompletionRecheckAccepted:            r.completionRecheckAccepted.Load(),
		CompletionRecheckRejectedNoEvidence:  r.completionRecheckRejectedNoEvidence.Load(),
		CompletionRecheckRejectedBelowQuorum: r.completionRecheckRejectedBelowQuorum.Load(),
		CompletionRecheckRejectedUnavailable: r.completionRecheckRejectedUnavailable.Load(),
		TargetSuperseded:                     r.targetSuperseded.Load(),
		ObsoleteAcquisitionCompleted:         r.obsoleteAcquisitionCompleted.Load(),
		ReplayPipelineRequested:              r.replayPipelineRequested.Load(),
		ReplayPipelineReady:                  r.replayPipelineReady.Load(),
		ReplayPipelineApplied:                r.replayPipelineApplied.Load(),
		ReplayPipelineDiscarded:              r.replayPipelineDiscarded.Load(),
		ReplayPipelineRetried:                r.replayPipelineRetried.Load(),
		ReplayPipelineFallbacks:              r.replayPipelineFallbacks.Load(),
		ReplayPipelineBackpressureEvents:     r.replayPipelineBackpressureEvents.Load(),
		ReplayPipelineRetargetFailures:       r.replayPipelineRetargetFailures.Load(),
		ReplayPipelineAcquireUs:              r.replayPipelineAcquireUs.Load(),
		ReplayPipelineReadyWaitUs:            r.replayPipelineReadyWaitUs.Load(),
		ReplayPipelineApplyUs:                r.replayPipelineApplyUs.Load(),
		ReplayPipelinePersistUs:              r.replayPipelinePersistUs.Load(),
		ReplayPipelineWindow:                 standardReplayPipelineWindow,
		ReplayPipelinePreparedLimit:          standardReplayPreparedLimit,
		ReplayPipelineDepth:                  uint32(depth),
		ReplayPipelineReadyDepth:             uint32(readyDepth),
		ReplayPipelinePivotSeq:               pivotSeq,
		ReplayPipelinePreparedTailSeq:        preparedTailSeq,
		ReplayPipelineTrustedHeadSeq:         trustedHeadSeq,
		ReplayPipelineGeneration:             generation,
		ReplayPipelinePivotStateNodesPerSec:  pivotStateRate,
		ReplayPipelineHeadSeq:                headSeq,
		ReplayPipelineTargetSeq:              targetSeq,
		ReplayPipelineHeadBlockedUs:          blockedUs,
	}
}

func (r *Router) recordCompletionRecheck(result validationRecheckResult) {
	switch result {
	case validationRecheckAccepted:
		r.completionRecheckAccepted.Add(1)
	case validationRecheckNoEvidence:
		r.completionRecheckRejectedNoEvidence.Add(1)
	case validationRecheckBelowQuorum:
		r.completionRecheckRejectedBelowQuorum.Add(1)
	case validationRecheckUnavailable:
		r.completionRecheckRejectedUnavailable.Add(1)
	}
}

// ClearFetchInfo resets the acquisition counters and recent-failure history,
// backing fetch_info's `clear` param.
func (r *Router) ClearFetchInfo() {
	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	ledgers := r.fetchTracker.Clear()
	retirement := r.cancelStandardReplayPipelineLocked()
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()
	r.retireLegacyAcquisitions(ledgers)
	r.retireStandardReplay(retirement)
}

func (r *Router) retireLegacyAcquisitions(ledgers []*inbound.Ledger) {
	for _, ledger := range ledgers {
		if lane := r.currentAcquisitionWork(); lane != nil {
			lane.cancelLedger(ledger)
		}
		r.retireAcquisitionStore(r.lifecycleContext(), ledger)
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
// before creation. If the selected peer disconnects during the initial request,
// the acquisition remains active so maintenance can select a replacement. The
// fetchTracker's GetOrCreate is atomic, so a concurrent consensus catch-up
// arming the same hash is joined rather than duplicated.
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
		r.requestLedgerBase(il, peerID, "ledger_request: failed to request ledger base")
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
		r.acquisition.IncPeerBadData(uint64(msg.PeerID), "replay-delta-resp-decode")
		return
	}
	resp, ok := decoded.(*message.ReplayDeltaResponse)
	if !ok || resp == nil {
		return
	}

	rd, err := r.replayer.HandleResponseFrom(uint64(msg.PeerID), resp)
	if errors.Is(err, inbound.ErrNoMatchingAcquisition) {
		// Stale or unsolicited — drop silently without charging the
		// peer. A misbehaving peer sending genuinely bogus data would
		// fail its ACTIVE acquisition's verifier (branch below), which
		// IS attributed via IncPeerBadData.
		r.logger.Debug("replay delta response with no matching acquisition",
			"peer", msg.PeerID)
		return
	}
	if errors.Is(err, inbound.ErrUnexpectedReplayPeer) {
		hash := rd.Hash()
		r.logger.Warn("replay delta response from unexpected peer",
			"peer", msg.PeerID,
			"expected_peer", rd.PeerID(),
			"hash", fmt.Sprintf("%x", hash[:8]),
		)
		r.acquisition.IncPeerBadData(uint64(msg.PeerID), "replay-delta-peer")
		return
	}
	if err != nil {
		// Verification failed. rd is still registered in the Replayer so
		// we can read its provenance before abandoning the slot.
		seq := rd.Seq()
		hash := rd.Hash()
		peerID := uint64(msg.PeerID)
		r.replayer.Abandon(hash)
		r.logger.Warn("replay delta verification failed; falling back to legacy",
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"peer", peerID,
			"error", err,
		)
		routeMismatch := errors.Is(err, inbound.ErrReplayParentMismatch) ||
			errors.Is(err, inbound.ErrReplaySequenceMismatch)
		if !routeMismatch {
			r.acquisition.IncPeerBadData(peerID, "replay-delta-verify")
		}
		r.fallbackReplayAcquisition(seq, hash, peerID)
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
		r.fallbackReplayAcquisition(seq, hash, peerID)
		return
	}
	r.replayer.Complete(rd.Hash())
	if err := r.adoptVerifiedLedger(derived); err != nil {
		r.logger.Warn("failed to store replay-delta ledger", "error", err)
	}
}

// adoptVerifiedLedger stores a ledger reconstructed from a verified replay
// delta until consensus selects it as the canonical frontier.
func (r *Router) adoptVerifiedLedger(l *ledger.Ledger) error {
	hdr, initialCandidate, err := r.storeVerifiedLedger(l)
	if err != nil {
		return err
	}
	r.logger.Info("acquired ledger via replay delta",
		"seq", hdr.LedgerIndex,
		"hash", fmt.Sprintf("%x", hdr.Hash[:8]),
		"initial_candidate", initialCandidate,
	)
	r.completeStoredConsensusRecovery(hdr.LedgerIndex, hdr.Hash, hdr.ParentHash, initialCandidate)
	return nil
}

func (r *Router) storeVerifiedLedger(l *ledger.Ledger) (header.LedgerHeader, bool, error) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return header.LedgerHeader{}, false, errors.New("no ledger service")
	}
	hdr := l.Header()
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		return header.LedgerHeader{}, false, fmt.Errorf("snapshot state map: %w", err)
	}
	// Pass the verified tx map through so the stored ledger carries
	// real transactions — without this, tx/tx_history/account_tx RPCs
	// can't answer for replay-delta ledgers and we can't
	// re-serve the replay-delta to other peers.
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		return header.LedgerHeader{}, false, fmt.Errorf("snapshot tx map: %w", err)
	}
	initialCandidate, err := svc.BootstrapLedgerWithState(r.lifecycleContext(), &hdr, stateMap, txMap)
	if err != nil {
		return header.LedgerHeader{}, false, fmt.Errorf("store replay-delta ledger: %w", err)
	}
	return hdr, initialCandidate, nil
}

func (r *Router) completeStoredConsensusRecovery(seq uint32, hash, parentHash [32]byte, initialCandidate bool) bool {
	_, result := r.adaptor.recheckFullyValidated(seq, hash)
	r.recordCompletionRecheck(result)
	obsolete := r.isObsoleteRecoveryCompletion(seq, hash)
	if result == validationRecheckAccepted {
		r.recordAcquiredSeqHash(seq, hash, parentHash)
		r.promoteCompletedLedger(seq, hash)
	} else if !obsolete {
		r.recordAcquiredSeqHash(seq, hash, parentHash)
	}
	if obsolete {
		r.obsoleteAcquisitionCompleted.Add(1)
		r.armConsensusCatchup()
		return false
	}
	if initialCandidate {
		accepted, rearm := r.tryInitialLedgerSwitch(seq, hash)
		if rearm {
			r.armConsensusCatchup()
		}
		return accepted
	}

	if !r.shouldSwitchConsensusLedger(seq, hash) {
		_, rearm := r.finishConsensusRecoveryStep(seq, hash)
		if rearm {
			r.armConsensusCatchup()
		}
		return false
	}

	accepted, rearm := r.tryConsensusLedgerSwitch(seq, hash)
	if rearm {
		r.armConsensusCatchup()
	}
	return accepted
}

func (r *Router) isObsoleteRecoveryCompletion(seq uint32, hash [32]byte) bool {
	r.acquisitionMu.Lock()
	target := r.consensusRecovery.targetHash
	step := r.consensusRecovery.stepHash
	r.acquisitionMu.Unlock()
	if target != ([32]byte{}) {
		return target != hash && step != hash
	}
	frontier := r.credibleCatchupFrontier()
	if frontier.source == catchupSourcePeer {
		return frontier.seq == seq && frontier.hash != hash
	}
	return frontier.hash != ([32]byte{}) &&
		frontier.hash != hash && frontier.seq >= seq
}

func (r *Router) promoteCompletedLedger(seq uint32, hash [32]byte) {
	if r.engine == nil {
		return
	}
	acceptable, err := r.engine.CanAcceptLedger(consensus.LedgerID(hash))
	if err != nil {
		r.logger.Debug("validated ledger acceptance check failed", "error", err, "seq", seq)
		return
	}
	if !acceptable {
		return
	}
	signTime, result := r.adaptor.recheckFullyValidated(seq, hash)
	if result != validationRecheckAccepted {
		return
	}
	if svc := r.adaptor.LedgerService(); svc != nil {
		svc.PromoteStoredValidatedLedgerAt(seq, hash, signTime)
	}
}

func (r *Router) shouldSwitchConsensusLedger(seq uint32, hash [32]byte) bool {
	frontier := r.credibleCatchupFrontier()

	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()

	target := r.consensusRecovery.targetHash
	if target != ([32]byte{}) {
		return target == hash
	}
	if frontier.seq == seq && frontier.hash != ([32]byte{}) && frontier.hash != hash {
		return false
	}
	return !aheadByMoreThan(frontier.seq, seq, 1) && seq > r.lastHandoffSeq
}

func (r *Router) tryConsensusLedgerSwitch(seq uint32, hash [32]byte) (accepted, rearm bool) {
	result := consensus.LedgerSwitchIrrelevant
	if r.engine != nil {
		var err error
		result, err = r.engine.TrySwitchToLedger(consensus.LedgerID(hash))
		if err != nil {
			r.logger.Debug("consensus ledger switch failed", "error", err, "seq", seq)
			result = consensus.LedgerSwitchIrrelevant
		}
	}
	if result != consensus.LedgerSwitchAccepted {
		r.retainConsensusLedgerSwitch(hash)
		return false, false
	}

	_, rearm = r.finishConsensusRecoveryStep(seq, hash)
	if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
		r.adaptor.SetOperatingMode(consensus.OpModeTracking)
	}
	return true, rearm
}

func (r *Router) retainConsensusLedgerSwitch(hash [32]byte) {
	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()

	if r.consensusRecovery.targetHash == ([32]byte{}) {
		r.consensusRecovery.targetHash = hash
	}
}

func (r *Router) finishConsensusRecoveryStep(seq uint32, hash [32]byte) (notify, rearm bool) {
	frontierSeq := r.credibleCatchupFrontier().seq

	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()

	target := r.consensusRecovery.targetHash
	if target != ([32]byte{}) {
		if r.consensusRecovery.stepHash == hash {
			r.consensusRecovery.stepHash = [32]byte{}
		}
		if target != hash {
			currentAnchorUsable := r.consensusRecovery.anchorHash != ([32]byte{}) &&
				r.recoveryAnchorReachesTarget(
					r.consensusRecovery.anchorSeq,
					r.consensusRecovery.anchorHash,
					target,
				)
			if r.recoveryAnchorReachesTarget(seq, hash, target) &&
				(!currentAnchorUsable || seq > r.consensusRecovery.anchorSeq) {
				r.consensusRecovery.anchorSeq = seq
				r.consensusRecovery.anchorHash = hash
			}
			return false, true
		}
		r.consensusRecovery.anchorSeq = seq
		r.consensusRecovery.anchorHash = hash
		r.consensusRecovery.targetHash = [32]byte{}
		r.consensusRecovery.stepHash = [32]byte{}
		r.recordConsensusHandoffLocked(seq)
		return true, false
	}

	if aheadByMoreThan(frontierSeq, seq, 1) {
		return false, true
	}
	if seq <= r.lastHandoffSeq {
		return false, aheadByMoreThan(frontierSeq, seq, 0)
	}
	r.lastHandoffSeq = seq
	return true, false
}

func (r *Router) tryInitialLedgerSwitch(seq uint32, hash [32]byte) (accepted, rearm bool) {
	result := consensus.LedgerSwitchIrrelevant
	if r.engine != nil {
		var err error
		result, err = r.engine.TrySwitchToLedger(consensus.LedgerID(hash))
		if err != nil {
			r.logger.Debug("initial ledger switch failed", "error", err, "seq", seq)
			result = consensus.LedgerSwitchIrrelevant
		}
	}

	rearm = r.finishInitialLedgerSwitch(seq, hash, result)
	if result == consensus.LedgerSwitchAccepted {
		if r.adaptor.GetOperatingMode() < consensus.OpModeTracking {
			r.adaptor.SetOperatingMode(consensus.OpModeTracking)
		}
		return true, rearm
	}
	return false, rearm
}

func (r *Router) finishInitialLedgerSwitch(
	seq uint32,
	hash [32]byte,
	result consensus.LedgerSwitchResult,
) bool {
	frontierSeq := r.credibleCatchupFrontier().seq

	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()

	if r.consensusRecovery.stepHash == hash {
		r.consensusRecovery.stepHash = [32]byte{}
	}
	target := r.consensusRecovery.targetHash
	if result == consensus.LedgerSwitchBusy {
		if target == ([32]byte{}) {
			r.consensusRecovery.targetHash = hash
		}
		return false
	}

	if target == hash {
		r.consensusRecovery.anchorSeq = seq
		r.consensusRecovery.anchorHash = hash
		r.consensusRecovery.targetHash = [32]byte{}
	}
	if target != ([32]byte{}) && target != hash &&
		r.recoveryAnchorReachesTarget(seq, hash, target) {
		currentAnchorUsable := r.consensusRecovery.anchorHash != ([32]byte{}) &&
			r.recoveryAnchorReachesTarget(
				r.consensusRecovery.anchorSeq,
				r.consensusRecovery.anchorHash,
				target,
			)
		if !currentAnchorUsable || seq > r.consensusRecovery.anchorSeq {
			r.consensusRecovery.anchorSeq = seq
			r.consensusRecovery.anchorHash = hash
		}
	}

	if result == consensus.LedgerSwitchAccepted {
		r.recordConsensusHandoffLocked(seq)
	}
	return (target != ([32]byte{}) && target != hash) ||
		aheadByMoreThan(frontierSeq, seq, 0)
}

func (r *Router) recordConsensusHandoffLocked(seq uint32) {
	if seq > r.lastHandoffSeq {
		r.lastHandoffSeq = seq
	}
}

func (r *Router) failConsensusRecoveryStep(hash [32]byte) {
	r.acquisitionMu.Lock()
	defer r.acquisitionMu.Unlock()
	if r.consensusRecovery.stepHash != hash {
		return
	}
	if r.consensusRecovery.targetHash == hash {
		r.consensusRecovery = consensusRecovery{
			anchorHash: r.consensusRecovery.anchorHash,
			anchorSeq:  r.consensusRecovery.anchorSeq,
		}
		return
	}
	r.consensusRecovery.stepHash = [32]byte{}
}

// maybeAcquireFromValidation arms inbound acquisition for a ledger attested
// by a single TRUSTED validation, before the hash reaches quorum. It is the
// non-quorum counterpart to armValidatedLedgerAcquisition, acquiring the
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
	r.recordValidationSeqHash(v.LedgerSeq, [32]byte(v.LedgerID))

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	ourSeq := svc.GetClosedLedgerIndex()
	if r.adaptor.GetOperatingMode() == consensus.OpModeFull && aheadByMoreThan(v.LedgerSeq, ourSeq, 2) {
		r.adaptor.SetOperatingMode(consensus.OpModeConnected)
		r.logger.Warn("trusted validation is ahead; leaving Full mode",
			"our_seq", ourSeq,
			"validated_seq", v.LedgerSeq,
		)
	}
	// Gate on the VALIDATED tip, never the closed/built tip. The equal-sequence
	// exception retains the validating peer only while a fast-loaded ledger is
	// provisional; promotion still requires quorum.
	validated := svc.GetValidatedLedgerIndex()
	if v.LedgerSeq < validated ||
		(v.LedgerSeq == validated && !svc.IsFastLoadProvisional()) {
		return
	}
	hash := [32]byte(v.LedgerID)
	r.recordValidationCatchupTarget(
		v.LedgerSeq,
		hash,
		originPeer,
		catchupSourceValidation,
	)
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
		if r.isBuildingLedger(v.LedgerSeq) {
			return
		}
		r.startLedgerAcquisition(v.LedgerSeq, hash, originPeer)
		return
	}
	r.ensureValidationCatchupAcquisition(v.LedgerSeq, hash, originPeer)
}

func (r *Router) armValidatedLedgerAcquisition(seq uint32, hash [32]byte) {
	defer func() {
		if rv := recover(); rv != nil {
			r.logger.Error("armValidatedLedgerAcquisition panic recovered",
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
	// Ordinary validated history stays monotonic. The equal-sequence exception
	// is limited to replacing a provisional fast-loaded ledger after quorum;
	// gating on the closed-ledger index would also swallow recovery from a
	// private chain whose closed frontier ran ahead of its validated frontier.
	validated := svc.GetValidatedLedgerIndex()
	if seq < validated ||
		(seq == validated && !svc.IsFastLoadProvisional()) {
		return
	}
	if held, err := svc.GetLedgerByHash(hash); err == nil && held != nil && held.Sequence() == seq {
		r.acquisitionMu.Lock()
		r.consensusRecovery.targetHash = hash
		r.acquisitionMu.Unlock()
		r.runLifecycleTask(func(context.Context) {
			r.promoteCompletedLedger(seq, hash)
			r.armConsensusCatchup()
		})
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

	// Keep the newest target even when the speculative slots are full; completion
	// or maintenance will re-arm it when capacity becomes available.
	r.recordValidationCatchupTarget(
		seq,
		hash,
		preferredPeerID,
		catchupSourceQuorum,
	)
	if r.isBuildingLedger(seq) {
		return
	}
	r.logger.Info("arming acquisition for stashed validation",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"preferred_peer", preferredPeerID,
	)
	r.armCatchupTowardTargetWithPeer(preferredPeerID)
}

type buildingLedgerEngine interface {
	BuildingLedgerSeq() uint32
}

func (r *Router) isBuildingLedger(seq uint32) bool {
	engine, ok := r.engine.(buildingLedgerEngine)
	return ok && seq != 0 && engine.BuildingLedgerSeq() == seq
}

func (r *Router) onLedgerBuilt(uint32, [32]byte) {
	r.armCatchupTowardTarget()
}

// checkBehind decides what to do based on how far behind a peer
// reports. Two outcomes:
//
//   - peerSeq <= ourSeq+1: we're caught up. If still in Tracking and
//     our LCL hash matches peers' majority, transition to Full.
//     Otherwise stay in Tracking until network preference is established.
//   - peerSeq > ourSeq+1: we're behind by more than one ledger. If the
//     peer's tip is network-preferred, arm one acquisition. Subsequent
//     status changes chain acquisitions forward as we adopt each ledger
//     and ourSeq advances.
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
	networkSeq := ourSeq
	if validatedSeq := svc.GetValidatedLedgerIndex(); validatedSeq > networkSeq {
		networkSeq = validatedSeq
	}
	if validatedSeq := r.adaptor.networkValidatedSeq.Load(); validatedSeq > networkSeq {
		networkSeq = validatedSeq
	}
	frontier := r.credibleCatchupFrontier()
	if frontier.seq > networkSeq {
		networkSeq = frontier.seq
	}
	peerPreferred := r.peerLedgerIsPreferred(peerHash)
	if peerPreferred && peerSeq > networkSeq {
		networkSeq = peerSeq
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
	if !peerPreferred {
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

func (r *Router) catchupFrontierIsCredible(target catchupTarget) bool {
	if target.hash == ([32]byte{}) {
		return false
	}
	if target.source != catchupSourcePeer {
		return true
	}

	found := false
	r.peersMu.RLock()
	for _, state := range r.peerStates {
		if state != nil && state.LedgerSeq == target.seq && state.LedgerHash == target.hash {
			found = true
			break
		}
	}
	r.peersMu.RUnlock()
	return found && r.peerLedgerIsPreferred(target.hash)
}

func (r *Router) credibleCatchupFrontier() catchupTarget {
	r.catchupMu.Lock()
	target := r.catchup
	r.catchupMu.Unlock()
	if !r.catchupFrontierIsCredible(target) {
		return catchupTarget{}
	}
	return target
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

func (r *Router) handleLedgerData(msg *peermanagement.InboundMessage) bool {
	receivedAt := time.Now()
	decodeStarted := time.Now()
	decoded, err := message.Decode(message.TypeLedgerData, msg.Payload)
	decodeDuration := time.Since(decodeStarted)
	if err != nil {
		r.logger.Warn("failed to decode ledger_data", "error", err, "peer", msg.PeerID)
		r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-decode")
		return false
	}
	ld, ok := decoded.(*message.LedgerData)
	if !ok {
		return false
	}
	if len(ld.LedgerHash) != 32 {
		r.logger.Warn("invalid ledger_data ledger hash", "peer", msg.PeerID, "length", len(ld.LedgerHash))
		r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-hash")
		return false
	}
	if ld.InfoType < message.LedgerInfoBase || ld.InfoType > message.LedgerInfoTsCandidate {
		r.logger.Warn("invalid ledger_data info type", "peer", msg.PeerID, "info_type", ld.InfoType)
		r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-type")
		return false
	}
	if (ld.InfoType == message.LedgerInfoTsCandidate && ld.LedgerSeq != 0) ||
		(ld.InfoType != message.LedgerInfoTsCandidate && r.invalidFutureLedgerSequence(ld.LedgerSeq)) {
		r.logger.Warn("invalid ledger_data ledger sequence", "peer", msg.PeerID, "seq", ld.LedgerSeq)
		r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-sequence")
		return false
	}
	if ld.HasError() &&
		(ld.Error < message.ReplyErrorNoLedger || ld.Error > message.ReplyErrorBadRequest) {
		r.logger.Warn("invalid ledger_data reply error", "peer", msg.PeerID, "error", ld.Error)
		r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-error")
		return false
	}
	if ld.HasError() {
		r.logger.Warn("inbound ledger: peer returned reply error",
			"peer", msg.PeerID,
			"seq", ld.LedgerSeq,
			"info_type", ld.InfoType,
			"reply_error", ld.Error,
			"nodes", len(ld.Nodes),
		)
	}
	if err := inbound.ValidateReplyNodeCount(ld.Nodes); err != nil {
		r.logger.Warn("invalid ledger_data node count",
			"error", err,
			"peer", msg.PeerID,
			"seq", ld.LedgerSeq,
			"info_type", ld.InfoType,
			"reply_error", ld.Error,
			"nodes", len(ld.Nodes),
		)
		r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-count")
		return false
	}
	if ld.InfoType == message.LedgerInfoAsNode || ld.InfoType == message.LedgerInfoTxNode {
		for _, node := range ld.Nodes {
			if len(node.NodeData) == 0 {
				r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-node")
				return false
			}
			if _, err := shamap.ParseNodeID(node.NodeID); err != nil {
				r.acquisition.IncPeerBadData(uint64(msg.PeerID), "ledger-data-node")
				return false
			}
		}
	}

	// A reply carrying a request_cookie answers a GetLedger we relayed on
	// another peer's behalf. Route it back to the original requester named
	// by the cookie and do not consume it locally. Mirrors rippled
	// onMessage(TMLedgerData).
	if ld.HasRequestCookie() {
		r.routeRelayedLedgerData(ld, msg.PeerID)
		return false
	}

	var il *inbound.Ledger
	if len(ld.LedgerHash) == 32 {
		var h [32]byte
		copy(h[:], ld.LedgerHash)
		il = r.fetchTracker.Find(h)
	}

	r.logger.Debug("received ledger data",
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
		return false
	}

	if il != nil {
		il.RecordDecodeDuration(decodeDuration)
		if consumed, transferred := r.handleInboundLedgerDataOwned(il, ld, uint64(msg.PeerID), msg, receivedAt); consumed {
			return transferred
		}
	}
	if il == nil && ld.InfoType == message.LedgerInfoAsNode {
		r.cacheStaleStateNodes(ld)
	}
	return false
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
	consumed, _ := r.handleInboundLedgerDataOwned(il, ld, peerID, nil, time.Now())
	return consumed
}

func (r *Router) handleInboundLedgerDataOwned(
	il *inbound.Ledger,
	ld *message.LedgerData,
	peerID uint64,
	owner *peermanagement.InboundMessage,
	receivedAt time.Time,
) (bool, bool) {
	if il == nil {
		return false, false
	}
	if lane := r.currentAcquisitionWork(); lane != nil {
		wireBytes := 0
		payloadBytes := 0
		if owner != nil {
			wireBytes = int(owner.WireSize)
			payloadBytes = len(owner.Payload)
		}
		switch ld.InfoType {
		case message.LedgerInfoBase, message.LedgerInfoAsNode, message.LedgerInfoTxNode:
			if lane.submit(il, acquisitionWorkEvent{
				kind: acquisitionWorkData, data: ld, owner: owner, peerID: peerID,
				receivedAt: receivedAt, wireBytes: wireBytes, payloadBytes: payloadBytes,
			}) {
				return true, owner != nil
			}
			r.logger.Warn("inbound ledger reply deferred: acquisition worker saturated",
				"peer", peerID, "seq", il.Seq(), "info_type", ld.InfoType)
			il.RecordWorkerSaturation()
			return true, false
		}
	}

	switch ld.InfoType {
	case message.LedgerInfoBase:
		if len(ld.Nodes) < 2 {
			r.logger.Debug("inbound ledger: response has < 2 nodes", "nodes", len(ld.Nodes))
			if r.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), false) {
				r.retireAcquisitionStore(r.lifecycleContext(), il)
			}
			return true, false
		}
		if err := il.GotBase(ld.Nodes); err != nil {
			r.logger.Warn("inbound ledger: GotBase failed", "error", err)
			if errors.Is(err, inbound.ErrHeaderRejected) {
				r.failInboundAcquisition(il)
			} else {
				r.acquisition.IncPeerBadData(peerID, "ledger-data-base")
				if r.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), false) {
					r.retireAcquisitionStore(r.lifecycleContext(), il)
				}
			}
			return true, false
		}
		r.promoteResolvedFrozenPivot(il, peerID)

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true, false
		}

		// Re-request the missing state and transaction nodes from the peer
		// that answered, mirroring rippled trigger(peer) on a reply.
		r.requestMissingAcquisitionNodes(il, peerID)
		return true, false

	case message.LedgerInfoAsNode:
		il.ReleaseMissingPeer(peerID)
		useful, err := il.GotStateNodesUseful(ld.Nodes)
		if err != nil {
			r.logger.Warn("inbound ledger: GotStateNodes failed", "error", err)
			r.acquisition.IncPeerBadData(peerID, "ledger-data-state")
			return true, false
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true, false
		}

		if useful > 0 {
			r.requestMissingAcquisitionNodes(il, peerID)
		}
		return true, false

	case message.LedgerInfoTxNode:
		il.ReleaseMissingPeer(peerID)
		useful, err := il.GotTransactionNodesUseful(ld.Nodes)
		if err != nil {
			r.logger.Warn("inbound ledger: GotTransactionNodes failed", "error", err)
			r.acquisition.IncPeerBadData(peerID, "ledger-data-tx")
			return true, false
		}

		if il.IsComplete() {
			r.completeInboundLedger(il)
			return true, false
		}

		if useful > 0 {
			r.requestMissingAcquisitionNodes(il, peerID)
		}
		return true, false
	}

	return false, false
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
	if target != 0 {
		requests, complete, err := il.CollectMissingReplyRequestsContext(context.Background(), []uint64{target})
		if err != nil {
			r.logger.Warn("inbound ledger: failed to collect missing nodes", "error", err)
			return
		}
		if complete {
			r.completeInboundLedger(il)
			return
		}
		released := 0
		for _, request := range requests {
			if r.sendMissingReplyRequest(il, request) {
				released++
			}
		}
		r.retryMissingAcquisitionNodes(il, missingNodeRetry{}, released)
		return
	}

	stateIDs, txIDs, complete, err := il.CollectMissingRequestContext(context.Background(), false)
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
	peers := il.Peers()
	retry := r.sendMissingAcquisitionNodes(il, peers, stateIDs, txIDs, 0)
	r.retryMissingAcquisitionNodes(il, retry, 0)
}

func (r *Router) requestMissingAcquisitionNodesFromAddedPeer(il *inbound.Ledger, peerID uint64) {
	requests, complete, err := il.CollectMissingAddedRequestsContext(context.Background(), []uint64{peerID})
	if err != nil {
		r.logger.Warn("inbound ledger: failed to collect missing nodes for added peer", "error", err)
		return
	}
	if complete {
		r.completeInboundLedger(il)
		return
	}
	released := 0
	for _, request := range requests {
		if r.sendMissingReplyRequest(il, request) {
			released++
		}
	}
	r.retryMissingAcquisitionNodes(il, missingNodeRetry{}, released)
}

func (r *Router) handleMissingNodeSendFailure(
	il *inbound.Ledger,
	peerID uint64,
	transaction bool,
	err error,
) bool {
	hash := il.Hash()
	r.logger.Warn(
		"inbound ledger: failed to request missing nodes",
		"peer", peerID,
		"ledger_seq", il.Seq(),
		"ledger_hash", fmt.Sprintf("%x", hash[:8]),
		"transaction", transaction,
		"error", err,
	)
	if !isAcquisitionDisconnectError(err) {
		return false
	}
	il.RemovePeer(peerID)
	r.HandlePeerDisconnect(peermanagement.PeerID(peerID))
	return true
}

func (r *Router) retryMissingAcquisitionNodes(
	il *inbound.Ledger,
	retry missingNodeRetry,
	released int,
) {
	if len(retry.stateIDs) == 0 && len(retry.txIDs) == 0 && released == 0 {
		return
	}

	peers := il.Peers()
	replacements := released
	if len(retry.stateIDs) > 0 || len(retry.txIDs) > 0 {
		replacements++
	}
	replacements = min(max(replacements, 1), acquisitionMaxUsefulPeers)
	added := make([]uint64, 0, replacements)
	for _, peerID := range r.acquisition.SelectLedgerPeers(il.Hash(), il.Seq(), peers, replacements) {
		if il.AddPeer(peerID) {
			added = append(added, peerID)
		}
	}
	candidates := acquisitionRequestCandidates(added, il.Peers())
	if len(candidates) > acquisitionMaxUsefulPeers {
		candidates = candidates[:acquisitionMaxUsefulPeers]
	}
	if len(candidates) == 0 {
		if len(retry.stateIDs) > 0 || len(retry.txIDs) > 0 {
			il.ReleaseUnreservedMissingNodes()
		}
		return
	}

	queued := r.submitAcquisitionWork(il, acquisitionWorkEvent{
		kind:       acquisitionWorkRetarget,
		peers:      candidates,
		stateIDs:   append([][]byte(nil), retry.stateIDs...),
		txIDs:      append([][]byte(nil), retry.txIDs...),
		queryDepth: retry.queryDepth,
		collect:    released > 0,
	})
	if !queued {
		if len(retry.stateIDs) > 0 || len(retry.txIDs) > 0 {
			il.ReleaseUnreservedMissingNodes()
		}
		hash := il.Hash()
		r.logger.Warn(
			"inbound ledger: missing-node retarget deferred; acquisition worker saturated",
			"ledger_seq", il.Seq(),
			"ledger_hash", fmt.Sprintf("%x", hash[:8]),
		)
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
	for _, peerID := range r.acquisition.SelectLedgerPeers(il.Hash(), il.Seq(), peers, limit) {
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
	r.retireAcquisitionStore(r.lifecycleContext(), il)
	r.logger.Warn("inbound ledger acquisition failed",
		"seq", il.Seq(),
		"hash", fmt.Sprintf("%x", hash[:8]),
		"timeouts", il.Timeouts(),
	)
	if reason == inbound.ReasonConsensus && il.TransactionOnly() && r.failStandardReplayPipelineEntry(il) {
		return
	}
	if reason == inbound.ReasonConsensus {
		r.markFailedCatchupAcquisition(hash)
		r.failFrozenPivotRecovery(hash)
		r.failConsensusRecoveryStep(hash)
	}
	if reason == inbound.ReasonConsensus && r.engine != nil {
		r.engine.OnLedgerAcquireFailed(consensus.LedgerID(hash))
	}
	if reason == inbound.ReasonConsensus {
		r.armConsensusCatchup()
	}
}

func (r *Router) discardFailedInboundAcquisition(il *inbound.Ledger) {
	if !r.fetchTracker.DiscardExpected(il) {
		return
	}
	r.finishDiscardedInboundAcquisition(il)
}

func (r *Router) discardFailedInboundAcquisitionWithSnapshot(il *inbound.Ledger, snapshot inbound.Snapshot) {
	if !r.fetchTracker.RemoveExpectedWithSnapshot(il, snapshot, false) {
		return
	}
	r.finishDiscardedInboundAcquisition(il)
}

func (r *Router) finishDiscardedInboundAcquisition(il *inbound.Ledger) {
	r.retireAcquisitionStore(r.lifecycleContext(), il)
	if il.Reason() != inbound.ReasonConsensus {
		return
	}
	if il.TransactionOnly() {
		r.failStandardReplayPipelineEntry(il)
		return
	}
	if r.failFrozenPivotRecovery(il.Hash()) {
		r.failConsensusRecoveryStep(il.Hash())
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
		if err := r.acquisition.SendPriorityToPeer(peerID, frame); err != nil {
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
	if err := r.flushAcquisitionStore(r.lifecycleContext(), il); err != nil {
		r.logger.Warn("inbound ledger: verified-node persistence failed", "error", err, "seq", il.Seq())
		r.discardFailedInboundAcquisition(il)
		return
	}
	r.completeInboundLedgerReady(il)
}

func (r *Router) completeInboundLedgerReady(il *inbound.Ledger) {
	h, stateMap, txMap, err := il.Result()
	if err != nil {
		r.logger.Warn("inbound ledger: failed to get result", "error", err)
		r.discardFailedInboundAcquisition(il)
		return
	}
	if r.adaptor == nil {
		r.discardFailedInboundAcquisition(il)
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		r.discardFailedInboundAcquisition(il)
		return
	}
	if err = r.promoteAcquisitionStore(r.lifecycleContext(), il); err != nil {
		r.logger.Warn("inbound ledger: failed to promote persistence scope", "error", err, "seq", il.Seq())
		r.discardFailedInboundAcquisition(il)
		return
	}
	peerID := il.PeerID()

	// A forward child fetched from a standard rippled peer contains only its
	// header and transaction SHAMap. Rebuild the state from the locally-held
	// parent, verify the derived roots against the peer header, then feed the
	// same adoption path as the optional replay-delta protocol.
	if il.TransactionOnly() {
		r.acquisitionMu.Lock()
		if !r.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), true) {
			r.acquisitionMu.Unlock()
			r.retireAcquisitionStore(r.lifecycleContext(), il)
			return
		}
		handled, startDrain := r.completeStandardReplayPipelineEntryLocked(il, h, txMap, peerID)
		r.acquisitionMu.Unlock()
		if handled {
			r.refillStandardReplayCollector(peerID)
			if startDrain {
				r.drainStandardReplayPipeline()
			}
			return
		}
		r.completeStandardTransactionReplay(h, txMap, peerID)
		return
	}
	if !r.fetchTracker.RemoveExpectedWithSnapshot(il, il.Snapshot(), true) {
		r.retireAcquisitionStore(r.lifecycleContext(), il)
		return
	}
	r.acquisitionMu.Lock()
	recoveryTarget := r.consensusRecovery.targetHash == h.Hash
	r.acquisitionMu.Unlock()

	// A history backfill installs validated sequence history below the closed tip,
	// then advances the backward walk to its parent. It never touches operating
	// mode or the consensus engine.
	if il.Reason() == inbound.ReasonHistory {
		if err = svc.IngestHistoricalLedgerWithState(r.lifecycleContext(), h, stateMap, txMap); err != nil {
			r.logger.Warn("inbound ledger: history backfill ingest failed",
				"error", err, "seq", h.LedgerIndex)
			return
		}
		if recoveryTarget {
			r.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, false)
		} else {
			r.completeHistoryBackfill(h.LedgerIndex, h.Hash, h.ParentHash, peerID)
		}
		return
	}

	// The acquisition fetches the header, state map, and transaction map; txMap
	// is nil only when the ledger has no transactions (empty tx tree), in which
	// case the service installs the genesis-shaped empty tx map.
	//
	// Generic acquisitions are queryable by hash but never mutate the service's
	// canonical frontier or feed consensus.
	if il.Reason() == inbound.ReasonGeneric {
		if err = svc.StoreLedgerWithState(r.lifecycleContext(), h, stateMap, txMap); err != nil {
			r.logger.Warn("inbound ledger: generic store failed", "error", err, "seq", h.LedgerIndex)
			return
		}
		r.logger.Info("acquired ledger (generic) with full state from peer",
			"seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]),
		)
		if recoveryTarget {
			r.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, false)
		}
		return
	}

	initialCandidate, err := svc.BootstrapLedgerWithState(r.lifecycleContext(), h, stateMap, txMap)
	if err != nil {
		r.logger.Warn("inbound ledger: failed to store consensus ledger", "error", err, "seq", h.LedgerIndex)
		frozenPivot := r.failFrozenPivotRecovery(h.Hash)
		r.retireAcquisitionStore(r.lifecycleContext(), il)
		if frozenPivot {
			r.armConsensusCatchup()
		}
		return
	}

	r.logger.Info("acquired ledger with full state from peer",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"account_hash", fmt.Sprintf("%x", h.AccountHash[:8]),
		"initial_candidate", initialCandidate,
	)
	if r.completeFrozenPivotAcquisition(h, initialCandidate) {
		return
	}
	r.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, initialCandidate)
}

func (r *Router) completeStandardTransactionReplay(
	h *header.LedgerHeader,
	txMap *shamap.SHAMap,
	peerID uint64,
) {
	if h == nil {
		return
	}
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}
	fallback := func(err error) {
		r.logger.Warn("standard transaction replay failed; falling back to full-state acquisition",
			"seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]),
			"peer", peerID,
			"error", err,
		)
		r.startLedgerAcquisitionLegacy(h.LedgerIndex, h.Hash, peerID)
	}

	parent, err := svc.GetLedgerByHash(h.ParentHash)
	if err != nil || parent == nil {
		if err == nil {
			err = errors.New("locally-held replay parent is unavailable")
		}
		fallback(err)
		return
	}
	if parent.Sequence()+1 != h.LedgerIndex {
		fallback(fmt.Errorf("replay parent sequence %d is not predecessor of %d", parent.Sequence(), h.LedgerIndex))
		return
	}

	stateMap, err := parent.StateMapSnapshot()
	if err != nil {
		fallback(fmt.Errorf("snapshot replay parent state: %w", err))
		return
	}
	if txMap == nil {
		if h.TxHash != ([32]byte{}) {
			fallback(errors.New("missing transaction map for non-empty transaction root"))
			return
		}
		txMap = shamap.New(shamap.TypeTransaction)
	}

	// NewStoredLedgerReplay only needs a header and verified transaction map
	// from target. Carrying the parent's state snapshot here is intentional:
	// ReplayDelta.Apply replaces it with the derived child state and checks the
	// resulting AccountHash before adoption.
	target, err := ledger.NewFromHeader(*h, stateMap, txMap, parent.Fees())
	if err != nil {
		fallback(fmt.Errorf("construct transaction-only replay target: %w", err))
		return
	}
	replay, err := inbound.NewStoredLedgerReplay(parent, target, r.logger)
	if err != nil {
		fallback(fmt.Errorf("prepare standard transaction replay: %w", err))
		return
	}
	derived, err := replay.Apply(r.adaptor.EngineConfigForReplay(parent))
	if err != nil {
		// The transaction SHAMap root was proven against the canonical header;
		// failure here means local transaction-engine divergence, not bad peer
		// data. Preserve the full-state fallback as a safe recovery path.
		r.logger.Error("ENGINE DIVERGENCE: standard transaction replay apply failed",
			"seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]),
			"error", err,
		)
		fallback(err)
		return
	}
	r.logger.Info("acquired ledger via standard transaction replay",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"peer", peerID,
	)
	if err := r.adoptVerifiedLedger(derived); err != nil {
		r.logger.Warn("failed to store standard transaction replay ledger", "error", err)
	}
}
