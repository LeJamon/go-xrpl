package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

const detachedIngressWaitLimit = 16

var errDetachedIngressBusy = errors.New("open-ledger ingress admission is full")

type detachedIngressWaiter struct {
	ready chan error
}

// detachedIngressAdmission owns the one private ingress execution slot. The
// bounded waiter list prevents an unbounded number of callers from retaining
// transaction blobs while a slow execution is in progress.
type detachedIngressAdmission struct {
	mu               sync.Mutex
	active           bool
	stopped          bool
	waiters          []*detachedIngressWaiter
	activeDone       chan struct{}
	lifecycleContext context.Context
	cancel           context.CancelFunc
}

func (a *detachedIngressAdmission) acquire() (func(), error) {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return nil, errServiceNotRunning
	}
	if !a.active {
		if a.lifecycleContext == nil {
			a.lifecycleContext, a.cancel = context.WithCancel(context.Background())
		}
		a.active = true
		a.activeDone = make(chan struct{})
		a.mu.Unlock()
		return sync.OnceFunc(a.release), nil
	}
	if len(a.waiters) >= detachedIngressWaitLimit {
		a.mu.Unlock()
		return nil, errDetachedIngressBusy
	}
	waiter := &detachedIngressWaiter{ready: make(chan error, 1)}
	a.waiters = append(a.waiters, waiter)
	a.mu.Unlock()
	if err := <-waiter.ready; err != nil {
		return nil, err
	}
	return sync.OnceFunc(a.release), nil
}

func (a *detachedIngressAdmission) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return
	}
	if len(a.waiters) > 0 && !a.stopped {
		waiter := a.waiters[0]
		a.waiters[0] = nil
		a.waiters = a.waiters[1:]
		waiter.ready <- nil
		return
	}
	a.active = false
	if a.activeDone != nil {
		close(a.activeDone)
		a.activeDone = nil
	}
}

func (a *detachedIngressAdmission) ctx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lifecycleContext == nil {
		return context.Background()
	}
	return a.lifecycleContext
}

// stop rejects waiters and returns a channel that closes after the active
// private execution has released its slot.
func (a *detachedIngressAdmission) stop() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return a.activeDoneOrClosedLocked()
	}
	a.stopped = true
	if a.cancel != nil {
		a.cancel()
	}
	for _, waiter := range a.waiters {
		waiter.ready <- errServiceNotRunning
	}
	a.waiters = nil
	return a.activeDoneOrClosedLocked()
}

func (a *detachedIngressAdmission) activeDoneOrClosedLocked() <-chan struct{} {
	if a.activeDone != nil {
		return a.activeDone
	}
	done := make(chan struct{})
	close(done)
	return done
}

type detachedIngressRequest struct {
	pendingTx openledger.PendingTx
	origin    string
	// ownedParsed transfers exclusive ownership of pendingTx.Parsed to the
	// first private attempt. A stale publication retry reparses Blob instead,
	// because the apply engine may mutate the parsed object in place.
	ownedParsed bool

	initialRules          *amendment.Rules
	signatureEligible     bool
	initialSignatureCheck bool
	initialSignatureErr   error
	verifySignature       func(tx.Transaction, *amendment.Rules) error
	localReason           string

	prepare       func(context.Context)
	configure     func(openledger.ApplyConfig) openledger.ApplyConfig
	prepareCommit func(*ledger.Ledger, *ledger.Ledger, *txq.TxQ, openledger.ApplyConfig, openledger.SubmitOutcome, openledger.PendingTx) detachedIngressCommitData
	onCommit      func(*ledger.Ledger, *txq.TxQ, *localtxs.LocalTxs, openledger.ApplyConfig, openledger.SubmitOutcome, detachedIngressCommitData, openledger.PendingTx)
}

type detachedIngressCommitData struct {
	ledgerState *SubmitLedgerState
	proposed    *SubmittedTxEvent
}

