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
)

type acquisitionWorkEvent struct {
	kind       acquisitionWorkKind
	data       *message.LedgerData
	peerID     uint64
	resume     bool
	useful     int
	fetch      func([32]byte) ([]byte, bool)
	after      func()
	peers      []uint64
	added      []uint64
	stateIDs   [][]byte
	txIDs      [][]byte
	queryDepth uint32
	collect    bool
	at         time.Time
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
	retryBase      bool
	retarget       bool
	queryDepth     uint32
	complete       bool
	snapshot       inbound.Snapshot
	haveSnapshot   bool
	err            error
	persistenceErr error
	ack            chan struct{}
	after          []func()
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

	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}
	result  chan acquisitionWorkResult
	done    chan struct{}
	started chan struct{}

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
		started:    make(chan struct{}),
		queueDepth: queueDepth,
		pending:    make(map[*inbound.Ledger]*acquisitionWorkBatch),
	}
}

func (l *acquisitionWorkLane) start(parent context.Context) {
	l.ctx, l.cancel = context.WithCancel(parent)
	close(l.started)
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
	defer close(l.done)
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

	result := l.process(batch.ctx, batch.ledger, events)
	if errors.Is(result.err, shamap.ErrTraversalBudget) && batch.ctx.Err() == nil {
		result.err = nil
		result.yielded = true
		for i := range events {
			events[i].after = nil
		}
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
		if !result.yielded && events[i].kind == acquisitionWorkTimer {
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

	l.mu.Lock()
	var trailingAfter []func()
	if result.complete || result.remove || batch.ctx.Err() != nil {
		if result.complete || result.remove {
			for i := range batch.events {
				if batch.events[i].after != nil {
					trailingAfter = append(trailingAfter, batch.events[i].after)
				}
			}
		}
		delete(l.pending, batch.ledger)
		batch.cancel()
	} else if result.yielded {
		batch.events = append(batch.events, events...)
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
	if len(trailingAfter) > 0 {
		followup := acquisitionWorkResult{ledger: batch.ledger, after: trailingAfter, ack: make(chan struct{})}
		select {
		case <-l.ctx.Done():
			return false
		case l.result <- followup:
		}
		select {
		case <-l.ctx.Done():
			return false
		case <-followup.ack:
		}
	}
	return true
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
	for _, event := range events {
		if event.after != nil {
			result.after = append(result.after, event.after)
		}
	}
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
			useful, badKind, remove, complete, err := applyAcquisitionData(ctx, ledger, event.data)
			event.resume = true
			event.useful = useful
			if event.data != nil {
				result.replies = append(result.replies, acquisitionReplyStat{
					peerID: event.peerID, infoType: event.data.InfoType,
					received: len(event.data.Nodes), useful: useful,
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

	workCtx := shamap.WithTraversalBudget(ctx, visitBudget)

	if runLocal || runTimer {
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
			result.requests, result.complete, result.err = ledger.CollectMissingAddedRequestsContext(workCtx, addedPeers)
		}
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
	defer func() {
		for _, after := range result.after {
			after()
		}
	}()
	if errors.Is(result.err, context.Canceled) {
		return
	}
	ledger := result.ledger
	if ledger == nil || r.fetchTracker.Find(ledger.Hash()) != ledger {
		if ledger != nil {
			for _, request := range result.requests {
				ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
			}
			r.retireAcquisitionStore(context.TODO(), ledger)
		}
		return
	}
	if result.rearmTimer && !result.complete && !result.remove {
		defer ledger.RearmTimer(time.Now())
	}
	for _, bad := range result.badData {
		if bad.kind != "" {
			r.adaptor.IncPeerBadData(bad.peerID, bad.kind)
		}
	}
	if result.err != nil && !result.remove {
		r.logger.Warn("inbound ledger: acquisition worker failed", "error", result.err)
		return
	}
	if result.persistenceErr != nil {
		r.logger.Warn("inbound ledger: verified-node persistence failed", "error", result.persistenceErr, "seq", ledger.Seq())
		if r.fetchTracker.DiscardExpected(ledger) {
			r.retireAcquisitionStore(context.TODO(), ledger)
		}
		return
	}
	if result.remove {
		if result.err != nil {
			r.logger.Warn("inbound ledger: acquisition data rejected", "error", result.err)
		}
		if result.timerFailure || result.policyFailure {
			r.failInboundAcquisitionWithSnapshot(ledger, result.snapshot)
		} else if result.haveSnapshot {
			if r.fetchTracker.RemoveExpectedWithSnapshot(ledger, result.snapshot, false) {
				r.retireAcquisitionStore(context.TODO(), ledger)
			}
		} else {
			if r.fetchTracker.RemoveExpectedWithSnapshot(ledger, ledger.Snapshot(), false) {
				r.retireAcquisitionStore(context.TODO(), ledger)
			}
		}
		return
	}
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
			"received", reply.received,
			"useful", reply.useful,
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
}

func (r *Router) sendMissingReplyRequest(ledger *inbound.Ledger, request inbound.MissingRequest) bool {
	indirect := ledger.Timeouts() > 0
	queryDepth := uint32(1)
	if request.Blind {
		queryDepth = 0
	} else if latency, ok := r.adaptor.PeerLatency(request.PeerID); ok && latency >= 300*time.Millisecond {
		queryDepth = 2
	}
	if !r.acquisitionPeerConnected(request.PeerID) {
		ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
		r.removeStaleAcquisitionPeer(ledger, request.PeerID)
		return true
	}
	var err error
	if request.Transaction {
		err = r.adaptor.RequestTransactionNodes(request.PeerID, ledger.Hash(), request.NodeIDs, queryDepth, indirect)
	} else {
		err = r.adaptor.RequestStateNodes(request.PeerID, ledger.Hash(), request.NodeIDs, queryDepth, indirect)
	}
	if err == nil {
		return false
	}
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
			if err := r.adaptor.RequestStateNodes(peerID, ledger.Hash(), stateIDs, queryDepth, indirect); err != nil {
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
			if err := r.adaptor.RequestTransactionNodes(peerID, ledger.Hash(), txIDs, queryDepth, indirect); err != nil {
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
	if data == nil {
		return 0, "", false, false, nil
	}
	switch data.InfoType {
	case message.LedgerInfoBase:
		useful, err = ledger.GotBaseUsefulContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		policyFailure := errors.Is(err, inbound.ErrHeaderRejected)
		badKind := "ledger-data-base"
		if localFailure || policyFailure {
			badKind = ""
		}
		return useful, badKind, localFailure || policyFailure, ledger.IsComplete(), err
	case message.LedgerInfoAsNode:
		var added int
		added, err = ledger.GotStateNodesUsefulContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		badKind := "ledger-data-state"
		if localFailure {
			badKind = ""
		}
		return added, badKind, localFailure, ledger.IsComplete(), err
	case message.LedgerInfoTxNode:
		var added int
		added, err = ledger.GotTransactionNodesUsefulContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		badKind := "ledger-data-tx"
		if localFailure {
			badKind = ""
		}
		return added, badKind, localFailure, ledger.IsComplete(), err
	default:
		return 0, "", false, false, nil
	}
}
