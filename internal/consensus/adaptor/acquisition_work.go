package adaptor

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	// The consensus path drives one catch-up and one history acquisition; two
	// extra slots leave room for bounded RPC-driven fetches without retaining an
	// unbounded number of decoded ledger replies.
	acquisitionWorkQueueDepth = 4
	// LedgerData frames can contain thousands of nodes, so bound the per-ledger
	// stash while a cold traversal is running. The overlay lane applies
	// backpressure before this bound is reached.
	acquisitionWorkBatchLimit = 8
	acquisitionMaxUsefulPeers = 6
	// A backed SHAMap walk may need millions of Pebble reads before it finds the
	// next missing node. Bound each pass so replies and timers for other ledgers
	// are serviced between resumable cursor passes.
	acquisitionWorkVisitBudget int64 = 2 * 1024
)

type acquisitionWorkKind uint8

const (
	acquisitionWorkData acquisitionWorkKind = iota
	acquisitionWorkTimerCheck
	acquisitionWorkTimer
	acquisitionWorkLocal
	acquisitionWorkFailure
	acquisitionWorkAdded
	acquisitionWorkRetarget
	acquisitionWorkContinue
)

type ledgerAcquisitionNetwork interface {
	RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32, indirect bool) error
	RequestReplayDelta(peerID uint64, hash [32]byte) error
	RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error
	RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error
	PeerSupportsReplay(peerID uint64) bool
	ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64
	SelectLedgerPeers(target [32]byte, seq uint32, excluded []uint64, max int) []uint64
	PeerLatency(peerID uint64) (time.Duration, bool)
	SendPriorityToPeer(peerID uint64, frame []byte) error
	IncPeerBadData(peerID uint64, reason string)
}

type acquisitionWorkEvent struct {
	kind       acquisitionWorkKind
	data       *message.LedgerData
	owner      *peermanagement.InboundMessage
	peerID     uint64
	resume     bool
	useful     int
	consumed   int
	fulfilled  int
	fetch      func([32]byte) ([]byte, bool)
	peers      []uint64
	added      []uint64
	stateIDs   [][]byte
	txIDs      [][]byte
	queryDepth uint32
	collect    bool
	at         time.Time
}

func (e *acquisitionWorkEvent) release() {
	if e == nil || e.owner == nil {
		return
	}
	_ = e.owner.Close()
	e.owner = nil
}

func releaseAcquisitionWorkEvents(events []acquisitionWorkEvent) {
	for i := range events {
		events[i].release()
	}
}

type acquisitionWorkBatch struct {
	ledger       *inbound.Ledger
	events       []acquisitionWorkEvent
	inFlightData int
	ctx          context.Context
	cancel       context.CancelFunc
	waiting      bool
}

type acquisitionWorkResult struct {
	ledger           *inbound.Ledger
	targets          []uint64
	stateIDs         [][]byte
	txIDs            [][]byte
	requests         []inbound.MissingRequest
	replies          []acquisitionReplyStat
	byHashState      [][32]byte
	byHashTx         [][32]byte
	badData          []acquisitionBadData
	remove           bool
	canceled         bool
	timerFailure     bool
	policyFailure    bool
	failureClass     standardReplayFailureClass
	yielded          bool
	continueRequests bool
	pendingReply     bool
	memoryWait       bool
	requestWait      inbound.MissingRequestOutcome
	timerEscalate    bool
	timerAt          time.Time
	rearmTimer       bool
	localFetch       func([32]byte) ([]byte, bool)
	retryBase        bool
	retarget         bool
	queryDepth       uint32
	complete         bool
	snapshot         inbound.Snapshot
	haveSnapshot     bool
	err              error
	persistenceErr   error
	ack              chan struct{}
}

func (r *acquisitionWorkResult) setPersistenceError(ctx context.Context, err error) {
	r.complete = false
	r.remove = true
	if !errors.Is(err, inbound.ErrPersistence) &&
		(errors.Is(err, inbound.ErrAcquisitionRetired) && errors.Is(err, context.Canceled) ||
			ctx.Err() != nil && errors.Is(err, ctx.Err())) {
		r.err = err
		r.canceled = true
		return
	}
	r.persistenceErr = err
	r.failureClass = standardReplayFailurePersistence
}

type acquisitionBadData struct {
	peerID uint64
	kind   string
}

type acquisitionReplyStat struct {
	peerID   uint64
	infoType message.LedgerInfoType
	received int
	useful   int
}

type acquisitionWorkLane struct {
	process func(context.Context, *inbound.Ledger, []acquisitionWorkEvent) acquisitionWorkResult
	flush   func(context.Context, *inbound.Ledger) error

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	result chan acquisitionWorkResult
	done   chan struct{}

	mu         sync.Mutex
	queueDepth int
	ready      []*acquisitionWorkBatch
	pending    map[*inbound.Ledger]*acquisitionWorkBatch
}

func newAcquisitionWorkLane(queueDepth int) *acquisitionWorkLane {
	return &acquisitionWorkLane{
		process:    processAcquisitionWorkBudgeted,
		wake:       make(chan struct{}, 1),
		result:     make(chan acquisitionWorkResult),
		done:       make(chan struct{}),
		queueDepth: queueDepth,
		pending:    make(map[*inbound.Ledger]*acquisitionWorkBatch),
	}
}

