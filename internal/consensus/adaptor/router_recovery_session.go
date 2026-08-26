package adaptor

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
)

type frozenPivotRetargetReason string

const (
	frozenPivotRetargetCapacity frozenPivotRetargetReason = "prepared_capacity"
	frozenPivotRetargetStalled  frozenPivotRetargetReason = "replay_stalled"
)

func (r *Router) beginFrozenPivotRecovery(seq uint32, hash [32]byte, peerID uint64) bool {
	if seq == 0 || hash == ([32]byte{}) {
		return false
	}

	r.acquisitionMu.Lock()
	if r.standardReplay.active {
		r.acquisitionMu.Unlock()
		return r.continueFrozenPivotRecovery(seq, hash, peerID)
	}
	r.standardReplay.generation++
	r.standardReplay.active = true
	r.standardReplay.applying = false
	r.standardReplay.pivotReady = false
	r.standardReplay.initialCandidate = false
	r.standardReplay.pivotSeq = seq
	r.standardReplay.pivotHash = hash
	r.standardReplay.anchorSeq = seq
	r.standardReplay.anchorHash = hash
	r.standardReplay.collectSeq = seq
	r.standardReplay.collectHash = hash
	r.standardReplay.targetSeq = seq
	r.standardReplay.targetHash = hash
	r.standardReplay.entries = make(map[uint32]*standardReplayEntry, standardReplayPreparedLimit)
	r.standardReplay.headBlockedAt = time.Time{}
	r.standardReplay.pivotStartedAt = time.Now()
	r.standardReplay.progressSampleAt = time.Time{}
	r.standardReplay.sampleAnchorSeq = seq
	r.standardReplay.stalledSamples = 0
	r.standardReplay.retargetAttemptAt = time.Time{}
	if r.consensusRecovery.targetHash != ([32]byte{}) {
		r.consensusRecovery.stepHash = hash
	}
	identity := r.standardReplayIdentityLocked()
	r.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
	il := r.fetchTracker.Find(hash)
	if il != nil && !il.TransactionOnly() {
		r.acquisitionMu.Unlock()
		return true
	}
	r.acquisitionMu.Unlock()

	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	if r.standardReplay.active && r.standardReplay.generation == identity.generation &&
		!r.standardReplay.pivotReady && r.standardReplay.pivotSeq == identity.pivotSeq &&
		r.standardReplay.pivotHash == identity.pivotHash {
		retired := r.cancelStandardReplayPipelineLocked()
		r.acquisitionMu.Unlock()
		r.replayCommitMu.Unlock()
		r.retireLegacyAcquisitions(retired)
		return false
	}
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()
	return false
}

