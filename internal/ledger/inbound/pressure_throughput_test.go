package inbound

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

const pressureLeaves = 8192

type delayedPressureReply struct {
	at      time.Duration
	request MissingRequest
}

type pressureThroughput struct {
	nodes      int
	requests   int
	elapsed    time.Duration
	cursorPeak uint64
}

func newPressureAcquisition(t *testing.T, limit uint64) (*Ledger, map[string]message.LedgerNode, [32]byte, *shamap.AcquisitionBudget) {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for i := range pressureLeaves {
		var data [16]byte
		copy(data[:], "pressure")
		binary.BigEndian.PutUint64(data[8:], uint64(i))
		key := sha256.Sum256(data[:])
		require.NoError(t, source.Put(key, data[:]))
	}
	root, err := source.Hash()
	require.NoError(t, err)
	family := backend.NewMemory()
	require.NoError(t, source.StoreDirty(func(entries []shamap.FlushEntry) error {
		inners := make([]shamap.FlushEntry, 0, len(entries))
		for _, entry := range entries {
			node, err := shamap.DeserializeFromPrefix(entry.Data)
			if err != nil {
				return err
			}
			if _, inner := node.(shamap.InnerNodeReader); inner {
				inners = append(inners, entry)
			}
		}
		return family.StoreBatch(t.Context(), inners)
	}))
	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	nodes := make(map[string]message.LedgerNode, len(wire))
	for _, node := range wire {
		nodes[string(node.NodeID)] = message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data}
	}
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 2043, AccountHash: root})
	budget := shamap.NewAcquisitionBudget(limit)
	ledger := New(hash, 2043, 1, discardLogger(), WithFamily(family), WithAcquisitionBudget(budget))
	require.NoError(t, ledger.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: rootData}}))
	return ledger, nodes, root, budget
}

func runDelayedPressure(t *testing.T, limit uint64) pressureThroughput {
	t.Helper()
	ledger, wire, root, budget := newPressureAcquisition(t, limit)
	t.Cleanup(func() { require.NoError(t, ledger.RetirePersistence(context.Background())) })
	excluded := make(missingNodeExclusionFilter)
	for pass := 0; pass < 128; pass++ {
		missing, err := ledger.stateMap.GetMissingNodesContext(shamap.WithTraversalBudget(t.Context(), 8192), 256, excluded)
		for _, node := range missing {
			excluded[node.Hash] = struct{}{}
		}
		require.True(t, err == nil || errors.Is(err, shamap.ErrTraversalBudget), "prime frontier: %v", err)
		if len(excluded) == pressureLeaves {
			break
		}
	}
	require.Len(t, excluded, pressureLeaves, "discovery must cover the frontier within its fixed cursor budget")
	require.Zero(t, ledger.stateMap.BackedWalkStats().BlockedItems)
	peers := []uint64{11, 12, 13, 14, 15, 16}
	rtts := [...]int{200, 80, 160, 100, 180, 120}
	queue := make([]delayedPressureReply, 0, len(peers))
	outstanding := make(map[[32]byte]uint64)
	metrics := pressureThroughput{}
	checkBudget := func() {
		stats := budget.Snapshot()
		require.LessOrEqual(t, stats.Used, stats.Limit)
		require.LessOrEqual(t, stats.CursorPeak, stats.Limit/4)
		metrics.cursorPeak = stats.CursorPeak
		require.LessOrEqual(t, len(queue), requestPeerLimit)
		require.LessOrEqual(t, len(outstanding), requestPeerLimit*reqNodesReply)
	}
	collect := func(now time.Duration, initial bool) {
		requests, complete, err := ledger.CollectMissingReplyRequestsContext(shamap.WithTraversalBudget(t.Context(), 8192), peers)
		require.True(t, err == nil || errors.Is(err, shamap.ErrAcquisitionMemory) || errors.Is(err, shamap.ErrTraversalBudget), "collect: %v", err)
		if initial {
			require.Equal(t, len(peers), len(requests), "initial collect: %v", err)
		}
		for _, request := range requests {
			if initial {
				require.Equal(t, reqNodesReply, len(request.NodeIDs), "initial saturated batch; collect error=%v", err)
			}
			require.NotEmpty(t, request.NodeIDs)
			require.LessOrEqual(t, len(request.NodeIDs), reqNodesReply)
			for _, hash := range request.NodeHashes {
				require.NotContains(t, outstanding, hash, "two peers own the same node")
				outstanding[hash] = request.PeerID
			}
			queue = append(queue, delayedPressureReply{at: now + time.Duration(rtts[request.PeerID-11])*time.Millisecond, request: request})
			metrics.requests++
		}
		if complete {
			require.Equal(t, pressureLeaves, metrics.nodes)
		}
		checkBudget()
	}
	collect(0, true)
	for steps := 0; !ledger.IsComplete() && steps < 512; steps++ {
		if len(queue) == 0 {
			collect(metrics.elapsed, false)
			if ledger.IsComplete() {
				break
			}
			require.NotEmpty(t, queue, "acquisition stalled without a pending reply: %+v", ledger.Snapshot())
		}
		slices.SortStableFunc(queue, func(a, b delayedPressureReply) int {
			if a.at < b.at {
				return -1
			}
			if a.at > b.at {
				return 1
			}
			return 0
		})
		reply := queue[0]
		queue = queue[1:]
		metrics.elapsed = reply.at
		nodes := make([]message.LedgerNode, 0, len(reply.request.NodeIDs))
		for i, id := range reply.request.NodeIDs {
			node, ok := wire[string(id)]
			require.True(t, ok)
			nodes = append(nodes, node)
			delete(outstanding, reply.request.NodeHashes[i])
		}
		if steps%2 == 0 {
			slices.Reverse(nodes)
		}
		useful, err := ledger.GotStateNodesUsefulContext(t.Context(), nodes)
		require.NoError(t, err)
		require.Equal(t, len(nodes), useful)
		metrics.nodes += useful
		ledger.ReleaseMissingPeer(reply.request.PeerID)
		require.NoError(t, ledger.CheckpointMemory(t.Context()))
		collect(metrics.elapsed, false)
	}
	require.True(t, ledger.IsComplete(), "delayed replies failed to converge")
	require.Equal(t, pressureLeaves, metrics.nodes)
	require.Empty(t, outstanding)
	_, state, _, err := ledger.Result()
	require.NoError(t, err)
	got, err := state.Hash()
	require.NoError(t, err)
	require.Equal(t, root, got)
	require.NoError(t, ledger.RetirePersistence(t.Context()))
	require.Zero(t, budget.Snapshot().Used)
	return metrics
}

func TestCollectMissingReplyRequests_DelayedRepliesSurvivePressure(t *testing.T) {
	pressure := runDelayedPressure(t, 8<<20)
	control := runDelayedPressure(t, 512<<20)
	pressureRate := float64(pressure.nodes) / pressure.elapsed.Seconds()
	controlRate := float64(control.nodes) / control.elapsed.Seconds()
	require.GreaterOrEqual(t, pressureRate, controlRate/2, "pressure throughput collapsed")
	require.GreaterOrEqual(t, float64(pressure.nodes)/float64(pressure.requests), float64(reqNodesReply)/2, "underfilled request stream")
	t.Logf("simulated 80–200ms RTT: pressure=%0.1f nodes/s %d requests %.1f IDs/request cursor_peak=%d; control=%0.1f nodes/s %d requests", pressureRate, pressure.requests, float64(pressure.nodes)/float64(pressure.requests), pressure.cursorPeak, controlRate, control.requests)
}
