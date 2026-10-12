package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

var (
	// ErrPreferredTransitionBusy is returned only when a caller explicitly
	// requests an overlapping detached transition. The synchronous wrapper
	// waits for the active owner and therefore normally does not expose it.
	ErrPreferredTransitionBusy     = errors.New("preferred ledger transition already active")
	ErrPreferredTransitionNotReady = errors.New("preferred ledger transition is not ready")
	ErrPreferredTransitionStale    = errors.New("preferred ledger transition became stale")
	ErrPreferredTransitionMemory   = errors.New("preferred ledger transition memory budget exhausted")
)

const (
	preferredTransitionBaseBytes = 1 << 20
)

type preferredRelay struct {
	hash [32]byte
	blob []byte
}

type preferredTransitionState uint8

const (
	preferredTransitionPreparing preferredTransitionState = iota
	preferredTransitionReady
	preferredTransitionConsuming
	preferredTransitionPublished
	preferredTransitionReleased
)

// PreparedPreferredLedger owns a detached successor until the service's
// publication fence accepts it. Preparation, including TxQ and open-ledger
// mutation, happens against private objects. TryPublish is deliberately
// non-blocking on openLedgerMu because consensus callers may hold their engine
// mutex while invoking it. Finish performs relay and notifications after that
// mutex and the service publication locks have been released.
type PreparedPreferredLedger struct {
	service *Service

	parent         *ledger.Ledger
	expectedClosed *ledger.Ledger
	expectedOpen   *ledger.Ledger
	expectedView   *openledger.OpenLedger
	expectedQueue  *txq.TxQ
	expectedLocals *localtxs.LocalTxs
	expectedReplay *inbound.ReplayDelta

	validatedAtPrepare *ledger.Ledger
	provisionalReplace bool
	sameLedger         bool

	prior  []openledger.PendingTx
	locals []openledger.PendingTx

	networkID     uint32
	lastRoundSlow bool

	openView     *openledger.OpenLedger
	queue        *txq.TxQ
	staged       *stagedTransactionResults
	relay        []preferredRelay
	relayFn      func(blob []byte)
	privateBytes uint64

	releaseWork   func()
	releaseMemory func()
	ctx           context.Context
	cancel        context.CancelFunc
	ready         chan struct{}
	readyOnce     sync.Once
	finishOnce    sync.Once
	releaseOnce   sync.Once

	stateMu sync.Mutex
	state   preferredTransitionState
	runErr  error
	notify  validatedLedgerNotification

	timings preferredTransitionTimings
}

type preferredTransitionTimings struct {
	started        time.Time
	preparation    time.Duration
	stageResults   time.Duration
	retryApply     time.Duration
	openLedgerWait time.Duration
	openLedgerHold time.Duration
	serviceWait    time.Duration
	historyWait    time.Duration
	publication    time.Duration
}

// Ready is closed when detached preparation has either completed or failed.
// It never waits for the service gate or any engine mutex.
func (p *PreparedPreferredLedger) Ready() <-chan struct{} {
	if p == nil {
		return closedPreferredTransitionChan()
	}
	return p.ready
}

var preferredClosedReady = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

func closedPreferredTransitionChan() <-chan struct{} { return preferredClosedReady }

