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

type successorOptions struct {
	deferSpeculation   bool
	replaySpeculation  bool
	preferred          bool
	admitted           bool
	allowReplayFault   bool
	onlyMatchingParent bool
	retries            []openledger.PendingTx
	retrySalt          *[32]byte
	anyDisputes        bool
	check              func(*Service) error
}

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
	service     *Service
	options     successorOptions
	replayFault *replayfault.Fault

	parent         *ledger.Ledger
	expectedClosed *ledger.Ledger
	expectedOpen   *ledger.Ledger
	expectedView   *openledger.OpenLedger
	expectedQueue  *txq.TxQ
	expectedLocals *localtxs.LocalTxs
	expectedReplay *inbound.ReplayDelta

	validatedAtPrepare *ledger.Ledger
	provisionalReplace bool
	keepOpen           bool

	prior      []openledger.PendingTx
	locals     []openledger.PendingTx
	unfinished *speculativeSuccessor

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
	memory        *successorMemory
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
// successor preparation. Callers must call Release on every returned owner,
// including after cancellation. After publication they must call Finish outside
// engine locks before Release. TryPublish releases transition ownership; Finish
// or Release drains ledger-work admission so Service.Stop can complete.
func (s *Service) PreparePreferredLedger(ctx context.Context, parent *ledger.Ledger) (*PreparedPreferredLedger, error) {
	return s.prepareSuccessor(ctx, parent, successorOptions{preferred: true})
}

func (s *Service) prepareSuccessor(ctx context.Context, parent *ledger.Ledger, options successorOptions) (*PreparedPreferredLedger, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	retry := memoryAdmissionRetry{origin: "successor"}
	for {
		prepared, err := s.prepareSuccessorOnce(ctx, parent, options)
		if err == nil {
			retry.finish(s, nil)
			return prepared, nil
		}
		// A failed attempt releases transition ownership before waiting: other
		// holders may need to publish their work to return these credits.
		if err = retry.wait(ctx, s, err); err != nil {
			retry.finish(s, err)
			return nil, err
		}
	}
}

