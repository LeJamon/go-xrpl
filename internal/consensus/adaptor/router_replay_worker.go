package adaptor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/shamap"
)

var errStandardReplayHeadFailed = errors.New("standard replay head failed")

type standardReplayDrainResult struct {
	coordinator         *catchupReplayCoordinator
	owner               *standardReplayDrainOwner
	generation          uint64
	entry               *standardReplayEntry
	startedAt           time.Time
	applyDuration       time.Duration
	persistDuration     time.Duration
	preparationDuration time.Duration
	lockWaitDuration    time.Duration
	queueDuration       time.Duration
	enqueuedAt          time.Time
	parentStats         service.ReplayAdmissionStats
	ledgerStats         service.ReplayAdmissionStats
	header              header.LedgerHeader
	initialCandidate    bool
	prepared            *service.PreparedLedger
	replayRelease       func()
	err                 error
	processingAccount   *shamap.AcquisitionBudget
	processingReserved  bool
	entryRetained       bool
	memoryRequired      uint64
	dropEntryPayload    bool
	releaseOnce         sync.Once
}

func (r *standardReplayDrainResult) releaseResources() {
	if r == nil {
		return
	}
	r.releaseOnce.Do(func() {
		if r.prepared != nil {
			r.prepared.Release()
			r.prepared = nil
		}
		if r.replayRelease != nil {
			r.replayRelease()
			r.replayRelease = nil
		}
		if r.coordinator != nil {
			r.coordinator.acquisitionMu.Lock()
			if r.dropEntryPayload && r.entry != nil {
				// A result may remain buffered after shutdown. Drop map and
				// acquisition ownership here so the queue cannot retain a
				// detached replay payload after its generation is retired.
				r.entry.txMap = nil
				r.entry.acquisition = nil
				r.entry.replayPayloadBytes = 0
			}
			if r.entryRetained && r.entry != nil && r.entry.replayMemoryActive != 0 {
				r.entry.replayMemoryActive--
			}
			if r.entryRetained && r.entry != nil && r.entry.replayMemoryActive == 0 && r.entry.replayMemoryReleasePending {
				r.entry.replayMemory.release()
				r.entry.replayMemoryReleasePending = false
			}
			r.coordinator.acquisitionMu.Unlock()
		}
		if r.processingReserved {
			releaseStandardReplayProcessing(r.processingAccount, standardReplayProcessingBytes)
			r.processingReserved = false
		}
		r.processingAccount = nil
	})
}

type standardReplayWorkerClaimStatus uint8

const (
	standardReplayWorkerClaimed standardReplayWorkerClaimStatus = iota + 1
	standardReplayWorkerIdle
	standardReplayWorkerMemoryWait
	standardReplayWorkerCanceled
)

type standardReplayWorkerClaim struct {
	status             standardReplayWorkerClaimStatus
	entry              *standardReplayEntry
	copy               standardReplayEntry
	generation         uint64
	processingAccount  *shamap.AcquisitionBudget
	processingReserved bool
	entryRetained      bool
}

// startStandardReplayDrain claims the current generation and moves one replay
// entry through preparation on the lifecycle worker. The router receives the
// result and remains the only place that advances the replay frontier.
func (c *catchupReplayCoordinator) startStandardReplayDrain() {
	if c == nil {
		return
	}
	c.lifecycleMu.RLock()
	lifecycleReady := !c.stopped.Load() && c.lifecycleCtx != nil && c.lifecycleCancel != nil
	c.lifecycleMu.RUnlock()
	if !lifecycleReady {
		// Construction-time and unit-test coordinators do not start the
		// lifecycle worker. Preserve their direct-drive behavior without
		// creating an intermediate owner that can enqueue a redundant wake.
		c.drainStandardReplayPipeline()
		return
	}
	owner := c.beginStandardReplayDrain()
	if owner == nil {
		return
	}
	if c.runLifecycleTask(func(_ context.Context) {
		c.runStandardReplayDrain(owner)
	}) {
		return
	}
	owner.cancelWork()
	c.finishStandardReplayDrain(owner)
	// If the lifecycle retired between the readiness check and task launch,
	// preserve the direct-drive behavior for that transition.
	c.drainStandardReplayPipeline()
}