// Wait waits only for detached preparation. A canceled wait does not cancel
// the prepared owner; callers that abandon it must still call Release.
func (p *PreparedPreferredLedger) Wait(ctx context.Context) error {
	if p == nil {
		return ErrPreferredTransitionStale
	}
	select {
	case <-p.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.stateMu.Lock()
	err := p.runErr
	p.stateMu.Unlock()
	return err
}

// PreparePreferredLedger captures the canonical frontier and starts detached
// successor preparation. It holds the ledger-work admission and the explicit
// transition owner until TryPublish or Release completes.
func (s *Service) PreparePreferredLedger(ctx context.Context, parent *ledger.Ledger) (*PreparedPreferredLedger, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fail := func(err error) (*PreparedPreferredLedger, error) {
		return nil, err
	}
	if parent == nil || !parent.IsClosed() {
		return fail(ErrPreferredChainSwitch)
	}
	if err := s.waitPreferredTransition(ctx); err != nil {
		return fail(err)
	}

	p := &PreparedPreferredLedger{
		service: s,
		parent:  parent,
		ready:   make(chan struct{}),
		state:   preferredTransitionPreparing,
		timings: preferredTransitionTimings{started: time.Now()},
	}
	p.ctx, p.cancel = context.WithCancel(ctx)
	if err := s.beginPreferredTransition(p); err != nil {
		p.cancel()
		return fail(err)
	}
	failPrepared := func(err error) (*PreparedPreferredLedger, error) {
		if p.cancel != nil {
			p.cancel()
		}
		p.releaseWorkAdmission()
		p.markReady(err)
		return nil, err
	}
	releaseWork, err := s.acquireLedgerWork(p.ctx)
	if err != nil {
		return failPrepared(err)
	}
	p.releaseWork = releaseWork
	if !s.isServiceOwnedLedger(parent) {
		if err := s.VerifyDetachedLedger(p.ctx, parent); err != nil {
			return failPrepared(fmt.Errorf("preferred ledger state admission: %w", err))
		}
	}
	gateWait, err := s.openLedgerMu.LockRoleContext(p.ctx, openLedgerPreferredSwitch)
	if err != nil {
		return failPrepared(err)
	}
	gateHeld := true
	gateAcquired := gateWait.AcquiredAt
	defer func() {
		if gateHeld {
			s.openLedgerMu.Unlock()
		}
	}()
	p.timings.openLedgerWait = gateWait.Wait
	if !s.preferredLifecycleRunning() {
		return failPrepared(errServiceNotRunning)
	}
	if err := p.ctx.Err(); err != nil {
		return failPrepared(err)
	}

	s.mu.RLock()
	if s.ReplayBlocked() {
		s.mu.RUnlock()
		return failPrepared(replayfault.ErrBlocked)
	}
	if s.closedLedger == nil {
		s.mu.RUnlock()
		return failPrepared(svcerr.ErrNoClosedLedger)
	}
	if s.openLedger == nil || s.openLedgerView == nil {
		s.mu.RUnlock()
		return failPrepared(svcerr.ErrNoOpenLedger)
	}
	p.expectedClosed = s.closedLedger
	p.expectedOpen = s.openLedger
	p.expectedView = s.openLedgerView
	p.expectedQueue = s.txQueue
	p.expectedLocals = s.localTxs
	p.expectedReplay = s.startupReplay
	p.validatedAtPrepare = s.validatedLedger
	p.networkID = s.config.NetworkID
	p.lastRoundSlow = true
	p.relayFn = s.txRelay
	p.provisionalReplace = s.networkLedgerState == networkLedgerFastLoadProvisional &&
		parent.Sequence() == s.closedLedger.Sequence() && parent.Hash() != s.closedLedger.Hash()
	if s.validatedLedger != nil {
		validatedSeq := s.validatedLedger.Sequence()
		parentHash := parent.Hash()
		if parent.Sequence() < validatedSeq ||
			(parent.Sequence() == validatedSeq && parentHash != s.validatedLedger.Hash() &&
				s.networkLedgerState != networkLedgerFastLoadProvisional) {
			s.mu.RUnlock()
			return failPrepared(ErrPreferredChainSwitch)
		}
		p.provisionalReplace = p.provisionalReplace ||
			parent.Sequence() == validatedSeq && parentHash != s.validatedLedger.Hash()
	}
	p.sameLedger = parent.Sequence() == s.closedLedger.Sequence() && parent.Hash() == s.closedLedger.Hash()
	openView := s.openLedgerView
	queue := s.txQueue
	localsPool := s.localTxs
	s.mu.RUnlock()
	s.openLedgerMu.Unlock()
	gateHeld = false
	p.timings.openLedgerHold = time.Since(gateAcquired)

	if p.sameLedger {
		p.timings.openLedgerHold = time.Since(gateAcquired)
		p.timings.preparation = time.Since(p.timings.started)
		p.markReady(nil)
		return p, nil
	}

	cleanupPrepared := func(err error) (*PreparedPreferredLedger, error) {
		if p.cancel != nil {
			p.cancel()
		}
		p.releaseRetainedMemory()
		p.releaseWorkAdmission()
		p.markReady(err)
		return nil, err
	}

	// The transition owner now parks all mutating ingress while these snapshots
	// run. Reads remain available because neither the service nor history locks
	// are held during traversal or queue fork.
	stats, err := openView.CurrentTransactionStats(p.ctx)
	if err != nil {
		return cleanupPrepared(fmt.Errorf("size open-ledger transactions: %w", err))
	}
	localSnapshot := []openledger.PendingTx(nil)
	if localsPool != nil {
		localSnapshot = localsPool.GetTxSet()
	}
	queueSnapshot := []*txq.CandidateDetails(nil)
	if queue != nil {
		queueSnapshot = queue.AllTxs()
	}
	privateBytes := preferredTransitionEstimate(stats, localSnapshot, queueSnapshot)
	p.privateBytes = privateBytes
	p.releaseMemory, err = s.acquirePreferredTransitionMemory(p.ctx, privateBytes)
	if err != nil {
		return cleanupPrepared(err)
	}
	queueSnapshot = nil

	// Parse the old view only after admission has covered the retained
	// successor. The service gate is free, but the owner keeps all mutating
	// ingress parked until publication or Release.
	prior, err := openView.CurrentTransactions(p.ctx)
	if err != nil {
		return cleanupPrepared(fmt.Errorf("capture open-ledger transactions: %w", err))
	}
	p.locals = localSnapshot
	if err := p.ctx.Err(); err != nil {
		return cleanupPrepared(err)
	}
	if queue != nil {
		// Fork snapshots every queue-owned candidate, account ordering and fee
		// metric so private preparation cannot mutate live admission state.
		p.queue, err = queue.Fork()
		if err != nil {
			return cleanupPrepared(fmt.Errorf("preferred ledger transition: fork TxQ: %w", err))
		}
		if p.queue == nil {
			return cleanupPrepared(errors.New("preferred ledger transition: TxQ.Fork returned nil"))
		}
	}
	if prior == nil {
		prior = make([]openledger.PendingTx, 0)
	}
	p.prior = prior
	go p.run()
	return p, nil
}

// SwitchToPreferredLedger preserves the synchronous public behavior while the
// successor itself is prepared and fenced through the detached API.
func (s *Service) switchToPreferredLedger(parent *ledger.Ledger, beforeLock func()) error {
	if beforeLock != nil {
		beforeLock()
	}
	var p *PreparedPreferredLedger
	var err error
	for {
		p, err = s.PreparePreferredLedger(context.Background(), parent)
		if !errors.Is(err, ErrPreferredTransitionBusy) {
			break
		}
		if waitErr := s.waitPreferredTransition(context.Background()); waitErr != nil {
			return waitErr
		}
	}
	if err != nil {
		return err
	}
	if err := p.Wait(context.Background()); err != nil {
		p.Release()
		return err
	}
	published, err := p.TryPublish()
	if err == nil && published {
		p.Finish()
	}
	p.Release()
	return err
}

// TryPublish performs only the short service publication fence. It never
// acquires openLedgerMu, waits for ingress, or calls relay/notification code.
func (p *PreparedPreferredLedger) TryPublish() (bool, error) {
	if p == nil || p.service == nil {
		return false, ErrPreferredTransitionStale
	}
	select {
	case <-p.ready:
	default:
		return false, ErrPreferredTransitionNotReady
	}
	p.stateMu.Lock()
	runErr := p.runErr
	state := p.state
	p.stateMu.Unlock()
	if runErr != nil {
		return false, runErr
	}
	if state != preferredTransitionReady {
		if state == preferredTransitionPublished {
			return true, nil
		}
		return false, ErrPreferredTransitionStale
	}
	if p.ctx != nil {
		if err := p.ctx.Err(); err != nil {
			p.abort(err)
			return false, err
		}
	}
	p.stateMu.Lock()
	if p.state != preferredTransitionReady {
		state := p.state
		p.stateMu.Unlock()
		if state == preferredTransitionPublished {
			return true, nil
		}
		return false, ErrPreferredTransitionStale
	}
	p.state = preferredTransitionConsuming
	p.stateMu.Unlock()

	s := p.service
	s.preferredTransitionMu.Lock()
	if !s.preferredTransitionActive || s.preferredTransitionOwner != p {
		s.preferredTransitionMu.Unlock()
		return p.abort(ErrPreferredTransitionStale)
	}
	defer s.preferredTransitionMu.Unlock()

	if !s.preferredLifecycleRunning() {
		return p.abortLocked(errServiceNotRunning)
	}
	serviceStarted := time.Now()
	s.mu.Lock()
	p.timings.serviceWait = time.Since(serviceStarted)
	if s.ReplayBlocked() {
		s.mu.Unlock()
		return p.abortLocked(replayfault.ErrBlocked)
	}
	if s.closedLedger != p.expectedClosed || s.openLedger != p.expectedOpen ||
		s.openLedgerView != p.expectedView || s.txQueue != p.expectedQueue ||
		s.localTxs != p.expectedLocals || s.startupReplay != p.expectedReplay ||
		s.validatedLedger != p.validatedAtPrepare {
		s.mu.Unlock()
		return p.abortLocked(fmt.Errorf("%w: ledger ownership changed during preparation: %w", ErrPreferredChainSwitch, ErrPreferredTransitionStale))
	}
	if p.parent == nil || !p.parent.IsClosed() {
		s.mu.Unlock()
		return p.abortLocked(ErrPreferredChainSwitch)
	}
	if s.validatedLedger != nil {
		validatedSeq := s.validatedLedger.Sequence()
		if p.parent.Sequence() < validatedSeq ||
			(p.parent.Sequence() == validatedSeq && p.parent.Hash() != s.validatedLedger.Hash() &&
				s.networkLedgerState != networkLedgerFastLoadProvisional) {
			s.mu.Unlock()
			return p.abortLocked(ErrPreferredChainSwitch)
		}
	}
	if p.sameLedger {
		publicationStarted := time.Now()
		s.completeInitialSyncLocked()
		if p.parent.IsValidated() {
			s.confirmFastLoadLocked(p.parent.Sequence(), p.parent.Hash())
		}
		p.stateMu.Lock()
		p.state = preferredTransitionPublished
		p.stateMu.Unlock()
		s.mu.Unlock()
		p.timings.publication = time.Since(publicationStarted)
		s.endPreferredTransitionLocked(p)
		return true, nil
	}

	previousValidated := s.validatedLedger
	publicationStarted := time.Now()
	historyStarted := time.Now()
	s.historyComponent.mu.Lock()
	p.timings.historyWait = time.Since(historyStarted)

	s.fixMismatchLocked(p.parent)
	s.purgeConflictingHistoryLocked(p.parent)
	s.putHistoryLocked(p.parent)
	s.cachePersistedLedgerLocked(p.parent)
	s.closedLedger = p.parent
	s.openLedgerView = p.openView
	s.openLedger = p.openView.Current()
	s.txQueue = p.queue
	s.pendingTxs = nil
	if p.provisionalReplace {
		s.ledgerEventMu.Lock()
		s.ledgerEventHaveFrontier = p.parent.Sequence() != 0
		if s.ledgerEventHaveFrontier {
			s.ledgerEventFrontierSeq = p.parent.Sequence() - 1
			s.ledgerEventFrontierHash = p.parent.ParentHash()
		}
		for seq := range s.ledgerEventCandidates {
			delete(s.ledgerEventCandidates, seq)
		}
		s.ledgerEventMu.Unlock()
	}
	s.completeInitialSyncLocked()
	if p.parent.IsValidated() {
		s.confirmFastLoadLocked(p.parent.Sequence(), p.parent.Hash())
	}
	s.commitTransactionResultsLocked(p.staged)
	var txResults []TransactionResultEvent
	if s.hasEventSink() {
		txResults = p.staged.results
	}
	if p.parent.IsValidated() {
		s.evictOldHistoryLocked(p.parent.Sequence())
		s.enqueuePersist(p.parent)
	} else {
		s.retainValidationCandidateLocked(p.parent)
		if s.hasEventSink() {
			s.stashPendingValidationLocked(p.parent.Hash(), &LedgerAcceptedEvent{
				LedgerInfo:         ledgerInfo(p.parent),
				Ledger:             p.parent,
				TransactionResults: txResults,
			})
		}
	}
	p.notify = s.validatedLedgerNotificationLocked(previousValidated)
	s.tickLoadFeeLocked()
	s.historyComponent.mu.Unlock()
	s.mu.Unlock()
	p.timings.publication = time.Since(publicationStarted)

	p.stateMu.Lock()
	p.state = preferredTransitionPublished
	p.stateMu.Unlock()
	s.endPreferredTransitionLocked(p)
	return true, nil
}

// Finish runs observable side effects after TryPublish has released all
// service/history locks and after the caller has released its engine mutex.
func (p *PreparedPreferredLedger) Finish() {
	if p == nil || p.service == nil {
		return
	}
	p.finishOnce.Do(func() {
		p.stateMu.Lock()
		published := p.state == preferredTransitionPublished
		notify := p.notify
		relay := append([]preferredRelay(nil), p.relay...)
		relayFn := p.relayFn
		p.stateMu.Unlock()
		if !published {
			return
		}
		// Complete all service-owned relay-cache mutations before invoking any
		// external callback. A callback may synchronously stop the service, which
		// clears the cache and waits for ledger-work admission to drain.
		for _, item := range relay {
			p.service.rememberRelayTransaction(item.hash, item.blob, false)
		}
		p.stateMu.Lock()
		p.relay = nil
		p.stateMu.Unlock()
		p.releaseWorkAdmission()
		// The staged relay payload is independently bounded by
		// relayTxCacheMaxBytes and is now owned by this finish operation. Drop
		// the persistence reservation before callbacks so a callback can stop
		// the service without waiting behind the private successor budget.
		p.releaseRetainedMemory()
		p.logTimings()
		for _, item := range relay {
			if relayFn != nil {
				relayFn(item.blob)
			}
		}
		notify.notify()
	})
}

// Release cancels unfinished preparation and releases every retained
// generation, memory and lifecycle admission. It is safe to call repeatedly.
func (p *PreparedPreferredLedger) Release() {
	if p == nil {
		return
	}
	p.releaseOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		<-p.ready
		p.service.endPreferredTransition(p)
		p.stateMu.Lock()
		if p.state != preferredTransitionPublished {
			p.state = preferredTransitionReleased
		}
		p.stateMu.Unlock()
		p.releaseRetainedMemory()
		p.releaseWorkAdmission()
	})
}