// promoteResolvedFrozenPivot turns an already-running hash-only consensus
// acquisition into the fast-load frozen pivot as soon as its verified header
// supplies the sequence. Startup can receive a consensus view before peer
// status/validation bookkeeping has indexed hash -> sequence; without this
// promotion the full-state fetch remains outside standardReplay and no P+1
// transaction-only collector is armed.
func (r *Router) promoteResolvedFrozenPivot(il *inbound.Ledger, peerID uint64) bool {
	if il == nil || !il.SequenceInitiallyUnknown() || il.Reason() != inbound.ReasonConsensus ||
		il.TransactionOnly() || il.Seq() == 0 {
		return false
	}
	svc := r.adaptor.LedgerService()
	if svc == nil || !svc.IsFastLoadProvisional() || il.Seq() <= svc.GetClosedLedgerIndex() ||
		r.fetchTracker.Find(il.Hash()) != il {
		return false
	}

	r.acquisitionMu.Lock()
	active := r.standardReplay.active
	eligible := r.consensusRecovery.targetHash == il.Hash() || r.consensusRecovery.stepHash == il.Hash()
	r.acquisitionMu.Unlock()
	if active {
		return r.continueFrozenPivotRecovery(il.Seq(), il.Hash(), peerID)
	}
	if !eligible || !r.beginFrozenPivotRecovery(il.Seq(), il.Hash(), peerID) {
		return false
	}
	hash := il.Hash()
	r.logger.Info("promoted resolved hash-only acquisition to frozen recovery pivot",
		"seq", il.Seq(),
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	return true
}

func (r *Router) continueFrozenPivotRecovery(seq uint32, hash [32]byte, peerID uint64) bool {
	if seq == 0 || hash == ([32]byte{}) {
		return false
	}

	r.catchupMu.Lock()
	trustedReplacement := r.catchup.source != catchupSourcePeer &&
		r.catchup.seq == seq && r.catchup.hash == hash
	r.catchupMu.Unlock()

	r.acquisitionMu.Lock()
	if !r.standardReplay.active {
		r.acquisitionMu.Unlock()
		return false
	}

	conflict := (seq == r.standardReplay.pivotSeq && hash != r.standardReplay.pivotHash) ||
		(seq == r.standardReplay.anchorSeq && hash != r.standardReplay.anchorHash) ||
		(seq == r.standardReplay.targetSeq && hash != r.standardReplay.targetHash) ||
		(trustedReplacement && seq < r.standardReplay.targetSeq)
	if entry := r.standardReplay.entries[seq]; entry != nil && entry.hash != hash {
		conflict = true
	}
	if conflict {
		identity := r.standardReplayIdentityLocked()
		r.acquisitionMu.Unlock()
		r.cancelStandardReplayPipelineIdentity(identity)
		return false
	}
	if seq > r.standardReplay.targetSeq {
		r.standardReplay.targetSeq = seq
		r.standardReplay.targetHash = hash
		if trustedReplacement && r.consensusRecovery.targetHash != ([32]byte{}) {
			r.consensusRecovery.targetHash = hash
		}
	}
	r.acquisitionMu.Unlock()

	return r.refillStandardReplayCollector(peerID)
}

func (r *Router) completeFrozenPivotAcquisition(h *header.LedgerHeader, initialCandidate bool) bool {
	if h == nil {
		return false
	}

	r.acquisitionMu.Lock()
	if !r.standardReplay.active || r.standardReplay.pivotReady ||
		r.standardReplay.pivotSeq != h.LedgerIndex || r.standardReplay.pivotHash != h.Hash {
		r.acquisitionMu.Unlock()
		return false
	}
	r.standardReplay.pivotReady = true
	r.standardReplay.initialCandidate = initialCandidate
	now := time.Now()
	r.standardReplay.progressSampleAt = now
	r.standardReplay.sampleAnchorSeq = h.LedgerIndex
	r.standardReplay.stalledSamples = 0
	generation := r.standardReplay.generation
	reachedTarget := r.standardReplay.targetSeq == h.LedgerIndex && r.standardReplay.targetHash == h.Hash
	startDrain := false
	if entry := r.standardReplay.entries[h.LedgerIndex+1]; entry != nil &&
		(!entry.readyAt.IsZero() || entry.failed) && !r.standardReplay.applying {
		r.standardReplay.applying = true
		startDrain = true
	}
	r.acquisitionMu.Unlock()

	r.logger.Info("verified frozen recovery pivot",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"initial_candidate", initialCandidate,
	)
	if !reachedTarget {
		_, result := r.adaptor.recheckFullyValidated(h.LedgerIndex, h.Hash)
		r.recordCompletionRecheck(result)
		r.recordAcquiredSeqHash(h.LedgerIndex, h.Hash, h.ParentHash)
		if result == validationRecheckAccepted {
			r.promoteCompletedLedger(h.LedgerIndex, h.Hash)
		}
	}
	r.acquisitionMu.Lock()
	current := r.standardReplay.active && r.standardReplay.generation == generation &&
		r.standardReplay.pivotReady && r.standardReplay.pivotSeq == h.LedgerIndex &&
		r.standardReplay.pivotHash == h.Hash
	if current {
		if r.consensusRecovery.stepHash == h.Hash {
			r.consensusRecovery.stepHash = [32]byte{}
		}
		if r.consensusRecovery.targetHash != ([32]byte{}) {
			r.consensusRecovery.anchorSeq = h.LedgerIndex
			r.consensusRecovery.anchorHash = h.Hash
		}
	}
	r.acquisitionMu.Unlock()
	if !current {
		return true
	}
	if reachedTarget {
		r.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, initialCandidate)
		r.acquisitionMu.Lock()
		current = r.standardReplay.active && r.standardReplay.generation == generation &&
			r.standardReplay.pivotReady && r.standardReplay.pivotSeq == h.LedgerIndex &&
			r.standardReplay.pivotHash == h.Hash
		if current && r.standardReplay.targetSeq == h.LedgerIndex && r.standardReplay.targetHash == h.Hash {
			r.standardReplay.active = false
			r.standardReplay.entries = nil
		}
		r.acquisitionMu.Unlock()
		if !current {
			return true
		}
	}
	r.refillStandardReplayCollector(0)
	if startDrain {
		r.drainStandardReplayPipeline()
	}
	return true
}

