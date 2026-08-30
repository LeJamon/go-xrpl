// Copyright (c) 2024-2026. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package inbound

import (
	"sort"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	acquisitionRateBucketDuration = 5 * time.Second
	acquisitionRateBucketCount    = 12
)

type acquisitionRequestTrace struct {
	id             uint64
	sentAt         time.Time
	requestedNodes int
	queryDepth     uint32
	transaction    bool
	blind          bool
}

type acquisitionRequestKey struct {
	peerID      uint64
	transaction bool
}

type acquisitionRateBucket struct {
	epoch       int64
	usefulNodes uint64
	usefulBytes uint64
}

type acquisitionPeerMetrics struct {
	requests        uint64
	sendFailures    uint64
	replies         uint64
	requestedNodes  uint64
	returnedNodes   uint64
	usefulNodes     uint64
	receivedBytes   uint64
	wireBytes       uint64
	usefulBytes     uint64
	emptyReplies    uint64
	lateReplies     uint64
	invalidNodes    uint64
	disconnects     uint64
	responseTotal   time.Duration
	responseMax     time.Duration
	lastReplyAt     time.Time
	lastQueryDepth  uint32
	lastTransaction bool
}

type acquisitionDiagnostics struct {
	startedAt      time.Time
	lastProgressAt time.Time
	nextRequestID  uint64
	outstanding    map[acquisitionRequestKey]acquisitionRequestTrace
	peers          map[uint64]*acquisitionPeerMetrics
	rate           [acquisitionRateBucketCount]acquisitionRateBucket

	requests         uint64
	sendFailures     uint64
	replies          uint64
	receivedBytes    uint64
	wireBytes        uint64
	usefulBytes      uint64
	duplicateNodes   uint64
	reRequestNodes   uint64
	invalidNodes     uint64
	unprocessed      uint64
	emptyReplies     uint64
	lateReplies      uint64
	workerSaturation uint64

	decodeTotal time.Duration
	decodeMax   time.Duration
	queueTotal  time.Duration
	queueMax    time.Duration
	applyTotal  time.Duration
	applyMax    time.Duration
	walkTotal   time.Duration
	walkMax     time.Duration
	refillTotal time.Duration
	refillMax   time.Duration

	lastReplyReceivedAt time.Time
	stage               string
}

// NodeApplyStats classifies one verified-node reply without retaining node
// identifiers or payloads. It is aggregated once per reply, outside logging and
// metric hot paths.
type NodeApplyStats struct {
	ReceivedNodes    int
	ReceivedBytes    int
	UsefulNodes      int
	UsefulBytes      int
	DuplicateNodes   int
	ReRequestNodes   int
	InvalidNodes     int
	UnprocessedNodes int
}

// ReplyTrace correlates a peer reply with its outstanding request. The router
// passes it back to FinishReplyDiagnostics after SHAMap attachment.
type ReplyTrace struct {
	RequestID       uint64
	PeerID          uint64
	RequestedNodes  int
	QueryDepth      uint32
	Transaction     bool
	Blind           bool
	ResponseLatency time.Duration
	QueueDelay      time.Duration
}

// PeerDiagnostics is the bounded per-peer portion of fetch_info diagnostics.
type PeerDiagnostics struct {
	PeerID          uint64
	Requests        uint64
	SendFailures    uint64
	Replies         uint64
	RequestedNodes  uint64
	ReturnedNodes   uint64
	UsefulNodes     uint64
	ReceivedBytes   uint64
	WireBytes       uint64
	UsefulBytes     uint64
	EmptyReplies    uint64
	LateReplies     uint64
	InvalidNodes    uint64
	Disconnects     uint64
	ResponseTotal   time.Duration
	ResponseMax     time.Duration
	LastReplyAt     time.Time
	LastQueryDepth  uint32
	LastTransaction bool
}