func (l *acquisitionWorkLane) start(parent context.Context) {
	l.ctx, l.cancel = context.WithCancel(parent)
	go l.run()
}

func (l *acquisitionWorkLane) stop() {
	if l == nil || l.cancel == nil {
		return
	}
	l.cancel()
	<-l.done
}

func (l *acquisitionWorkLane) results() <-chan acquisitionWorkResult {
	if l == nil {
		return nil
	}
	return l.result
}

func (l *acquisitionWorkLane) has(ledger *inbound.Ledger) bool {
	if l == nil || ledger == nil {
		return false
	}
	l.mu.Lock()
	_, ok := l.pending[ledger]
	l.mu.Unlock()
	return ok
}

func (l *acquisitionWorkLane) cancelLedger(ledger *inbound.Ledger) bool {
	if l == nil || ledger == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	batch := l.pending[ledger]
	if batch == nil {
		return false
	}
	batch.cancel()
	l.wakeWaitingLocked(batch)
	return true
}

func (l *acquisitionWorkLane) wakeWaitingLocked(batch *acquisitionWorkBatch) {
	if batch.waiting {
		batch.waiting = false
		l.ready = append(l.ready, batch)
		l.notifyLocked()
	}
}

// wakeMemoryWaiters requeues batches whose last reply was retained because
// applying it exhausted the acquisition memory budget. Other ready or waiting
// work is left untouched so a budget release cannot trigger unrelated scans.
func (l *acquisitionWorkLane) wakeMemoryWaiters() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	woken := 0
	for _, batch := range l.pending {
		if !batch.waiting {
			continue
		}
		batch.waiting = false
		l.ready = append(l.ready, batch)
		woken++
	}
	if woken != 0 {
		l.notifyLocked()
	}
	return woken
}

func (l *acquisitionWorkLane) canAcceptNew() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ctx != nil && l.ctx.Err() == nil && len(l.pending) < l.queueDepth+1
}

func (l *acquisitionWorkLane) canAcceptData() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx == nil || l.ctx.Err() != nil || len(l.pending) >= l.queueDepth+1 {
		return false
	}
	for _, batch := range l.pending {
		if batch.inFlightData+acquisitionDataEvents(batch.events) >= acquisitionWorkBatchLimit {
			return false
		}
	}
	return true
}

func (l *acquisitionWorkLane) submit(ledger *inbound.Ledger, event acquisitionWorkEvent) bool {
	if l == nil || ledger == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx == nil || l.ctx.Err() != nil {
		return false
	}
	if batch := l.pending[ledger]; batch != nil {
		if event.kind != acquisitionWorkData && event.kind != acquisitionWorkAdded {
			for i := range batch.events {
				if batch.events[i].kind == event.kind {
					batch.events[i] = event
					l.wakeWaitingLocked(batch)
					return true
				}
			}
		}
		if event.kind == acquisitionWorkData && batch.inFlightData+acquisitionDataEvents(batch.events) >= acquisitionWorkBatchLimit {
			return false
		}
		if batch.waiting && event.kind == acquisitionWorkData {
			batch.events = append(batch.events, acquisitionWorkEvent{})
			copy(batch.events[1:], batch.events[:len(batch.events)-1])
			batch.events[0] = event
		} else {
			batch.events = append(batch.events, event)
		}
		l.wakeWaitingLocked(batch)
		return true
	}
	if len(l.pending) >= l.queueDepth+1 {
		return false
	}
	ctx, cancel := context.WithCancel(l.ctx)
	owned := true
	defer func() {
		if owned {
			cancel()
		}
	}()
	batch := &acquisitionWorkBatch{
		ledger: ledger,
		events: []acquisitionWorkEvent{event},
		ctx:    ctx,
		cancel: cancel,
	}
	l.pending[ledger] = batch
	l.ready = append(l.ready, batch)
	l.notifyLocked()
	owned = false
	return true
}

func (l *acquisitionWorkLane) run() {
	defer close(l.done)
	defer l.releasePending()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-l.wake:
			for {
				batch := l.takeReady()
				if batch == nil {
					break
				}
				if !l.runBatch(batch) {
					return
				}
			}
		}
	}
}

