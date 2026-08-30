package inbound

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionDiagnosticsCorrelateRequestReplyAndRefill(t *testing.T) {
	now := time.Now()
	ledger := New([32]byte{1}, 10, 1, nil)

	requestID := ledger.RecordRequestStart(1, 128, 2, false, false, now)
	require.NotZero(t, requestID)
	trace := ledger.BeginReplyDiagnostics(1, false, 2, 200, 120, now.Add(100*time.Millisecond), now.Add(110*time.Millisecond))
	assert.Equal(t, requestID, trace.RequestID)
	assert.Equal(t, 100*time.Millisecond, trace.ResponseLatency)
	assert.Equal(t, 10*time.Millisecond, trace.QueueDelay)

	ledger.FinishReplyDiagnostics(trace, NodeApplyStats{
		ReceivedNodes: 2, ReceivedBytes: 200, UsefulNodes: 1, UsefulBytes: 80, DuplicateNodes: 1,
	}, 5*time.Millisecond)
	ledger.RecordFrontierWalk(3 * time.Millisecond)
	secondID := ledger.RecordRequestStart(2, 64, 1, false, false, now.Add(150*time.Millisecond))

	snap := ledger.Snapshot().Diagnostics
	assert.Equal(t, uint64(2), snap.Requests)
	assert.Equal(t, uint64(1), snap.Replies)
	assert.Equal(t, uint64(200), snap.ReceivedBytes)
	assert.Equal(t, uint64(120), snap.WireBytes)
	assert.Equal(t, uint64(80), snap.UsefulBytes)
	assert.Equal(t, uint64(1), snap.DuplicateNodes)
	assert.Equal(t, 1, snap.OutstandingReplies)
	assert.Equal(t, 64, snap.OutstandingNodes)
	assert.Equal(t, 10*time.Millisecond, snap.WorkerQueueTotal)
	assert.Equal(t, 5*time.Millisecond, snap.ApplyTotal)
	assert.Equal(t, 3*time.Millisecond, snap.FrontierWalkTotal)
	assert.Equal(t, 50*time.Millisecond, snap.RequestRefillTotal)
	assert.Equal(t, "peer_wait", snap.LimitingStage)
	require.Len(t, snap.Peers, 2)
	assert.Equal(t, uint64(1), snap.Peers[0].UsefulNodes)

	ledger.RecordRequestSendFailure(2, secondID)
	snap = ledger.Snapshot().Diagnostics
	assert.Equal(t, uint64(1), snap.SendFailures)
	assert.Zero(t, snap.OutstandingReplies)
	assert.Equal(t, "request_refill", snap.LimitingStage)
}

func TestAcquisitionDiagnosticsClassifyLateEmptyReply(t *testing.T) {
	now := time.Now()
	ledger := New([32]byte{2}, 11, 7, nil)

	trace := ledger.BeginReplyDiagnostics(7, false, 0, 0, 0, now, now)
	ledger.FinishReplyDiagnostics(trace, NodeApplyStats{}, 0)

	snap := ledger.Snapshot().Diagnostics
	assert.Equal(t, uint64(1), snap.EmptyReplies)
	assert.Equal(t, uint64(1), snap.LateReplies)
	require.Len(t, snap.Peers, 1)
	assert.Equal(t, uint64(1), snap.Peers[0].EmptyReplies)
	assert.Equal(t, uint64(1), snap.Peers[0].LateReplies)
}

func TestAcquisitionDiagnosticsRecentRateExpires(t *testing.T) {
	now := time.Now()
	ledger := New([32]byte{3}, 12, 0, nil)
	ledger.mu.Lock()
	ledger.addRecentRateLocked(now, 5, 500)
	recent := ledger.diagnosticsSnapshotLocked(now.Add(time.Second))
	expired := ledger.diagnosticsSnapshotLocked(now.Add(61 * time.Second))
	ledger.mu.Unlock()

	assert.Equal(t, uint64(5), recent.RecentUsefulNodes)
	assert.Equal(t, uint64(500), recent.RecentUsefulBytes)
	assert.Zero(t, expired.RecentUsefulNodes)
	assert.Zero(t, expired.RecentUsefulBytes)
}

func TestAcquisitionJSONIncludesNestedDiagnostics(t *testing.T) {
	ledger := New([32]byte{4}, 13, 9, nil)
	ledger.RecordRequestStart(9, 12, 1, false, false, time.Now())

	entry := AcquisitionJSON(ledger.Snapshot())
	assert.Contains(t, entry, "state_received_total")
	assert.Contains(t, entry, "state_useful_total")
	diagnostics, ok := entry["diagnostics"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, uint64(1), diagnostics["requests_total"])
	assert.Equal(t, 1, diagnostics["outstanding_requests"])
	assert.Equal(t, "peer_wait", diagnostics["limiting_stage"])
	peers, ok := diagnostics["peers"].([]any)
	require.True(t, ok)
	require.Len(t, peers, 1)
}