// AcquisitionDiagnostics is a scalar-only snapshot. It is safe for admin RPC
// rendering without a SHAMap walk or NodeStore access.
type AcquisitionDiagnostics struct {
	StartedAt          time.Time
	LastProgressAt     time.Time
	Requests           uint64
	SendFailures       uint64
	Replies            uint64
	ReceivedBytes      uint64
	WireBytes          uint64
	UsefulBytes        uint64
	DuplicateNodes     uint64
	ReRequestNodes     uint64
	InvalidNodes       uint64
	UnprocessedNodes   uint64
	EmptyReplies       uint64
	LateReplies        uint64
	WorkerSaturation   uint64
	OutstandingReplies int
	OutstandingNodes   int
	RecentUsefulNodes  uint64
	RecentUsefulBytes  uint64
	RecentWindow       time.Duration
	DecodeTotal        time.Duration
	DecodeMax          time.Duration
	WorkerQueueTotal   time.Duration
	WorkerQueueMax     time.Duration
	ApplyTotal         time.Duration
	ApplyMax           time.Duration
	FrontierWalkTotal  time.Duration
	FrontierWalkMax    time.Duration
	RequestRefillTotal time.Duration
	RequestRefillMax   time.Duration
	Persistence        shamap.PersistenceStats
	LimitingStage      string
	Peers              []PeerDiagnostics
}

func newAcquisitionDiagnostics(now time.Time) acquisitionDiagnostics {
	return acquisitionDiagnostics{
		startedAt:      now,
		lastProgressAt: now,
		outstanding:    make(map[acquisitionRequestKey]acquisitionRequestTrace),
		peers:          make(map[uint64]*acquisitionPeerMetrics),
		stage:          "idle",
	}
}

func (l *Ledger) peerMetricsLocked(peerID uint64) *acquisitionPeerMetrics {
	peer := l.diagnostics.peers[peerID]
	if peer == nil {
		peer = &acquisitionPeerMetrics{}
		l.diagnostics.peers[peerID] = peer
	}
	return peer
}

// RecordRequestStart installs request correlation before the network call. The
// caller must invoke RecordRequestSendFailure if that call fails.
func (l *Ledger) RecordRequestStart(peerID uint64, requestedNodes int, queryDepth uint32, transaction, blind bool, now time.Time) uint64 {
	if peerID == 0 || requestedNodes <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.diagnostics.nextRequestID++
	id := l.diagnostics.nextRequestID
	l.diagnostics.outstanding[acquisitionRequestKey{peerID: peerID, transaction: transaction}] = acquisitionRequestTrace{
		id:             id,
		sentAt:         now,
		requestedNodes: requestedNodes,
		queryDepth:     queryDepth,
		transaction:    transaction,
		blind:          blind,
	}
	l.diagnostics.requests++
	peer := l.peerMetricsLocked(peerID)
	peer.requests++
	peer.requestedNodes += uint64(requestedNodes)
	peer.lastQueryDepth = queryDepth
	peer.lastTransaction = transaction
	if !l.diagnostics.lastReplyReceivedAt.IsZero() {
		d := nonNegativeDuration(now.Sub(l.diagnostics.lastReplyReceivedAt))
		l.diagnostics.refillTotal += d
		l.diagnostics.refillMax = maxDuration(l.diagnostics.refillMax, d)
		l.diagnostics.lastReplyReceivedAt = time.Time{}
	}
	l.diagnostics.stage = "peer_wait"
	l.publishSnapshotLocked()
	return id
}

// RecordRequestSendFailure rolls back an unobserved request reservation while
// preserving a reply that raced with an error return.
func (l *Ledger) RecordRequestSendFailure(peerID, requestID uint64) {
	if requestID == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var key acquisitionRequestKey
	found := false
	for candidate, trace := range l.diagnostics.outstanding {
		if candidate.peerID == peerID && trace.id == requestID {
			key = candidate
			found = true
			break
		}
	}
	if !found {
		return
	}
	delete(l.diagnostics.outstanding, key)
	l.diagnostics.sendFailures++
	peer := l.peerMetricsLocked(peerID)
	peer.sendFailures++
	l.diagnostics.stage = "request_refill"
	l.publishSnapshotLocked()
}

