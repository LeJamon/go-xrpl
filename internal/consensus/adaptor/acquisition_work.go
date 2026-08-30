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
	acquisitionWorkWorkers           = 4
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
	kind         acquisitionWorkKind
	data         *message.LedgerData
	owner        *peermanagement.InboundMessage
	peerID       uint64
	resume       bool
	useful       int
	fetch        func([32]byte) ([]byte, bool)
	peers        []uint64
	added        []uint64
	stateIDs     [][]byte
	txIDs        [][]byte
	queryDepth   uint32
	collect      bool
	at           time.Time
	receivedAt   time.Time
	enqueuedAt   time.Time
	wireBytes    int
	payloadBytes int
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
	ledger *inbound.Ledger
	events []acquisitionWorkEvent
	ctx    context.Context
	cancel context.CancelFunc
}

type acquisitionWorkResult struct {
	ledger         *inbound.Ledger
	targets        []uint64
	stateIDs       [][]byte
	txIDs          [][]byte
	requests       []inbound.MissingRequest
	replies        []acquisitionReplyStat
	byHashState    [][32]byte
	byHashTx       [][32]byte
	badData        []acquisitionBadData
	remove         bool
	timerFailure   bool
	policyFailure  bool
	yielded        bool
	timerEscalate  bool
	timerAt        time.Time
	rearmTimer     bool
	localFetch     func([32]byte) ([]byte, bool)
	retryBase      bool
	retarget       bool
	queryDepth     uint32
	complete       bool
	snapshot       inbound.Snapshot
	haveSnapshot   bool
	err            error
	persistenceErr error
	ack            chan struct{}
}

type acquisitionBadData struct {
	peerID uint64
	kind   string
}

type acquisitionReplyStat struct {
	peerID          uint64
	infoType        message.LedgerInfoType
	requestID       uint64
	requested       int
	queryDepth      uint32
	received        int
	useful          int
	receivedBytes   int
	usefulBytes     int
	duplicates      int
	rerequests      int
	invalid         int
	unprocessed     int
	wireBytes       int
	responseLatency time.Duration
	queueDelay      time.Duration
	applyDuration   time.Duration
}

type acquisitionWorkLane struct {
	process func(context.Context, *inbound.Ledger, []acquisitionWorkEvent) acquisitionWorkResult
	flush   func(context.Context, *inbound.Ledger) error

	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}
	result  chan acquisitionWorkResult
	done    chan struct{}
	workers sync.WaitGroup

	mu          sync.Mutex
	queueDepth  int
	workerCount int
	ready       []*acquisitionWorkBatch
	pending     map[*inbound.Ledger]*acquisitionWorkBatch
}

func newAcquisitionWorkLane(queueDepth int) *acquisitionWorkLane {
	return newAcquisitionWorkLaneWithWorkers(queueDepth, acquisitionWorkWorkers)
}

func newAcquisitionWorkLaneWithWorkers(queueDepth, workerCount int) *acquisitionWorkLane {
	if workerCount < 1 {
		workerCount = 1
	}
	return &acquisitionWorkLane{
		process:     processAcquisitionWorkBudgeted,
		wake:        make(chan struct{}, workerCount),
		result:      make(chan acquisitionWorkResult),
		done:        make(chan struct{}),
		queueDepth:  queueDepth,
		workerCount: workerCount,
		pending:     make(map[*inbound.Ledger]*acquisitionWorkBatch),
	}
}

func (l *acquisitionWorkLane) start(parent context.Context) {
	l.ctx, l.cancel = context.WithCancel(parent)
	l.workers.Add(l.workerCount)
	for range l.workerCount {
		go l.run()
	}
	go func() {
		l.workers.Wait()
		l.releasePending()
		close(l.done)
	}()
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
	return true
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
	if l.ctx == nil || l.ctx.Err() != nil {
		return false
	}
	for _, batch := range l.pending {
		if acquisitionDataEvents(batch.events) >= acquisitionWorkBatchLimit {
			return false
		}
	}
	return true
}