func (r *Router) rebootstrapFrozenPivotIfStalled(now time.Time) bool {
	r.acquisitionMu.Lock()
	if !r.standardReplay.active {
		r.acquisitionMu.Unlock()
		return false
	}

	reason := frozenPivotRetargetReason("")
	if !r.standardReplay.pivotReady && len(r.standardReplay.entries) >= standardReplayRetargetThreshold {
		reason = frozenPivotRetargetCapacity
	} else {
		if !r.standardReplay.pivotReady || r.standardReplay.applying ||
			r.standardReplay.targetSeq <= r.standardReplay.anchorSeq {
			r.acquisitionMu.Unlock()
			return false
		}
		if r.standardReplay.progressSampleAt.IsZero() {
			r.standardReplay.progressSampleAt = now
			r.standardReplay.sampleAnchorSeq = r.standardReplay.anchorSeq
			r.acquisitionMu.Unlock()
			return false
		}
		if now.Sub(r.standardReplay.progressSampleAt) < standardReplayProgressWindow {
			r.acquisitionMu.Unlock()
			return false
		}

		if r.standardReplay.anchorSeq > r.standardReplay.sampleAnchorSeq {
			r.standardReplay.stalledSamples = 0
			r.standardReplay.retargetAttemptAt = time.Time{}
		} else if r.standardReplay.stalledSamples < ^uint8(0) {
			r.standardReplay.stalledSamples++
		}
		r.standardReplay.progressSampleAt = now
		r.standardReplay.sampleAnchorSeq = r.standardReplay.anchorSeq
		if r.standardReplay.stalledSamples < standardReplayStallWindows {
			r.acquisitionMu.Unlock()
			return false
		}
		reason = frozenPivotRetargetStalled
	}

	if !r.standardReplay.retargetAttemptAt.IsZero() &&
		now.Sub(r.standardReplay.retargetAttemptAt) < standardReplayProgressWindow {
		r.acquisitionMu.Unlock()
		return false
	}
	r.standardReplay.retargetAttemptAt = now
	generation := r.standardReplay.generation
	frontierSeq := r.standardReplay.anchorSeq
	pivotSeq := r.standardReplay.pivotSeq
	r.acquisitionMu.Unlock()

	return r.retargetFrozenPivot(generation, frontierSeq, pivotSeq, reason, now)
}