// BeginReplyDiagnostics records network and worker-queue delay and consumes at
// most one outstanding request for the replying peer.
func (l *Ledger) BeginReplyDiagnostics(peerID uint64, transaction bool, receivedNodes, receivedBytes, wireBytes int, receivedAt, processingAt time.Time) ReplyTrace {
	if receivedAt.IsZero() {
		receivedAt = processingAt
	}
	trace := ReplyTrace{PeerID: peerID}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.diagnostics.replies++
	l.diagnostics.receivedBytes += uint64(max(receivedBytes, 0))
	l.diagnostics.wireBytes += uint64(max(wireBytes, 0))
	queueDelay := nonNegativeDuration(processingAt.Sub(receivedAt))
	l.diagnostics.queueTotal += queueDelay
	l.diagnostics.queueMax = maxDuration(l.diagnostics.queueMax, queueDelay)
	l.diagnostics.lastReplyReceivedAt = receivedAt
	trace.QueueDelay = queueDelay
	peer := l.peerMetricsLocked(peerID)
	peer.replies++
	peer.returnedNodes += uint64(max(receivedNodes, 0))
	peer.receivedBytes += uint64(max(receivedBytes, 0))
	peer.wireBytes += uint64(max(wireBytes, 0))
	peer.lastReplyAt = receivedAt
	if receivedNodes == 0 {
		l.diagnostics.emptyReplies++
		peer.emptyReplies++
	}
	key := acquisitionRequestKey{peerID: peerID, transaction: transaction}
	if request, ok := l.diagnostics.outstanding[key]; ok {
		delete(l.diagnostics.outstanding, key)
		trace.RequestID = request.id
		trace.RequestedNodes = request.requestedNodes
		trace.QueryDepth = request.queryDepth
		trace.Transaction = request.transaction
		trace.Blind = request.blind
		trace.ResponseLatency = nonNegativeDuration(receivedAt.Sub(request.sentAt))
		peer.responseTotal += trace.ResponseLatency
		peer.responseMax = maxDuration(peer.responseMax, trace.ResponseLatency)
	} else {
		l.diagnostics.lateReplies++
		peer.lateReplies++
	}
	l.diagnostics.stage = "apply"
	l.publishSnapshotLocked()
	return trace
}

// FinishReplyDiagnostics records aggregate attachment results once per reply.
func (l *Ledger) FinishReplyDiagnostics(trace ReplyTrace, stats NodeApplyStats, applyDuration time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.diagnostics.usefulBytes += uint64(max(stats.UsefulBytes, 0))
	l.diagnostics.duplicateNodes += uint64(max(stats.DuplicateNodes, 0))
	l.diagnostics.reRequestNodes += uint64(max(stats.ReRequestNodes, 0))
	l.diagnostics.invalidNodes += uint64(max(stats.InvalidNodes, 0))
	l.diagnostics.unprocessed += uint64(max(stats.UnprocessedNodes, 0))
	l.diagnostics.applyTotal += applyDuration
	l.diagnostics.applyMax = maxDuration(l.diagnostics.applyMax, applyDuration)
	peer := l.peerMetricsLocked(trace.PeerID)
	peer.usefulNodes += uint64(max(stats.UsefulNodes, 0))
	peer.usefulBytes += uint64(max(stats.UsefulBytes, 0))
	peer.invalidNodes += uint64(max(stats.InvalidNodes, 0))
	l.addRecentRateLocked(SystemClock.Now(), uint64(max(stats.UsefulNodes, 0)), uint64(max(stats.UsefulBytes, 0)))
	l.diagnostics.stage = "frontier_walk"
	l.publishSnapshotLocked()
}

func (l *Ledger) RecordDecodeDuration(d time.Duration) {
	l.mu.Lock()
	l.diagnostics.decodeTotal += d
	l.diagnostics.decodeMax = maxDuration(l.diagnostics.decodeMax, d)
	l.publishSnapshotLocked()
	l.mu.Unlock()
}

func (l *Ledger) RecordWorkerSaturation() {
	l.mu.Lock()
	l.diagnostics.workerSaturation++
	l.diagnostics.stage = "worker_queue"
	l.publishSnapshotLocked()
	l.mu.Unlock()
}

func (l *Ledger) RecordFrontierWalk(d time.Duration) {
	l.mu.Lock()
	l.diagnostics.walkTotal += d
	l.diagnostics.walkMax = maxDuration(l.diagnostics.walkMax, d)
	l.diagnostics.stage = "request_refill"
	l.publishSnapshotLocked()
	l.mu.Unlock()
}

func (l *Ledger) clearOutstandingDiagnosticsLocked() {
	clear(l.diagnostics.outstanding)
}

func (l *Ledger) addRecentRateLocked(now time.Time, nodes, bytes uint64) {
	if nodes == 0 && bytes == 0 {
		return
	}
	epoch := now.UnixNano() / int64(acquisitionRateBucketDuration)
	i := epoch % acquisitionRateBucketCount
	if i < 0 {
		i += acquisitionRateBucketCount
	}
	bucket := &l.diagnostics.rate[i]
	if bucket.epoch != epoch {
		*bucket = acquisitionRateBucket{epoch: epoch}
	}
	bucket.usefulNodes += nodes
	bucket.usefulBytes += bytes
}

