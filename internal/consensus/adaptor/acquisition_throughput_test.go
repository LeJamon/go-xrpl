package adaptor

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

const (
	acquisitionThroughputLeafCount          = 65_536
	acquisitionThroughputBudget             = 8 << 20
	acquisitionThroughputVisitSlice   int64 = 2 * 1024
	acquisitionThroughputPeers              = 6
	acquisitionThroughputBatch              = 128
	acquisitionThroughputSimulatedRTT       = 20 * time.Millisecond
	acquisitionThroughputReserve            = acquisitionThroughputPeers*acquisitionThroughputBatch + acquisitionThroughputBatch
)

type acquisitionThroughputFixture struct {
	root          [32]byte
	wireByID      map[string]message.LedgerNode
	descendants   map[string][]message.LedgerNode
	withheld      map[[32]byte]struct{}
	withheldInner int
	withheldLeaf  int
	ledger        *inbound.Ledger
	budget        *shamap.AcquisitionBudget
}

type acquisitionThroughputDelivery struct {
	at     time.Duration
	peerID uint64
	hashes [][32]byte
	nodes  []message.LedgerNode
}

type acquisitionThroughputFlight struct {
	peerID    uint64
	hashes    [][32]byte
	nodeCount int
	consumed  int
}

type acquisitionThroughputMetrics struct {
	simulatedElapsed     time.Duration
	requests             int
	requestedIDs         int
	requestWidthSum      int64
	fullWidthRequests    int
	replies              int
	received             int
	useful               int
	maxRequestWidth      int
	maxOutstanding       int
	maxRequestPeers      int
	peakBudget           uint64
	continuationFollowup int
}

