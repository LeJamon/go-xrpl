package adaptor

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
)

const replayHeadDiagnosticInterval = 5 * time.Second

type replayHeadDiagnostics struct {
	ledger        *inbound.Ledger
	requests      uint64
	requestNodes  uint64
	hedgeRequests uint64
	sendFailures  uint64
	replyBytes    uint64
	wireBytes     uint64
	ingressMax    time.Duration
	queueMax      time.Duration
	processMax    time.Duration
	workMax       time.Duration
	resultMax     time.Duration
	sendMax       time.Duration
	lastPeer      uint64
	lastRTT       time.Duration
	wait          string
}

func (c *catchupReplayCoordinator) replayHeadDiagnosticsLocked(ledger *inbound.Ledger) *replayHeadDiagnostics {
	pipeline := &c.standardReplay
	entry := pipeline.entries[pipeline.anchorSeq+1]
	if ledger == nil || !pipeline.active || !pipeline.pivotReady || entry == nil || entry.acquisition != ledger {
		return nil
	}
	if c.replayHeadDiagnostics.ledger != ledger {
		c.replayHeadDiagnostics = replayHeadDiagnostics{ledger: ledger, wait: "header"}
	}
	return &c.replayHeadDiagnostics
}

func (c *catchupReplayCoordinator) recordReplayHeadWork(result acquisitionWorkResult) {
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	d := c.replayHeadDiagnosticsLocked(result.ledger)
	if d == nil {
		return
	}
	d.workMax = max(d.workMax, result.workDuration)
	if !result.readyAt.IsZero() {
		d.resultMax = max(d.resultMax, time.Since(result.readyAt))
	}
	for _, reply := range result.replies {
		d.replyBytes += reply.bytes
		d.wireBytes += reply.wire
		d.ingressMax = max(d.ingressMax, reply.ingress)
		d.queueMax = max(d.queueMax, reply.queue)
		d.processMax = max(d.processMax, reply.process)
	}
	switch {
	case result.complete:
		d.wait = "complete"
	case result.remove:
		d.wait = "failed"
	case result.pendingReply || result.requestWait == inbound.MissingRequestWaitMemory:
		d.wait = "memory"
	case result.requestWait == inbound.MissingRequestWaitPersistence:
		d.wait = "persistence"
	case result.yielded || result.continueRequests:
		d.wait = "traversal"
	case len(result.requests) > 0 || len(result.stateIDs) > 0 || len(result.txIDs) > 0 ||
		len(result.byHashState) > 0 || len(result.byHashTx) > 0 || result.requestWait == inbound.MissingRequestWaitReply:
		d.wait = "reply"
	}
}

func (c *catchupReplayCoordinator) recordReplayHeadSend(ledger *inbound.Ledger, request inbound.MissingRequest, elapsed time.Duration, failed bool) {
	rtt, _ := c.acquisition.PeerLatency(request.PeerID)
	c.acquisitionMu.Lock()
	defer c.acquisitionMu.Unlock()
	if d := c.replayHeadDiagnosticsLocked(ledger); d != nil {
		d.requests++
		d.requestNodes += uint64(max(len(request.NodeHashes), len(request.NodeIDs)))
		if request.Hedge {
			d.hedgeRequests++
		}
		d.sendMax = max(d.sendMax, elapsed)
		d.lastPeer, d.lastRTT = request.PeerID, rtt
		if failed {
			d.sendFailures++
		}
	}
}

func (c *catchupReplayCoordinator) reportReplayHead(now time.Time) {
	c.acquisitionMu.Lock()
	pipeline := &c.standardReplay
	entry := pipeline.entries[pipeline.anchorSeq+1]
	if !pipeline.active || !pipeline.pivotReady || entry == nil || entry.acquisition == nil {
		c.replayHeadDiagnostics = replayHeadDiagnostics{}
		c.acquisitionMu.Unlock()
		return
	}
	if now.Sub(c.replayHeadLogAt) < replayHeadDiagnosticInterval {
		c.acquisitionMu.Unlock()
		return
	}
	d := *c.replayHeadDiagnosticsLocked(entry.acquisition)
	c.replayHeadLogAt = now
	age := max(0, now.Sub(entry.requestedAt))
	var readyBlocked int
	var readyWait time.Duration
	for seq, prepared := range pipeline.entries {
		if seq > entry.seq && !prepared.readyAt.IsZero() {
			readyBlocked++
			readyWait = max(readyWait, now.Sub(prepared.readyAt))
		}
	}
	c.acquisitionMu.Unlock()
	snapshot := d.ledger.Snapshot()
	var requestAge time.Duration
	if !snapshot.OldestRequest.IsZero() {
		requestAge = max(0, now.Sub(snapshot.OldestRequest))
	}
	c.logger.Info("replay head acquisition",
		"seq", snapshot.Seq, "hash", fmt.Sprintf("%x", snapshot.Hash[:8]),
		"phase", snapshot.Phase(), "age", age, "planner_wait", d.wait,
		"outstanding_nodes", snapshot.OutstandingNodes, "request_peers", snapshot.RequestPeers,
		"oldest_request_age", requestAge, "request_leases", snapshot.RequestPeerSnapshots, "hedged_nodes", snapshot.Hedges,
		"needed_tx", len(snapshot.NeededTx), "timeouts", snapshot.Timeouts,
		"leased_requests", d.requests, "leased_request_nodes", d.requestNodes, "leased_send_failures", d.sendFailures,
		"hedge_requests", d.hedgeRequests,
		"last_leased_peer", d.lastPeer, "last_peer_ping_rtt", d.lastRTT, "leased_send_admission_max", d.sendMax,
		"reply_payload_bytes", d.replyBytes, "reply_wire_bytes", d.wireBytes,
		"ingress_queue_max", d.ingressMax, "worker_queue_max", d.queueMax, "reply_process_max", d.processMax,
		"worker_batch_max", d.workMax, "result_queue_max", d.resultMax,
		"tx_received", snapshot.TxReceived, "tx_useful", snapshot.TxUseful,
		"tx_nonuseful", snapshot.TxReceived-min(snapshot.TxReceived, snapshot.TxUseful),
		"ready_blocked", readyBlocked, "ready_wait_max", readyWait,
	)
}
