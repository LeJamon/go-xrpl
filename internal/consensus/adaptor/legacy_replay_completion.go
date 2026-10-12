package adaptor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Legacy replay is detached just like pipeline replay. In particular, a parent
// proof cache miss may walk the entire state and must never run in Router.Run.
// Standard completions use the existing single transaction completion lane;
// verified deltas use one additional lane, leaving queued work in Replayer's
// bounded, memory-accounted registry rather than adding an unbounded queue.
func (c *catchupReplayCoordinator) prepareLegacyTransactionReplay(ctx context.Context, result *acquisitionCompletionResult) error {
	svc := c.adaptor.LedgerService()
	h := result.header
	parent, err := svc.GetLedgerByHashContext(ctx, h.ParentHash)
	if err != nil {
		return err
	}
	if parent == nil || parent.Sequence()+1 != h.LedgerIndex {
		return errors.New("locally-held replay parent is unavailable or has the wrong sequence")
	}
	result.parentHeld = true
	c.logger.Info("inbound ledger: legacy replay preparation started", "seq", h.LedgerIndex)
	return c.prepareLegacyReplay(ctx, result, parent, nil)
}

func (c *catchupReplayCoordinator) prepareLegacyReplay(ctx context.Context, result *acquisitionCompletionResult, parent *ledger.Ledger, rd *inbound.ReplayDelta) (err error) {
	svc := c.adaptor.LedgerService()
	defer func() {
		if err != nil && ctx.Err() == nil && !errors.Is(err, inbound.ErrReplayMemory) {
			svc.RecordReplayPreparationFailure(ctx, result.header, result.txMap, parent,
				c.replayTargetAuthenticated(result.header), err)
		}
	}()
	proof, err := svc.PrepareReplayParent(ctx, parent)
	if err != nil {
		return err
	}
	defer proof.Release()
	if rd == nil {
		state, err := parent.StateMapSnapshotContext(ctx)
		if err != nil {
			return err
		}
		txMap := result.txMap
		if txMap == nil {
			if result.header.TxHash != ([32]byte{}) {
				return errors.New("missing transaction map for non-empty transaction root")
			}
			txMap = shamap.New(shamap.TypeTransaction)
		}
		target, err := ledger.NewFromHeader(result.header, state, txMap, parent.Fees(), parent.ExecutionStore())
		if err != nil {
			return err
		}
		defer target.Discard()
		var account shamap.AcquisitionMemoryAccount
		if budget := c.standardReplayMemoryAccount(); budget != nil {
			account = budget
		}
		rd, err = inbound.NewStoredLedgerReplayWithMemoryContext(ctx, parent, target, c.logger, account)
		if err != nil {
			return err
		}
		defer rd.ReleaseMemory()
	} else {
		// Apply holds ReplayDelta.mu across execution and I/O. Work on a
		// private copy so router-side timeout/identity queries of the registry
		// entry never wait behind that mutex.
		var err error
		rd, err = rd.RetryContext(ctx, parent)
		if err != nil {
			return err
		}
		defer rd.ReleaseMemory()
	}
	derived, err := svc.ApplyReplay(ctx, rd, c.adaptor.EngineConfigForReplay(parent), c.replayTargetAuthenticated(result.header))
	if err != nil {
		return err
	}
	result.prepared, err = svc.PrepareReplayLedger(ctx, derived, proof)
	return err
}

func (c *catchupReplayCoordinator) finishLegacyTransactionReplay(result *acquisitionCompletionResult, peerID uint64) {
	h := result.header
	if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) || c.stoppedForShutdown() {
		return
	}
	if errors.Is(result.err, inbound.ErrReplayMemory) && result.ledger != nil {
		required := uint64(0)
		var memoryErr *inbound.ReplayMemoryError
		if errors.As(result.err, &memoryErr) {
			required = memoryErr.Required
		}
		c.installStandardReplayMemoryWake()
		c.acquisitionMu.Lock()
		if !c.retainsStandardReplayLegacyLocked(result.ledger) {
			c.standardReplay.legacyMemoryWait = append(c.standardReplay.legacyMemoryWait,
				&standardReplayLegacyMemoryWait{ledger: result.ledger, required: required})
		}
		c.acquisitionMu.Unlock()
		return
	}
	if result.err != nil {
		c.logger.Warn("standard transaction replay unavailable", "seq", h.LedgerIndex,
			"hash", fmt.Sprintf("%x", h.Hash[:8]), "peer", peerID, "error", result.err)
		c.acquisitionMu.Lock()
		if result.parentHeld {
			c.requireReplayFullStateLocked(h.LedgerIndex, h.Hash)
		}
		c.startLedgerAcquisitionLegacyLocked(h.LedgerIndex, h.Hash, peerID)
		c.acquisitionMu.Unlock()
		return
	}
	if err := c.publishLegacyReplay(result.prepared, h); err != nil {
		c.logger.Warn("failed to store standard transaction replay ledger", "seq", h.LedgerIndex, "error", err)
	}
}

