package adaptor

import (
	"context"
	"errors"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/shamap"
)

// One full-state admission and one transaction-only admission may run at once.
// A slow pivot walk must not stall successor collection, the acquisition work
// lane, or Router.Run. Other completions remain in the bounded fetch tracker;
// maintenance and the completion wake retry them without another payload queue.
type acquisitionCompletionTask struct {
	ledger    *inbound.Ledger
	cancel    context.CancelFunc
	startedAt time.Time
	loggedAt  time.Time
}

type acquisitionCompletionResult struct {
	ledger           *inbound.Ledger
	delta            *inbound.ReplayDelta
	legacy           bool
	parentHeld       bool
	header           header.LedgerHeader
	txMap            *shamap.SHAMap
	prepared         *service.PreparedLedger
	releaseAdmission func()
	err              error
	failureClass     standardReplayFailureClass
	ack              chan struct{}
}

func (r *acquisitionCompletionResult) release() {
	if r.prepared != nil {
		r.prepared.Release()
	}
	if r.releaseAdmission != nil {
		r.releaseAdmission()
	}
}

func (c *catchupReplayCoordinator) completeInboundLedgerReady(il *inbound.Ledger) {
	if il == nil || c.stoppedForShutdown() || c.fetchTracker.Find(il.Hash()) != il {
		return
	}
	if il.Reason() == inbound.ReasonHistory && !c.historySequenceAllowed(il.Seq()) {
		c.discardHistoryAcquisition(il, "outside_history_window")
		return
	}
	c.lifecycleMu.RLock()
	ctx, running := c.lifecycleCtx, c.lifecycleCancel != nil
	c.lifecycleMu.RUnlock()
	if ctx != nil && ctx.Err() != nil {
		return
	}
	if !running {
		// Direct-drive construction/tests have no router result loop.
		result := c.prepareAcquisitionCompletion(context.Background(), il)
		defer result.release()
		c.handleAcquisitionCompletion(result)
		return
	}
	lane := 0
	if il.TransactionOnly() {
		lane = 1
	}
	c.completionMu.Lock()
	if c.completionTasks[lane] != nil {
		c.completionMu.Unlock()
		return
	}
	workCtx, cancel := context.WithCancel(ctx)
	task := &acquisitionCompletionTask{ledger: il, cancel: cancel, startedAt: time.Now(), loggedAt: time.Now()}
	c.completionTasks[lane] = task
	c.completionMu.Unlock()
	finish := func() {
		cancel()
		c.completionMu.Lock()
		if c.completionTasks[lane] == task {
			c.completionTasks[lane] = nil
		}
		c.completionMu.Unlock()
		select {
		case c.completionWake <- struct{}{}:
		default:
		}
	}
	if !c.runLifecycleTask(func(context.Context) {
		defer finish()
		if !il.TransactionOnly() {
			c.logger.Info("inbound ledger: detached verification started", "seq", il.Seq())
		}
		result := c.prepareAcquisitionCompletion(workCtx, il)
		defer result.release()
		result.ack = make(chan struct{})
		select {
		case <-workCtx.Done():
			return
		case c.completionResults <- result:
			// The unbuffered handoff commits the router to acknowledge this
			// result, even during shutdown. Keep the proof pinned until then.
			<-result.ack
		}
	}) {
		finish()
	}
}

func (c *catchupReplayCoordinator) retryAcquisitionCompletions() {
	if c.stoppedForShutdown() {
		return
	}
	now := time.Now()
	c.completionMu.Lock()
	if task := c.legacyDeltaTask; task != nil {
		if c.replayer.Find(task.delta.Hash()) != task.delta {
			task.cancel()
		} else if now.Sub(task.logged) >= 30*time.Second {
			c.logger.Info("inbound ledger: replay delta preparation in progress", "seq", task.delta.Seq(), "elapsed", now.Sub(task.started))
			task.logged = now
		}
	}
	for _, task := range c.completionTasks {
		if task == nil {
			continue
		}
		if c.fetchTracker.Find(task.ledger.Hash()) != task.ledger {
			task.cancel()
		} else if now.Sub(task.loggedAt) >= 30*time.Second {
			c.logger.Info("inbound ledger: detached verification in progress", "seq", task.ledger.Seq(), "transaction_only", task.ledger.TransactionOnly(), "elapsed", now.Sub(task.startedAt))
			task.loggedAt = now
		}
	}
	c.completionMu.Unlock()
	for _, il := range c.fetchTracker.Active() {
		if il.IsComplete() {
			c.completeInboundLedgerReady(il)
		}
	}
	for _, hash := range c.replayer.InFlight() {
		if rd := c.replayer.Find(hash); rd != nil && rd.State() == inbound.StateReplayReady {
			c.completeLegacyDeltaReady(rd)
		}
	}
}

func (c *catchupReplayCoordinator) prepareAcquisitionCompletion(ctx context.Context, il *inbound.Ledger) *acquisitionCompletionResult {
	result := &acquisitionCompletionResult{ledger: il, failureClass: standardReplayFailureExecution}
	if c.adaptor == nil || c.adaptor.LedgerService() == nil {
		result.err = errors.New("inbound ledger: ledger service unavailable")
		return result
	}
	svc := c.adaptor.LedgerService()
	result.releaseAdmission, result.err = svc.AcquireStateAdmission(ctx)
	if result.err != nil {
		return result
	}
	if result.err = c.flushAcquisitionStore(ctx, il); result.err != nil {
		result.failureClass = standardReplayFailurePersistence
		return result
	}
	h, stateMap, txMap, err := il.Result()
	if err != nil {
		result.err = err
		return result
	}
	result.header = *h
	if result.err = c.promoteAcquisitionStore(ctx, il); result.err != nil {
		result.failureClass = standardReplayFailurePersistence
		return result
	}
	if il.TransactionOnly() {
		if txMap != nil {
			result.txMap, result.err = txMap.SnapshotImmutableContext(ctx)
			if result.err != nil {
				return result
			}
		}
		result.err = svc.VerifyDetachedMaps(ctx, nil, result.txMap)
		if result.err == nil {
			c.acquisitionMu.Lock()
			pipelineOwned := c.standardReplayEntryOwnedLocked(h.Hash)
			c.acquisitionMu.Unlock()
			if !pipelineOwned {
				result.legacy = true
				result.err = c.prepareLegacyTransactionReplay(ctx, result)
			}
		}
		return result
	}
	result.prepared, result.err = svc.PrepareAcquiredLedger(ctx, h, stateMap, txMap)
	return result
}

func (c *catchupReplayCoordinator) handleAcquisitionCompletion(result *acquisitionCompletionResult) {
	if result.ack != nil {
		defer close(result.ack)
	}
	if result.delta != nil {
		c.handleLegacyDeltaCompletion(result)
		return
	}
	il := result.ledger
	if c.stoppedForShutdown() || c.fetchTracker.Find(il.Hash()) != il {
		return
	}
	if result.err != nil && !result.legacy {
		c.logger.Warn("inbound ledger: detached admission failed", "seq", il.Seq(), "error", result.err)
		c.discardFailedInboundAcquisition(il, result.err, result.failureClass)
		return
	}
	if !il.TransactionOnly() {
		stats := result.prepared.Stats()
		c.logger.Info("inbound ledger: detached verification complete", "seq", il.Seq(), "elapsed", stats.Elapsed,
			"state_nodes", stats.State.NodesLoaded, "state_reads", stats.State.DurableReads,
			"state_verified_base", stats.State.UsedVerifiedBase,
			"state_equal_subtrees_skipped", stats.State.EqualSubtreesSkipped)
	}
	c.publishInboundLedger(result)
}