func (c *catchupReplayCoordinator) runStandardReplayDrain(owner *standardReplayDrainOwner) {
	claim := c.claimStandardReplayWorker(owner)
	switch claim.status {
	case standardReplayWorkerIdle, standardReplayWorkerMemoryWait, standardReplayWorkerCanceled:
		c.finishStandardReplayDrain(owner)
		return
	}

	result := c.prepareStandardReplayDrainResult(owner, claim)
	if result == nil {
		c.finishStandardReplayDrain(owner)
		return
	}
	if !c.enqueueStandardReplayDrainResult(owner, result) {
		result.releaseResources()
		c.finishStandardReplayDrain(owner)
	}
}

func (c *catchupReplayCoordinator) claimStandardReplayWorker(owner *standardReplayDrainOwner) standardReplayWorkerClaim {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	if owner == nil || c.standardReplayDrainOwner != owner || owner.workCtx == nil || owner.workCtx.Err() != nil {
		return standardReplayWorkerClaim{status: standardReplayWorkerCanceled}
	}
	if c.standardReplay.generation != owner.generation || !c.standardReplay.active {
		c.standardReplay.applying = false
		return standardReplayWorkerClaim{status: standardReplayWorkerIdle}
	}
	entry := c.standardReplay.entries[c.standardReplay.anchorSeq+1]
	if entry == nil || (entry.readyAt.IsZero() && !entry.failed) {
		c.standardReplay.applying = false
		c.updateStandardReplayHeadBlockLocked(time.Now())
		return standardReplayWorkerClaim{status: standardReplayWorkerIdle}
	}
	if entry.availabilityPending || entry.availabilityRetrying || entry.availabilityExhausted {
		c.standardReplay.applying = false
		c.updateStandardReplayHeadBlockLocked(time.Now())
		return standardReplayWorkerClaim{status: standardReplayWorkerIdle}
	}
	if entry.failed {
		return standardReplayWorkerClaim{
			status:     standardReplayWorkerClaimed,
			entry:      entry,
			copy:       *entry,
			generation: c.standardReplay.generation,
		}
	}
	if entry.replayMemory.kind != standardReplayMemoryHead && !c.promoteStandardReplayHeadLocked(entry) {
		c.standardReplay.applying = false
		c.updateStandardReplayHeadBlockLocked(time.Now())
		c.markStandardReplayMemoryWaitLocked(standardReplayMemoryWaitHead, entry.replayMemory.bytes)
		owner.memoryWait = true
		return standardReplayWorkerClaim{status: standardReplayWorkerMemoryWait}
	}
	if entry.replayMemoryPending {
		c.standardReplay.applying = false
		c.updateStandardReplayHeadBlockLocked(time.Now())
		owner.memoryWait = true
		return standardReplayWorkerClaim{status: standardReplayWorkerMemoryWait}
	}
	processingAccount := c.standardReplayMemoryAccount()
	if !reserveStandardReplayProcessing(processingAccount, standardReplayProcessingBytes) {
		c.standardReplay.applying = false
		c.updateStandardReplayHeadBlockLocked(time.Now())
		c.markStandardReplayMemoryWaitLocked(standardReplayMemoryWaitProcessing, standardReplayProcessingBytes)
		owner.memoryWait = true
		return standardReplayWorkerClaim{status: standardReplayWorkerMemoryWait}
	}
	c.standardReplay.memoryWait = false
	c.standardReplay.memoryWaitKind = 0
	c.standardReplay.memoryWaitBytes = 0
	if !c.retainStandardReplayEntryMemoryLocked(entry) {
		releaseStandardReplayProcessing(processingAccount, standardReplayProcessingBytes)
		c.standardReplay.applying = false
		c.updateStandardReplayHeadBlockLocked(time.Now())
		return standardReplayWorkerClaim{status: standardReplayWorkerCanceled}
	}
	if owner.workCtx.Err() != nil {
		c.releaseStandardReplayEntryMemoryLocked(entry)
		releaseStandardReplayProcessing(processingAccount, standardReplayProcessingBytes)
		c.standardReplay.applying = false
		return standardReplayWorkerClaim{status: standardReplayWorkerCanceled}
	}
	return standardReplayWorkerClaim{
		status:             standardReplayWorkerClaimed,
		entry:              entry,
		copy:               *entry,
		generation:         c.standardReplay.generation,
		processingAccount:  processingAccount,
		processingReserved: true,
		entryRetained:      true,
	}
}

