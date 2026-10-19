package adaptor

import (
	"context"
	"unsafe"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

type ledgerServeClass uint8

const (
	ledgerServeOrdinary ledgerServeClass = iota
	ledgerServeTxSet
	ledgerServeClassCount
	ledgerServePerPeerQueue = 8
	ledgerServeMaxBytes     = 32 << 20
)

type LedgerServeMetrics struct {
	Ledger peermanagement.ServeSchedulerStats
	TxSet  peermanagement.ServeSchedulerStats
}

func (r *Router) LedgerServeMetrics() LedgerServeMetrics {
	r.lifecycleMu.RLock()
	pools := r.servePools
	r.lifecycleMu.RUnlock()
	var metrics LedgerServeMetrics
	if pools[ledgerServeOrdinary] != nil {
		metrics.Ledger = pools[ledgerServeOrdinary].Snapshot()
	}
	if pools[ledgerServeTxSet] != nil {
		metrics.TxSet = pools[ledgerServeTxSet].Snapshot()
	}
	return metrics
}

func newLedgerServePools(ctx context.Context, workers int) [ledgerServeClassCount]*peermanagement.ServeScheduler {
	workers = max(2, workers)
	txSetWorkers := max(1, workers/4)
	return [ledgerServeClassCount]*peermanagement.ServeScheduler{
		peermanagement.NewServeScheduler(ctx, peermanagement.ServeSchedulerConfig{
			Workers: workers - txSetWorkers, MaxPending: serveQueueDepth * 3 / 4,
			PerPeerQueue: ledgerServePerPeerQueue, PerPeerActive: 1,
			MaxBytes: ledgerServeMaxBytes * 3 / 4, PerPeerBytes: ledgerServeMaxBytes * 3 / 16,
		}),
		peermanagement.NewServeScheduler(ctx, peermanagement.ServeSchedulerConfig{
			Workers: txSetWorkers, MaxPending: serveQueueDepth / 4,
			PerPeerQueue: ledgerServePerPeerQueue, PerPeerActive: 1,
			MaxBytes: ledgerServeMaxBytes / 4, PerPeerBytes: ledgerServeMaxBytes / 16,
		}),
	}
}

func getLedgerServeClass(msg *peermanagement.InboundMessage) ledgerServeClass {
	if msg.GetLedger != nil && msg.GetLedger.InfoType == message.LedgerInfoTsCandidate {
		return ledgerServeTxSet
	}
	return ledgerServeOrdinary
}

// Count request storage separately from the overlay reservation, which stays
// owned by the message until serving or discard completes.
func getLedgerRetainedBytes(msg *peermanagement.InboundMessage) int64 {
	bytes := int64(unsafe.Sizeof(*msg)) + int64(cap(msg.Payload))
	if req := msg.GetLedger; req != nil {
		bytes += int64(unsafe.Sizeof(*req)) + int64(cap(req.LedgerHash))
		if req.QueryType != nil {
			bytes += int64(unsafe.Sizeof(*req.QueryType))
		}
		bytes += int64(cap(req.NodeIDs)) * int64(unsafe.Sizeof([]byte{}))
		for _, id := range req.NodeIDs {
			bytes += int64(cap(id))
		}
	}
	return bytes
}

func (r *Router) submitServeJob(msg *peermanagement.InboundMessage) {
	if msg == nil {
		return
	}
	r.lifecycleMu.RLock()
	if r.lifecycleState == routerLifecycleInitial {
		r.lifecycleMu.RUnlock()
		defer func() { _ = msg.Close() }()
		r.handleGetLedger(context.Background(), msg)
		return
	}
	pool := r.servePools[getLedgerServeClass(msg)]
	if r.lifecycleState != routerLifecycleRunning || pool == nil || r.lifecycleCtx.Err() != nil ||
		(r.peerSessions != nil && !r.peerSessions.IsPeerConnected(msg.PeerID)) {
		r.lifecycleMu.RUnlock()
		r.droppedServeJobs.Add(1)
		_ = msg.Close()
		return
	}
	accepted := pool.SubmitOwnedBytes(r.lifecycleCtx, msg.PeerID, getLedgerRetainedBytes(msg), func(ctx context.Context) {
		defer func() { _ = msg.Close() }()
		if r.peerSessions == nil || r.peerSessions.IsPeerConnected(msg.PeerID) {
			r.handleGetLedger(ctx, msg)
		}
	}, func() { _ = msg.Close() })
	r.lifecycleMu.RUnlock()
	if accepted && r.peerSessions != nil && !r.peerSessions.IsPeerConnected(msg.PeerID) {
		pool.CancelPeer(msg.PeerID)
	}
	if !accepted {
		r.droppedServeJobs.Add(1)
		_ = msg.Close()
		r.logger.Debug("inbound get_ledger dropped: serve pool saturated",
			"t", "consensus", "event", "serve-shed", "peer", msg.PeerID)
	}
}

func (r *Router) cancelPeerServes(peerID peermanagement.PeerID) {
	r.lifecycleMu.RLock()
	pools := r.servePools
	r.lifecycleMu.RUnlock()
	for _, pool := range pools {
		if pool != nil {
			pool.CancelPeer(peerID)
		}
	}
}
