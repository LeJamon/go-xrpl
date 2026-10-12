package rcl

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

const preferredRecoveryRetryDelay = 5 * time.Millisecond

type preferredRecoveryRequest struct {
	id              consensus.LedgerID
	ledger          consensus.Ledger
	resetRoundState bool
	expectedPrev    consensus.LedgerID
	roundGeneration uint64
	parent          context.Context
}

type preferredRecoveryJob struct {
	request         preferredRecoveryRequest
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	retired         bool
	resetRoundState atomic.Bool
}

// preferredRecoveryWorker owns at most one detached preparation and one
// coalesced replacement target. Its mutex never nests with Engine.mu: submit
// is called while Engine.mu is held, while completion checks the job identity,
// releases this mutex, and only then enters Engine.mu.
type preferredRecoveryWorker struct {
	engine *Engine

	mu      sync.Mutex
	active  *preferredRecoveryJob
	pending *preferredRecoveryRequest
	stopped bool
}

func newPreferredRecoveryWorker(engine *Engine) *preferredRecoveryWorker {
	return &preferredRecoveryWorker{engine: engine}
}

// submitLocked admits a target while the caller holds Engine.mu. The return
// value says the target was retained by the worker; it does not mean that the
// target was published or adopted.
func (w *preferredRecoveryWorker) submitLocked(request preferredRecoveryRequest) bool {
	if w == nil || request.ledger == nil {
		return false
	}
	if request.parent == nil {
		request.parent = context.Background()
	}

	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return false
	}
	if w.active != nil {
		if w.active.request.id == request.id {
			if w.active.request.expectedPrev != request.expectedPrev ||
				w.active.request.roundGeneration != request.roundGeneration {
				w.pending = &request
				w.active.cancel()
				w.mu.Unlock()
				return true
			}
			// Preserve the stronger recovery reset request if the same target was
			// admitted by heartbeat after a router handoff.
			if request.resetRoundState {
				w.active.resetRoundState.Store(true)
			}
			w.mu.Unlock()
			return true
		}
		// There is one bounded pending slot. Replacing it cancels the active
		// preparation so a changed quorum/preference cannot publish stale work.
		w.pending = &request
		w.active.cancel()
		w.mu.Unlock()
		return true
	}

	job := w.newJobLocked(request)
	w.active = job
	w.mu.Unlock()
	go w.run(job)
	return true
}