type detachedIngressSnapshot struct {
	openView   *openledger.OpenLedger
	current    *ledger.Ledger
	queue      *txq.TxQ
	localTxs   *localtxs.LocalTxs
	closed     *ledger.Ledger
	openMarker *ledger.Ledger
	rules      *amendment.Rules
	config     openledger.ApplyConfig
	fault      *replayfault.Fault
	validated  *ledger.Ledger
	stopDone   chan struct{}
}

type detachedIngressValidationKind uint8

const (
	detachedIngressSignatureFailure detachedIngressValidationKind = iota + 1
	detachedIngressLocalFailure
)

type detachedIngressValidationError struct {
	kind detachedIngressValidationKind
	err  error
}

func (e *detachedIngressValidationError) Error() string {
	if e == nil || e.err == nil {
		return "detached ingress validation failed"
	}
	return e.err.Error()
}

func (e *detachedIngressValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (s *Service) reparseDetachedIngress(blob []byte) (tx.Transaction, error) {
	started := time.Now()
	parsed, err := tx.ParseFromBinary(blob)
	s.recordIngressReparse(started)
	return parsed, err
}

func (s *Service) reparseDetachedPending(blob []byte) (openledger.PendingTx, error) {
	started := time.Now()
	pending, err := openledger.ParsePendingTx(blob)
	s.recordIngressReparse(started)
	return pending, err
}

func (s *Service) recordIngressReparse(started time.Time) {
	duration := time.Since(started)
	s.ingressMetrics.reparses.Add(1)
	s.ingressMetrics.reparseNanos.Add(uint64(duration))
}

func (s *Service) captureDetachedIngress(req detachedIngressRequest) (detachedIngressSnapshot, error) {
	if err := s.lockOpenLedgerIfRunning(openLedgerIngress); err != nil {
		return detachedIngressSnapshot{}, err
	}

	s.mu.RLock()
	openView := s.openLedgerView
	current := (*ledger.Ledger)(nil)
	if openView != nil {
		current = openView.Current()
	}
	queue := s.txQueue
	localTxs := s.localTxs
	closed := s.closedLedger
	openMarker := s.openLedger
	validated := s.validatedLedger
	cfg, cfgErr := s.applyConfigLocked()
	if req.configure != nil {
		cfg = req.configure(cfg)
	}
	s.mu.RUnlock()

	if cfgErr != nil {
		s.openLedgerMu.Unlock()
		return detachedIngressSnapshot{}, cfgErr
	}
	if openView == nil || current == nil {
		s.openLedgerMu.Unlock()
		return detachedIngressSnapshot{}, errors.New("openLedgerView not initialised")
	}

	s.lifecycleMu.Lock()
	running := s.lifecycleState == serviceRunning
	stopDone := s.stopDone
	s.lifecycleMu.Unlock()
	if !running {
		s.openLedgerMu.Unlock()
		return detachedIngressSnapshot{}, errServiceNotRunning
	}
	if s.ReplayBlocked() {
		s.openLedgerMu.Unlock()
		return detachedIngressSnapshot{}, replayfault.ErrBlocked
	}

	fault := (*replayfault.Fault)(nil)
	if s.replayFaults != nil {
		fault = s.replayFaults.Snapshot()
	}
	s.openLedgerMu.Unlock()

	return detachedIngressSnapshot{
		openView:   openView,
		current:    current,
		queue:      queue,
		localTxs:   localTxs,
		closed:     closed,
		openMarker: openMarker,
		rules:      cfg.Rules,
		config:     cfg,
		fault:      fault,
		validated:  validated,
		stopDone:   stopDone,
	}, nil
}

// submitDetachedIngressOwned runs with the caller's admission slot held.
func (s *Service) submitDetachedIngressOwned(req detachedIngressRequest) (
	resultOutcome openledger.SubmitOutcome,
	resultCurrent *ledger.Ledger,
	resultQueue *txq.TxQ,
	resultConfig openledger.ApplyConfig,
	resultErr error,
) {
	failure := openledger.SubmitOutcome{Class: openledger.ResultFailure}
	workCtx := s.detachedIngress.ctx()
	releaseWork, err := s.acquireLedgerWork(workCtx)
	if err != nil {
		if workCtx.Err() != nil {
			err = errServiceNotRunning
		}
		return failure, nil, nil, openledger.ApplyConfig{}, err
	}
	defer releaseWork()
	if req.prepare != nil {
		req.prepare(workCtx)
	}

	retry := memoryAdmissionRetry{origin: req.origin}
	ownedParsedAvailable := req.ownedParsed
	var parsedForRetry tx.Transaction
	defer func() {
		if retry.last != nil {
			retry.finish(s, resultErr)
		}
	}()
	for {
		snapshot, err := s.captureDetachedIngress(req)
		if err != nil {
			return failure, nil, nil, openledger.ApplyConfig{}, err
		}

		if req.signatureEligible && !snapshot.config.SkipSignatureVerification {
			if req.initialSignatureErr != nil && snapshot.rules == req.initialRules {
				current, fenceErr := s.detachedIngressSnapshotCurrent(req, snapshot)
				if fenceErr != nil {
					return failure, snapshot.current, snapshot.queue, snapshot.config, fenceErr
				}
				if !current {
					continue
				}
				return failure, snapshot.current, snapshot.queue, snapshot.config, &detachedIngressValidationError{
					kind: detachedIngressSignatureFailure,
					err:  req.initialSignatureErr,
				}
			}
			if req.initialSignatureErr != nil || !req.initialSignatureCheck || snapshot.rules != req.initialRules {
				if req.verifySignature == nil {
					return failure, snapshot.current, snapshot.queue, snapshot.config, errors.New("detached ingress: signature verifier unavailable")
				}
				verificationTx := req.pendingTx.Parsed
				if !ownedParsedAvailable {
					if parsedForRetry == nil {
						parsedForRetry, err = s.reparseDetachedIngress(req.pendingTx.Blob)
						if err != nil {
							return failure, snapshot.current, snapshot.queue, snapshot.config, err
						}
					}
					verificationTx = parsedForRetry
				}
				if err := req.verifySignature(verificationTx, snapshot.rules); err != nil {
					current, fenceErr := s.detachedIngressSnapshotCurrent(req, snapshot)
					if fenceErr != nil {
						return failure, snapshot.current, snapshot.queue, snapshot.config, fenceErr
					}
					if !current {
						continue
					}
					return failure, snapshot.current, snapshot.queue, snapshot.config, &detachedIngressValidationError{
						kind: detachedIngressSignatureFailure,
						err:  err,
					}
				}
			}
		}
		if req.localReason != "" {
			return failure, snapshot.current, snapshot.queue, snapshot.config, &detachedIngressValidationError{
				kind: detachedIngressLocalFailure,
				err:  errors.New(req.localReason),
			}
		}

		releaseMemory, err := s.reserveDetachedIngressMemory(workCtx, snapshot, req.pendingTx)
		if err != nil {
			if err = retry.wait(workCtx, s, err); err == nil {
				continue
			}
			if workCtx.Err() != nil {
				err = errServiceNotRunning
			}
			return failure, snapshot.current, snapshot.queue, snapshot.config, err
		}
		retry.finish(s, nil)
		retry = memoryAdmissionRetry{origin: req.origin}
		privateView, err := snapshot.openView.ForkCurrent(snapshot.current)
		if err != nil {
			releaseMemory()
			return failure, snapshot.current, snapshot.queue, snapshot.config, err
		}
		privateQueue := (*txq.TxQ)(nil)
		if snapshot.queue != nil {
			privateQueue, err = snapshot.queue.Fork()
			if err != nil {
				releaseMemory()
				return failure, snapshot.current, snapshot.queue, snapshot.config, err
			}
		}

		// SubmitDetailed may annotate parsed transactions while applying them.
		// A transferred parsed value is consumed by the first private attempt;
		// every later attempt starts from a fresh parse of the canonical blob.
		attemptTx := req.pendingTx
		if !ownedParsedAvailable {
			if parsedForRetry == nil {
				parsedForRetry, err = s.reparseDetachedIngress(attemptTx.Blob)
				if err != nil {
					releaseMemory()
					return failure, snapshot.current, snapshot.queue, snapshot.config, err
				}
			}
			attemptTx.Parsed = parsedForRetry
		}
		if attemptTx.Parsed == nil {
			releaseMemory()
			return failure, snapshot.current, snapshot.queue, snapshot.config, errors.New("detached ingress: parsed transaction unavailable")
		}
		attemptStarted := time.Now()
		s.ingressMetrics.privateAttempts.Add(1)
		outcome := privateView.SubmitDetailed(attemptTx, snapshot.config, privateQueue)
		attemptDuration := time.Since(attemptStarted)
		s.ingressMetrics.privateAttemptNanos.Add(uint64(attemptDuration))
		ownedParsedAvailable = false
		parsedForRetry = nil
		privateCurrent := privateView.Current()
		commitData := detachedIngressCommitData{}
		if req.prepareCommit != nil {
			commitData = req.prepareCommit(privateCurrent, snapshot.validated, privateQueue, snapshot.config, outcome, attemptTx)
		}
		commitPending := req.pendingTx
		commitPending.Parsed = nil
		committed, err := s.commitDetachedIngress(req, snapshot, privateCurrent, privateQueue, outcome, commitData, commitPending)
		releaseMemory()
		if err != nil {
			return failure, nil, nil, openledger.ApplyConfig{}, err
		}
		if !committed {
			s.ingressMetrics.staleRetries.Add(1)
			s.ingressMetrics.staleRetryNanos.Add(uint64(attemptDuration))
			continue
		}
		publishedCurrent := snapshot.current
		if outcome.Changed {
			publishedCurrent = privateCurrent
		}
		return outcome, publishedCurrent, privateQueue, snapshot.config, nil
	}
}

func (s *Service) detachedIngressSnapshotCurrent(req detachedIngressRequest, snapshot detachedIngressSnapshot) (bool, error) {
	if err := s.lockOpenLedgerIfRunning(openLedgerIngress); err != nil {
		return false, err
	}
	defer s.openLedgerMu.Unlock()

	s.lifecycleMu.Lock()
	if s.lifecycleState != serviceRunning || s.stopDone != snapshot.stopDone {
		s.lifecycleMu.Unlock()
		return false, nil
	}
	if s.ReplayBlocked() {
		s.lifecycleMu.Unlock()
		return false, replayfault.ErrBlocked
	}
	s.mu.RLock()
	stale := s.openLedgerView == nil ||
		s.openLedgerView != snapshot.openView ||
		s.openLedgerView.Current() != snapshot.current ||
		s.txQueue != snapshot.queue ||
		s.localTxs != snapshot.localTxs ||
		s.closedLedger != snapshot.closed ||
		s.openLedger != snapshot.openMarker
	if !stale {
		cfg, cfgErr := s.applyConfigLocked()
		if req.configure != nil {
			cfg = req.configure(cfg)
		}
		stale = cfgErr != nil || cfg.Rules != snapshot.rules
	}
	if !stale {
		fault := (*replayfault.Fault)(nil)
		if s.replayFaults != nil {
			fault = s.replayFaults.Snapshot()
		}
		stale = (fault == nil) != (snapshot.fault == nil) ||
			(fault != nil && snapshot.fault != nil && fault.ID != snapshot.fault.ID)
	}
	s.mu.RUnlock()
	s.lifecycleMu.Unlock()
	return !stale, nil
}

func (s *Service) reserveDetachedIngressMemory(
	ctx context.Context,
	snapshot detachedIngressSnapshot,
	pending openledger.PendingTx,
) (func(), error) {
	stats, err := openledger.TransactionStatsContext(ctx, snapshot.current)
	if err != nil {
		return nil, err
	}
	var queueStats openledger.TransactionSnapshotStats
	if snapshot.queue != nil {
		count, bytes := snapshot.queue.SnapshotStats()
		queueStats = openledger.TransactionSnapshotStats{Count: count, Bytes: bytes}
	}
	bytes := preferredTransitionEstimate(
		stats,
		openledger.TransactionSnapshotStats{},
		queueStats,
		[]openledger.PendingTx{pending},
	)
	if s.speculativeMemoryBudget() == nil && bytes > speculativeSuccessorMemoryLimit {
		return nil, newSpeculativeMemoryError(nil, bytes, speculativeSuccessorMemoryLimit, true, nil)
	}
	return s.acquirePreferredTransitionMemory(ctx, bytes)
}

func (s *Service) commitDetachedIngress(
	req detachedIngressRequest,
	snapshot detachedIngressSnapshot,
	privateCurrent *ledger.Ledger,
	privateQueue *txq.TxQ,
	outcome openledger.SubmitOutcome,
	commitData detachedIngressCommitData,
	commitPending openledger.PendingTx,
) (bool, error) {
	if err := s.lockOpenLedgerIfRunning(openLedgerIngress); err != nil {
		return false, err
	}

	// Keep the lifecycle state transition and publication fence adjacent. Stop
	// waits for this admission owner, so it cannot close the service between the
	// running check and the private publication.
	s.lifecycleMu.Lock()
	if s.lifecycleState != serviceRunning || s.stopDone != snapshot.stopDone {
		s.lifecycleMu.Unlock()
		s.openLedgerMu.Unlock()
		return false, nil
	}
	if s.ReplayBlocked() {
		s.lifecycleMu.Unlock()
		s.openLedgerMu.Unlock()
		return false, replayfault.ErrBlocked
	}
	s.mu.Lock()
	stale := s.openLedgerView != snapshot.openView ||
		s.openLedgerView.Current() != snapshot.current ||
		s.txQueue != snapshot.queue ||
		s.localTxs != snapshot.localTxs ||
		s.closedLedger != snapshot.closed ||
		s.openLedger != snapshot.openMarker
	if !stale {
		cfg, cfgErr := s.applyConfigLocked()
		if req.configure != nil {
			cfg = req.configure(cfg)
		}
		stale = cfgErr != nil || cfg.Rules != snapshot.rules
	}
	if !stale {
		fault := (*replayfault.Fault)(nil)
		if s.replayFaults != nil {
			fault = s.replayFaults.Snapshot()
		}
		stale = (fault == nil) != (snapshot.fault == nil) ||
			(fault != nil && snapshot.fault != nil && fault.ID != snapshot.fault.ID)
	}
	if !stale && outcome.Changed {
		stale = !snapshot.openView.PublishCurrent(snapshot.current, privateCurrent)
	}
	if !stale {
		// Publish the complete private queue even when its size is unchanged:
		// fee metrics, penalties, ordering, and account state are mutable queue
		// state that count/byte comparisons cannot capture.
		s.txQueue = privateQueue
	}
	s.mu.Unlock()
	s.lifecycleMu.Unlock()
	if stale {
		s.openLedgerMu.Unlock()
		return false, nil
	}
	// Publish the relay disposition before a successor can promote this transaction.
	if outcome.Class == openledger.ResultSuccess {
		s.rememberRelayTransaction(req.pendingTx.Hash, req.pendingTx.Blob, outcome.Queued)
	}
	publishedCurrent := snapshot.current
	if outcome.Changed {
		publishedCurrent = privateCurrent
	}
	if req.onCommit != nil {
		req.onCommit(publishedCurrent, privateQueue, snapshot.localTxs, snapshot.config, outcome, commitData, commitPending)
	}
	s.openLedgerMu.Unlock()
	return true, nil
}