func (l *acquisitionWorkLane) runBatch(batch *acquisitionWorkBatch) bool {
	l.mu.Lock()
	events := append([]acquisitionWorkEvent(nil), batch.events...)
	batch.inFlightData = acquisitionDataEvents(events)
	clear(batch.events)
	batch.events = batch.events[:0]
	l.mu.Unlock()
	retained := false
	defer func() {
		if !retained {
			releaseAcquisitionWorkEvents(events)
		}
	}()

	previous := batch.ledger.Snapshot()
	result := l.process(batch.ctx, batch.ledger, events)
	if errors.Is(result.err, shamap.ErrAcquisitionMemory) && batch.ctx.Err() == nil {
		result.err = nil
		result.memoryWait = true
	}
	if (result.continueRequests || errors.Is(result.err, shamap.ErrTraversalBudget)) && batch.ctx.Err() == nil {
		result.err = nil
		result.yielded = true
		result.rearmTimer = batch.ledger.RecordTraversalProgress(previous) || len(result.requests) > 0
		if !result.rearmTimer {
			switch batch.ledger.OnTimer(time.Now()) {
			case inbound.TimerFailed:
				result.snapshot = batch.ledger.Snapshot()
				result.haveSnapshot = true
				result.remove = true
				result.timerFailure = true
				result.failureClass = standardReplayFailureAvailability
			case inbound.TimerEscalate:
				result.timerEscalate = true
				result.timerAt = time.Now()
			}
		}
	}
	beforeCheckpoint := batch.ledger.Snapshot().AcquisitionMemory.Used
	if result.err == nil && !result.complete && !result.remove {
		useful := 0
		for _, reply := range result.replies {
			useful += reply.useful
		}
		current := batch.ledger.Snapshot()
		useful += int(current.StateCacheUseful - previous.StateCacheUseful + current.TxCacheUseful - previous.TxCacheUseful)
		if err := batch.ledger.CheckpointPersistence(batch.ctx, useful); err != nil {
			result.setPersistenceError(batch.ctx, err)
		}
	}
	pressure := batch.ledger.Snapshot()
	if (result.memoryWait || pressure.StateWalk.RetentionPressure || pressure.TxWalk.RetentionPressure) && !result.remove && !result.complete {
		if err := batch.ledger.CheckpointMemory(batch.ctx); err != nil {
			result.setPersistenceError(batch.ctx, err)
		} else if result.requestWait == inbound.MissingRequestWaitPersistence || result.memoryWait && batch.ledger.Snapshot().AcquisitionMemory.Used < beforeCheckpoint {
			result.yielded = true
			if (result.requestWait == inbound.MissingRequestWaitMemory || result.requestWait == inbound.MissingRequestWaitPersistence) && !result.pendingReply {
				result.continueRequests = true
			}
		}
	}
	var deferred []acquisitionWorkEvent
	partialRequests := (result.yielded || result.memoryWait) && (len(result.requests) > 0 || len(result.stateIDs) > 0 || len(result.txIDs) > 0 ||
		len(result.byHashState) > 0 || len(result.byHashTx) > 0)
	timerDone := !result.yielded && slices.ContainsFunc(events, func(event acquisitionWorkEvent) bool {
		return event.kind == acquisitionWorkTimer
	})
	if (partialRequests || timerDone || result.continueRequests) && !result.pendingReply {
		// Replies resume the walk. Keep independent peer work without resending
		// the timer or releasing reservations made for an already applied reply.
		for _, event := range events {
			switch event.kind {
			case acquisitionWorkAdded:
				deferred = append(deferred, event)
			case acquisitionWorkRetarget:
				if event.resume {
					event.stateIDs, event.txIDs = nil, nil
				}
				if event.collect || len(event.stateIDs) > 0 || len(event.txIDs) > 0 {
					deferred = append(deferred, event)
				}
			}
		}
	}
	if partialRequests && !result.continueRequests {
		result.yielded = false
		result.rearmTimer = true
	}
	batch.ledger.SetMemoryWaiting(result.memoryWait && !result.yielded && !result.remove)
	for i := range events {
		if !result.yielded && !result.timerEscalate && events[i].kind == acquisitionWorkTimer {
			result.rearmTimer = true
			break
		}
	}
	if result.complete && l.flush != nil {
		if err := l.flush(batch.ctx, batch.ledger); err != nil {
			result.setPersistenceError(batch.ctx, err)
		}
	}
	result.ack = make(chan struct{})
	select {
	case <-l.ctx.Done():
		return false
	case l.result <- result:
	}
	select {
	case <-l.ctx.Done():
		return false
	case <-result.ack:
	}

	var discarded []acquisitionWorkEvent
	l.mu.Lock()
	batch.inFlightData = 0
	batch.events = append(batch.events, deferred...)
	if result.complete || result.remove || batch.ctx.Err() != nil {
		delete(l.pending, batch.ledger)
		batch.cancel()
		discarded = append(discarded, batch.events...)
		batch.events = nil
	} else if result.pendingReply && !result.yielded {
		wake := len(batch.events) != 0
		batch.events = append(batch.events, events...)
		retained = true
		batch.waiting = true
		if wake {
			l.wakeWaitingLocked(batch)
		}
	} else if result.continueRequests {
		if !slices.ContainsFunc(batch.events, func(event acquisitionWorkEvent) bool {
			return event.kind == acquisitionWorkContinue
		}) {
			batch.events = append(batch.events, acquisitionWorkEvent{kind: acquisitionWorkContinue})
		}
		l.ready = append(l.ready, batch)
		l.notifyLocked()
	} else if result.yielded {
		batch.events = append(batch.events, events...)
		retained = true
		l.ready = append(l.ready, batch)
		l.notifyLocked()
	} else if len(batch.events) == 0 {
		delete(l.pending, batch.ledger)
		batch.cancel()
	} else {
		l.ready = append(l.ready, batch)
		l.notifyLocked()
	}
	l.mu.Unlock()
	releaseAcquisitionWorkEvents(discarded)
	return true
}