func (l *Ledger) diagnosticsSnapshotLocked(now time.Time) AcquisitionDiagnostics {
	d := AcquisitionDiagnostics{
		StartedAt:          l.diagnostics.startedAt,
		LastProgressAt:     l.diagnostics.lastProgressAt,
		Requests:           l.diagnostics.requests,
		SendFailures:       l.diagnostics.sendFailures,
		Replies:            l.diagnostics.replies,
		ReceivedBytes:      l.diagnostics.receivedBytes,
		WireBytes:          l.diagnostics.wireBytes,
		UsefulBytes:        l.diagnostics.usefulBytes,
		DuplicateNodes:     l.diagnostics.duplicateNodes,
		ReRequestNodes:     l.diagnostics.reRequestNodes,
		InvalidNodes:       l.diagnostics.invalidNodes,
		UnprocessedNodes:   l.diagnostics.unprocessed,
		EmptyReplies:       l.diagnostics.emptyReplies,
		LateReplies:        l.diagnostics.lateReplies,
		WorkerSaturation:   l.diagnostics.workerSaturation,
		OutstandingReplies: len(l.diagnostics.outstanding),
		DecodeTotal:        l.diagnostics.decodeTotal,
		DecodeMax:          l.diagnostics.decodeMax,
		WorkerQueueTotal:   l.diagnostics.queueTotal,
		WorkerQueueMax:     l.diagnostics.queueMax,
		ApplyTotal:         l.diagnostics.applyTotal,
		ApplyMax:           l.diagnostics.applyMax,
		FrontierWalkTotal:  l.diagnostics.walkTotal,
		FrontierWalkMax:    l.diagnostics.walkMax,
		RequestRefillTotal: l.diagnostics.refillTotal,
		RequestRefillMax:   l.diagnostics.refillMax,
		LimitingStage:      l.diagnostics.stage,
		Peers:              make([]PeerDiagnostics, 0, len(l.diagnostics.peers)),
	}
	if provider, ok := l.family.(interface {
		PersistenceStats() shamap.PersistenceStats
	}); ok {
		d.Persistence = provider.PersistenceStats()
		if d.Persistence.CurrentBytes >= d.Persistence.CapacityBytes && d.Persistence.CapacityBytes > 0 {
			d.LimitingStage = "persistence"
		}
	}
	for _, request := range l.diagnostics.outstanding {
		d.OutstandingNodes += request.requestedNodes
	}
	currentEpoch := now.UnixNano() / int64(acquisitionRateBucketDuration)
	for _, bucket := range l.diagnostics.rate {
		if bucket.epoch <= currentEpoch && currentEpoch-bucket.epoch < acquisitionRateBucketCount {
			d.RecentUsefulNodes += bucket.usefulNodes
			d.RecentUsefulBytes += bucket.usefulBytes
		}
	}
	d.RecentWindow = minDuration(nonNegativeDuration(now.Sub(l.diagnostics.startedAt)), acquisitionRateBucketDuration*acquisitionRateBucketCount)
	for peerID, peer := range l.diagnostics.peers {
		d.Peers = append(d.Peers, PeerDiagnostics{
			PeerID: peerID, Requests: peer.requests, SendFailures: peer.sendFailures,
			Replies: peer.replies, RequestedNodes: peer.requestedNodes,
			ReturnedNodes: peer.returnedNodes, UsefulNodes: peer.usefulNodes,
			ReceivedBytes: peer.receivedBytes, WireBytes: peer.wireBytes, UsefulBytes: peer.usefulBytes,
			EmptyReplies: peer.emptyReplies, LateReplies: peer.lateReplies,
			InvalidNodes: peer.invalidNodes, Disconnects: peer.disconnects,
			ResponseTotal: peer.responseTotal, ResponseMax: peer.responseMax,
			LastReplyAt: peer.lastReplyAt, LastQueryDepth: peer.lastQueryDepth,
			LastTransaction: peer.lastTransaction,
		})
	}
	sort.Slice(d.Peers, func(i, j int) bool { return d.Peers[i].PeerID < d.Peers[j].PeerID })
	return d
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func nonNegativeDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