func (s *Service) prepareSuccessorOnce(ctx context.Context, parent *ledger.Ledger, options successorOptions) (*PreparedPreferredLedger, error) {
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
		options: options,
		parent:  parent,
		ready:   make(chan struct{}),
		state:   preferredTransitionPreparing,
		timings: preferredTransitionTimings{started: time.Now()},
	}
	p.ctx, p.cancel = context.WithCancel(ctx) //nolint:gosec // G118: cancellation belongs to the prepared ledger and runs on cleanup, Release, or Stop.
	if err := s.beginPreferredTransition(p); err != nil {
		p.cancel()
		return fail(err)
	}
	failPrepared := func(err error) (*PreparedPreferredLedger, error) {
		err = s.preferredTransitionError(err)
		if p.cancel != nil {
			p.cancel()
		}
		p.releaseWorkAdmission()
		p.markReady(err)
		return nil, err
	}
	var err error
	if !options.admitted {
		p.releaseWork, err = s.acquireLedgerWork(p.ctx)
		if err != nil {
			return failPrepared(err)
		}
	}
	if !options.admitted && !options.allowReplayFault && !s.isServiceOwnedLedger(parent) {
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
	if !options.allowReplayFault && s.ReplayBlocked() {
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
	if options.check != nil {
		if err := options.check(s); err != nil {
			s.mu.RUnlock()
			return failPrepared(err)
		}
	}
	if options.allowReplayFault && s.replayFaults != nil {
		p.replayFault = s.replayFaults.Snapshot()
	}
	p.expectedClosed = s.closedLedger
	p.expectedOpen = s.openLedger
	p.expectedView = s.openLedgerView
	p.expectedQueue = s.txQueue
	p.expectedLocals = s.localTxs
	p.expectedReplay = s.startupReplay
	p.unfinished = s.speculativeSuccessor
	p.validatedAtPrepare = s.validatedLedger
	p.networkID = s.config.NetworkID
	p.lastRoundSlow = options.preferred || options.allowReplayFault || s.lastConsensusRoundTime > slowConsensusThreshold
	p.relayFn = s.txRelay
	p.provisionalReplace = s.networkLedgerState == networkLedgerFastLoadProvisional &&
		parent.Sequence() == s.closedLedger.Sequence() && parent.Hash() != s.closedLedger.Hash()
	if options.preferred && s.validatedLedger != nil {
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
	p.keepOpen = (options.preferred && parent.Sequence() == s.closedLedger.Sequence() && parent.Hash() == s.closedLedger.Hash()) || (options.onlyMatchingParent && parent.Hash() != s.closedLedger.Hash())
	openView := s.openLedgerView
	queue := s.txQueue
	localsPool := s.localTxs
	s.mu.RUnlock()
	s.openLedgerMu.Unlock()
	gateHeld = false
	p.timings.openLedgerHold = time.Since(gateAcquired)

	if p.keepOpen {
		p.timings.openLedgerHold = time.Since(gateAcquired)
		p.timings.preparation = time.Since(p.timings.started)
		p.markReady(nil)
		return p, nil
	}

	cleanupPrepared := func(err error) (*PreparedPreferredLedger, error) {
		err = s.preferredTransitionError(err)
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
	var localStats, queueStats openledger.TransactionSnapshotStats
	if localsPool != nil {
		localStats.Count, localStats.Bytes = localsPool.SnapshotStats()
	}
	if queue != nil {
		queueStats.Count, queueStats.Bytes = queue.SnapshotStats()
	}
	privateBytes := preferredTransitionEstimate(stats, localStats, queueStats, options.retries)
	if p.unfinished != nil {
		privateBytes = saturatedSuccessorBytes(privateBytes, p.unfinished.inputBytes())
	}
	// Accepted leaves include inner Batch transactions and metadata, even when
	// none of their outer transactions remain in the current open ledger.
	if !options.allowReplayFault {
		err = parent.ForEachTransactionContext(p.ctx, func(_ [32]byte, data []byte) bool {
			size := uint64(len(data))
			if size > (^uint64(0)-preferredTransitionResultBytes)/32 {
				privateBytes = ^uint64(0)
				return false
			}
			size = size*32 + preferredTransitionResultBytes
			if privateBytes > ^uint64(0)-size {
				privateBytes = ^uint64(0)
				return false
			}
			privateBytes += size
			return true
		})
		if err != nil {
			return cleanupPrepared(err)
		}
	}

	p.privateBytes = privateBytes
	if options.deferSpeculation && s.speculativeMemoryBudget() == nil && privateBytes > speculativeSuccessorMemoryLimit {
		return cleanupPrepared(newSpeculativeMemoryError(nil, privateBytes, speculativeSuccessorMemoryLimit, true, nil))
	}
	// A detached ingress or older successor may need publication to release
	// its reservation. Do not wait for its memory while owning that fence.
	p.releaseMemory, err = p.reserveMemory(privateBytes)
	if err != nil {
		return cleanupPrepared(err)
	}

	// Parse the old view only after admission has covered the retained
	// successor. The service gate is free, but the owner keeps all mutating
	// ingress parked until publication or Release.
	prior, err := openView.CurrentTransactions(p.ctx)
	if err != nil {
		return cleanupPrepared(fmt.Errorf("capture open-ledger transactions: %w", err))
	}
	if localsPool != nil {
		p.locals = localsPool.GetTxSet()
	}
	if queue != nil || options.preferred || options.allowReplayFault {
		for _, local := range p.locals {
			prepared, parseErr := openledger.ParsePendingTx(local.Blob)
			if parseErr != nil {
				return cleanupPrepared(fmt.Errorf("parse successor local transaction %x: %w", local.Hash, parseErr))
			}
			if prepared.Hash != local.Hash || prepared.Account != local.Account {
				return cleanupPrepared(fmt.Errorf("successor local transaction identity does not match blob %x", local.Hash))
			}
		}
	}
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
	if p.unfinished != nil {
		p.prior = mergeSuccessorTransactions(p.prior, p.unfinished.prior)
		p.options.retries = mergeSuccessorTransactions(p.options.retries, p.unfinished.retries)
		p.options.anyDisputes = p.options.anyDisputes || p.unfinished.anyDisputes
		if p.options.retrySalt != nil {
			openledger.CanonicalSort(p.options.retries, *p.options.retrySalt)
		}
	}
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
	return p.publish(p.publishPreferredLocked)
}

func (p *PreparedPreferredLedger) publish(publication func() error) (bool, error) {
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
			err = p.service.preferredTransitionError(err)
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
	if !p.options.allowReplayFault && s.ReplayBlocked() {
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
	if p.options.check != nil {
		if err := p.options.check(s); err != nil {
			s.mu.Unlock()
			return p.abortLocked(err)
		}
	}
	if p.options.allowReplayFault && s.replayFaults != nil {
		fault := s.replayFaults.Snapshot()
		if (fault == nil) != (p.replayFault == nil) || (fault != nil && fault.ID != p.replayFault.ID) {
			s.mu.Unlock()
			return p.abortLocked(ErrPreferredTransitionStale)
		}
	}
	if err := publication(); err != nil {
		s.mu.Unlock()
		return p.abortLocked(err)
	}
	if !p.keepOpen {
		s.cancelSpeculativeSuccessorLocked()
		s.openLedgerView = p.openView
		s.openLedger = p.openView.Current()
		s.txQueue = p.queue
	}
	if !p.options.allowReplayFault && !p.options.deferSpeculation && !p.keepOpen {
		s.tickLoadFeeLocked()
	}
	if p.options.deferSpeculation && !p.keepOpen {
		s.scheduleSpeculativeSuccessorLocked(p)
	}
	s.mu.Unlock()
	p.stateMu.Lock()
	p.state = preferredTransitionPublished
	p.stateMu.Unlock()
	s.endPreferredTransitionLocked(p)
	return true, nil
}

func (p *PreparedPreferredLedger) publishPreferredLocked() error {
	s := p.service
	if s.validatedLedger != nil {
		validatedSeq := s.validatedLedger.Sequence()
		if p.parent.Sequence() < validatedSeq ||
			(p.parent.Sequence() == validatedSeq && p.parent.Hash() != s.validatedLedger.Hash() &&
				s.networkLedgerState != networkLedgerFastLoadProvisional) {
			return ErrPreferredChainSwitch
		}
	}
	if p.keepOpen {
		publicationStarted := time.Now()
		s.completeInitialSyncLocked()
		if p.parent.IsValidated() {
			s.confirmFastLoadLocked(p.parent.Sequence(), p.parent.Hash())
		}
		p.timings.publication = time.Since(publicationStarted)
		return nil
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
	s.historyComponent.mu.Unlock()
	p.timings.publication = time.Since(publicationStarted)

	return nil
}

// Finish runs observable side effects after TryPublish has released all
// service/history locks and after the caller has released its engine mutex.
func (p *PreparedPreferredLedger) Finish() {
	if p == nil || p.service == nil {
		return
	}
	p.finishOnce.Do(func() {
		p.stateMu.Lock()
		if p.state != preferredTransitionPublished {
			p.stateMu.Unlock()
			return
		}
		notify := p.notify
		relay := p.relay
		relayFn := p.relayFn
		p.stateMu.Unlock()
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
	err = p.service.preferredTransitionError(err)
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
	started := p.timings.started
	if p.keepOpen {
		p.timings.preparation = time.Since(started)
		p.markReady(nil)
		return
	}
	if p.options.deferSpeculation && len(p.prior) == 0 && len(p.locals) == 0 && len(p.options.retries) == 0 && (p.queue == nil || p.queue.Size() == 0) {
		p.options.deferSpeculation = false
	}
	var err error
	stageStarted := time.Now()
	if !p.options.allowReplayFault && !p.options.replaySpeculation {
		p.staged, err = stageTransactionResultsContext(p.ctx, p.parent, p.parent.Sequence(), p.parent.Hash())
	}
	p.timings.stageResults = time.Since(stageStarted)
	if err == nil && p.options.deferSpeculation {
		p.openView, err = openledger.New(p.parent, openledger.Config{
			NetworkID: p.networkID,
			Logger:    p.service.logger,
			Rules:     openLedgerRules(p.validatedAtPrepare, p.service.logger),
		})
		if err == nil && p.queue != nil {
			baseFee, reserveBase, reserveIncrement := readFeesFromLedger(p.parent)
			p.queue.ProcessClosedLedger(&closedLedgerCtx{ledger: p.parent, baseFee: baseFee, reserveBase: reserveBase, reserveIncrement: reserveIncrement}, p.lastRoundSlow)
		}
		p.timings.preparation = time.Since(started)
		p.markReady(err)
		return
	}
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
		if p.options.replaySpeculation {
			var slowReported sync.Once
			cfg.AllowPostPrecommitCancellation = true
			cfg.SlowTransactionThreshold = openLedgerGateSlowWait
			cfg.SlowTransactionObserver = func(d openledger.SlowTransactionDiagnostic) {
				slowReported.Do(func() {
					p.service.logger.Warn("Slow speculative transaction",
						"seq", cfg.LedgerSequence, "hash", fmt.Sprintf("%x", d.Hash),
						"type", d.Type, "phase", d.Phase, "pass", d.Pass, "duration", d.Duration,
						"offers_considered", d.OffersConsidered)
				})
			}
		}
		var retrySalt [32]byte
		cfg.RetrySalt = p.options.retrySalt
		if p.options.preferred || p.options.allowReplayFault {
			cfg.RetrySalt = &retrySalt
		}
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
			retries := append([]openledger.PendingTx(nil), p.options.retries...)
			locals := p.locals
			if p.options.preferred || p.options.allowReplayFault {
				retries = append(retries, locals...)
				locals = nil
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
				locals,
				p.options.anyDisputes,
				&retries,
				cfg,
				p.queue,
				func() {
					if p.queue != nil && !p.options.replaySpeculation {
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
					adapter := openledger.NewTxqAdapter(view, viewCfg)
					if p.options.replaySpeculation {
						p.queue.AcceptContextWithObserver(p.ctx, adapter, cfg.SlowTransactionThreshold, cfg.SlowTransactionObserver)
					} else {
						p.queue.Accept(adapter)
					}
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
	if p == nil || p.service == nil || !p.options.preferred || p.timings.started.IsZero() {
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

func (s *Service) preferredTransitionError(err error) error {
	if errors.Is(err, context.Canceled) && !s.preferredLifecycleRunning() {
		return errServiceNotRunning
	}
	return err
}

const (
	preferredTransitionMetadataBytes = 4 << 10
	preferredTransitionResultBytes   = 4 << 10
)

// preferredTransitionEstimate charges private inputs and successor staging
// before snapshots copy transaction blobs from the live pools.
func preferredTransitionEstimate(
	stats openledger.TransactionSnapshotStats,
	locals openledger.TransactionSnapshotStats,
	queue openledger.TransactionSnapshotStats,
	retries []openledger.PendingTx,
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
	for _, pool := range []openledger.TransactionSnapshotStats{locals, queue} {
		if pool.Count > ^uint64(0)-items {
			return ^uint64(0)
		}
		items += pool.Count
		if !add(pool.Bytes) {
			return bytes
		}
	}
	for _, retry := range retries {
		if items == ^uint64(0) {
			return ^uint64(0)
		}
		items++
		if !add(uint64(len(retry.Blob))) {
			return bytes
		}
	}
	// The private open ledger and relay staging can retain a second copy of
	// every input blob while the publication fence still owns the first.
	inputBytes := bytes - preferredTransitionBaseBytes
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if bytes == 0 || s.speculativeMemoryBudget() == nil {
		return func() {}, nil
	}
	budget := s.speculativeMemoryBudget()
	if err := reserveSpeculativeBytes(budget, bytes, s.preferredTransitionMemoryCapacity()); err != nil {
		return nil, err
	}
	return sync.OnceFunc(func() { budget.Release(bytes) }), nil
}

func (s *Service) preferredTransitionMemoryCapacity() uint64 {
	budget := s.speculativeMemoryBudget()
	if budget == nil {
		return ^uint64(0)
	}
	capacity := budget.Limit()
	if provider, ok := budget.(interface{ Capacity() uint64 }); ok {
		capacity = min(capacity, provider.Capacity())
	}
	return capacity
}