func (l *acquisitionWorkLane) releasePending() {
	l.mu.Lock()
	batches := make([]*acquisitionWorkBatch, 0, len(l.pending))
	for _, batch := range l.pending {
		batches = append(batches, batch)
	}
	l.pending = make(map[*inbound.Ledger]*acquisitionWorkBatch)
	l.ready = nil
	l.mu.Unlock()

	for _, batch := range batches {
		batch.cancel()
		releaseAcquisitionWorkEvents(batch.events)
		batch.events = nil
	}
}

func (l *acquisitionWorkLane) takeReady() *acquisitionWorkBatch {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ready) == 0 {
		return nil
	}
	batch := l.ready[0]
	l.ready[0] = nil
	l.ready = l.ready[1:]
	return batch
}

func (l *acquisitionWorkLane) notifyLocked() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func acquisitionDataEvents(events []acquisitionWorkEvent) int {
	n := 0
	for i := range events {
		if events[i].kind == acquisitionWorkData {
			n++
		}
	}
	return n
}

func processAcquisitionWork(ctx context.Context, ledger *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
	return processAcquisitionWorkWithBudget(ctx, ledger, events, 0)
}

func processAcquisitionWorkBudgeted(ctx context.Context, ledger *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
	return processAcquisitionWorkWithBudget(ctx, ledger, events, acquisitionWorkVisitBudget)
}

func processAcquisitionWorkWithBudget(ctx context.Context, ledger *inbound.Ledger, events []acquisitionWorkEvent, visitBudget int64) acquisitionWorkResult {
	result := acquisitionWorkResult{ledger: ledger}
	usefulByPeer := make(map[uint64]int)
	var addedPeers []uint64
	var retargetPeers []uint64
	var runLocal, runTimerCheck, runTimer, runFailure, runRequests bool
	var timerAt time.Time
	var fetch func([32]byte) ([]byte, bool)
	for _, event := range events {
		if event.kind == acquisitionWorkTimer {
			runTimer = true
		}
		if event.kind == acquisitionWorkFailure {
			runFailure = true
			break
		}
	}
	if runFailure {
		result.snapshot = ledger.Snapshot()
		result.haveSnapshot = true
		result.remove = true
		result.timerFailure = true
		result.failureClass = standardReplayFailureAvailability
		return result
	}

	for i := range events {
		event := &events[i]
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		switch event.kind {
		case acquisitionWorkData:
			if event.resume {
				runRequests = runRequests || event.fulfilled > 0
				if event.useful == 0 {
					continue
				}
				if event.data != nil && event.data.InfoType == message.LedgerInfoBase {
					if ledger.State() == inbound.StateWantBase {
						result.retryBase = true
					} else {
						for _, peerID := range ledger.Peers() {
							if !slices.Contains(addedPeers, peerID) {
								addedPeers = append(addedPeers, peerID)
							}
						}
					}
				} else if event.peerID != 0 {
					usefulByPeer[event.peerID] = event.useful
				}
				continue
			}
			useful, received, badKind, remove, complete, err := applyAcquisitionReply(ctx, ledger, event)
			event.resume = !errors.Is(err, shamap.ErrAcquisitionMemory)
			event.useful += useful
			if event.data != nil {
				result.replies = append(result.replies, acquisitionReplyStat{
					peerID: event.peerID, infoType: event.data.InfoType,
					received: received, useful: useful,
				})
			}
			if err != nil {
				if errors.Is(err, shamap.ErrAcquisitionMemory) {
					result.err = err
					result.pendingReply = true
					return result
				}
				if errors.Is(err, inbound.ErrPersistence) {
					result.setPersistenceError(ctx, err)
					return result
				}
				if errors.Is(err, context.Canceled) || ctx.Err() != nil && errors.Is(err, ctx.Err()) {
					result.err = err
					result.canceled = true
					result.remove = true
					return result
				}
				if badKind != "" {
					result.badData = append(result.badData, acquisitionBadData{peerID: event.peerID, kind: badKind})
				}
				if remove {
					result.err = err
					result.remove = true
					result.policyFailure = errors.Is(err, inbound.ErrHeaderRejected)
					if result.policyFailure {
						result.failureClass = standardReplayFailureInvalidData
					}
					result.snapshot = ledger.Snapshot()
					result.haveSnapshot = true
					return result
				}
				continue
			}
			if complete {
				result.complete = true
				return result
			}
			if useful > 0 && event.data != nil && event.data.InfoType == message.LedgerInfoBase &&
				ledger.State() == inbound.StateWantBase {
				result.retryBase = true
			}
			if useful > 0 && event.data != nil && event.data.InfoType == message.LedgerInfoBase &&
				ledger.State() != inbound.StateWantBase {
				for _, peerID := range ledger.Peers() {
					if !slices.Contains(addedPeers, peerID) {
						addedPeers = append(addedPeers, peerID)
					}
				}
				continue
			}
			runRequests = runRequests || event.fulfilled > 0
			if event.useful > usefulByPeer[event.peerID] {
				usefulByPeer[event.peerID] = event.useful
			}
		case acquisitionWorkTimerCheck:
			runTimerCheck = true
			if event.at.After(timerAt) {
				timerAt = event.at
			}
		case acquisitionWorkTimer:
			runTimer = true
			fetch = event.fetch
			result.targets = append(result.targets[:0], event.peers...)
			for _, peerID := range event.added {
				if !slices.Contains(addedPeers, peerID) {
					addedPeers = append(addedPeers, peerID)
				}
			}
		case acquisitionWorkContinue:
			runRequests = true
		case acquisitionWorkLocal:
			runLocal = true
			fetch = event.fetch
		case acquisitionWorkFailure:
		case acquisitionWorkAdded:
			addedPeers = append(addedPeers, event.peerID)
		case acquisitionWorkRetarget:
			if runTimer {
				continue
			}
			result.retarget = true
			if len(result.stateIDs) == 0 && len(result.txIDs) == 0 &&
				len(event.peers) > 0 && (len(event.stateIDs) > 0 || len(event.txIDs) > 0) {
				result.targets = append(result.targets[:0], event.peers[0])
				result.stateIDs = event.stateIDs
				result.txIDs = event.txIDs
				result.queryDepth = event.queryDepth
				event.resume = true
			}
			if event.collect {
				retargetPeers = append(retargetPeers, event.peers...)
			}
		}
	}

	if runTimerCheck {
		switch ledger.OnTimer(timerAt) {
		case inbound.TimerFailed:
			result.snapshot = ledger.Snapshot()
			result.haveSnapshot = true
			result.remove = true
			result.timerFailure = true
			result.failureClass = standardReplayFailureAvailability
			return result
		case inbound.TimerEscalate:
			result.timerEscalate = true
			result.timerAt = timerAt
			return result
		case inbound.TimerRefresh:
			addedPeers = append(addedPeers, acquisitionRequestCandidates(nil, ledger.Peers())...)
		}
	}

	workCtx := shamap.WithTraversalBudget(ctx, visitBudget)

	// Fetch-pack arrivals explicitly enqueue acquisitionWorkLocal. A timeout
	// must schedule network retries first rather than repeating the full local
	// SHAMap/fetch-pack scan before it can send a request.
	if runLocal {
		_, complete, err := ledger.CheckLocalContext(workCtx, fetch)
		if err != nil {
			result.err = err
			return result
		}
		if complete {
			result.complete = true
			return result
		}
	}

	// Once a pivot needs content-addressed recovery, use the same bounded,
	// disjoint peer windows as normal acquisition. Cache placement fulfills the
	// leases; a reply (or resumable discovery pass) refills them immediately.
	// Timers only retry expired work, rather than pacing every four hashes.
	if ledger.ByHashMode() && (runTimer || runLocal || runRequests || len(addedPeers) > 0 || len(usefulByPeer) > 0) {
		peers := acquisitionRequestCandidates(selectUsefulAcquisitionPeers(usefulByPeer), ledger.Peers())
		plan, err := ledger.PlanMissingReplyRequestsContext(workCtx, peers)
		result.applyMissingPlan(plan, err)
		return result
	}

	if runTimer {
		result.stateIDs, result.txIDs, result.complete, result.err = ledger.CollectMissingRequestContext(workCtx, false)
		if result.err != nil || result.complete {
			return result
		}
		result.byHashState, result.byHashTx, result.err = ledger.TakeByHashRequestContext(workCtx, inboundByHashBatch)
		if result.err != nil {
			return result
		}
		if len(addedPeers) > 0 {
			result.requests = ledger.CollectMissingCachedAddedRequests(addedPeers)
		}
		result.localFetch = fetch
		return result
	}
	preferred := selectUsefulAcquisitionPeers(usefulByPeer)
	if runRequests || len(preferred) > 0 {
		peers := acquisitionRequestCandidates(preferred, ledger.Peers())
		plan, err := ledger.PlanMissingReplyRequestsContext(workCtx, peers)
		if result.applyMissingPlan(plan, err) {
			return result
		}
	}
	if len(retargetPeers) > 0 {
		plan, err := ledger.PlanMissingReplyRequestsContext(workCtx, retargetPeers)
		if result.applyMissingPlan(plan, err) {
			return result
		}
	}
	if len(addedPeers) > 0 {
		plan, err := ledger.PlanMissingAddedRequestsContext(workCtx, addedPeers)
		result.applyMissingPlan(plan, err)
	}
	return result
}

