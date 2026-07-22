package adaptor

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
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
)

type acquisitionWorkKind uint8

const (
	acquisitionWorkData acquisitionWorkKind = iota
	acquisitionWorkTimerCheck
	acquisitionWorkTimer
	acquisitionWorkLocal
	acquisitionWorkFailure
	acquisitionWorkAdded
)

type acquisitionWorkEvent struct {
	kind   acquisitionWorkKind
	data   *message.LedgerData
	peerID uint64
	fetch  func([32]byte) ([]byte, bool)
	after  func()
	peers  []uint64
	added  []uint64
	at     time.Time
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
	byHashState    [][32]byte
	byHashTx       [][32]byte
	badData        []acquisitionBadData
	remove         bool
	timerFailure   bool
	timerEscalate  bool
	timerAt        time.Time
	rearmTimer     bool
	retryBase      bool
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
		process:    processAcquisitionWork,
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
	if l.ctx == nil || l.ctx.Err() != nil || len(l.pending) >= l.queueDepth+1 {
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
	for i := range events {
		if events[i].kind == acquisitionWorkTimer {
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
	if result.complete || result.remove || batch.ctx.Err() != nil || len(batch.events) == 0 {
		if result.complete || result.remove {
			for i := range batch.events {
				if batch.events[i].after != nil {
					trailingAfter = append(trailingAfter, batch.events[i].after)
				}
			}
		}
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
	result := acquisitionWorkResult{ledger: ledger}
	for _, event := range events {
		if event.after != nil {
			result.after = append(result.after, event.after)
		}
	}
	usefulByPeer := make(map[uint64]int)
	var addedPeers []uint64
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
		result.snapshot, result.err = ledger.SnapshotContext(ctx)
		result.haveSnapshot = result.err == nil
		result.remove = result.err == nil
		result.timerFailure = result.remove
		return result
	}

	for _, event := range events {
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		switch event.kind {
		case acquisitionWorkData:
			useful, badKind, remove, complete, err := applyAcquisitionData(ctx, ledger, event.data)
			if err != nil {
				result.badData = append(result.badData, acquisitionBadData{peerID: event.peerID, kind: badKind})
				if remove {
					result.err = err
					result.remove = true
					result.snapshot, _ = ledger.SnapshotContext(ctx)
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
			timerAt = event.at
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
		}
	}

	if runLocal || runTimer {
		_, complete, err := ledger.CheckLocalContext(ctx, fetch)
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
			result.snapshot, result.err = ledger.SnapshotContext(ctx)
			result.haveSnapshot = result.err == nil
			result.remove = result.err == nil
			result.timerFailure = result.remove
			return result
		case inbound.TimerEscalate:
			result.timerEscalate = true
			result.timerAt = timerAt
			return result
		}
	}

	if runTimer {
		result.stateIDs, result.txIDs, result.complete, result.err = ledger.CollectMissingRequestContext(ctx, false)
		if result.err != nil || result.complete {
			return result
		}
		result.byHashState, result.byHashTx, result.err = ledger.TakeByHashRequestContext(ctx, inboundByHashBatch)
		if result.err != nil {
			return result
		}
		if len(addedPeers) > 0 {
			result.requests, result.complete, result.err = ledger.CollectMissingAddedRequestsContext(ctx, addedPeers)
		}
		return result
	}
	if peers := selectUsefulAcquisitionPeers(usefulByPeer); len(peers) > 0 {
		result.requests, result.complete, result.err = ledger.CollectMissingReplyRequestsContext(ctx, peers)
		if result.err != nil || result.complete {
			return result
		}
	}
	if len(addedPeers) > 0 {
		requests, complete, err := ledger.CollectMissingAddedRequestsContext(ctx, addedPeers)
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
	if result.remove {
		if result.err != nil {
			r.logger.Warn("inbound ledger: acquisition data rejected", "error", result.err)
		}
		if result.timerFailure {
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
		if result.persistenceErr != nil {
			r.logger.Warn("inbound ledger: verified-node persistence failed", "error", result.persistenceErr, "seq", ledger.Seq())
			if r.fetchTracker.DiscardExpected(ledger) {
				r.retireAcquisitionStore(context.TODO(), ledger)
			}
			return
		}
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
	if len(result.stateIDs) > 0 || len(result.txIDs) > 0 {
		r.sendMissingAcquisitionNodes(ledger, result.targets, result.stateIDs, result.txIDs)
	}
	for _, request := range result.requests {
		r.sendMissingReplyRequest(ledger, request)
	}
	if len(result.byHashState) > 0 || len(result.byHashTx) > 0 {
		peers := ledger.Peers()
		r.sendNodesByHash(peers, ledger.Hash(), ledger.Seq(), result.byHashState, message.ObjectTypeStateNode)
		r.sendNodesByHash(peers, ledger.Hash(), ledger.Seq(), result.byHashTx, message.ObjectTypeTransactionNode)
	}
}

func (r *Router) sendMissingReplyRequest(ledger *inbound.Ledger, request inbound.MissingRequest) {
	indirect := ledger.Timeouts() > 0
	queryDepth := uint32(1)
	if request.Blind {
		queryDepth = 0
	} else if latency, ok := r.adaptor.PeerLatency(request.PeerID); ok && latency >= 300*time.Millisecond {
		queryDepth = 2
	}
	var err error
	if request.Transaction {
		err = r.adaptor.RequestTransactionNodes(request.PeerID, ledger.Hash(), request.NodeIDs, queryDepth, indirect)
	} else {
		err = r.adaptor.RequestStateNodes(request.PeerID, ledger.Hash(), request.NodeIDs, queryDepth, indirect)
	}
	if err == nil {
		return
	}
	ledger.ReleaseMissingRequest(request.PeerID, request.NodeHashes)
	r.logger.Warn("inbound ledger: failed to request missing nodes", "peer", request.PeerID, "transaction", request.Transaction, "error", err)
}

func (r *Router) sendMissingAcquisitionNodes(ledger *inbound.Ledger, peers []uint64, stateIDs, txIDs [][]byte) {
	indirect := ledger.Timeouts() > 0
	queryDepth := uint32(0)
	for _, peerID := range peers {
		if len(stateIDs) > 0 {
			if err := r.adaptor.RequestStateNodes(peerID, ledger.Hash(), stateIDs, queryDepth, indirect); err != nil {
				r.logger.Warn("inbound ledger: failed to request state nodes", "error", err)
			}
		}
		if len(txIDs) > 0 {
			if err := r.adaptor.RequestTransactionNodes(peerID, ledger.Hash(), txIDs, queryDepth, indirect); err != nil {
				r.logger.Warn("inbound ledger: failed to request tx nodes", "error", err)
			}
		}
	}
}

func applyAcquisitionData(ctx context.Context, ledger *inbound.Ledger, data *message.LedgerData) (useful int, badKind string, remove, complete bool, err error) {
	if data == nil {
		return 0, "", false, false, nil
	}
	switch data.InfoType {
	case message.LedgerInfoBase:
		useful, err = ledger.GotBaseUsefulContext(ctx, data.Nodes)
		localFailure := errors.Is(err, shamap.ErrNodeSerialization)
		badKind := "ledger-data-base"
		if localFailure {
			badKind = ""
		}
		return useful, badKind, localFailure, ledger.IsComplete(), err
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