// A successful preparation owns the durable generation through this router-side
// handoff. There are no cold tree walks in publication.
func (c *catchupReplayCoordinator) publishLegacyReplay(prepared *service.PreparedLedger, h header.LedgerHeader) error {
	ctx := c.lifecycleContext()
	if prepared == nil || c.stoppedForShutdown() || !c.lockReplayCommitContext(ctx) {
		return context.Canceled
	}
	if c.stoppedForShutdown() {
		c.replayCommitMu.Unlock()
		return context.Canceled
	}
	initial, err := c.adaptor.LedgerService().BootstrapPreparedLedger(ctx, prepared)
	c.replayCommitMu.Unlock()
	if err != nil {
		return err
	}
	c.logger.Info("acquired ledger via detached legacy replay", "seq", h.LedgerIndex, "hash", fmt.Sprintf("%x", h.Hash[:8]))
	c.completeStoredConsensusRecovery(h.LedgerIndex, h.Hash, h.ParentHash, initial)
	return nil
}

type legacyDeltaTask struct {
	delta   *inbound.ReplayDelta
	cancel  context.CancelFunc
	started time.Time
	logged  time.Time
}

func (c *catchupReplayCoordinator) completeLegacyDeltaReady(rd *inbound.ReplayDelta) {
	if c.stoppedForShutdown() || rd == nil || c.replayer.Find(rd.Hash()) != rd {
		return
	}
	c.lifecycleMu.RLock()
	ctx, running := c.lifecycleCtx, c.lifecycleCancel != nil
	c.lifecycleMu.RUnlock()
	if ctx != nil && ctx.Err() != nil {
		return
	}
	if !running {
		result := c.prepareLegacyDelta(context.Background(), rd)
		defer result.release()
		c.handleLegacyDeltaCompletion(result)
		return
	}
	c.completionMu.Lock()
	if c.legacyDeltaTask != nil || time.Now().Before(c.legacyDeltaRetryAt) {
		c.completionMu.Unlock()
		return
	}
	workCtx, cancel := context.WithCancel(ctx)
	task := &legacyDeltaTask{delta: rd, cancel: cancel, started: time.Now(), logged: time.Now()}
	c.legacyDeltaTask = task
	releaseMemory := rd.RetainMemory()
	if releaseMemory == nil {
		c.legacyDeltaTask = nil
		cancel()
		c.completionMu.Unlock()
		return
	}
	c.completionMu.Unlock()
	finish := func() {
		releaseMemory()
		cancel()
		c.completionMu.Lock()
		if c.legacyDeltaTask == task {
			c.legacyDeltaTask = nil
		}
		c.completionMu.Unlock()
		select {
		case c.completionWake <- struct{}{}:
		default:
		}
	}
	if !c.runLifecycleTask(func(context.Context) {
		defer finish()
		result := c.prepareLegacyDelta(workCtx, rd)
		defer result.release()
		result.ack = make(chan struct{})
		select {
		case <-workCtx.Done():
		case c.completionResults <- result:
			<-result.ack
		}
	}) {
		finish()
	}
}

func (c *catchupReplayCoordinator) prepareLegacyDelta(ctx context.Context, rd *inbound.ReplayDelta) *acquisitionCompletionResult {
	result := &acquisitionCompletionResult{delta: rd, header: rd.TargetHeader()}
	svc := c.adaptor.LedgerService()
	result.releaseAdmission, result.err = svc.AcquireStateAdmission(ctx)
	if result.err != nil {
		return result
	}
	result.txMap, result.err = rd.TransactionMapSnapshotContext(ctx)
	if result.err != nil {
		return result
	}
	c.logger.Info("inbound ledger: replay delta preparation started", "seq", result.header.LedgerIndex)
	result.err = c.prepareLegacyReplay(ctx, result, rd.Parent(), rd)
	return result
}

func (c *catchupReplayCoordinator) handleLegacyDeltaCompletion(result *acquisitionCompletionResult) {
	rd := result.delta
	if c.stoppedForShutdown() || c.replayer.Find(rd.Hash()) != rd {
		return
	}
	if result.err != nil {
		if errors.Is(result.err, inbound.ErrReplayMemory) {
			// Keep the charged registry owner; periodic maintenance retries.
			c.completionMu.Lock()
			c.legacyDeltaRetryAt = time.Now().Add(time.Second)
			c.completionMu.Unlock()
			return
		}
		if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
			return
		}
		c.acquisitionMu.Lock()
		c.requireReplayFullStateLocked(rd.Seq(), rd.Hash())
		c.replayer.Abandon(rd.Hash())
		c.acquisitionMu.Unlock()
		c.logger.Warn("detached replay delta preparation failed", "seq", rd.Seq(), "error", result.err)
		c.fallbackReplayAcquisition(rd.Seq(), rd.Hash(), rd.PeerID())
		return
	}
	c.replayer.Complete(rd.Hash())
	c.clearReplayAvailabilityRetry(rd.Hash())
	if err := c.publishLegacyReplay(result.prepared, result.header); err != nil {
		c.logger.Warn("failed to store replay-delta ledger", "seq", rd.Seq(), "error", err)
	}
}
