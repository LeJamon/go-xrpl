package adaptor

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func TestReplayHeadDiagnosticsBoundedAcrossHeadChanges(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	now := time.Now()
	first := inbound.New([32]byte{1}, 10, 7, logger)
	second := inbound.New([32]byte{2}, 11, 8, logger)
	c := &catchupReplayCoordinator{
		logger: logger,
		acquisition: &fixedLatencySender{latencies: map[uint64]time.Duration{
			7: 20 * time.Millisecond,
		}},
		standardReplay: standardReplayPipeline{
			active: true, pivotReady: true, anchorSeq: 9,
			entries: map[uint32]*standardReplayEntry{
				10: {seq: 10, acquisition: first, requestedAt: now.Add(-time.Second)},
				11: {seq: 11, acquisition: second, requestedAt: now, readyAt: now.Add(-time.Second)},
			},
		},
	}
	c.recordReplayHeadSend(second, inbound.MissingRequest{PeerID: 8, NodeIDs: make([][]byte, 100)}, time.Second, false)
	c.recordReplayHeadSend(first, inbound.MissingRequest{PeerID: 7, NodeIDs: make([][]byte, 5), Hedge: true}, time.Millisecond, false)
	c.recordReplayHeadWork(acquisitionWorkResult{
		ledger: first, requestWait: inbound.MissingRequestWaitReply,
		replies: []acquisitionReplyStat{{bytes: 100, queue: 20 * time.Millisecond, process: time.Millisecond}},
	})
	c.reportReplayHead(now)
	var record map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record))
	require.Equal(t, float64(10), record["seq"])
	require.Equal(t, float64(1), record["leased_requests"])
	require.Equal(t, float64(5), record["leased_request_nodes"])
	require.Equal(t, float64(1), record["hedge_requests"])
	require.Equal(t, float64(100), record["reply_payload_bytes"])
	require.Equal(t, float64(20*time.Millisecond), record["worker_queue_max"])
	require.Equal(t, "reply", record["planner_wait"])
	require.Equal(t, float64(1), record["ready_blocked"])
	require.Equal(t, float64(time.Second), record["ready_wait_max"])
	output.Reset()
	c.standardReplay.anchorSeq = 10
	c.recordReplayHeadSend(second, inbound.MissingRequest{PeerID: 8, NodeIDs: make([][]byte, 3)}, time.Millisecond, true)
	c.reportReplayHead(now.Add(time.Second))
	require.Empty(t, output.String(), "head changes must not bypass the log rate limit")
	c.reportReplayHead(now.Add(replayHeadDiagnosticInterval))
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record))
	require.Equal(t, float64(11), record["seq"])
	require.Equal(t, float64(1), record["leased_requests"])
	require.Equal(t, float64(1), record["leased_send_failures"])
	require.Equal(t, float64(0), record["reply_payload_bytes"])
	require.Equal(t, float64(0), record["hedge_requests"])
	output.Reset()
	c.standardReplay.active = false
	c.reportReplayHead(now.Add(2 * replayHeadDiagnosticInterval))
	require.Empty(t, output.String())
	require.Nil(t, c.replayHeadDiagnostics.ledger)
}

func TestAcquisitionReplyDiagnosticsDoNotCountResumedFrameTwice(t *testing.T) {
	ledger, replies := newWideWorkLedger(t)
	now := time.Now()
	events := []acquisitionWorkEvent{{
		kind: acquisitionWorkData, peerID: 1,
		queuedAt: now.Add(-time.Second),
		owner:    &peermanagement.InboundMessage{Payload: make([]byte, 100), WireSize: 50, ReceivedAt: now.Add(-2 * time.Second)},
		data:     &message.LedgerData{InfoType: message.LedgerInfoAsNode, Nodes: replies[:1]},
	}}
	result := processAcquisitionWork(t.Context(), ledger, events)
	require.NoError(t, result.err)
	require.Len(t, result.replies, 1)
	require.Equal(t, uint64(100), result.replies[0].bytes)
	require.Equal(t, uint64(50), result.replies[0].wire)
	require.Equal(t, time.Second, result.replies[0].ingress)
	require.GreaterOrEqual(t, result.replies[0].queue, time.Second)
	require.Positive(t, result.replies[0].process)
	result = processAcquisitionWork(t.Context(), ledger, events)
	require.NoError(t, result.err)
	require.Empty(t, result.replies)
}