func (r *acquisitionWorkResult) applyMissingPlan(plan inbound.MissingRequestPlan, err error) bool {
	r.requests = append(r.requests, plan.Requests...)
	if err != nil {
		for _, request := range r.requests {
			r.ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
		}
		r.requests = nil
		r.err = err
		if errors.Is(err, inbound.ErrPersistence) {
			r.persistenceErr = err
			r.failureClass = standardReplayFailurePersistence
			r.remove = true
		}
		return true
	}
	switch plan.Outcome {
	case inbound.MissingRequestContinue:
		r.continueRequests = true
		return true
	case inbound.MissingRequestWaitMemory, inbound.MissingRequestWaitPersistence:
		r.memoryWait = true
		r.requestWait = plan.Outcome
		return true
	case inbound.MissingRequestComplete:
		r.complete = true
		return true
	default:
		return false
	}
}

func acquisitionRequestCandidates(preferred, available []uint64) []uint64 {
	peers := make([]uint64, 0, len(preferred)+len(available))
	for _, peerID := range preferred {
		if peerID != 0 && !slices.Contains(peers, peerID) {
			peers = append(peers, peerID)
		}
	}
	remaining := make([]uint64, 0, len(available))
	for _, peerID := range available {
		if peerID != 0 && !slices.Contains(peers, peerID) && !slices.Contains(remaining, peerID) {
			remaining = append(remaining, peerID)
		}
	}
	// #nosec G404 -- Peer ordering distributes requests; it does not establish trust.
	rand.Shuffle(len(remaining), func(i, j int) {
		remaining[i], remaining[j] = remaining[j], remaining[i]
	})
	return append(peers, remaining...)
}

