package adaptor

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
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
	r.standardReplay.progressSampleAt = time.Time{}
	r.standardReplay.sampleAnchorSeq = seq
	r.standardReplay.stalledSamples = 0
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
	if !r.standardReplay.active || !r.standardReplay.pivotReady || r.standardReplay.applying ||
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
	} else if r.standardReplay.stalledSamples < ^uint8(0) {
		r.standardReplay.stalledSamples++
	}
	r.standardReplay.progressSampleAt = now
	r.standardReplay.sampleAnchorSeq = r.standardReplay.anchorSeq
	if r.standardReplay.stalledSamples < standardReplayStallWindows {
		r.acquisitionMu.Unlock()
		return false
	}
	generation := r.standardReplay.generation
	frontierSeq := r.standardReplay.anchorSeq
	r.acquisitionMu.Unlock()

	r.replayCommitMu.Lock()
	r.acquisitionMu.Lock()
	if !r.standardReplay.active || !r.standardReplay.pivotReady ||
		r.standardReplay.generation != generation || r.standardReplay.anchorSeq != frontierSeq ||
		r.standardReplay.stalledSamples < standardReplayStallWindows {
		r.acquisitionMu.Unlock()
		r.replayCommitMu.Unlock()
		return false
	}
	targetSeq := r.standardReplay.targetSeq
	targetHash := r.standardReplay.targetHash
	retired := r.cancelStandardReplayPipelineLocked()
	r.consensusRecovery.anchorSeq = 0
	r.consensusRecovery.anchorHash = [32]byte{}
	r.consensusRecovery.stepHash = [32]byte{}
	r.acquisitionMu.Unlock()
	r.replayCommitMu.Unlock()
	r.retireLegacyAcquisitions(retired)
	r.replayPipelineFallbacks.Add(1)
	r.logger.Warn("replay recovery is not converging; selecting a new full-state pivot",
		"frontier_seq", frontierSeq,
		"target_seq", targetSeq,
		"target_hash", fmt.Sprintf("%x", targetHash[:8]),
	)
	if peerID, ok := r.resolveAcquisitionPeer(targetSeq, 0); ok {
		r.beginFrozenPivotRecovery(targetSeq, targetHash, peerID)
	}
	return true
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