func (r *Router) retargetFrozenPivot(
	generation uint64,
	frontierSeq uint32,
	pivotSeq uint32,
	reason frozenPivotRetargetReason,
	now time.Time,
) bool {
	r.catchupMu.Lock()
	target := r.catchup
	r.catchupMu.Unlock()
	minimumSeq := pivotSeq
	if reason == frozenPivotRetargetStalled {
		minimumSeq = frontierSeq
	}
	if target.source != catchupSourceQuorum || target.seq <= minimumSeq || target.hash == ([32]byte{}) {
		r.replayPipelineRetargetFailures.Add(1)
		r.logger.Warn("cannot retarget frozen recovery pivot without a newer quorum target",
			"reason", string(reason),
			"pivot_seq", pivotSeq,
			"frontier_seq", frontierSeq,
			"trusted_head_seq", target.seq,
		)
		return false
	}

	peerID, ok := r.resolveAcquisitionPeer(target.seq, target.peerID)
	if !ok {
		r.replayPipelineRetargetFailures.Add(1)
		r.logger.Warn("cannot retarget frozen recovery pivot without an acquisition peer",
			"reason", string(reason),
			"pivot_seq", pivotSeq,
			"frontier_seq", frontierSeq,
			"trusted_head_seq", target.seq,
		)
		return false
	}
	if r.belowFloor(target.seq) || r.catchupRetryBlocked(target.hash, now) {
		r.replayPipelineRetargetFailures.Add(1)
		r.logger.Warn("cannot retarget frozen recovery pivot while the quorum target is not admissible",
			"reason", string(reason),
			"pivot_seq", pivotSeq,
			"frontier_seq", frontierSeq,
			"trusted_head_seq", target.seq,
		)
		return false
	}

	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	capacityCurrent := reason == frozenPivotRetargetCapacity && !r.standardReplay.pivotReady &&
		len(r.standardReplay.entries) >= standardReplayRetargetThreshold
	stallCurrent := reason == frozenPivotRetargetStalled && r.standardReplay.pivotReady &&
		!r.standardReplay.applying && r.standardReplay.stalledSamples >= standardReplayStallWindows
	if !r.standardReplay.active || r.standardReplay.generation != generation ||
		r.standardReplay.anchorSeq != frontierSeq || (!capacityCurrent && !stallCurrent) {
		r.acquisitionMu.Unlock()
		r.replayCommitMu.Unlock()
		return false
	}
	preparedTailSeq := r.standardReplay.collectSeq
	preparedOccupancy := len(r.standardReplay.entries)
	pivotHash := r.standardReplay.pivotHash
	pivotStartedAt := r.standardReplay.pivotStartedAt
	pivotStateRate := uint64(0)
	if pivot := r.fetchTracker.Find(pivotHash); pivot != nil && !pivotStartedAt.IsZero() {
		elapsed := now.Sub(pivotStartedAt)
		if elapsed > 0 {
			pivotStateRate = uint64(float64(pivot.Snapshot().StateUseful) / elapsed.Seconds())
		}
	}
	retired := r.cancelStandardReplayPipelineLocked()
	retired = append(retired, r.discardSupersededProvisionalFullStateLocked(target.hash)...)
	r.consensusRecovery.targetHash = target.hash
	r.consensusRecovery.anchorSeq = 0
	r.consensusRecovery.anchorHash = [32]byte{}
	r.consensusRecovery.stepHash = [32]byte{}
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()
	r.retireLegacyAcquisitions(retired)
	if reason == frozenPivotRetargetCapacity {
		r.replayPipelineCapacityRetargets.Add(1)
	} else {
		r.replayPipelineFallbacks.Add(1)
	}
	r.logger.Warn("retargeting frozen recovery to a newer full-state pivot",
		"reason", string(reason),
		"pivot_seq", pivotSeq,
		"frontier_seq", frontierSeq,
		"prepared_tail_seq", preparedTailSeq,
		"trusted_head_seq", target.seq,
		"trusted_head_hash", fmt.Sprintf("%x", target.hash[:8]),
		"prepared_occupancy", preparedOccupancy,
		"prepared_limit", standardReplayPreparedLimit,
		"pivot_state_nodes_per_sec", pivotStateRate,
	)
	if r.beginFrozenPivotRecovery(target.seq, target.hash, peerID) {
		return true
	}
	r.replayPipelineRetargetFailures.Add(1)
	r.logger.Warn("failed to start replacement full-state pivot",
		"reason", string(reason),
		"target_seq", target.seq,
		"target_hash", fmt.Sprintf("%x", target.hash[:8]),
	)
	return false
}

func (r *Router) failFrozenPivotRecovery(hash [32]byte) bool {
	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	if !r.standardReplay.active || r.standardReplay.pivotReady || r.standardReplay.pivotHash != hash {
		r.acquisitionMu.Unlock()
		r.replayCommitMu.Unlock()
		return false
	}
	retired := r.cancelStandardReplayPipelineLocked()
	if r.consensusRecovery.stepHash == hash {
		r.consensusRecovery.stepHash = [32]byte{}
	}
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()
	r.retireLegacyAcquisitions(retired)
	return true
}
