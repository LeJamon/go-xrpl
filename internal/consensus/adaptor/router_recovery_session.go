package adaptor

import (
	"context"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
)

type frozenPivotRetargetReason string

const frozenPivotRetargetStalled frozenPivotRetargetReason = "replay_stalled"

func (c *catchupReplayCoordinator) beginFrozenPivotRecovery(seq uint32, hash [32]byte, peerID uint64) bool {
	if c.stoppedForShutdown() || seq == 0 || hash == ([32]byte{}) {
		return false
	}
	if c.retireLocallySatisfiedFrozenPivot("local_frontier") {
		return false
	}

	c.acquisitionMu.Lock()
	if c.standardReplay.active {
		c.acquisitionMu.Unlock()
		return c.continueFrozenPivotRecovery(seq, hash, peerID)
	}
	c.acquisitionMu.Unlock()
	if c.locallySatisfiesLedger(seq, hash) {
		return false
	}

	var baseRoot [32]byte
	var baseRelease func()
	baseCtx := c.lifecycleContext()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	baseCtx, cancelBase := context.WithTimeout(baseCtx, time.Second)
	defer cancelBase()
	if c.adaptor != nil {
		if svc := c.adaptor.LedgerService(); svc != nil {
			root, release, ok, err := svc.AcquireValidatedStateBase(baseCtx)
			if err != nil {
				c.logger.Warn("checkpoint-relative pivot discovery unavailable", "error", err)
			} else if ok {
				baseRoot = root
				baseRelease = release
			}
		}
	}
	if c.locallySatisfiesLedger(seq, hash) {
		if baseRelease != nil {
			baseRelease()
		}
		return false
	}

	c.acquisitionMu.Lock()
	if c.stoppedForShutdown() {
		c.acquisitionMu.Unlock()
		if baseRelease != nil {
			baseRelease()
		}
		return false
	}
	if c.standardReplay.active {
		c.acquisitionMu.Unlock()
		if baseRelease != nil {
			baseRelease()
		}
		return c.continueFrozenPivotRecovery(seq, hash, peerID)
	}
	c.standardReplay.generation++
	c.standardReplay.active = true
	c.standardReplay.applying = false
	c.standardReplay.pivotReady = false
	c.standardReplay.initialCandidate = false
	c.standardReplay.pivotSeq = seq
	c.standardReplay.pivotHash = hash
	c.standardReplay.anchorSeq = seq
	c.standardReplay.anchorHash = hash
	c.standardReplay.collectSeq = seq
	c.standardReplay.collectHash = hash
	c.standardReplay.targetSeq = seq
	c.standardReplay.targetHash = hash
	c.standardReplay.entries = make(map[uint32]*standardReplayEntry, standardReplayPreparedLimit)
	c.standardReplay.headBlockedAt = time.Time{}
	c.standardReplay.pivotStartedAt = time.Now()
	c.standardReplay.progressSampleAt = time.Time{}
	c.standardReplay.sampleAnchorSeq = seq
	c.standardReplay.stalledSamples = 0
	c.standardReplay.retargetAttemptAt = time.Time{}
	c.standardReplay.backpressured = false
	c.standardReplay.pivotHandoff = nil
	c.standardReplay.baseRelease = baseRelease
	if c.consensusRecovery.targetHash != ([32]byte{}) {
		c.consensusRecovery.stepHash = hash
	}
	identity := c.standardReplayIdentityLocked()
	c.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
	il := c.fetchTracker.Find(hash)
	if il != nil && !il.TransactionOnly() {
		if baseRoot != ([32]byte{}) {
			if err := il.SetVerifiedStateBaseContext(baseCtx, baseRoot); err != nil {
				c.logger.Warn("checkpoint-relative pivot discovery unavailable", "error", err)
				c.releaseStandardReplayBaseLocked()
			} else {
				c.standardReplay.baseLedger = il
			}
		}
		c.acquisitionMu.Unlock()
		return true
	}
	c.acquisitionMu.Unlock()

	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if c.standardReplay.active && c.standardReplay.generation == identity.generation &&
		!c.standardReplay.pivotReady && c.standardReplay.pivotSeq == identity.pivotSeq &&
		c.standardReplay.pivotHash == identity.pivotHash {
		retired := c.cancelStandardReplayPipelineLocked("pivot_start_failed")
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		c.retireStandardReplay(retired)
		return false
	}
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	return false
}