func (c *catchupReplayCoordinator) prepareStandardReplayDrainResult(
	owner *standardReplayDrainOwner,
	claim standardReplayWorkerClaim,
) *standardReplayDrainResult {
	result := &standardReplayDrainResult{
		coordinator:        c,
		owner:              owner,
		generation:         claim.generation,
		entry:              claim.entry,
		startedAt:          time.Now(),
		processingAccount:  claim.processingAccount,
		processingReserved: claim.processingReserved,
		entryRetained:      claim.entryRetained,
	}
	if claim.entry == nil {
		result.err = errStandardReplayHeadFailed
		return result
	}
	if claim.entry.failed {
		result.err = errStandardReplayHeadFailed
		return result
	}

	preparationStarted := time.Now()
	var (
		parent      *ledger.Ledger
		parentProof *service.PreparedReplayParent
		txMap       *shamap.SHAMap
	)
	defer func() {
		if parentProof != nil {
			parentProof.Release()
		}
		if result.err != nil {
			var memoryErr *inbound.ReplayMemoryError
			if errors.As(result.err, &memoryErr) && memoryErr.Required != 0 {
				result.memoryRequired = memoryErr.Required
			}
		}
		result.preparationDuration = time.Since(preparationStarted) - result.applyDuration -
			result.persistDuration - result.lockWaitDuration - result.parentStats.Elapsed - result.ledgerStats.Elapsed
		if result.preparationDuration < 0 {
			result.preparationDuration = 0
		}
	}()

	ctx := owner.workCtx
	svc := c.adaptor.LedgerService()
	if svc == nil {
		result.err = errors.New("no ledger service")
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	h := claim.copy.header
	if h.Hash != claim.copy.hash {
		result.err = errors.New("prepared ledger hash changed")
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	if h.LedgerIndex != claim.copy.seq {
		result.err = fmt.Errorf("prepared ledger sequence %d does not match expected %d", h.LedgerIndex, claim.copy.seq)
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	if h.ParentHash != claim.copy.parentHash {
		result.err = errors.New("prepared ledger no longer attaches to the accepted predecessor")
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	parent, err := svc.GetLedgerByHash(claim.copy.parentHash)
	if err != nil || parent == nil {
		if err == nil {
			err = errors.New("accepted replay predecessor is unavailable")
		}
		result.err = err
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	if parent.Sequence()+1 != claim.copy.seq {
		result.err = fmt.Errorf("replay predecessor sequence %d is not before %d", parent.Sequence(), claim.copy.seq)
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	parentProof, err = svc.PrepareReplayParent(ctx, parent)
	if err != nil {
		result.err = err
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	result.parentStats = parentProof.Stats()
	txMap, err = c.loadStandardReplayTransactionMap(ctx, &claim.copy)
	if err != nil {
		result.err = err
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	processingAccount := c.standardReplayMemoryAccount()
	var replayAccount shamap.AcquisitionMemoryAccount
	if processingAccount != nil {
		replayAccount = processingAccount
	}
	replay, err := inbound.NewTransactionReplayContext(ctx, parent, h, txMap, c.logger, replayAccount)
	if err != nil {
		result.err = fmt.Errorf("prepare transaction-only replay: %w", err)
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	result.replayRelease = replay.ReleaseMemory
	applyStarted := time.Now()
	derived, err := svc.ApplyReplay(ctx, replay, c.adaptor.EngineConfigForReplay(parent), c.replayTargetAuthenticated(h))
	result.applyDuration = time.Since(applyStarted)
	if err != nil {
		result.err = err
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	if err := ctx.Err(); err != nil {
		result.err = err
		return result
	}
	prepared, err := svc.PrepareReplayLedger(ctx, derived, parentProof)
	if err != nil {
		result.err = err
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	result.prepared = prepared
	result.ledgerStats = prepared.Stats()
	result.header = derived.Header()
	lockStarted := time.Now()
	locked := c.lockReplayCommitContext(ctx)
	result.lockWaitDuration = time.Since(lockStarted)
	if !locked {
		result.err = ctx.Err()
		if result.err == nil {
			result.err = context.Canceled
		}
		return result
	}
	persistStarted := time.Now()
	current := false
	c.acquisitionMu.Lock()
	current = c.standardReplayDrainOwner == owner && c.standardReplay.active &&
		c.standardReplay.generation == claim.generation &&
		c.standardReplay.entries[claim.entry.seq] == claim.entry &&
		c.standardReplay.anchorSeq+1 == claim.entry.seq
	c.acquisitionMu.Unlock()
	if !current || owner.workCtx.Err() != nil {
		c.replayCommitMu.Unlock()
		result.err = errors.New("standard replay pipeline entry was superseded")
		return result
	}
	initialCandidate, err := svc.BootstrapPreparedLedger(ctx, prepared)
	result.ledgerStats = prepared.Stats()
	result.persistDuration = time.Since(persistStarted)
	c.replayCommitMu.Unlock()
	result.initialCandidate = initialCandidate
	if err != nil {
		result.err = err
		return c.finishStandardReplayWorkerFailure(result, claim.copy, parent, txMap)
	}
	return result
}

func (c *catchupReplayCoordinator) finishStandardReplayWorkerFailure(
	result *standardReplayDrainResult,
	entry standardReplayEntry,
	parent *ledger.Ledger,
	txMap *shamap.SHAMap,
) *standardReplayDrainResult {
	if result == nil || result.err == nil || errors.Is(result.err, context.Canceled) ||
		errors.Is(result.err, context.DeadlineExceeded) || errors.Is(result.err, inbound.ErrReplayMemory) ||
		c.replayFaultBlocked() || result.owner == nil || result.owner.workCtx.Err() != nil {
		return result
	}
	c.acquisitionMu.Lock()
	current := c.standardReplayDrainOwner == result.owner && c.standardReplay.active &&
		c.standardReplay.generation == result.generation &&
		c.standardReplay.entries[entry.seq] == result.entry && c.standardReplay.anchorSeq+1 == entry.seq
	c.acquisitionMu.Unlock()
	if !current {
		return result
	}

	svc := c.adaptor.LedgerService()
	if svc != nil {
		svc.RecordReplayPreparationFailure(result.owner.workCtx, entry.header, txMap, parent,
			c.replayTargetAuthenticated(entry.header), result.err)
	}
	return result
}

func (c *catchupReplayCoordinator) lockReplayCommitContext(ctx context.Context) bool {
	for {
		if c.replayCommitMu.TryLock() {
			return true
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false
		case <-timer.C:
		}
	}
}

func (c *catchupReplayCoordinator) enqueueStandardReplayDrainResult(
	owner *standardReplayDrainOwner,
	result *standardReplayDrainResult,
) bool {
	if owner == nil || result == nil || owner.workCtx == nil {
		return false
	}
	c.acquisitionMu.Lock()
	if c.standardReplayDrainOwner != owner || owner.workCtx.Err() != nil {
		c.acquisitionMu.Unlock()
		return false
	}
	result.enqueuedAt = time.Now()
	owner.result = result
	c.acquisitionMu.Unlock()
	select {
	case c.standardReplayResults <- result:
		return true
	case <-owner.workCtx.Done():
		return false
	}
}

func (c *catchupReplayCoordinator) handleStandardReplayDrainResult(result *standardReplayDrainResult) {
	if result == nil || result.owner == nil {
		if result != nil {
			result.releaseResources()
		}
		return
	}
	if !result.enqueuedAt.IsZero() {
		result.queueDuration = time.Since(result.enqueuedAt)
	}
	owner := result.owner
	defer c.finishStandardReplayDrain(owner)
	defer result.releaseResources()
	c.replayCommitMu.Lock()
	c.acquisitionMu.Lock()
	current := c.standardReplayDrainOwner == owner && c.standardReplay.active &&
		c.standardReplay.generation == result.generation &&
		c.standardReplay.entries[result.entry.seq] == result.entry &&
		c.standardReplay.anchorSeq+1 == result.entry.seq
	if !current || owner.workCtx.Err() != nil || c.stoppedForShutdown() {
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		return
	}
	// Cancellation owns queued results; this handler owns a claimed result
	// through activation, including its durable generation pin.
	owner.result = nil
	if result.err != nil {
		if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
			c.acquisitionMu.Unlock()
			c.replayCommitMu.Unlock()
			return
		}
		if errors.Is(result.err, inbound.ErrReplayMemory) {
			c.standardReplay.applying = false
			required := uint64(standardReplayProcessingBytes)
			if result.memoryRequired > ^uint64(0)-required {
				required = ^uint64(0)
			} else {
				required += result.memoryRequired
			}
			c.markStandardReplayMemoryWaitLocked(standardReplayMemoryWaitProcessing, required)
			owner.memoryWait = true
			c.updateStandardReplayHeadBlockLocked(time.Now())
			c.acquisitionMu.Unlock()
			c.replayCommitMu.Unlock()
			return
		}
		retired, target, current := c.discardStandardReplayHeadLocked(result.entry, result.generation)
		c.acquisitionMu.Unlock()
		c.replayCommitMu.Unlock()
		result.releaseResources()
		c.retireStandardReplay(retired)
		if current {
			c.replayPipelineFallbacks.Add(1)
			if c.logger != nil {
				c.logger.Error("standard transaction replay pipeline apply failed; recovery gated",
					"seq", result.entry.seq, "hash", fmt.Sprintf("%x", result.entry.hash[:8]), "error", result.err)
			}
			c.fallbackStandardReplayAcquisition(result.entry.seq, result.entry.hash, result.entry.peerID, target)
		}
		return
	}

	delete(c.standardReplay.entries, result.entry.seq)
	result.dropEntryPayload = true
	c.releaseStandardReplayEntryMemoryLocked(result.entry)
	c.standardReplay.anchorSeq = result.entry.seq
	c.standardReplay.anchorHash = result.entry.hash
	if next := c.standardReplay.entries[result.entry.seq+1]; next != nil {
		c.promoteStandardReplayHeadLocked(next)
	}
	supersededReplacement := c.discardObsoleteStandardReplayReplacementLocked()
	c.replayPipelineApplied.Add(1)
	c.replayPipelineApplyUs.Add(durationMicros(result.applyDuration))
	c.replayPipelinePersistUs.Add(durationMicros(result.persistDuration))
	c.replayPipelineReadyWaitUs.Add(durationMicros(result.startedAt.Sub(result.entry.readyAt)))
	reachedTarget := result.entry.seq == c.standardReplay.targetSeq && result.entry.hash == c.standardReplay.targetHash
	initialCandidate := result.initialCandidate
	if reachedTarget {
		initialCandidate = initialCandidate || c.standardReplay.initialCandidate
	}
	c.acquisitionMu.Unlock()
	c.replayCommitMu.Unlock()
	if supersededReplacement != nil {
		c.retireLegacyAcquisitions([]*inbound.Ledger{supersededReplacement})
	}
	if c.stoppedForShutdown() {
		result.releaseResources()
		return
	}

	c.logStandardReplayDrainResult(result)

	c.completeStoredConsensusRecovery(result.header.LedgerIndex, result.header.Hash, result.header.ParentHash, initialCandidate)

	c.acquisitionMu.Lock()
	current = c.standardReplayDrainOwner == owner && c.standardReplay.active &&
		c.standardReplay.generation == result.generation &&
		c.standardReplay.anchorSeq == result.entry.seq && c.standardReplay.anchorHash == result.entry.hash
	if !current {
		c.acquisitionMu.Unlock()
		result.releaseResources()
		return
	}
	if reachedTarget && c.standardReplay.targetSeq == result.entry.seq && c.standardReplay.targetHash == result.entry.hash {
		c.standardReplay.active = false
		c.standardReplay.applying = false
		for _, remaining := range c.standardReplay.entries {
			c.releaseStandardReplayEntryMemoryLocked(remaining)
		}
		c.dropStandardReplayEntriesLocked()
		c.standardReplay.headBlockedAt = time.Time{}
		c.standardReplay.backpressured = false
		c.releaseStandardReplayBaseLocked()
	}
	active := c.standardReplay.active
	c.acquisitionMu.Unlock()
	if active {
		c.refillStandardReplayCollector(result.entry.peerID)
		if !owner.synchronous {
			owner.yielded = true
		}
	}
	result.releaseResources()
}