func TestAcquisitionThroughput_MissingInnerDelayedRepliesKeepWindowFull(t *testing.T) {
	fixture := newAcquisitionThroughputFixture(t)
	ledger := fixture.ledger
	peers := ledger.Peers()
	require.Len(t, peers, acquisitionThroughputPeers)
	require.GreaterOrEqual(t, fixture.withheldInner, acquisitionThroughputPeers)
	require.Equal(t, acquisitionThroughputLeafCount, fixture.withheldLeaf)
	require.Greater(t, fixture.withheldInner+fixture.withheldLeaf, acquisitionThroughputLeafCount)

	lane := newAcquisitionWorkLane(1)
	lane.process = func(ctx context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		return processAcquisitionWorkWithBudget(ctx, current, events, acquisitionThroughputVisitSlice)
	}
	persistenceFlushes := 0
	lane.flush = func(ctx context.Context, current *inbound.Ledger) error {
		persistenceFlushes++
		return current.FlushPersistence(ctx)
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)

	metrics := acquisitionThroughputMetrics{}
	deliveries := make([]acquisitionThroughputDelivery, 0, acquisitionThroughputPeers)
	outstanding := make(map[[32]byte]uint64, acquisitionThroughputReserve)
	requestOrdinal := 0
	continuationPending := false
	var flights []acquisitionThroughputFlight
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkContinue}))

	for step := 0; step < 20_000; step++ {
		var result acquisitionWorkResult
		select {
		case result = <-lane.results():
		case <-time.After(time.Second):
			t.Fatalf("acquisition stopped responding at step %d: %+v", step, ledger.Snapshot())
		}
		require.NoError(t, result.err)
		require.NoError(t, result.persistenceErr)
		for _, reply := range result.replies {
			require.NotEmpty(t, flights, "reply arrived without a submitted delivery")
			flight := &flights[0]
			require.Equal(t, flight.peerID, reply.peerID)
			flight.consumed += reply.received
			if flight.consumed < flight.nodeCount && result.pendingReply {
				break
			}
			for _, hash := range flight.hashes {
				delete(outstanding, hash)
			}
			flights = flights[1:]
		}

		if continuationPending && result.ledger == ledger {
			metrics.continuationFollowup++
			continuationPending = false
		}
		if result.continueRequests {
			// A continuation must re-enter discovery before the virtual network
			// delivers a reply. The next result observed by this loop proves that
			// a partial CPU slice did not turn into a reply-only wait.
			continuationPending = true
		}

		for _, reply := range result.replies {
			metrics.replies++
			metrics.received += reply.received
			metrics.useful += reply.useful
		}
		for requestIndex, request := range result.requests {
			require.False(t, request.Transaction, "the fixture intentionally has no transaction tree")
			require.NotEmpty(t, request.NodeIDs)
			require.Len(t, request.NodeIDs, len(request.NodeHashes))
			require.LessOrEqual(t, len(request.NodeIDs), acquisitionThroughputBatch)
			metrics.requests++
			metrics.maxRequestWidth = max(metrics.maxRequestWidth, len(request.NodeIDs))
			if len(request.NodeIDs) == acquisitionThroughputBatch {
				metrics.fullWidthRequests++
			}
			metrics.requestedIDs += len(request.NodeIDs)
			metrics.requestWidthSum += int64(len(request.NodeIDs))
			for nodeIndex, hash := range request.NodeHashes {
				if owner, exists := outstanding[hash]; exists {
					t.Fatalf("node %x assigned to peers %d and %d at step=%d request=%d request_index=%d node_index=%d flights=%d deliveries=%d", hash[:4], owner, request.PeerID, step, requestOrdinal, requestIndex, nodeIndex, len(flights), len(deliveries))
				}
				outstanding[hash] = request.PeerID
			}
			deliveries = append(deliveries, fixture.deliveries(request, requestOrdinal, metrics.simulatedElapsed)...)
			requestOrdinal++
		}
		metrics.maxOutstanding = max(metrics.maxOutstanding, len(outstanding))

		snapshot := ledger.Snapshot()
		metrics.maxRequestPeers = max(metrics.maxRequestPeers, snapshot.RequestPeers)
		metrics.peakBudget = max(metrics.peakBudget, snapshot.AcquisitionMemory.Peak)
		require.LessOrEqual(t, snapshot.RequestPeers, acquisitionThroughputPeers)
		require.LessOrEqual(t, len(outstanding), acquisitionThroughputReserve)
		require.LessOrEqual(t, snapshot.AcquisitionMemory.Used, snapshot.AcquisitionMemory.Limit)

		if result.complete {
			close(result.ack)
			require.True(t, ledger.IsComplete())
			break
		}

		// Keep discovery continuations ahead of virtual replies. This is the
		// scheduling edge that previously converted a partial result into a
		// reply-only wait and left disjoint work behind the first blocked lane.
		if !result.continueRequests && len(deliveries) > 0 {
			slices.SortStableFunc(deliveries, func(a, b acquisitionThroughputDelivery) int {
				if a.at < b.at {
					return -1
				}
				if a.at > b.at {
					return 1
				}
				return 0
			})
			delivery := deliveries[0]
			deliveries = deliveries[1:]
			metrics.simulatedElapsed = delivery.at
			require.True(t, lane.submit(ledger, acquisitionWorkEvent{
				kind:   acquisitionWorkData,
				peerID: delivery.peerID,
				data: &message.LedgerData{
					InfoType: message.LedgerInfoAsNode,
					Nodes:    delivery.nodes,
				},
			}))
			flights = append(flights, acquisitionThroughputFlight{
				peerID: delivery.peerID, hashes: delivery.hashes, nodeCount: len(delivery.nodes),
			})
		} else if !result.continueRequests && !result.yielded && len(deliveries) == 0 {
			t.Fatalf("acquisition stalled after step %d: %+v", step, snapshot)
		}
		close(result.ack)
	}
	lane.stop()

	final := ledger.Snapshot()
	require.True(t, final.Complete, "acquisition did not complete: %+v", final)
	require.GreaterOrEqual(t, persistenceFlushes, 1, "completion was reported without the persistence barrier")
	require.Empty(t, flights, "completion left an unprocessed reply")
	require.Empty(t, outstanding, "completion left request leases in the virtual network")
	require.GreaterOrEqual(t, metrics.maxRequestPeers, 2, "disjoint work must occupy multiple peers")
	require.GreaterOrEqual(t, metrics.fullWidthRequests, 2, "request window never stayed wide")
	require.GreaterOrEqual(t, metrics.continuationFollowup, 1, "partial CPU yield was not followed by discovery")
	require.GreaterOrEqual(t, metrics.requestedIDs, acquisitionThroughputBatch*4, "request planner did not sustain enough frontier work")
	meanRequestWidth := float64(metrics.requestWidthSum) / float64(max(1, metrics.requests))
	fullWidthRatio := float64(metrics.fullWidthRequests) / float64(max(1, metrics.requests))
	require.GreaterOrEqual(t, meanRequestWidth, 64.0, "request batches collapsed under pressure")
	require.GreaterOrEqual(t, fullWidthRatio, 0.10, "full-width requests were not sustained")
	require.LessOrEqual(t, metrics.maxOutstanding, acquisitionThroughputReserve)
	require.LessOrEqual(t, metrics.peakBudget, uint64(acquisitionThroughputBudget))
	require.Greater(t, metrics.useful, 0)

	headerResult, state, tx, err := ledger.Result()
	require.NoError(t, err)
	require.NotNil(t, headerResult)
	require.Nil(t, tx)
	gotRoot, err := state.Hash()
	require.NoError(t, err)
	require.Equal(t, fixture.root, gotRoot)
	require.NoError(t, ledger.FlushPersistence(t.Context()))
	require.NoError(t, ledger.RetirePersistence(t.Context()))
	require.Zero(t, fixture.budget.Snapshot().Used)
	require.LessOrEqual(t, fixture.budget.Snapshot().Peak, fixture.budget.Snapshot().Limit)

	reads := final.StateDurableReads
	usefulPerReply := float64(metrics.useful) / float64(max(1, metrics.replies))
	t.Logf("missing-inner baseline_candidate leaves=%d withheld_inner=%d withheld_leaf=%d requests=%d requested_ids=%d mean_request_width=%.1f full_width_ratio=%.2f replies=%d useful=%d useful/reply=%.1f durable_reads=%d simulated_rtt=%s simulated_elapsed=%s max_request_width=%d full_width_requests=%d max_peer_occupancy=%d max_outstanding=%d budget_peak=%d", fixture.withheldLeaf, fixture.withheldInner, fixture.withheldLeaf, metrics.requests, metrics.requestedIDs, meanRequestWidth, fullWidthRatio, metrics.replies, metrics.useful, usefulPerReply, reads, acquisitionThroughputSimulatedRTT, metrics.simulatedElapsed, metrics.maxRequestWidth, metrics.fullWidthRequests, metrics.maxRequestPeers, metrics.maxOutstanding, metrics.peakBudget)
}