// promoteResolvedFrozenPivot turns an already-running hash-only consensus
// acquisition into the fast-load frozen pivot as soon as its verified header
// supplies the sequence. Startup can receive a consensus view before peer
// status/validation bookkeeping has indexed hash -> sequence; without this
// promotion the full-state fetch remains outside standardReplay and no P+1
// transaction-only collector is armed.
func (c *catchupReplayCoordinator) promoteResolvedFrozenPivot(il *inbound.Ledger, peerID uint64) bool {
	if il == nil || !il.SequenceInitiallyUnknown() || il.Reason() != inbound.ReasonConsensus ||
		il.TransactionOnly() || il.Seq() == 0 {
		return false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil || !svc.IsFastLoadProvisional() || il.Seq() <= svc.GetClosedLedgerIndex() ||
		c.fetchTracker.Find(il.Hash()) != il {
		return false
	}

	c.acquisitionMu.Lock()
	active := c.standardReplay.active
	eligible := c.consensusRecovery.targetHash == il.Hash() || c.consensusRecovery.stepHash == il.Hash()
	c.acquisitionMu.Unlock()
	if active {
		return c.continueFrozenPivotRecovery(il.Seq(), il.Hash(), peerID)
	}
	if !eligible || !c.beginFrozenPivotRecovery(il.Seq(), il.Hash(), peerID) {
		return false
	}
	hash := il.Hash()
	c.logger.Info("promoted resolved hash-only acquisition to frozen recovery pivot",
		"seq", il.Seq(),
		"hash", fmt.Sprintf("%x", hash[:8]),
		"peer", peerID,
	)
	return true
}

func (c *catchupReplayCoordinator) continueFrozenPivotRecovery(seq uint32, hash [32]byte, peerID uint64) bool {
	if seq == 0 || hash == ([32]byte{}) {
		return false
	}
	if c.retireLocallySatisfiedFrozenPivot("local_frontier") {
		return false
	}

	c.catchupMu.Lock()
	trustedReplacement := c.catchup.source != catchupSourcePeer &&
		c.catchup.seq == seq && c.catchup.hash == hash
	c.catchupMu.Unlock()

	c.acquisitionMu.Lock()
	if !c.standardReplay.active {
		c.acquisitionMu.Unlock()
		return false
	}

	conflict := (seq == c.standardReplay.pivotSeq && hash != c.standardReplay.pivotHash) ||
		(seq == c.standardReplay.anchorSeq && hash != c.standardReplay.anchorHash) ||
		(seq == c.standardReplay.targetSeq && hash != c.standardReplay.targetHash)
	if entry := c.standardReplay.entries[seq]; entry != nil && entry.hash != hash {
		conflict = true
	}
	if conflict {
		identity := c.standardReplayIdentityLocked()
		c.acquisitionMu.Unlock()
		c.cancelStandardReplayPipelineIdentity(identity, "pivot_conflict")
		return false
	}
	if seq > c.standardReplay.targetSeq {
		c.standardReplay.targetSeq = seq
		c.standardReplay.targetHash = hash
		if trustedReplacement && c.consensusRecovery.targetHash != ([32]byte{}) {
			c.consensusRecovery.targetHash = hash
		}
	}
	c.acquisitionMu.Unlock()

	return c.refillStandardReplayCollector(peerID)
}

func (c *catchupReplayCoordinator) locallySatisfiesLedger(seq uint32, hash [32]byte) bool {
	if seq == 0 || hash == ([32]byte{}) || c.adaptor == nil {
		return false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return false
	}
	if closed := svc.GetClosedLedger(); closed != nil && closed.Sequence() == seq && closed.Hash() == hash {
		return true
	}
	if validated := svc.GetValidatedLedger(); validated != nil {
		if validated.Sequence() > seq || validated.Sequence() == seq && validated.Hash() == hash {
			return true
		}
	}
	held, err := svc.GetLedgerByHash(hash)
	return err == nil && held != nil && held.Sequence() == seq
}

func (c *catchupReplayCoordinator) consensusHandoffComplete(seq uint32, hash [32]byte) bool {
	if seq == 0 || hash == ([32]byte{}) || c.adaptor == nil {
		return false
	}
	svc := c.adaptor.LedgerService()
	if svc == nil {
		return false
	}
	if closed := svc.GetClosedLedger(); closed != nil && closed.Sequence() == seq && closed.Hash() == hash {
		return true
	}
	validated := svc.GetValidatedLedger()
	return validated != nil && validated.Sequence() > seq
}

func (c *catchupReplayCoordinator) retireLocallySatisfiedFrozenPivot(reason string) bool {
	c.acquisitionMu.Lock()
	if !c.standardReplay.active || c.standardReplay.pivotReady || c.standardReplay.pivotHandoff != nil {
		c.acquisitionMu.Unlock()
		return false
	}
	seq := c.standardReplay.pivotSeq
	hash := c.standardReplay.pivotHash
	c.acquisitionMu.Unlock()
	if !c.locallySatisfiesLedger(seq, hash) {
		return false
	}
	return c.retireLocallySatisfiedLedger(seq, hash, reason)
}

func (c *catchupReplayCoordinator) retireLocallySatisfiedLedger(seq uint32, hash [32]byte, reason string) bool {
	if seq == 0 || hash == ([32]byte{}) {
		return false
	}
	handoffComplete := c.consensusHandoffComplete(seq, hash)

	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if c.standardReplay.active && c.standardReplay.pivotHandoff != nil &&
		c.standardReplay.pivotSeq == seq && c.standardReplay.pivotHash == hash {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return false
	}
	retirement := standardReplayRetirement{}
	retiredPipeline := c.standardReplay.active && !c.standardReplay.pivotReady && c.standardReplay.pivotSeq == seq &&
		c.standardReplay.pivotHash == hash
	if retiredPipeline {
		retirement = c.cancelStandardReplayPipelineLocked(reason)
	}
	if acquisition := c.fetchTracker.Find(hash); acquisition != nil &&
		c.fetchTracker.DiscardExpected(acquisition) {
		retirement.ledgers = append(retirement.ledgers, acquisition)
	}
	retiredReplay := c.replayer.Has(hash)
	if retiredReplay {
		c.replayer.Abandon(hash)
	}
	releasedRecovery := false
	if c.consensusRecovery.targetHash == hash {
		if handoffComplete {
			c.consensusRecovery = consensusRecovery{}
		} else {
			c.consensusRecovery.stepHash = [32]byte{}
		}
		releasedRecovery = true
	} else if c.consensusRecovery.stepHash == hash {
		c.consensusRecovery.stepHash = [32]byte{}
		releasedRecovery = true
	}
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()

	retiredLegacy := len(retirement.ledgers)
	c.retireStandardReplay(retirement)
	retired := retiredPipeline || retiredLegacy != 0 || retiredReplay || releasedRecovery
	if retired {
		c.logger.Info("retired locally satisfied recovery work",
			"reason", reason,
			"seq", seq,
			"hash", fmt.Sprintf("%x", hash[:8]),
			"pipeline", retiredPipeline,
			"legacy", retiredLegacy,
			"replay", retiredReplay,
		)
	}
	return retired
}

func (c *catchupReplayCoordinator) completeFrozenPivotAcquisitionOwned(
	h *header.LedgerHeader,
	initialCandidate bool,
	handoff standardReplayPivotHandoff,
) bool {
	if h == nil {
		return false
	}

	c.acquisitionMu.Lock()
	if !c.standardReplay.active || c.standardReplay.pivotReady ||
		c.standardReplay.pivotSeq != h.LedgerIndex || c.standardReplay.pivotHash != h.Hash {
		c.acquisitionMu.Unlock()
		return false
	}
	if !c.standardReplayPivotHandoffMatchesLocked(handoff) {
		c.acquisitionMu.Unlock()
		return false
	}
	c.standardReplay.pivotReady = true
	c.standardReplay.initialCandidate = initialCandidate
	now := time.Now()
	c.standardReplay.progressSampleAt = now
	c.standardReplay.sampleAnchorSeq = h.LedgerIndex
	c.standardReplay.stalledSamples = 0
	generation := c.standardReplay.generation
	c.clearStandardReplayPivotHandoffLocked(handoff)
	reachedTarget := c.standardReplay.targetSeq == h.LedgerIndex && c.standardReplay.targetHash == h.Hash
	startDrain := false
	if entry := c.standardReplay.entries[h.LedgerIndex+1]; entry != nil &&
		(!entry.readyAt.IsZero() || entry.failed) && !c.standardReplay.applying {
		c.standardReplay.applying = true
		startDrain = true
	}
	c.acquisitionMu.Unlock()

	c.logger.Info("verified frozen recovery pivot",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
		"initial_candidate", initialCandidate,
	)
	if !reachedTarget {
		_, result := c.adaptor.recheckFullyValidated(h.LedgerIndex, h.Hash)
		c.recordCompletionRecheck(result)
		c.recordAcquiredSeqHash(h.LedgerIndex, h.Hash, h.ParentHash)
		if result == validationRecheckAccepted {
			c.promoteCompletedLedger(h.LedgerIndex, h.Hash)
		}
	}
	c.acquisitionMu.Lock()
	current := c.standardReplay.active && c.standardReplay.generation == generation &&
		c.standardReplay.pivotReady && c.standardReplay.pivotSeq == h.LedgerIndex &&
		c.standardReplay.pivotHash == h.Hash
	if current {
		if c.consensusRecovery.stepHash == h.Hash {
			c.consensusRecovery.stepHash = [32]byte{}
		}
		if c.consensusRecovery.targetHash != ([32]byte{}) {
			c.consensusRecovery.anchorSeq = h.LedgerIndex
			c.consensusRecovery.anchorHash = h.Hash
		}
	}
	c.acquisitionMu.Unlock()
	if !current {
		return true
	}
	if reachedTarget {
		c.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, initialCandidate)
		c.acquisitionMu.Lock()
		current = c.standardReplay.active && c.standardReplay.generation == generation &&
			c.standardReplay.pivotReady && c.standardReplay.pivotSeq == h.LedgerIndex &&
			c.standardReplay.pivotHash == h.Hash
		if current && c.standardReplay.targetSeq == h.LedgerIndex && c.standardReplay.targetHash == h.Hash {
			c.standardReplay.active = false
			c.standardReplay.entries = nil
			c.standardReplay.backpressured = false
			c.releaseStandardReplayBaseLocked()
		}
		c.acquisitionMu.Unlock()
		if !current {
			return true
		}
	}
	c.refillStandardReplayCollector(0)
	if startDrain {
		c.drainStandardReplayPipeline()
	}
	return true
}