func (w *preferredRecoveryWorker) newJobLocked(request preferredRecoveryRequest) *preferredRecoveryJob {
	ctx, cancel := context.WithCancel(request.parent)
	job := &preferredRecoveryJob{request: request, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	job.resetRoundState.Store(request.resetRoundState)
	return job
}

func (w *preferredRecoveryWorker) isCurrent(job *preferredRecoveryJob) bool {
	w.mu.Lock()
	current := !w.stopped && w.active == job
	w.mu.Unlock()
	return current
}

func (w *preferredRecoveryWorker) target() (consensus.LedgerID, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active != nil {
		return w.active.request.id, true
	}
	if w.pending != nil {
		return w.pending.id, true
	}
	return consensus.LedgerID{}, false
}

func (w *preferredRecoveryWorker) currentTargetMatchesLocked(id consensus.LedgerID, prev consensus.Ledger, generation uint64) bool {
	if w == nil {
		return false
	}
	var expectedPrev consensus.LedgerID
	if prev != nil {
		expectedPrev = prev.ID()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.active == nil {
		return false
	}
	request := w.active.request
	return request.id == id && request.expectedPrev == expectedPrev && request.roundGeneration == generation
}

// stop cancels and joins all admitted work. It must be called without
// Engine.mu, because a completion may be waiting for that mutex to finish its
// publication fence.
func (w *preferredRecoveryWorker) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	active := w.active
	if active != nil {
		active.cancel()
	}
	w.pending = nil
	w.mu.Unlock()

	// A stopped worker's run method owns any prepared service object until its
	// Finish/Release sequence returns. Successful commits retire the engine
	// owner before observable callbacks so a callback can stop the engine.
	if active != nil {
		<-active.done
	}
}

// retire marks the engine commit complete before observable service or relay
// callbacks run. A callback may synchronously call Engine.Stop; keeping the
// job active until the deferred finish would make that call wait on itself.
// The job remains the manager's active owner until Finish and Release return,
// so a pending target cannot start while the prepared service object is live.
func (w *preferredRecoveryWorker) retire(job *preferredRecoveryJob) {
	w.mu.Lock()
	if w.active == job && !job.retired {
		job.retired = true
		close(job.done)
	}
	w.mu.Unlock()
}

func (w *preferredRecoveryWorker) finish(job *preferredRecoveryJob) {
	job.cancel()
	w.mu.Lock()
	if w.active != job {
		if !job.retired {
			job.retired = true
			close(job.done)
		}
		w.mu.Unlock()
		return
	}
	w.active = nil
	if w.stopped || w.pending == nil {
		w.pending = nil
		if !job.retired {
			job.retired = true
			close(job.done)
		}
		w.mu.Unlock()
		return
	}
	next := *w.pending
	w.pending = nil
	nextJob := w.newJobLocked(next)
	w.active = nextJob
	if !job.retired {
		job.retired = true
		close(job.done)
	}
	w.mu.Unlock()
	go w.run(nextJob)
}

type preferredCommitStatus uint8

const (
	preferredCommitStale preferredCommitStatus = iota
	preferredCommitGateBusy
	preferredCommitPublished
)

type preferredCommitResult struct {
	status  preferredCommitStatus
	pending []func()
}

func (w *preferredRecoveryWorker) run(job *preferredRecoveryJob) {
	defer w.finish(job)

	preparer, ok := w.engine.adaptor.(consensus.PreferredLedgerPreparer)
	if !ok {
		return
	}
	prepared, err := preparer.PreparePreferredLedger(job.ctx, job.request.ledger)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			slog.Debug("preferred ledger preparation failed", "t", "consensus", "seq", job.request.ledger.Seq(), "err", err)
		}
		return
	}
	if prepared == nil {
		return
	}
	owned := true
	defer func() {
		if owned {
			prepared.Release()
		}
	}()
	if err := prepared.Wait(job.ctx); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			slog.Debug("preferred ledger preparation became unavailable", "t", "consensus", "seq", job.request.ledger.Seq(), "err", err)
		}
		return
	}

	for {
		if job.ctx.Err() != nil || !w.isCurrent(job) {
			return
		}
		result := w.engine.commitPreparedRecovery(job, prepared)
		switch result.status {
		case preferredCommitPublished:
			// The service fence is never held while user callbacks run. Retire
			// the engine commit first so a callback may synchronously stop the
			// engine without waiting on this worker's own completion.
			w.retire(job)
			prepared.Finish()
			prepared.Release()
			owned = false
			runPostUnlock(result.pending)
			return
		case preferredCommitStale:
			return
		case preferredCommitGateBusy:
			timer := time.NewTimer(preferredRecoveryRetryDelay)
			select {
			case <-job.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}
	}
}

// commitPreparedRecovery is the only completion path allowed to call
// TryPublish. All target and round checks happen immediately before that call
// while Engine.mu is held, and the engine state adoption shares the same fence.
func (e *Engine) commitPreparedRecovery(
	job *preferredRecoveryJob,
	prepared consensus.PreferredLedgerPreparation,
) preferredCommitResult {
	if e.preferredRecovery == nil || !e.preferredRecovery.isCurrent(job) {
		return preferredCommitResult{status: preferredCommitStale}
	}
	e.mu.Lock()

	if job.ctx.Err() != nil || !e.preferredRecovery.isCurrent(job) || e.buildInProgress ||
		e.roundGeneration != job.request.roundGeneration ||
		(e.prevLedger == nil && job.request.expectedPrev != (consensus.LedgerID{})) ||
		(e.prevLedger != nil && e.prevLedger.ID() != job.request.expectedPrev) {
		e.mu.Unlock()
		return preferredCommitResult{status: preferredCommitStale}
	}
	l, err := e.adaptor.GetLedger(job.request.id)
	if err != nil || l == nil || !e.preferredSwitchEligibleLocked(job.request.id, l) {
		e.mu.Unlock()
		return preferredCommitResult{status: preferredCommitStale}
	}

	published, err := prepared.TryPublish()
	if err != nil {
		e.mu.Unlock()
		slog.Debug("preferred ledger publication failed", "t", "consensus", "seq", l.Seq(), "err", err)
		return preferredCommitResult{status: preferredCommitStale}
	}
	if !published {
		e.mu.Unlock()
		return preferredCommitResult{status: preferredCommitGateBusy}
	}
	// A normal same-ledger handoff is already satisfied and must not enter
	// switchedLedger. TryPublish still runs so a same-ledger service transition
	// can complete initial sync or other canonical bookkeeping in this race.
	if e.mode != consensus.ModeWrongLedger && e.prevLedger != nil && e.prevLedger.ID() == l.ID() {
		e.mu.Unlock()
		return preferredCommitResult{status: preferredCommitPublished}
	}

	if job.resetRoundState.Load() {
		e.resetWrongLedgerStateLocked(job.request.id)
	}
	// TryPublish and this state adoption share e.mu. The prepared path has no
	// fallible callback after publication, so the engine cannot leave the
	// service frontier committed while abandoning its consensus parent.
	e.switchToLedgerStatePreparedLocked(job.request.id, l)
	pending := e.takePendingPostUnlockLocked()
	e.mu.Unlock()
	return preferredCommitResult{status: preferredCommitPublished, pending: pending}
}