func (p *PreparedPreferredLedger) releaseWorkAdmission() {
	p.stateMu.Lock()
	release := p.releaseWork
	p.releaseWork = nil
	p.stateMu.Unlock()
	if release != nil {
		release()
	}
}

func (p *PreparedPreferredLedger) releaseRetainedMemory() {
	p.stateMu.Lock()
	release := p.releaseMemory
	p.releaseMemory = nil
	p.stateMu.Unlock()
	if release != nil {
		release()
	}
}

func (p *PreparedPreferredLedger) markReady(err error) {
	p.stateMu.Lock()
	p.runErr = err
	if err != nil {
		p.state = preferredTransitionReleased
	} else if p.state == preferredTransitionPreparing {
		p.state = preferredTransitionReady
	}
	p.stateMu.Unlock()
	p.readyOnce.Do(func() { close(p.ready) })
	if err != nil {
		p.service.endPreferredTransition(p)
	}
}

func (p *PreparedPreferredLedger) run() {
	started := time.Now()
	if p.sameLedger {
		p.timings.preparation = time.Since(started)
		p.markReady(nil)
		return
	}
	var err error
	stageStarted := time.Now()
	p.staged, err = stageTransactionResultsContext(p.ctx, p.parent, p.parent.Sequence(), p.parent.Hash())
	p.timings.stageResults = time.Since(stageStarted)
	if err == nil {
		var rules = openLedgerRules(p.validatedAtPrepare, p.service.logger)
		baseFee, reserveBase, reserveIncrement := readFeesFromLedger(p.parent)
		cfg := openledger.ApplyConfig{
			BaseFee:          baseFee,
			ReserveBase:      reserveBase,
			ReserveIncrement: reserveIncrement,
			LedgerSequence:   p.parent.Sequence() + 1,
			NetworkID:        p.networkID,
			ParentCloseTime:  parentCloseTimeRippleEpoch(p.parent),
			Logger:           p.service.logger,
			Rules:            rules,
			FeeTrack:         p.service.feeTrack,
		}
		var retrySalt [32]byte
		cfg.RetrySalt = &retrySalt
		private, privateErr := openledger.New(p.parent, openledger.Config{
			NetworkID: p.networkID,
			Logger:    p.service.logger,
			Rules:     rules,
		})
		if privateErr != nil {
			err = privateErr
		} else {
			p.openView = private
			retryStarted := time.Now()
			retries := append([]openledger.PendingTx(nil), p.locals...)
			for _, local := range p.locals {
				prepared, parseErr := openledger.ParsePendingTx(local.Blob)
				if parseErr != nil {
					err = fmt.Errorf("parse preferred-ledger local transaction %x: %w", local.Hash, parseErr)
					break
				}
				if prepared.Hash != local.Hash || prepared.Account != local.Account {
					err = fmt.Errorf("preferred-ledger local transaction identity does not match blob %x", local.Hash)
					break
				}
			}
			if err != nil {
				p.timings.retryApply = time.Since(retryStarted)
				p.timings.preparation = time.Since(started)
				p.markReady(err)
				return
			}
			var stagedRelayBytes int64
			var relayStageErr error
			err = private.AcceptWithPrecommitContextFrom(
				p.ctx,
				p.parent,
				p.prior,
				nil,
				false,
				&retries,
				cfg,
				p.queue,
				func() {
					if p.queue != nil {
						baseFee, reserveBase, reserveIncrement := readFeesFromLedger(p.parent)
						p.queue.ProcessClosedLedger(&closedLedgerCtx{ledger: p.parent, baseFee: baseFee, reserveBase: reserveBase, reserveIncrement: reserveIncrement}, p.lastRoundSlow)
					}
				},
				func(view *ledger.Ledger) {
					if p.queue == nil || view == nil {
						return
					}
					viewCfg := cfg
					viewCfg.LedgerSequence = view.Sequence()
					_ = p.queue.Accept(openledger.NewTxqAdapter(view, viewCfg))
				},
				func(hash [32]byte, blob []byte) {
					if stagedRelayBytes+int64(len(blob)) > relayTxCacheMaxBytes {
						relayStageErr = ErrPreferredTransitionMemory
						return
					}
					stagedRelayBytes += int64(len(blob))
					p.relay = append(p.relay, preferredRelay{hash: hash, blob: append([]byte(nil), blob...)})
				},
				nil,
			)
			if err == nil && relayStageErr != nil {
				err = relayStageErr
			}
			p.timings.retryApply = time.Since(retryStarted)
		}
	}
	p.timings.preparation = time.Since(started)
	p.markReady(err)
}