func newAcquisitionThroughputFixture(t *testing.T) *acquisitionThroughputFixture {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for first := byte(0); first < 16; first++ {
		for second := byte(0); second < 16; second++ {
			for third := byte(0); third < 16; third++ {
				for fourth := byte(0); fourth < 16; fourth++ {
					var key [32]byte
					key[0] = first<<4 | second
					key[1] = third<<4 | fourth
					key[31] = 0xA5
					data := []byte{first, second, third, fourth, 0xA5, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77}
					require.NoError(t, source.Put(key, data))
				}
			}
		}
	}

	root, err := source.Hash()
	require.NoError(t, err)
	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	fixture := &acquisitionThroughputFixture{
		root:        root,
		wireByID:    make(map[string]message.LedgerNode, len(wire)),
		descendants: make(map[string][]message.LedgerNode),
		withheld:    make(map[[32]byte]struct{}),
	}
	for _, node := range wire {
		reader, readErr := shamap.DeserializeFromWire(node.Data)
		require.NoError(t, readErr)
		ledgerNode := message.LedgerNode{NodeID: slices.Clone(node.NodeID), NodeData: slices.Clone(node.Data)}
		fixture.wireByID[string(node.NodeID)] = ledgerNode
		depth := int(node.NodeID[len(node.NodeID)-1])
		if depth < 3 {
			continue
		}
		fixture.withheld[reader.Hash()] = struct{}{}
		if reader.Type() == shamap.NodeTypeInner {
			fixture.withheldInner++
		} else {
			fixture.withheldLeaf++
		}
	}
	for _, node := range wire {
		depth := int(node.NodeID[len(node.NodeID)-1])
		for parentDepth := 3; parentDepth < depth; parentDepth++ {
			key := acquisitionThroughputPathKey(node.NodeID, parentDepth)
			fixture.descendants[key] = append(fixture.descendants[key], fixture.wireByID[string(node.NodeID)])
		}
	}
	require.Equal(t, acquisitionThroughputLeafCount, fixture.withheldLeaf)
	require.Greater(t, fixture.withheldInner, acquisitionThroughputPeers)

	base := backend.NewMemory()
	var stored []shamap.FlushEntry
	require.NoError(t, source.StoreDirty(func(entries []shamap.FlushEntry) error {
		for _, entry := range entries {
			if _, missing := fixture.withheld[entry.Hash]; !missing {
				stored = append(stored, entry)
			}
		}
		return nil
	}))
	require.NoError(t, base.StoreBatch(t.Context(), stored))

	h := header.LedgerHeader{LedgerIndex: 2043, AccountHash: root}
	headerData := header.AddRaw(h, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	fixture.budget = shamap.NewAcquisitionBudget(acquisitionThroughputBudget)
	peers := []uint64{11, 12, 13, 14, 15, 16}
	fixture.ledger = inbound.New(ledgerHash, h.LedgerIndex, peers[0], serveTestLogger(),
		inbound.WithFamily(base), inbound.WithAcquisitionBudget(fixture.budget))
	for _, peerID := range peers[1:] {
		require.True(t, fixture.ledger.AddPeer(peerID))
	}
	require.NoError(t, fixture.ledger.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: mustSerializeRoot(t, source)}}))
	require.NoError(t, fixture.ledger.FlushPersistence(t.Context()))
	return fixture
}