func (e *Engine) preferredSwitchEligibleLocked(id consensus.LedgerID, ledger consensus.Ledger) bool {
	if ledger == nil || e.deferAcquiredChildLocked(ledger) || !e.canSwitchToLedgerLocked(ledger) {
		return false
	}
	exactRecoveryTarget := e.mode == consensus.ModeWrongLedger && id == e.wrongLedgerID
	validatedCandidate := e.adaptor.GetValidatedLedgerHash() == id || e.isQuorumValidatedCandidateLocked(ledger)
	networkPreferred := e.prevLedger != nil && e.getNetworkLedger() == id
	return exactRecoveryTarget || validatedCandidate || networkPreferred
}

// resetWrongLedgerStateLocked clears round-scoped positions before a held
// wrong-ledger target is adopted. Relays are deferred so no network callback
// runs under Engine.mu.
func (e *Engine) resetWrongLedgerStateLocked(target consensus.LedgerID) {
	if e.prevLedger == nil || target == e.prevLedger.ID() {
		return
	}
	e.purgePendingTrustLocked()
	e.proposalTracker.resetProposals()
	e.disputeTracker = newDisputeTracker()
	e.acquiredTxSets = make(map[consensus.TxSetID]consensus.TxSet)
	e.comparesTxSets = make(map[consensus.TxSetID]struct{})
	e.peerUnchangedCounter = 0
	e.establishCounter = 0
	e.closeTime.haveConsensus = false
	if e.state != nil {
		e.state.CloseTimes.Peers = make(map[time.Time]int)
	}
	closeTimes, _, relay := e.proposalTracker.replay(target, e.trustedPredicate())
	e.unvoteDeadProposalsLocked()
	e.pruneUntrustedProposalsLocked()
	e.appendReplayCloseTimesLocked(closeTimes)
	trusted := e.trustedPredicate()
	for _, proposal := range relay {
		if !trusted(proposal.NodeID) {
			continue
		}
		proposal := proposal
		e.pendingPostUnlock = append(e.pendingPostUnlock, func() {
			_ = e.adaptor.RelayProposal(proposal, 0)
		})
	}
}

func (e *Engine) preferredRecoveryTarget() (consensus.LedgerID, bool) {
	if e.preferredRecovery == nil {
		return consensus.LedgerID{}, false
	}
	return e.preferredRecovery.target()
}

// preparePreferredSwitchLocked admits a detached service preparation. The
// caller has already resolved and checked the target while holding Engine.mu.
func (e *Engine) preparePreferredSwitchLocked(id consensus.LedgerID, ledger consensus.Ledger, resetRoundState bool) bool {
	if e.preferredRecovery == nil {
		return false
	}
	if _, ok := e.adaptor.(consensus.PreferredLedgerPreparer); !ok {
		return false
	}
	parent := e.ctx
	if parent == nil {
		parent = context.Background()
	}
	var expectedPrev consensus.LedgerID
	if e.prevLedger != nil {
		expectedPrev = e.prevLedger.ID()
	}
	return e.preferredRecovery.submitLocked(preferredRecoveryRequest{
		id:              id,
		ledger:          ledger,
		resetRoundState: resetRoundState,
		expectedPrev:    expectedPrev,
		roundGeneration: e.roundGeneration,
		parent:          parent,
	})
}