func selectUsefulAcquisitionPeers(counts map[uint64]int) []uint64 {
	best := 0
	for _, count := range counts {
		if count > best {
			best = count
		}
	}
	if best == 0 {
		return nil
	}
	threshold := best / 2
	peers := make([]uint64, 0, len(counts))
	for peerID, count := range counts {
		if peerID != 0 && count > 0 && count >= threshold {
			peers = append(peers, peerID)
		}
	}
	if len(peers) > acquisitionMaxUsefulPeers {
		// #nosec G404 -- Peer ordering distributes requests; it does not establish trust.
		rand.Shuffle(len(peers), func(i, j int) {
			peers[i], peers[j] = peers[j], peers[i]
		})
		peers = peers[:acquisitionMaxUsefulPeers]
	}
	return peers
}

func (c *catchupReplayCoordinator) submitAcquisitionWork(ledger *inbound.Ledger, event acquisitionWorkEvent) bool {
	if c.stoppedForShutdown() {
		return false
	}
	if lane := c.currentAcquisitionWork(); lane != nil {
		if lane.submit(ledger, event) {
			return true
		}
		return false
	}
	defer event.release()
	ctx := c.lifecycleContext()
	events := []acquisitionWorkEvent{event}
	for {
		result := processAcquisitionWork(ctx, ledger, events)
		if errors.Is(result.err, shamap.ErrAcquisitionMemory) && ctx.Err() == nil {
			result.err = nil
			result.memoryWait = true
		}
		resumeWork := false
		if result.memoryWait && !result.remove {
			before := ledger.Snapshot().AcquisitionMemory.Used
			if err := ledger.CheckpointMemory(ctx); err != nil {
				result.setPersistenceError(ctx, err)
			} else {
				resumable := result.requestWait == inbound.MissingRequestWaitPersistence || ledger.Snapshot().AcquisitionMemory.Used < before
				resumeWork = resumable
				if resumable && (result.requestWait == inbound.MissingRequestWaitMemory || result.requestWait == inbound.MissingRequestWaitPersistence) && !result.pendingReply {
					result.continueRequests = true
				}
			}
		}
		if errors.Is(result.err, shamap.ErrTraversalBudget) && ctx.Err() == nil {
			result.err = nil
			result.yielded = true
		}
		if result.complete {
			if err := ledger.FlushPersistence(ctx); err != nil {
				result.setPersistenceError(ctx, err)
			}
		}
		ledger.SetMemoryWaiting(result.memoryWait && !result.remove)
		c.handleAcquisitionWorkResult(result)
		if result.err != nil || result.remove || result.complete || ctx.Err() != nil {
			return true
		}
		if resumeWork && !result.continueRequests {
			continue
		}
		if !result.continueRequests {
			return true
		}
		events = []acquisitionWorkEvent{{kind: acquisitionWorkContinue}}
	}
}

func (c *catchupReplayCoordinator) currentAcquisitionWork() *acquisitionWorkLane {
	c.acquisitionWorkMu.RLock()
	lane := c.acquisitionWork
	c.acquisitionWorkMu.RUnlock()
	return lane
}