func (l *acquisitionWorkLane) submit(ledger *inbound.Ledger, event acquisitionWorkEvent) bool {
	if l == nil || ledger == nil {
		return false
	}
	if event.enqueuedAt.IsZero() {
		event.enqueuedAt = time.Now()
	}
	if event.receivedAt.IsZero() {
		event.receivedAt = event.enqueuedAt
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
					return true
				}
			}
		}
		if event.kind == acquisitionWorkData && acquisitionDataEvents(batch.events) >= acquisitionWorkBatchLimit {
			return false
		}
		batch.events = append(batch.events, event)
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
	defer l.workers.Done()
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
	batch.events = batch.events[:0]
	l.mu.Unlock()
	retained := false
	defer func() {
		if !retained {
			releaseAcquisitionWorkEvents(events)
		}
	}()

	result := l.process(batch.ctx, batch.ledger, events)
	if errors.Is(result.err, shamap.ErrTraversalBudget) && batch.ctx.Err() == nil {
		result.err = nil
		result.yielded = true
		// Exhausting a slice means the resumable SHAMap cursor made bounded
		// local progress; it is not a stalled acquisition. Keep the retry timer
		// behind the active walk so a large on-disk tree cannot consume the
		// terminal no-progress budget before the next missing frontier is found.
		result.rearmTimer = true
	}
	if result.err == nil && !result.complete && !result.remove {
		useful := 0
		for _, reply := range result.replies {
			useful += reply.useful
		}
		if err := batch.ledger.CheckpointPersistence(batch.ctx, useful); err != nil {
			result.persistenceErr = err
			result.remove = true
		}
	}
	for i := range events {
		if !result.yielded && !result.timerEscalate && events[i].kind == acquisitionWorkTimer {
			result.rearmTimer = true
			break
		}
	}
	if result.complete && l.flush != nil {
		result.persistenceErr = l.flush(batch.ctx, batch.ledger)
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
	if result.complete || result.remove || batch.ctx.Err() != nil {
		delete(l.pending, batch.ledger)
		batch.cancel()
		discarded = append(discarded, batch.events...)
		batch.events = nil
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
	var runLocal, runTimerCheck, runTimer, runFailure bool
	var timerAt time.Time
	var fetch func([32]byte) ([]byte, bool)
	for _, event := range events {
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
			if event.peerID != 0 && event.data != nil &&
				(event.data.InfoType == message.LedgerInfoAsNode || event.data.InfoType == message.LedgerInfoTxNode) {
				ledger.ReleaseMissingPeer(event.peerID)
			}
			if event.resume {
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
			var replyTrace inbound.ReplyTrace
			inputStats := acquisitionNodeInputStats(event.data)
			if event.data != nil {
				receivedBytes := max(inputStats.ReceivedBytes, event.payloadBytes)
				requestKind := inbound.AcquisitionRequestState
				switch event.data.InfoType {
				case message.LedgerInfoBase:
					requestKind = inbound.AcquisitionRequestBase
				case message.LedgerInfoTxNode:
					requestKind = inbound.AcquisitionRequestTransaction
				}
				replyTrace = ledger.BeginReplyDiagnostics(
					event.peerID,
					requestKind,
					inputStats.ReceivedNodes,
					receivedBytes,
					event.wireBytes,
					event.receivedAt,
					time.Now(),
				)
			}
			applyStarted := time.Now()
			stats, badKind, remove, complete, err := applyAcquisitionDataMeasured(ctx, ledger, event.data)
			applyDuration := time.Since(applyStarted)
			useful := stats.UsefulNodes
			if event.data != nil {
				ledger.FinishReplyDiagnostics(replyTrace, stats, applyDuration)
			}
			event.resume = true
			event.useful = useful
			if event.data != nil {
				result.replies = append(result.replies, acquisitionReplyStat{
					peerID: event.peerID, infoType: event.data.InfoType,
					requestID: replyTrace.RequestID, requested: replyTrace.RequestedNodes,
					queryDepth: replyTrace.QueryDepth,
					received:   stats.ReceivedNodes, useful: useful,
					receivedBytes: max(stats.ReceivedBytes, event.payloadBytes), usefulBytes: stats.UsefulBytes,
					duplicates: stats.DuplicateNodes, rerequests: stats.ReRequestNodes,
					invalid: stats.InvalidNodes, unprocessed: stats.UnprocessedNodes,
					wireBytes: event.wireBytes, responseLatency: replyTrace.ResponseLatency,
					queueDelay: replyTrace.QueueDelay, applyDuration: applyDuration,
				})
			}
			if err != nil {
				if badKind != "" {
					result.badData = append(result.badData, acquisitionBadData{peerID: event.peerID, kind: badKind})
				}
				if remove {
					result.err = err
					result.remove = true
					result.policyFailure = errors.Is(err, inbound.ErrHeaderRejected)
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
			if useful > usefulByPeer[event.peerID] {
				usefulByPeer[event.peerID] = useful
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
		case acquisitionWorkLocal:
			runLocal = true
			fetch = event.fetch
		case acquisitionWorkFailure:
		case acquisitionWorkAdded:
			addedPeers = append(addedPeers, event.peerID)
		case acquisitionWorkRetarget:
			result.retarget = true
			if len(result.stateIDs) == 0 && len(result.txIDs) == 0 &&
				len(event.peers) > 0 && (len(event.stateIDs) > 0 || len(event.txIDs) > 0) {
				result.targets = append(result.targets[:0], event.peers[0])
				result.stateIDs = event.stateIDs
				result.txIDs = event.txIDs
				result.queryDepth = event.queryDepth
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
		walkStarted := time.Now()
		_, complete, err := ledger.CheckLocalContext(workCtx, fetch)
		ledger.RecordFrontierWalk(time.Since(walkStarted))
		if err != nil {
			result.err = err
			return result
		}
		if complete {
			result.complete = true
			return result
		}
	}

	if runTimer {
		result.stateIDs, result.txIDs, result.complete, result.err = ledger.CollectMissingRequestContext(workCtx, false)
		if result.err != nil || result.complete {
			return result
		}
		walkStarted := time.Now()
		result.byHashState, result.byHashTx, result.err = ledger.TakeByHashRequestContext(workCtx, inboundByHashBatch)
		ledger.RecordFrontierWalk(time.Since(walkStarted))
		if result.err != nil {
			return result
		}
		if len(addedPeers) > 0 {
			result.requests = ledger.CollectMissingCachedAddedRequests(addedPeers)
		}
		result.localFetch = fetch
		return result
	}
	if preferred := selectUsefulAcquisitionPeers(usefulByPeer); len(preferred) > 0 {
		peers := acquisitionRequestCandidates(preferred, ledger.Peers())
		result.requests, result.complete, result.err = ledger.CollectMissingReplyRequestsContext(workCtx, peers)
		if result.err != nil || result.complete {
			return result
		}
	}
	if len(retargetPeers) > 0 {
		requests, complete, err := ledger.CollectMissingReplyRequestsContext(workCtx, retargetPeers)
		if err != nil {
			for _, request := range result.requests {
				ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
			}
			result.requests = nil
			result.err = err
			return result
		}
		result.requests = append(result.requests, requests...)
		if complete {
			result.complete = true
			return result
		}
	}
	if len(addedPeers) > 0 {
		requests, complete, err := ledger.CollectMissingAddedRequestsContext(workCtx, addedPeers)
		if err != nil {
			for _, request := range result.requests {
				ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
			}
			result.requests = nil
			result.err = err
			return result
		}
		result.requests = append(result.requests, requests...)
		result.complete = complete
	}
	return result
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
		rand.Shuffle(len(peers), func(i, j int) {
			peers[i], peers[j] = peers[j], peers[i]
		})
		peers = peers[:acquisitionMaxUsefulPeers]
	}
	return peers
}

func (r *Router) submitAcquisitionWork(ledger *inbound.Ledger, event acquisitionWorkEvent) bool {
	if lane := r.currentAcquisitionWork(); lane != nil {
		if lane.submit(ledger, event) {
			return true
		}
		return false
	}
	result := processAcquisitionWork(context.Background(), ledger, []acquisitionWorkEvent{event})
	r.handleAcquisitionWorkResult(result)
	return true
}

func (r *Router) currentAcquisitionWork() *acquisitionWorkLane {
	r.acquisitionWorkMu.RLock()
	lane := r.acquisitionWork
	r.acquisitionWorkMu.RUnlock()
	return lane
}

func (r *Router) handleAcquisitionWorkResult(result acquisitionWorkResult) {
	if result.ack != nil {
		defer close(result.ack)
	}
	if errors.Is(result.err, context.Canceled) {
		return
	}
	ledger := result.ledger
	if ledger == nil || r.fetchTracker.Find(ledger.Hash()) != ledger {
		if ledger != nil {
			for _, request := range result.requests {
				ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
			}
			r.retireAcquisitionStore(r.lifecycleContext(), ledger)
		}
		return
	}
	if result.rearmTimer && !result.complete && !result.remove {
		defer ledger.RearmTimer(time.Now())
	}
	for _, bad := range result.badData {
		if bad.kind != "" {
			r.acquisition.IncPeerBadData(bad.peerID, bad.kind)
		}
	}
	if result.err != nil && !result.remove {
		r.logger.Warn("inbound ledger: acquisition worker failed", "error", result.err)
		return
	}
	if result.persistenceErr != nil {
		r.logger.Warn("inbound ledger: verified-node persistence failed", "error", result.persistenceErr, "seq", ledger.Seq())
		r.discardFailedInboundAcquisition(ledger)
		return
	}
	if result.remove {
		if result.err != nil {
			r.logger.Warn("inbound ledger: acquisition data rejected", "error", result.err)
		}
		if result.timerFailure || result.policyFailure {
			r.failInboundAcquisitionWithSnapshot(ledger, result.snapshot)
		} else {
			snapshot := result.snapshot
			if !result.haveSnapshot {
				snapshot = ledger.Snapshot()
			}
			r.discardFailedInboundAcquisitionWithSnapshot(ledger, snapshot)
		}
		return
	}
	r.promoteResolvedFrozenPivot(ledger, ledger.PeerID())
	if result.complete {
		r.completeInboundLedgerReady(ledger)
		return
	}
	if result.timerEscalate {
		if !r.escalateAcquisition(ledger, result.timerAt) {
			ledger.RearmTimer(time.Now())
		}
		return
	}
	if result.retryBase {
		r.requestAcquisitionBase(ledger)
	}
	for _, reply := range result.replies {
		r.logger.Debug("inbound ledger reply processed",
			"seq", ledger.Seq(),
			"peer", reply.peerID,
			"info_type", reply.infoType,
			"request_id", reply.requestID,
			"requested", reply.requested,
			"query_depth", reply.queryDepth,
			"received", reply.received,
			"useful", reply.useful,
			"received_bytes", reply.receivedBytes,
			"wire_bytes", reply.wireBytes,
			"useful_bytes", reply.usefulBytes,
			"duplicates", reply.duplicates,
			"rerequests", reply.rerequests,
			"invalid", reply.invalid,
			"unprocessed", reply.unprocessed,
			"response_ms", reply.responseLatency.Milliseconds(),
			"worker_queue_ms", reply.queueDelay.Milliseconds(),
			"apply_ms", reply.applyDuration.Milliseconds(),
		)
	}
	retry := missingNodeRetry{queryDepth: result.queryDepth}
	if len(result.stateIDs) > 0 || len(result.txIDs) > 0 {
		retry = r.sendMissingAcquisitionNodes(
			ledger,
			result.targets,
			result.stateIDs,
			result.txIDs,
			result.queryDepth,
		)
	}
	released := 0
	for _, request := range result.requests {
		if r.sendMissingReplyRequest(ledger, request) {
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
		r.logger.Debug("inbound ledger requests scheduled",
			"seq", ledger.Seq(),
			"peers", requestPeers,
			"state_nodes", stateNodes,
			"tx_nodes", txNodes,
		)
	}
	if result.retarget && (len(retry.stateIDs) > 0 || len(retry.txIDs) > 0) {
		ledger.ReleaseUnreservedMissingNodes()
	} else if !result.retarget {
		r.retryMissingAcquisitionNodes(ledger, retry, released)
	}
	if len(result.byHashState) > 0 || len(result.byHashTx) > 0 {
		peers := ledger.Peers()
		r.sendNodesByHash(peers, ledger.Hash(), ledger.Seq(), result.byHashState, message.ObjectTypeStateNode)
		r.sendNodesByHash(peers, ledger.Hash(), ledger.Seq(), result.byHashTx, message.ObjectTypeTransactionNode)
	}
	if result.localFetch != nil && !r.submitAcquisitionWork(ledger, acquisitionWorkEvent{
		kind: acquisitionWorkLocal, fetch: result.localFetch,
	}) {
		r.logger.Warn("inbound ledger: post-timeout local refresh deferred; acquisition worker unavailable", "seq", ledger.Seq())
	}
}

func (r *Router) sendMissingReplyRequest(ledger *inbound.Ledger, request inbound.MissingRequest) bool {
	indirect := ledger.Timeouts() > 0
	queryDepth := uint32(1)
	if request.Blind {
		queryDepth = 0
	} else if latency, ok := r.acquisition.PeerLatency(request.PeerID); ok && latency >= 300*time.Millisecond {
		queryDepth = 2
	}
	if !r.acquisitionPeerConnected(request.PeerID) {
		ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
		r.removeStaleAcquisitionPeer(ledger, request.PeerID)
		return true
	}
	requestKind := inbound.AcquisitionRequestState
	if request.Transaction {
		requestKind = inbound.AcquisitionRequestTransaction
	}
	requestID := ledger.RecordRequestStart(
		request.PeerID,
		len(request.NodeIDs),
		queryDepth,
		requestKind,
		request.Blind,
		time.Now(),
	)
	var err error
	if request.Transaction {
		err = r.acquisition.RequestTransactionNodes(request.PeerID, ledger.Hash(), request.NodeIDs, queryDepth, indirect)
	} else {
		err = r.acquisition.RequestStateNodes(request.PeerID, ledger.Hash(), request.NodeIDs, queryDepth, indirect)
	}
	if err == nil {
		return false
	}
	ledger.RecordRequestSendFailure(request.PeerID, requestID)
	ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
	return r.handleMissingNodeSendFailure(ledger, request.PeerID, request.Transaction, err)
}

type missingNodeRetry struct {
	stateIDs   [][]byte
	txIDs      [][]byte
	queryDepth uint32
}

func (r *Router) sendMissingAcquisitionNodes(
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
		if !r.acquisitionPeerConnected(peerID) {
			r.removeStaleAcquisitionPeer(ledger, peerID)
			stateDisconnected = stateDisconnected || len(stateIDs) > 0
			txDisconnected = txDisconnected || len(txIDs) > 0
			continue
		}
		if len(stateIDs) > 0 {
			requestID := ledger.RecordRequestStart(peerID, len(stateIDs), queryDepth, inbound.AcquisitionRequestState, queryDepth == 0, time.Now())
			if err := r.acquisition.RequestStateNodes(peerID, ledger.Hash(), stateIDs, queryDepth, indirect); err != nil {
				ledger.RecordRequestSendFailure(peerID, requestID)
				disconnected = r.handleMissingNodeSendFailure(ledger, peerID, false, err)
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
			requestID := ledger.RecordRequestStart(peerID, len(txIDs), queryDepth, inbound.AcquisitionRequestTransaction, queryDepth == 0, time.Now())
			if err := r.acquisition.RequestTransactionNodes(peerID, ledger.Hash(), txIDs, queryDepth, indirect); err != nil {
				ledger.RecordRequestSendFailure(peerID, requestID)
				disconnected = r.handleMissingNodeSendFailure(ledger, peerID, true, err)
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

func (r *Router) acquisitionPeerConnected(peerID uint64) bool {
	return r.peerSessions == nil || r.peerSessions.IsPeerConnected(peermanagement.PeerID(peerID))
}

func (r *Router) removeStaleAcquisitionPeer(ledger *inbound.Ledger, peerID uint64) {
	ledger.RemovePeer(peerID)
	r.HandlePeerDisconnect(peermanagement.PeerID(peerID))
}

func applyAcquisitionData(ctx context.Context, ledger *inbound.Ledger, data *message.LedgerData) (useful int, badKind string, remove, complete bool, err error) {
	stats, badKind, remove, complete, err := applyAcquisitionDataMeasured(ctx, ledger, data)
	return stats.UsefulNodes, badKind, remove, complete, err
}

func applyAcquisitionDataMeasured(ctx context.Context, ledger *inbound.Ledger, data *message.LedgerData) (stats inbound.NodeApplyStats, badKind string, remove, complete bool, err error) {
	if data == nil {
		return stats, "", false, false, nil
	}
	stats = acquisitionNodeInputStats(data)
	switch data.InfoType {
	case message.LedgerInfoBase:
		stats, err = ledger.GotBaseMeasuredContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		policyFailure := errors.Is(err, inbound.ErrHeaderRejected)
		badKind := "ledger-data-base"
		if localFailure || policyFailure {
			badKind = ""
		}
		return stats, badKind, localFailure || policyFailure, ledger.IsComplete(), err
	case message.LedgerInfoAsNode:
		stats, err = ledger.GotStateNodesMeasuredContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		badKind := "ledger-data-state"
		if localFailure {
			badKind = ""
		}
		return stats, badKind, localFailure, ledger.IsComplete(), err
	case message.LedgerInfoTxNode:
		stats, err = ledger.GotTransactionNodesMeasuredContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		badKind := "ledger-data-tx"
		if localFailure {
			badKind = ""
		}
		return stats, badKind, localFailure, ledger.IsComplete(), err
	default:
		return stats, "", false, false, nil
	}
}

func acquisitionNodeInputStats(data *message.LedgerData) inbound.NodeApplyStats {
	if data == nil {
		return inbound.NodeApplyStats{}
	}
	stats := inbound.NodeApplyStats{ReceivedNodes: len(data.Nodes)}
	for i := range data.Nodes {
		stats.ReceivedBytes += len(data.Nodes[i].NodeData)
	}
	return stats
}