func (c *catchupReplayCoordinator) rearmFrozenPivotAcquisition(
	generation uint64,
	seq uint32,
	hash [32]byte,
	now time.Time,
) bool {
	if seq == 0 || hash == ([32]byte{}) || c.adaptor == nil ||
		c.belowFloor(seq) || c.catchupRetryBlocked(hash, now) {
		return false
	}
	peerID, ok := c.resolveAcquisitionPeer(seq, 0)
	if !ok {
		return false
	}

	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if !c.standardReplay.active || c.standardReplay.generation != generation ||
		c.standardReplay.pivotReady || c.standardReplay.pivotHandoff != nil ||
		c.standardReplay.pivotSeq != seq || c.standardReplay.pivotHash != hash ||
		c.fetchTracker.Find(hash) != nil || c.replayer.Has(hash) ||
		!c.canAdmitCatchupLocked(hash, maxConcurrentSpeculativeCatchup) {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return false
	}
	c.startLedgerAcquisitionLegacyLocked(seq, hash, peerID)
	acquisition := c.fetchTracker.Find(hash)
	if acquisition != nil {
		c.standardReplay.pivotStartedAt = now
		c.standardReplay.backpressured = false
		if c.consensusRecovery.targetHash != ([32]byte{}) {
			c.consensusRecovery.stepHash = hash
		}
	}
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	if acquisition == nil {
		return false
	}
	c.logger.Info("re-armed orphaned frozen recovery pivot",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
		"generation", generation,
		"peer", peerID,
	)
	return true
}