func (p *PreparedPreferredLedger) abort(err error) (bool, error) {
	p.service.preferredTransitionMu.Lock()
	defer p.service.preferredTransitionMu.Unlock()
	return p.abortLocked(err)
}

func (p *PreparedPreferredLedger) abortLocked(err error) (bool, error) {
	p.stateMu.Lock()
	p.state = preferredTransitionReleased
	p.runErr = err
	p.stateMu.Unlock()
	p.service.endPreferredTransitionLocked(p)
	return false, err
}

func (p *PreparedPreferredLedger) logTimings() {
	if p == nil || p.service == nil || p.timings.started.IsZero() {
		return
	}
	p.service.logger.Info("Preferred ledger switch timings",
		"seq", p.parent.Sequence(),
		"total", time.Since(p.timings.started),
		"preparation", p.timings.preparation,
		"stage_results", p.timings.stageResults,
		"retry_apply", p.timings.retryApply,
		"open_ledger_wait", p.timings.openLedgerWait,
		"open_ledger_hold", p.timings.openLedgerHold,
		"service_wait", p.timings.serviceWait,
		"history_wait", p.timings.historyWait,
		"publication", p.timings.publication,
	)
}

func (s *Service) beginPreferredTransition(p *PreparedPreferredLedger) error {
	s.preferredTransitionMu.Lock()
	defer s.preferredTransitionMu.Unlock()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycleState != serviceRunning {
		return errServiceNotRunning
	}
	if s.preferredTransitionActive {
		return ErrPreferredTransitionBusy
	}
	s.preferredTransitionActive = true
	s.preferredTransitionOwner = p
	s.preferredTransitionCancel = p.cancel
	s.preferredTransitionWake = make(chan struct{})
	return nil
}