func (f *acquisitionThroughputFixture) deliveries(request inbound.MissingRequest, ordinal int, now time.Duration) []acquisitionThroughputDelivery {
	indices := make([]int, len(request.NodeIDs))
	for i := range indices {
		indices[i] = i
	}
	if ordinal%3 != 1 || len(indices) < 2 {
		return []acquisitionThroughputDelivery{f.deliveryPart(request, indices, ordinal, 0, now)}
	}
	even := make([]int, 0, (len(indices)+1)/2)
	odd := make([]int, 0, len(indices)/2)
	for _, index := range indices {
		if index%2 == 0 {
			even = append(even, index)
		} else {
			odd = append(odd, index)
		}
	}
	return []acquisitionThroughputDelivery{
		f.deliveryPart(request, even, ordinal, 0, now),
		f.deliveryPart(request, odd, ordinal, 1, now),
	}
}

func (f *acquisitionThroughputFixture) deliveryPart(request inbound.MissingRequest, indices []int, ordinal, part int, now time.Duration) acquisitionThroughputDelivery {
	nodes := make([]message.LedgerNode, 0, len(indices)*2)
	hashes := make([][32]byte, 0, len(indices))
	for _, index := range indices {
		node := f.wireByID[string(request.NodeIDs[index])]
		nodes = append(nodes, cloneAcquisitionThroughputNode(node))
		hashes = append(hashes, request.NodeHashes[index])
		if ordinal%3 == 0 || (ordinal%3 == 1 && part == 0) {
			descendants := f.descendants[acquisitionThroughputPathKey(node.NodeID, int(node.NodeID[len(node.NodeID)-1]))]
			for _, descendant := range descendants {
				nodes = append(nodes, cloneAcquisitionThroughputNode(descendant))
			}
		}
	}
	if ordinal%2 == 0 {
		slices.Reverse(nodes)
	}
	if ordinal%5 == 0 && len(nodes) != 0 {
		nodes = append(nodes, cloneAcquisitionThroughputNode(nodes[0]))
	}
	at := now + acquisitionThroughputSimulatedRTT + time.Duration(part)*time.Millisecond
	return acquisitionThroughputDelivery{at: at, peerID: request.PeerID, hashes: hashes, nodes: nodes}
}

func cloneAcquisitionThroughputNode(node message.LedgerNode) message.LedgerNode {
	return message.LedgerNode{NodeID: slices.Clone(node.NodeID), NodeData: slices.Clone(node.NodeData)}
}

func acquisitionThroughputPathKey(nodeID []byte, depth int) string {
	if depth <= 0 {
		return string([]byte{0})
	}
	pathBytes := (depth + 1) / 2
	key := make([]byte, pathBytes+1)
	copy(key, nodeID[:pathBytes])
	if depth%2 != 0 {
		key[pathBytes-1] &= 0xf0
	}
	key[pathBytes] = byte(depth)
	return string(key)
}

func mustSerializeRoot(t *testing.T, source *shamap.SHAMap) []byte {
	t.Helper()
	root, err := source.SerializeRoot()
	require.NoError(t, err)
	return root
}