func (c *catchupReplayCoordinator) rebootstrapFrozenPivotIfStalled(now time.Time) bool {
	if c.headerDiscoveryRepairPending(now) {
		return false
	}
	c.acquisitionMu.Lock()
	if !c.standardReplay.active {
		c.acquisitionMu.Unlock()
		return false
	}

	if !c.standardReplay.pivotReady {
		if c.standardReplay.pivotHandoff != nil {
			c.acquisitionMu.Unlock()
			return false
		}
		generation := c.standardReplay.generation
		pivotSeq := c.standardReplay.pivotSeq
		pivotHash := c.standardReplay.pivotHash
		if pivotHash == ([32]byte{}) || c.fetchTracker.Find(pivotHash) != nil || c.replayer.Has(pivotHash) {
			c.acquisitionMu.Unlock()
			return false
		}
		c.acquisitionMu.Unlock()
		return c.rearmFrozenPivotAcquisition(generation, pivotSeq, pivotHash, now)
	}

	if c.standardReplayDrainOwner != nil {
		c.acquisitionMu.Unlock()
		return false
	}
	if c.scheduleReadyStandardReplayDrainLocked() {
		c.acquisitionMu.Unlock()
		return true
	}
	// A flag without an executing owner or a ready head is not progress.
	// Clear it so a missing-head stall can still reach normal recovery.
	c.standardReplay.applying = false
	if c.standardReplay.targetSeq <= c.standardReplay.anchorSeq {
		c.acquisitionMu.Unlock()
		return false
	}
	if c.standardReplay.progressSampleAt.IsZero() {
		c.standardReplay.progressSampleAt = now
		c.standardReplay.sampleAnchorSeq = c.standardReplay.anchorSeq
		c.acquisitionMu.Unlock()
		return false
	}
	if now.Sub(c.standardReplay.progressSampleAt) < standardReplayProgressWindow {
		c.acquisitionMu.Unlock()
		return false
	}

	if c.standardReplay.anchorSeq > c.standardReplay.sampleAnchorSeq {
		c.standardReplay.stalledSamples = 0
		c.standardReplay.retargetAttemptAt = time.Time{}
	} else if c.standardReplay.stalledSamples < ^uint8(0) {
		c.standardReplay.stalledSamples++
	}
	c.standardReplay.progressSampleAt = now
	c.standardReplay.sampleAnchorSeq = c.standardReplay.anchorSeq
	if c.standardReplay.stalledSamples < standardReplayStallWindows {
		c.acquisitionMu.Unlock()
		return false
	}

	if !c.standardReplay.retargetAttemptAt.IsZero() &&
		now.Sub(c.standardReplay.retargetAttemptAt) < standardReplayProgressWindow {
		c.acquisitionMu.Unlock()
		return false
	}
	c.standardReplay.retargetAttemptAt = now
	generation := c.standardReplay.generation
	frontierSeq := c.standardReplay.anchorSeq
	pivotSeq := c.standardReplay.pivotSeq
	c.acquisitionMu.Unlock()

	return c.retargetFrozenPivot(generation, frontierSeq, pivotSeq, frozenPivotRetargetStalled, now)
}

