package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type controlledAcquisitionResult struct {
	requests    int
	replies     int
	usefulNodes int
	usefulBytes int
	invalid     int
	rerequests  int
	unprocessed int
	peerReplies map[uint64]int
}

func TestControlledStateAcquisitionKeepsPeerStreamsProductive(t *testing.T) {
	peers := []uint64{101, 102, 103, 104, 105, 106}
	result := runControlledStateAcquisition(t, 4, peers)

	assert.Greater(t, result.usefulNodes, 2_000)
	assert.Greater(t, result.usefulBytes, 0)
	for _, peerID := range peers {
		assert.Greater(t, result.peerReplies[peerID], 0, "peer %d received no productive refill", peerID)
	}
}

func BenchmarkControlledStateAcquisition(b *testing.B) {
	peers := []uint64{101, 102, 103, 104, 105, 106}
	source := newControlledAcquisitionSource(b, 16)
	leafCount := source.Size()
	b.ReportAllocs()
	b.ResetTimer()
	var result controlledAcquisitionResult
	for i := 0; i < b.N; i++ {
		result = runControlledStateAcquisitionFromSource(b, source, leafCount, peers)
	}
	b.StopTimer()
	b.ReportMetric(float64(result.requests), "requests/op")
	b.ReportMetric(float64(result.replies), "replies/op")
	b.ReportMetric(float64(result.usefulNodes), "useful-nodes/op")
	b.ReportMetric(float64(result.usefulBytes), "useful-bytes/op")
}

func runControlledStateAcquisition(tb testing.TB, firstBranches byte, peers []uint64) controlledAcquisitionResult {
	tb.Helper()
	source := newControlledAcquisitionSource(tb, firstBranches)
	return runControlledStateAcquisitionFromSource(tb, source, source.Size(), peers)
}

func runControlledStateAcquisitionFromSource(tb testing.TB, source *shamap.SHAMap, leafCount int, peers []uint64) controlledAcquisitionResult {
	tb.Helper()
	require.NotEmpty(tb, peers)
	ledger, base := newControlledAcquisitionLedger(tb, source, peers)

	result := controlledAcquisitionResult{peerReplies: make(map[uint64]int, len(peers))}
	consecutiveUnproductive := 0
	work := processAcquisitionWork(tb.Context(), ledger, []acquisitionWorkEvent{{
		kind:   acquisitionWorkData,
		peerID: peers[0],
		data:   &message.LedgerData{InfoType: message.LedgerInfoBase, Nodes: base},
	}})
	require.NoError(tb, work.err)
	for round := 0; ; round++ {
		beforeUseful := result.usefulNodes
		collectControlledReplyStats(&result, work.replies)
		if len(work.replies) > 0 && result.usefulNodes == beforeUseful {
			consecutiveUnproductive++
		} else if result.usefulNodes > beforeUseful {
			consecutiveUnproductive = 0
		}
		require.Less(tb, consecutiveUnproductive, 3, "controlled acquisition repeated unproductive replies at round %d: %+v", round, work.replies)
		if work.complete {
			break
		}
		require.Less(tb, round, leafCount*4+1, "controlled acquisition made no bounded progress")
		if len(work.requests) == 0 {
			work = processAcquisitionWork(tb.Context(), ledger, []acquisitionWorkEvent{{kind: acquisitionWorkTimer, peers: peers}})
			require.NoError(tb, work.err)
			if work.complete {
				break
			}
			if len(work.stateIDs) > 0 {
				work.requests = []inbound.MissingRequest{{
					PeerID:  peers[round%len(peers)],
					NodeIDs: work.stateIDs,
				}}
			}
		}
		require.NotEmpty(tb, work.requests, "incomplete acquisition produced no requests at round %d", round)
		result.requests += len(work.requests)
		events := make([]acquisitionWorkEvent, 0, len(work.requests))
		for _, request := range work.requests {
			queryDepth := 1
			if request.Blind {
				queryDepth = 0
			}
			nodes := buildShaMapReplyNodes(source, request.NodeIDs, queryDepth, true, serveTestLogger(), peermanagement.PeerID(request.PeerID), "controlled state")
			require.NotEmpty(tb, nodes, "controlled source could not serve peer %d", request.PeerID)
			payloadBytes := 0
			wireBytes := 0
			for _, node := range nodes {
				payloadBytes += len(node.NodeData)
				wireBytes += len(node.NodeID) + len(node.NodeData)
			}
			now := time.Now()
			ledger.RecordRequestStart(request.PeerID, len(request.NodeIDs), uint32(queryDepth), false, request.Blind, now)
			events = append(events, acquisitionWorkEvent{
				kind:         acquisitionWorkData,
				peerID:       request.PeerID,
				data:         &message.LedgerData{InfoType: message.LedgerInfoAsNode, Nodes: nodes},
				receivedAt:   now,
				payloadBytes: payloadBytes,
				wireBytes:    wireBytes,
			})
		}
		work = processAcquisitionWork(tb.Context(), ledger, events)
		require.NoError(tb, work.err)
	}
	require.True(tb, work.complete, "state=%v useful=%d leaves=%d invalid=%d rerequests=%d unprocessed=%d pending_state=%d", ledger.State(), result.usefulNodes, leafCount, result.invalid, result.rerequests, result.unprocessed, len(work.stateIDs))
	require.True(tb, ledger.IsComplete())
	return result
}

func collectControlledReplyStats(result *controlledAcquisitionResult, replies []acquisitionReplyStat) {
	for _, reply := range replies {
		result.replies++
		result.usefulNodes += reply.useful
		result.usefulBytes += reply.usefulBytes
		result.invalid += reply.invalid
		result.rerequests += reply.rerequests
		result.unprocessed += reply.unprocessed
		if reply.useful > 0 {
			result.peerReplies[reply.peerID]++
		}
	}
}

func newControlledAcquisitionSource(tb testing.TB, firstBranches byte) *shamap.SHAMap {
	tb.Helper()
	source := shamap.New(shamap.TypeState)
	for first := byte(0); first < firstBranches; first++ {
		for second := byte(0); second < 16; second++ {
			for third := byte(0); third < 16; third++ {
				for fourth := byte(0); fourth < 2; fourth++ {
					var key [32]byte
					key[0] = first<<4 | second
					key[1] = third<<4 | fourth
					if key == ([32]byte{}) {
						continue
					}
					data := []byte{first, second, third, fourth, 4, 5, 6, 7, 8, 9, 10, 11}
					require.NoError(tb, source.Put(key, data))
				}
			}
		}
	}
	return source
}

func newControlledAcquisitionLedger(tb testing.TB, source *shamap.SHAMap, peers []uint64) (*inbound.Ledger, []message.LedgerNode) {
	tb.Helper()
	rootHash, err := source.Hash()
	require.NoError(tb, err)
	rootData, err := source.SerializeRoot()
	require.NoError(tb, err)
	h := header.LedgerHeader{LedgerIndex: 200, AccountHash: rootHash}
	headerData := header.AddRaw(h, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	ledger := inbound.New(ledgerHash, h.LedgerIndex, peers[0], serveTestLogger())
	for _, peerID := range peers[1:] {
		require.True(tb, ledger.AddPeer(peerID))
	}
	return ledger, []message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}
}