func (s *Service) endPreferredTransition(p *PreparedPreferredLedger) {
	s.preferredTransitionMu.Lock()
	s.endPreferredTransitionLocked(p)
	s.preferredTransitionMu.Unlock()
}

// endPreferredTransitionLocked releases the owner while preferredTransitionMu
// is already held. The wake channel is closed before unlock so every waiter
// observes the handoff as one atomic state change.
func (s *Service) endPreferredTransitionLocked(p *PreparedPreferredLedger) {
	if !s.preferredTransitionActive || (p != nil && s.preferredTransitionOwner != p) {
		return
	}
	s.preferredTransitionActive = false
	s.preferredTransitionOwner = nil
	s.preferredTransitionCancel = nil
	wake := s.preferredTransitionWake
	s.preferredTransitionWake = nil
	if wake != nil {
		close(wake)
	}
}

func (s *Service) preferredTransitionIsActive() bool {
	s.preferredTransitionMu.Lock()
	active := s.preferredTransitionActive
	s.preferredTransitionMu.Unlock()
	return active
}

func (s *Service) waitPreferredTransition(ctx context.Context) error {
	for {
		s.preferredTransitionMu.Lock()
		if !s.preferredTransitionActive {
			s.preferredTransitionMu.Unlock()
			return nil
		}
		wake := s.preferredTransitionWake
		s.preferredTransitionMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

func (s *Service) cancelPreferredTransition() {
	s.preferredTransitionMu.Lock()
	cancel := s.preferredTransitionCancel
	owner := s.preferredTransitionOwner
	s.preferredTransitionMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if owner != nil {
		// Keep the transition owner until detached preparation has observed
		// cancellation and closed ready. This prevents parked ingress or the
		// shutdown gate from overtaking a worker still draining private state.
		<-owner.ready
		s.endPreferredTransition(owner)
	}
}

func (s *Service) preferredLifecycleRunning() bool {
	s.lifecycleMu.Lock()
	running := s.lifecycleState == serviceRunning
	s.lifecycleMu.Unlock()
	return running
}

const (
	preferredTransitionMetadataBytes = 4 << 10
	preferredTransitionResultBytes   = 4 << 10
)

// preferredTransitionEstimate charges the detached inputs and a second copy
// for the private successor/staged relay path. Metadata covers decoded
// transaction objects, queue containers and staged result/event records. Blob
// sizes come from the snapshots themselves so a large local or queued
// transaction is accounted by its actual retained bytes rather than by a
// transaction-count surrogate.
func preferredTransitionEstimate(
	stats openledger.TransactionSnapshotStats,
	locals []openledger.PendingTx,
	queue []*txq.CandidateDetails,
) uint64 {
	bytes := uint64(preferredTransitionBaseBytes)
	items := stats.Count
	add := func(amount uint64) bool {
		if ^uint64(0)-bytes < amount {
			bytes = ^uint64(0)
			return false
		}
		bytes += amount
		return true
	}
	if !add(stats.Bytes) {
		return bytes
	}
	for _, local := range locals {
		if items == ^uint64(0) {
			return ^uint64(0)
		}
		items++
		if !add(uint64(len(local.Blob))) {
			return bytes
		}
	}
	for _, candidate := range queue {
		if items == ^uint64(0) {
			return ^uint64(0)
		}
		items++
		if candidate != nil && !add(uint64(len(candidate.TxBlob))) {
			return bytes
		}
	}
	// The private open ledger and relay staging can retain a second copy of
	// every input blob while the publication fence still owns the first.
	var inputBytes uint64
	if !add(0) {
		return bytes
	}
	inputBytes = bytes - preferredTransitionBaseBytes
	if !add(inputBytes) {
		return bytes
	}
	if items > ^uint64(0)/preferredTransitionMetadataBytes {
		return ^uint64(0)
	}
	if !add(items * preferredTransitionMetadataBytes) {
		return bytes
	}
	if stats.Count > ^uint64(0)/preferredTransitionResultBytes {
		return ^uint64(0)
	}
	if !add(stats.Count * preferredTransitionResultBytes) {
		return bytes
	}
	return bytes
}

func (s *Service) acquirePreferredTransitionMemory(ctx context.Context, bytes uint64) (func(), error) {
	if bytes == 0 || s.config.PersistenceMemory == nil {
		return func() {}, nil
	}
	budget := s.config.PersistenceMemory
	capacity := budget.Limit()
	if provider, ok := budget.(interface{ Capacity() uint64 }); ok {
		if available := provider.Capacity(); available < capacity {
			capacity = available
		}
	}
	if bytes > capacity {
		return nil, ErrPreferredTransitionMemory
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		wake := budget.Changed()
		if budget.TryReserve(bytes) {
			return sync.OnceFunc(func() { budget.Release(bytes) }), nil
		}
		if wake == nil {
			return nil, ErrPreferredTransitionMemory
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}