func (c *catchupReplayCoordinator) handleAcquisitionWorkResult(result acquisitionWorkResult) {
	if c.stoppedForShutdown() {
		if result.ack != nil {
			close(result.ack)
		}
		return
	}
	if result.ack != nil {
		defer close(result.ack)
	}
	if result.canceled || errors.Is(result.err, context.Canceled) && !errors.Is(result.err, inbound.ErrPersistence) {
		return
	}
	ledger := result.ledger
	if ledger != nil && ledger.Reason() == inbound.ReasonHistory && !c.historySequenceAllowed(ledger.Seq()) {
		c.discardHistoryAcquisition(ledger, "outside_history_window")
		return
	}
	if ledger == nil || c.fetchTracker.Find(ledger.Hash()) != ledger {
		if ledger != nil {
			if lane := c.currentAcquisitionWork(); lane != nil {
				lane.cancelLedger(ledger)
			}
			for _, request := range result.requests {
				ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
			}
			c.retireAcquisitionStore(c.lifecycleContext(), ledger)
		}
		return
	}
	if result.rearmTimer && !result.complete && !result.remove {
		defer ledger.RearmTimer(time.Now())
	}
	c.reportAcquisitionProgress(ledger, result.yielded)
	for _, bad := range result.badData {
		if bad.kind != "" {
			c.acquisition.IncPeerBadData(bad.peerID, bad.kind)
		}
	}
	if result.err != nil && !result.remove {
		c.logger.Warn("inbound ledger: acquisition worker failed", "error", result.err)
		return
	}
	if result.persistenceErr != nil {
		c.logger.Warn("inbound ledger: verified-node persistence failed", "error", result.persistenceErr, "seq", ledger.Seq())
		c.discardFailedInboundAcquisition(ledger, result.persistenceErr, standardReplayFailurePersistence)
		return
	}
	if result.remove {
		if result.err != nil {
			c.logger.Warn("inbound ledger: acquisition data rejected", "error", result.err)
		}
		cause := result.err
		failureClass := result.failureClass
		if result.timerFailure {
			cause = inboundAcquisitionTimerFailure(ledger)
			failureClass = standardReplayFailureAvailability
		} else if cause == nil && result.policyFailure {
			cause = errors.New("inbound ledger acquisition rejected by local policy")
			failureClass = standardReplayFailureInvalidData
		} else if failureClass == standardReplayFailureNone {
			failureClass = standardReplayFailureInvalidData
		}
		if result.timerFailure || result.policyFailure {
			c.failInboundAcquisitionWithSnapshot(ledger, result.snapshot, cause, failureClass)
		} else {
			snapshot := result.snapshot
			if !result.haveSnapshot {
				snapshot = ledger.Snapshot()
			}
			c.discardFailedInboundAcquisitionWithSnapshot(ledger, snapshot, cause, failureClass)
		}
		return
	}
	c.promoteResolvedFrozenPivot(ledger, ledger.PeerID())
	if result.complete {
		c.completeInboundLedgerReady(ledger)
		return
	}
	if result.timerEscalate {
		if !c.escalateAcquisition(ledger, result.timerAt) {
			ledger.RearmTimer(time.Now())
		}
		return
	}
	if result.retryBase {
		c.requestAcquisitionBase(ledger)
	}
	for _, reply := range result.replies {
		c.logger.Debug("inbound ledger reply processed",
			"seq", ledger.Seq(),
			"peer", reply.peerID,
			"info_type", reply.infoType,
			"received", reply.received,
			"useful", reply.useful,
		)
	}
	retry := missingNodeRetry{queryDepth: result.queryDepth}
	if len(result.stateIDs) > 0 || len(result.txIDs) > 0 {
		retry = c.sendMissingAcquisitionNodes(
			ledger,
			result.targets,
			result.stateIDs,
			result.txIDs,
			result.queryDepth,
		)
	}
	released := 0
	for _, request := range result.requests {
		if c.sendMissingReplyRequest(ledger, request) {
			released++
		}
	}
	if len(result.requests) > 0 {
		var stateNodes, txNodes int
		requestPeers := make([]uint64, 0, len(result.requests))
		for _, request := range result.requests {
			requestPeers = append(requestPeers, request.PeerID)
			if request.Transaction {
				txNodes += len(request.NodeIDs)
			} else {
				stateNodes += len(request.NodeIDs)
			}
		}
		c.logger.Debug("inbound ledger requests scheduled",
			"seq", ledger.Seq(),
			"peers", requestPeers,
			"state_nodes", stateNodes,
			"tx_nodes", txNodes,
		)
	}
	if result.retarget && (len(retry.stateIDs) > 0 || len(retry.txIDs) > 0) {
		ledger.ReleaseUnreservedMissingNodes()
	} else if !result.retarget {
		c.retryMissingAcquisitionNodes(ledger, retry, released)
	}
	if len(result.byHashState) > 0 || len(result.byHashTx) > 0 {
		peers := ledger.Peers()
		c.sendNodesByHash(peers, ledger.Hash(), ledger.Seq(), result.byHashState, message.ObjectTypeStateNode)
		c.sendNodesByHash(peers, ledger.Hash(), ledger.Seq(), result.byHashTx, message.ObjectTypeTransactionNode)
	}
	if result.localFetch != nil && !c.submitAcquisitionWork(ledger, acquisitionWorkEvent{
		kind: acquisitionWorkLocal, fetch: result.localFetch,
	}) {
		c.logger.Warn("inbound ledger: post-timeout local refresh deferred; acquisition worker unavailable", "seq", ledger.Seq())
	}
}

func (c *catchupReplayCoordinator) sendMissingReplyRequest(ledger *inbound.Ledger, request inbound.MissingRequest) bool {
	indirect := ledger.Timeouts() > 0
	queryDepth := uint32(1)
	if request.Blind {
		queryDepth = 0
	} else if latency, ok := c.acquisition.PeerLatency(request.PeerID); ok && latency >= 300*time.Millisecond {
		queryDepth = 2
	}
	if !c.acquisitionPeerConnected(request.PeerID) {
		ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
		c.removeStaleAcquisitionPeer(ledger, request.PeerID)
		return true
	}
	var err error
	if request.ByHash {
		objType := message.ObjectTypeStateNode
		if request.Transaction {
			objType = message.ObjectTypeTransactionNode
		}
		var frame []byte
		frame, err = encodeNodesByHash(ledger.Hash(), ledger.Seq(), request.NodeHashes, objType)
		if err == nil {
			err = c.acquisition.SendPriorityToPeer(request.PeerID, frame)
		}
	} else if request.Transaction {
		err = c.acquisition.RequestTransactionNodes(request.PeerID, ledger.Hash(), request.NodeIDs, queryDepth, indirect)
	} else {
		err = c.acquisition.RequestStateNodes(request.PeerID, ledger.Hash(), request.NodeIDs, queryDepth, indirect)
	}
	if err == nil {
		return false
	}
	ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
	return c.handleMissingNodeSendFailure(ledger, request.PeerID, request.Transaction, err)
}