func (c *catchupReplayCoordinator) retargetFrozenPivot(
	generation uint64,
	frontierSeq uint32,
	pivotSeq uint32,
	reason frozenPivotRetargetReason,
	now time.Time,
) bool {
	c.catchupMu.Lock()
	target := c.catchup
	c.catchupMu.Unlock()
	if target.source != catchupSourceQuorum || target.seq <= frontierSeq || target.hash == ([32]byte{}) {
		c.replayPipelineRetargetFailures.Add(1)
		c.logger.Warn("cannot retarget frozen recovery pivot without a newer quorum target",
			"reason", string(reason),
			"pivot_seq", pivotSeq,
			"frontier_seq", frontierSeq,
			"trusted_head_seq", target.seq,
		)
		return false
	}

	peerID, ok := c.resolveAcquisitionPeer(target.seq, target.peerID)
	if !ok {
		c.replayPipelineRetargetFailures.Add(1)
		c.logger.Warn("cannot retarget frozen recovery pivot without an acquisition peer",
			"reason", string(reason),
			"pivot_seq", pivotSeq,
			"frontier_seq", frontierSeq,
			"trusted_head_seq", target.seq,
		)
		return false
	}
	if c.belowFloor(target.seq) || c.catchupRetryBlocked(target.hash, now) {
		c.replayPipelineRetargetFailures.Add(1)
		c.logger.Warn("cannot retarget frozen recovery pivot while the quorum target is not admissible",
			"reason", string(reason),
			"pivot_seq", pivotSeq,
			"frontier_seq", frontierSeq,
			"trusted_head_seq", target.seq,
		)
		return false
	}

	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	stallCurrent := reason == frozenPivotRetargetStalled && c.standardReplay.pivotReady &&
		!c.standardReplay.applying && c.standardReplay.stalledSamples >= standardReplayStallWindows
	if !c.standardReplay.active || c.standardReplay.generation != generation ||
		c.standardReplay.anchorSeq != frontierSeq || !stallCurrent {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return false
	}
	preparedTailSeq := c.standardReplay.collectSeq
	preparedOccupancy := len(c.standardReplay.entries)
	pivotHash := c.standardReplay.pivotHash
	pivotStartedAt := c.standardReplay.pivotStartedAt
	pivotStateRate := uint64(0)
	if pivot := c.fetchTracker.Find(pivotHash); pivot != nil && !pivotStartedAt.IsZero() {
		elapsed := now.Sub(pivotStartedAt)
		if elapsed > 0 {
			pivotStateRate = uint64(float64(pivot.Snapshot().StateUseful) / elapsed.Seconds())
		}
	}
	retired := c.cancelStandardReplayPipelineLocked(string(reason))
	retired.ledgers = append(retired.ledgers, c.discardSupersededProvisionalFullStateLocked(target.hash)...)
	c.consensusRecovery.targetHash = target.hash
	c.consensusRecovery.anchorSeq = 0
	c.consensusRecovery.anchorHash = [32]byte{}
	c.consensusRecovery.stepHash = [32]byte{}
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	c.retireStandardReplay(retired)
	c.replayPipelineFallbacks.Add(1)
	c.logger.Warn("retargeting frozen recovery to a newer full-state pivot",
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
	if c.beginFrozenPivotRecovery(target.seq, target.hash, peerID) {
		return true
	}
	c.replayPipelineRetargetFailures.Add(1)
	c.logger.Warn("failed to start replacement full-state pivot",
		"reason", string(reason),
		"target_seq", target.seq,
		"target_hash", fmt.Sprintf("%x", target.hash[:8]),
	)
	return false
}

func (c *catchupReplayCoordinator) failFrozenPivotHandoff(handoff standardReplayPivotHandoff) bool {
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	if !c.standardReplayPivotHandoffMatchesLocked(handoff) || c.standardReplay.pivotReady {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return false
	}
	retired := c.cancelStandardReplayPipelineLocked("pivot_handoff_failed")
	if c.consensusRecovery.stepHash == handoff.hash {
		c.consensusRecovery.stepHash = [32]byte{}
	}
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	c.retireStandardReplay(retired)
	return true
}