type missingNodeRetry struct {
	stateIDs   [][]byte
	txIDs      [][]byte
	queryDepth uint32
}

func (c *catchupReplayCoordinator) sendMissingAcquisitionNodes(
	ledger *inbound.Ledger,
	peers []uint64,
	stateIDs, txIDs [][]byte,
	queryDepth uint32,
) missingNodeRetry {
	indirect := ledger.Timeouts() > 0
	var stateSent, stateDisconnected bool
	var txSent, txDisconnected bool
	for _, peerID := range peers {
		disconnected := false
		if !c.acquisitionPeerConnected(peerID) {
			c.removeStaleAcquisitionPeer(ledger, peerID)
			stateDisconnected = stateDisconnected || len(stateIDs) > 0
			txDisconnected = txDisconnected || len(txIDs) > 0
			continue
		}
		if len(stateIDs) > 0 {
			if err := c.acquisition.RequestStateNodes(peerID, ledger.Hash(), stateIDs, queryDepth, indirect); err != nil {
				disconnected = c.handleMissingNodeSendFailure(ledger, peerID, false, err)
				stateDisconnected = stateDisconnected || disconnected
			} else {
				stateSent = true
			}
		}
		if disconnected {
			txDisconnected = txDisconnected || len(txIDs) > 0
			continue
		}
		if len(txIDs) > 0 {
			if err := c.acquisition.RequestTransactionNodes(peerID, ledger.Hash(), txIDs, queryDepth, indirect); err != nil {
				disconnected = c.handleMissingNodeSendFailure(ledger, peerID, true, err)
				txDisconnected = txDisconnected || disconnected
			} else {
				txSent = true
			}
		}
	}
	retry := missingNodeRetry{queryDepth: queryDepth}
	if stateDisconnected && !stateSent {
		retry.stateIDs = stateIDs
	}
	if txDisconnected && !txSent {
		retry.txIDs = txIDs
	}
	return retry
}

func (c *catchupReplayCoordinator) acquisitionPeerConnected(peerID uint64) bool {
	return c.peerSessions == nil || c.peerSessions.IsPeerConnected(peermanagement.PeerID(peerID))
}

func (c *catchupReplayCoordinator) removeStaleAcquisitionPeer(ledger *inbound.Ledger, peerID uint64) {
	ledger.RemovePeer(peerID)
	c.onPeerDisconnect(peermanagement.PeerID(peerID))
}

func applyAcquisitionReply(ctx context.Context, ledger *inbound.Ledger, event *acquisitionWorkEvent) (useful, received int, badKind string, remove, complete bool, err error) {
	data := event.data
	if data == nil {
		return 0, 0, "", false, false, nil
	}
	if data.InfoType != message.LedgerInfoAsNode && data.InfoType != message.LedgerInfoTxNode {
		useful, badKind, remove, complete, err = applyAcquisitionData(ctx, ledger, data)
		return useful, len(data.Nodes), badKind, remove, complete, err
	}
	if err := inbound.ValidateReplyNodeCount(data.Nodes); err != nil {
		return 0, len(data.Nodes), "", false, false, err
	}
	reply, err := ledger.ApplyMissingReplyContext(ctx, event.peerID, data.InfoType == message.LedgerInfoTxNode, data.Nodes[event.consumed:])
	event.consumed += reply.Consumed
	event.fulfilled += reply.Fulfilled
	if errors.Is(err, inbound.ErrInvalidPeerNode) {
		badKind = "ledger-data-state"
		if data.InfoType == message.LedgerInfoTxNode {
			badKind = "ledger-data-tx"
		}
	}
	remove = errors.Is(err, shamap.ErrNodeSerialization) || errors.Is(err, inbound.ErrPersistence)
	return reply.Useful, reply.Consumed, badKind, remove, reply.Complete, err
}

func applyAcquisitionData(ctx context.Context, ledger *inbound.Ledger, data *message.LedgerData) (useful int, badKind string, remove, complete bool, err error) {
	if data == nil {
		return 0, "", false, false, nil
	}
	switch data.InfoType {
	case message.LedgerInfoBase:
		useful, err = ledger.GotBaseUsefulContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		policyFailure := errors.Is(err, inbound.ErrHeaderRejected)
		badKind := "ledger-data-base"
		if localFailure || policyFailure || errors.Is(err, shamap.ErrAcquisitionMemory) {
			badKind = ""
		}
		return useful, badKind, localFailure || policyFailure, ledger.IsComplete(), err
	case message.LedgerInfoAsNode:
		var added int
		added, err = ledger.GotStateNodesUsefulContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		badKind := ""
		if errors.Is(err, inbound.ErrInvalidPeerNode) {
			badKind = "ledger-data-state"
		}
		return added, badKind, localFailure, ledger.IsComplete(), err
	case message.LedgerInfoTxNode:
		var added int
		added, err = ledger.GotTransactionNodesUsefulContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		badKind := ""
		if errors.Is(err, inbound.ErrInvalidPeerNode) {
			badKind = "ledger-data-tx"
		}
		return added, badKind, localFailure, ledger.IsComplete(), err
	default:
		return 0, "", false, false, nil
	}
}
