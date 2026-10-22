package inbound

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func seedTransactionOnlyTail(t *testing.T) (*Ledger, replyFixture, []shamap.MissingNode) {
	t.Helper()
	fixture := newReplyFixture(t, true)
	ledger := fixture.ledger
	ledger.transactionOnly = true
	ledger.haveState = true
	ledger.haveTx = false
	ledger.state = StateWantState

	missing := ledger.txMap.GetMissingNodes(txOnlyTailNodeLimit, nil)
	require.GreaterOrEqual(t, len(missing), 3)
	if len(missing) > txOnlyTailNodeLimit {
		missing = missing[:txOnlyTailNodeLimit]
	}
	now := time.Now()
	started := now.Add(-2 * txOnlyTailHedgeDelay)
	ledger.mu.Lock()
	ledger.missingTx = append([]shamap.MissingNode(nil), missing...)
	ledger.neededTx = missingHashes(missing)
	for _, node := range missing {
		ledger.requestLeases[node.Hash] = missingRequestLease{
			peerID:      11,
			transaction: true,
			nodeID:      node.NodeID,
			started:     started,
			expires:     now.Add(missingRequestLeaseTTL),
		}
		ledger.requestByNode[requestNodeKey{transaction: true, nodeID: node.NodeID}] = node.Hash
		ledger.recentNodes[node.Hash] = 11
	}
	ledger.requestPeers[11] = struct{}{}
	ledger.mu.Unlock()
	return ledger, fixture, missing
}

func seedTransactionOnlyFinalTail(t *testing.T) (*Ledger, replyFixture, []shamap.MissingNode) {
	t.Helper()
	stateRootHash, stateRoot, _ := buildSourceMap(t, shamap.TypeState)
	txRootHash, txRoot, txWire := buildSourceMap(t, shamap.TypeTransaction)
	headerData, ledgerHash := encodeHeader(header.LedgerHeader{
		LedgerIndex: 102,
		AccountHash: stateRootHash,
		TxHash:      txRootHash,
	})
	ledger := New(ledgerHash, 102, 11, discardLogger())
	require.NoError(t, ledger.GotBase([]message.LedgerNode{
		{NodeData: headerData},
		{NodeData: stateRoot},
		{NodeData: txRoot},
	}))
	wire := make(map[string]message.LedgerNode, len(txWire))
	for _, node := range txWire {
		wire[string(node.NodeID)] = node
	}
	fixture := replyFixture{ledger: ledger, wire: wire}
	leafNodes := make([]message.LedgerNode, 0)
	available := make([]message.LedgerNode, 0, len(fixture.wire))
	for _, node := range fixture.wire {
		decoded, err := shamap.DeserializeFromWire(node.NodeData)
		require.NoError(t, err)
		if decoded.Type() != shamap.NodeTypeInner {
			leafNodes = append(leafNodes, node)
			continue
		}
		available = append(available, node)
	}
	slices.SortFunc(available, func(a, b message.LedgerNode) int {
		aID, aErr := shamap.ParseNodeID(a.NodeID)
		bID, bErr := shamap.ParseNodeID(b.NodeID)
		if aErr != nil || bErr != nil || aID.Depth() == bID.Depth() {
			return bytes.Compare(a.NodeID, b.NodeID)
		}
		if aID.Depth() < bID.Depth() {
			return -1
		}
		return 1
	})
	slices.SortFunc(leafNodes, func(a, b message.LedgerNode) int {
		return bytes.Compare(a.NodeID, b.NodeID)
	})
	require.GreaterOrEqual(t, len(leafNodes), txOnlyTailNodeLimit)
	available = append(available, leafNodes[:len(leafNodes)-txOnlyTailNodeLimit]...)
	_, err := fixture.ledger.GotTransactionNodesUseful(available)
	require.NoError(t, err)

	ledger.transactionOnly = true
	ledger.haveState = true
	ledger.haveTx = false
	ledger.state = StateWantState
	missing := ledger.txMap.GetMissingNodes(txOnlyTailNodeLimit, nil)
	require.Len(t, missing, txOnlyTailNodeLimit)
	for _, node := range missing {
		wire, ok := fixture.wire[string(node.NodeID.Bytes())]
		require.True(t, ok)
		decoded, err := shamap.DeserializeFromWire(wire.NodeData)
		require.NoError(t, err)
		require.NotEqual(t, shamap.NodeTypeInner, decoded.Type())
	}
	now := time.Now()
	started := now.Add(-2 * txOnlyTailHedgeDelay)
	ledger.mu.Lock()
	ledger.missingTx = append([]shamap.MissingNode(nil), missing...)
	ledger.neededTx = missingHashes(missing)
	for _, node := range missing {
		ledger.requestLeases[node.Hash] = missingRequestLease{
			peerID:      11,
			transaction: true,
			nodeID:      node.NodeID,
			started:     started,
			expires:     now.Add(missingRequestLeaseTTL),
		}
		ledger.requestByNode[requestNodeKey{transaction: true, nodeID: node.NodeID}] = node.Hash
		ledger.recentNodes[node.Hash] = 11
	}
	ledger.requestPeers[11] = struct{}{}
	ledger.mu.Unlock()
	return ledger, fixture, missing
}

func TestTransactionOnlyTailHedgeTransfersBoundedLogicalLeases(t *testing.T) {
	ledger, fixture, missing := seedTransactionOnlyTail(t)
	now := time.Now()
	require.True(t, ledger.TxOnlyTailHedgeDue(now))

	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{11, 22, 33, 44}, now)
	require.NoError(t, err)
	require.Len(t, plan.Requests, txOnlyTailPeerLimit)
	require.Equal(t, MissingRequestWaitReply, plan.Outcome)

	seen := make(map[[32]byte]uint64, len(missing))
	for _, request := range plan.Requests {
		require.True(t, request.Hedge)
		require.True(t, request.Transaction)
		require.NotEqual(t, uint64(11), request.PeerID)
		for _, hash := range request.NodeHashes {
			if owner, duplicate := seen[hash]; duplicate {
				t.Fatalf("logical node %x assigned to %d and %d", hash[:8], owner, request.PeerID)
			}
			seen[hash] = request.PeerID
		}
	}
	require.Len(t, seen, len(missing))
	require.Len(t, ledger.requestLeases, len(missing), "hedging must transfer leases, not allocate duplicates")
	require.Len(t, ledger.requestPeers, 1+txOnlyTailPeerLimit,
		"the original wire request keeps its peer slot until a reply boundary")
	afterHedge := ledger.Snapshot()
	var hedgeCount uint32
	for _, peer := range afterHedge.RequestPeerSnapshots {
		require.Positive(t, peer.OutstandingNodes)
		require.NotZero(t, peer.OldestRequest)
		require.Equal(t, peer.OldestRequest, afterHedge.OldestRequest)
		if peer.PeerID != 11 {
			require.Positive(t, peer.Hedges)
			hedgeCount += peer.Hedges
		}
	}
	require.Equal(t, uint32(len(missing)), hedgeCount)
	require.Equal(t, uint32(len(missing)), afterHedge.Hedges)

	// A late reply from the original owner can still fulfill the transferred
	// logical lease without releasing either replacement peer's other leases.
	late := fixture.wire[string(missing[0].NodeID.Bytes())]
	result, err := ledger.ApplyMissingReplyContext(t.Context(), 11, true, []message.LedgerNode{late})
	require.NoError(t, err)
	require.Equal(t, 1, result.Fulfilled)
	require.Len(t, ledger.requestLeases, len(missing), "the alternate wire frame remains charged after a late original reply")
	require.NotContains(t, ledger.requestPeers, uint64(11), "late reply releases the old wire slot")
	for _, request := range plan.Requests {
		require.Contains(t, ledger.requestPeers, request.PeerID)
	}
	ledger.mu.Lock()
	shadow, ok := ledger.requestLeases[missing[0].Hash]
	require.True(t, ok)
	require.True(t, shadow.wireShadow)
	require.Equal(t, plan.Requests[0].PeerID, shadow.peerID)
	require.Zero(t, shadow.previousPeerID)
	ledger.mu.Unlock()

	// The per-node hedge budget prevents a fast caller from creating an
	// unbounded duplicate stream while the first hedge is still in flight.
	require.False(t, ledger.TxOnlyTailHedgeDue(now.Add(txOnlyTailHedgeDelay)))
	second, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{44, 55, 66}, now.Add(txOnlyTailHedgeDelay))
	require.NoError(t, err)
	require.Empty(t, second.Requests)
}

func TestTransactionOnlyTailHedgeInnerReplyRetainsOriginalWireSlot(t *testing.T) {
	fixture := newReplyFixtureWithAncestors(t, true, false)
	ledger := fixture.ledger
	ledger.transactionOnly = true
	ledger.haveState = true
	ledger.haveTx = false
	ledger.state = StateWantState
	missing := ledger.txMap.GetMissingNodes(missingNodesFind, nil)
	var target message.LedgerNode
	var targetID shamap.NodeID
	var targetMissing shamap.MissingNode
	for _, node := range missing {
		candidate, ok := fixture.wire[string(node.NodeID.Bytes())]
		if !ok {
			continue
		}
		decoded, err := shamap.DeserializeFromWire(candidate.NodeData)
		require.NoError(t, err)
		if decoded.Type() == shamap.NodeTypeInner {
			target, targetID, targetMissing = candidate, node.NodeID, node
			break
		}
	}
	require.NotEqual(t, shamap.MissingNode{}, targetMissing)
	now := time.Now()
	started := now.Add(-2 * txOnlyTailHedgeDelay)
	ledger.mu.Lock()
	ledger.missingTx = []shamap.MissingNode{targetMissing}
	ledger.neededTx = missingHashes(ledger.missingTx)
	hash := targetMissing.Hash
	ledger.requestLeases[hash] = missingRequestLease{
		peerID:      11,
		transaction: true,
		nodeID:      targetID,
		started:     started,
		expires:     now.Add(missingRequestLeaseTTL),
	}
	ledger.requestByNode[requestNodeKey{transaction: true, nodeID: targetID}] = hash
	ledger.recentNodes[hash] = 11
	ledger.requestPeers[11] = struct{}{}
	ledger.mu.Unlock()

	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{11, 22}, now)
	require.NoError(t, err)
	require.Len(t, plan.Requests, 1)
	require.Equal(t, uint64(22), plan.Requests[0].PeerID)

	result, err := ledger.ApplyMissingReplyContext(t.Context(), 22, true, []message.LedgerNode{target})
	require.NoError(t, err)
	require.Equal(t, 1, result.Fulfilled)
	require.False(t, result.Complete)

	ledger.mu.Lock()
	shadow, ok := ledger.requestLeases[hash]
	require.True(t, ok)
	require.True(t, shadow.wireShadow)
	require.Equal(t, uint64(11), shadow.previousPeerID)
	require.Contains(t, ledger.requestPeers, uint64(11))
	require.NotContains(t, ledger.requestByNode, requestNodeKey{transaction: true, nodeID: targetID})
	ledger.mu.Unlock()

	next, err := ledger.PlanMissingReplyRequestsContext(t.Context(), []uint64{11, 33})
	require.NoError(t, err)
	require.Len(t, next.Requests, 1)
	require.Equal(t, uint64(33), next.Requests[0].PeerID,
		"a child frontier must not reuse the original peer while its wire frame is outstanding")

	ledger.ReleaseMissingPeer(11)
	ledger.mu.Lock()
	require.NotContains(t, ledger.requestPeers, uint64(11))
	_, shadowed := ledger.requestLeases[hash]
	require.False(t, shadowed)
	ledger.mu.Unlock()
}

func TestTransactionOnlyTailHedgeCachedReadyRetainsOriginalWireSlot(t *testing.T) {
	ledger, _, _ := seedTransactionOnlyTail(t)
	now := time.Now()
	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{11, 22}, now)
	require.NoError(t, err)
	require.Len(t, plan.Requests, 1)
	ledger.ReleaseMissingRequest(plan.Requests[0].PeerID, plan.Requests[0].NodeHashes)

	ledger.mu.Lock()
	var ready readyMissingNode
	for _, candidate := range ledger.readyRequests {
		if candidate.transaction && candidate.previousPeerID == 11 {
			ready = candidate
			break
		}
	}
	require.NotEqual(t, readyMissingNode{}, ready)
	ledger.fulfillCacheNodeLocked(true, ready.node.Hash)
	shadow, ok := ledger.requestLeases[ready.node.Hash]
	require.True(t, ok)
	require.True(t, shadow.wireShadow)
	require.Equal(t, uint64(11), shadow.previousPeerID)
	require.NotContains(t, ledger.readySet, ready.node.Hash)
	require.Contains(t, ledger.requestPeers, uint64(11))
	ledger.mu.Unlock()

	ledger.ReleaseMissingPeer(11)
	ledger.mu.Lock()
	_, retained := ledger.requestLeases[ready.node.Hash]
	require.False(t, retained)
	require.NotContains(t, ledger.requestPeers, uint64(11))
	ledger.mu.Unlock()
}

func TestTransactionOnlyTailHedgeCachedActiveRetainsBothWireSlots(t *testing.T) {
	ledger, _, _ := seedTransactionOnlyTail(t)
	now := time.Now()
	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{11, 22}, now)
	require.NoError(t, err)
	require.Len(t, plan.Requests, 1)
	hash := plan.Requests[0].NodeHashes[0]

	ledger.mu.Lock()
	ledger.fulfillCacheNodeLocked(true, hash)
	shadow, ok := ledger.requestLeases[hash]
	require.True(t, ok)
	require.True(t, shadow.wireShadow)
	require.Contains(t, ledger.requestPeers, uint64(11))
	require.Contains(t, ledger.requestPeers, uint64(22))
	ledger.mu.Unlock()

	ledger.ReleaseMissingPeer(22)
	ledger.mu.Lock()
	require.Contains(t, ledger.requestPeers, uint64(11))
	require.NotContains(t, ledger.requestPeers, uint64(22))
	ledger.mu.Unlock()
	ledger.ReleaseMissingPeer(11)
	ledger.mu.Lock()
	_, retained := ledger.requestLeases[hash]
	require.False(t, retained)
	ledger.mu.Unlock()
}

func TestTransactionOnlyTailHedgeFailedReplacementRetainsOriginalSlot(t *testing.T) {
	ledger, _, missing := seedTransactionOnlyTail(t)
	now := time.Now()
	ledger.mu.Lock()
	originalExpires := ledger.requestLeases[missing[0].Hash].expires
	ledger.mu.Unlock()
	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{11, 22}, now)
	require.NoError(t, err)
	require.Len(t, plan.Requests, 1)
	ledger.mu.Lock()
	require.Equal(t, originalExpires, ledger.requestLeases[missing[0].Hash].previousExpires)
	ledger.mu.Unlock()

	failed := plan.Requests[0]
	ledger.ReleaseMissingRequest(failed.PeerID, failed.NodeHashes)

	ledger.mu.Lock()
	require.NotContains(t, ledger.requestPeers, failed.PeerID)
	require.Contains(t, ledger.requestPeers, uint64(11))
	require.Empty(t, ledger.requestLeases)
	require.Len(t, ledger.readyRequests, len(missing))
	node, ok := ledger.claimReadyNodeLocked(true, 33, now.Add(10*time.Millisecond), false)
	require.True(t, ok)
	lease := ledger.requestLeases[node.Hash]
	ledger.refreshRequestPeersLocked()
	require.Equal(t, uint64(33), lease.peerID)
	require.Equal(t, uint64(11), lease.previousPeerID)
	require.Contains(t, ledger.requestPeers, uint64(11), "the original request remains in flight")
	require.Contains(t, ledger.requestPeers, uint64(33))
	ledger.mu.Unlock()
}

func TestTransactionOnlyTailHedgeExpiredLeaseIsNotTransferred(t *testing.T) {
	ledger, _, _ := seedTransactionOnlyTail(t)
	now := time.Now()
	ledger.mu.Lock()
	for hash, lease := range ledger.requestLeases {
		lease.expires = now.Add(-time.Nanosecond)
		ledger.requestLeases[hash] = lease
	}
	ledger.mu.Unlock()

	require.False(t, ledger.TxOnlyTailHedgeDue(now))
	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{22}, now)
	require.NoError(t, err)
	require.Empty(t, plan.Requests)
}

func TestTransactionOnlyTailHedgeReadyOverflowRetainsOriginalUntilExpiry(t *testing.T) {
	ledger, _, missing := seedTransactionOnlyTail(t)
	now := time.Now()
	ledger.mu.Lock()
	originalExpires := ledger.requestLeases[missing[0].Hash].expires
	for i := 0; i < missingRequestReadyReserve; i++ {
		var hash [32]byte
		hash[0] = 0xf0
		hash[31] = byte(i)
		nodeID, err := shamap.NewNodeID(1, hash)
		require.NoError(t, err)
		ready := readyMissingNode{node: shamap.MissingNode{Hash: hash, NodeID: nodeID}}
		ledger.readyRequests = append(ledger.readyRequests, ready)
		ledger.readySet[hash] = struct{}{}
	}
	ledger.mu.Unlock()

	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{11, 22}, now)
	require.NoError(t, err)
	require.Len(t, plan.Requests, 1)
	ledger.ReleaseMissingRequest(plan.Requests[0].PeerID, plan.Requests[0].NodeHashes)

	ledger.mu.Lock()
	lease := ledger.requestLeases[missing[0].Hash]
	require.Zero(t, lease.peerID)
	require.Equal(t, uint64(11), lease.previousPeerID)
	require.Equal(t, originalExpires, lease.previousExpires)
	ledger.expireMissingLeasesLocked(originalExpires.Add(-time.Nanosecond), true)
	require.Contains(t, ledger.requestLeases, missing[0].Hash,
		"an overflow shadow lease survives forced retry cleanup until the original request expires")
	ledger.mu.Unlock()

	ledger.mu.Lock()
	ledger.removeReadyLocked(ledger.readyRequests[0].node.Hash)
	ledger.mu.Unlock()
	ledger.ReleaseUnreservedMissingNodes()
	ledger.mu.Lock()
	require.Contains(t, ledger.requestPeers, uint64(11))
	var reclaimed bool
	for _, ready := range ledger.readyRequests {
		if ready.previousPeerID == 11 {
			reclaimed = true
			break
		}
	}
	require.True(t, reclaimed, "ready reclamation retains the original wire owner")
	ledger.mu.Unlock()
}

func TestTransactionOnlyTailHedgeRespectsFullPeerWindow(t *testing.T) {
	ledger, _, _ := seedTransactionOnlyTail(t)
	now := time.Now()
	started := now.Add(-2 * txOnlyTailHedgeDelay)
	ledger.mu.Lock()
	for i, peerID := range []uint64{21, 22, 23, 24, 25} {
		var hash [32]byte
		hash[0] = byte(i + 1)
		hash[31] = byte(i + 2)
		nodeID, err := shamap.NewNodeID(1, hash)
		require.NoError(t, err)
		ledger.requestLeases[hash] = missingRequestLease{
			peerID:  peerID,
			nodeID:  nodeID,
			started: started,
			expires: now.Add(missingRequestLeaseTTL),
		}
	}
	ledger.refreshRequestPeersAtLocked(now)
	require.Len(t, ledger.requestPeers, requestPeerLimit)
	ledger.mu.Unlock()

	require.True(t, ledger.TxOnlyTailHedgeDue(now))
	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{33, 34}, now)
	require.NoError(t, err)
	require.Empty(t, plan.Requests, "hedging cannot consume a seventh wire slot")
}

type tailRecoverySample struct {
	recovery           time.Duration
	requests           int
	requestNodeIDBytes int
}

func simulateTailRecovery(t *testing.T, originalDelay time.Duration, repaired bool) tailRecoverySample {
	t.Helper()
	ledger, fixture, missing := seedTransactionOnlyFinalTail(t)
	base := time.Now()
	ledger.mu.Lock()
	ledger.lastTimer = base
	for hash, lease := range ledger.requestLeases {
		lease.started = base
		lease.expires = base.Add(missingRequestLeaseTTL)
		ledger.requestLeases[hash] = lease
	}
	ledger.mu.Unlock()

	sample := tailRecoverySample{requests: 1}
	reply := make([]message.LedgerNode, 0, len(missing))
	for _, node := range missing {
		sample.requestNodeIDBytes += len(node.NodeID.Bytes())
		reply = append(reply, fixture.wire[string(node.NodeID.Bytes())])
	}
	originalReplyAt := base.Add(originalDelay + 100*time.Millisecond)
	var alternateReplyAt time.Time
	const originalPeer = uint64(11)
	const alternatePeer = uint64(22)
	for elapsed := 100 * time.Millisecond; elapsed <= 8*time.Second; elapsed += 100 * time.Millisecond {
		now := base.Add(elapsed)
		responseAt, responsePeer := originalReplyAt, originalPeer
		if !alternateReplyAt.IsZero() && alternateReplyAt.Before(responseAt) {
			responseAt, responsePeer = alternateReplyAt, alternatePeer
		}
		if !responseAt.After(now) {
			result, err := ledger.ApplyMissingReplyContext(t.Context(), responsePeer, true, reply)
			require.NoError(t, err)
			if !result.Complete {
				for pass := 0; pass < 8 && !result.Complete; pass++ {
					_, nodeIDs, complete, scanErr := ledger.CollectMissingRequestContext(t.Context(), true)
					require.NoError(t, scanErr)
					if complete {
						result.Complete = true
						break
					}
					require.Empty(t, nodeIDs, "all missing leaves must arrive in the timed reply")
				}
			}
			require.Truef(t, result.Complete, "delay=%s response=%s peer=%d", originalDelay, responseAt.Sub(base), responsePeer)
			require.Truef(t, ledger.IsComplete(), "delay=%s response=%s peer=%d", originalDelay, responseAt.Sub(base), responsePeer)
			sample.recovery = responseAt.Sub(base)
			return sample
		}
		if repaired && ledger.TxOnlyTailHedgeDue(now) {
			plan, err := ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{alternatePeer}, now)
			require.NoError(t, err)
			require.Len(t, plan.Requests, 1)
			for _, request := range plan.Requests {
				sample.requests++
				for _, nodeID := range request.NodeIDs {
					sample.requestNodeIDBytes += len(nodeID)
				}
			}
			alternateReplyAt = now.Add(100 * time.Millisecond)
			continue
		}
		if !repaired && ledger.TimerDue(now) {
			if action := ledger.OnTimer(now); action != TimerNone {
				plan, err := ledger.PlanMissingReplyRequestsContext(t.Context(), []uint64{alternatePeer})
				require.NoError(t, err)
				require.Len(t, plan.Requests, 1)
				for _, request := range plan.Requests {
					sample.requests++
					for _, nodeID := range request.NodeIDs {
						sample.requestNodeIDBytes += len(nodeID)
					}
				}
				alternateReplyAt = now.Add(100 * time.Millisecond)
			}
		}
	}
	t.Fatalf("tail did not recover by simulated 8s")
	return sample
}

func durationPercentile(samples []time.Duration, percentile int) time.Duration {
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	index := (len(sorted)*percentile+99)/100 - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func TestTransactionOnlyTailRecoverySimulation(t *testing.T) {
	// Each sample advances a simulated clock in 100 ms steps. The baseline
	// invokes the real OnTimer path at its three-second cadence; the repaired
	// path invokes the bounded tail planner at its one-second due point. No
	// wall-clock latency claim is made by this simulation.
	delays := []time.Duration{
		400 * time.Millisecond,
		900 * time.Millisecond,
		1100 * time.Millisecond,
		2 * time.Second,
		2500 * time.Millisecond,
		4 * time.Second,
		8 * time.Second,
	}
	baselineRecovery := make([]time.Duration, 0, len(delays))
	repairedRecovery := make([]time.Duration, 0, len(delays))
	var baselineRequests, repairedRequests, baselineBytes, repairedBytes int
	for _, delay := range delays {
		before := simulateTailRecovery(t, delay, false)
		after := simulateTailRecovery(t, delay, true)
		baselineRecovery = append(baselineRecovery, before.recovery)
		repairedRecovery = append(repairedRecovery, after.recovery)
		baselineRequests += before.requests
		repairedRequests += after.requests
		baselineBytes += before.requestNodeIDBytes
		repairedBytes += after.requestNodeIDBytes
	}
	require.Less(t, durationPercentile(repairedRecovery, 50), durationPercentile(baselineRecovery, 50))
	require.Less(t, durationPercentile(repairedRecovery, 95), durationPercentile(baselineRecovery, 95))
	require.Less(t, durationPercentile(repairedRecovery, 99), durationPercentile(baselineRecovery, 99))
	require.LessOrEqual(t, repairedRequests, len(repairedRecovery)*2,
		"one alternate request is the maximum simulated request amplification")
	require.LessOrEqual(t, repairedBytes, baselineBytes*2,
		"one alternate request is the maximum simulated request NodeID-byte amplification")
	t.Logf("simulated event time only; samples=%d baseline p50/p95/p99=%s/%s/%s repaired p50/p95/p99=%s/%s/%s requests=%d/%d request_nodeid_bytes=%d/%d",
		len(baselineRecovery),
		durationPercentile(baselineRecovery, 50), durationPercentile(baselineRecovery, 95), durationPercentile(baselineRecovery, 99),
		durationPercentile(repairedRecovery, 50), durationPercentile(repairedRecovery, 95), durationPercentile(repairedRecovery, 99),
		baselineRequests, repairedRequests, baselineBytes, repairedBytes)
}

func TestTransactionOnlyTailHedgePreservesCancellationAndFullState(t *testing.T) {
	ledger, _, _ := seedTransactionOnlyTail(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := ledger.Snapshot()
	plan, err := ledger.PlanTransactionOnlyTailRequestsContext(ctx, []uint64{22, 33}, time.Now())
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, plan.Requests)
	require.Equal(t, before.RequestPeerSnapshots, ledger.Snapshot().RequestPeerSnapshots)

	full := newReplyFixture(t, true).ledger
	require.False(t, full.TxOnlyTailHedgeDue(time.Now().Add(2*txOnlyTailHedgeDelay)),
		"full-state acquisitions must never enter tx-only tail hedging")
	require.NoError(t, ledger.RetirePersistence(t.Context()))
	plan, err = ledger.PlanTransactionOnlyTailRequestsContext(t.Context(), []uint64{22}, time.Now())
	require.ErrorIs(t, err, errAcquisitionCanceled)
	require.Empty(t, plan.Requests)
}

func TestTransactionOnlyTailHedgeRequiresSmallFrontier(t *testing.T) {
	ledger, _, missing := seedTransactionOnlyTail(t)
	now := time.Now()
	started := now.Add(-2 * txOnlyTailHedgeDelay)
	ledger.mu.Lock()
	for len(missing) <= txOnlyTailNodeLimit {
		var hash [32]byte
		hash[0] = byte(len(missing) + 1)
		hash[31] = 0x7f
		nodeID, err := shamap.NewNodeID(1, hash)
		require.NoError(t, err)
		node := shamap.MissingNode{Hash: hash, NodeID: nodeID}
		missing = append(missing, node)
		ledger.missingTx = append(ledger.missingTx, node)
		ledger.requestLeases[hash] = missingRequestLease{
			peerID:      11,
			transaction: true,
			nodeID:      nodeID,
			started:     started,
			expires:     now.Add(missingRequestLeaseTTL),
		}
		ledger.requestByNode[requestNodeKey{transaction: true, nodeID: nodeID}] = hash
		ledger.recentNodes[hash] = 11
	}
	ledger.neededTx = missingHashes(ledger.missingTx)
	ledger.mu.Unlock()
	require.Greater(t, len(ledger.missingTx), txOnlyTailNodeLimit)
	require.False(t, ledger.TxOnlyTailHedgeDue(now), "large tx frontiers stay on the normal planner")
}

func TestSnapshotIncludesBoundedRequestPeerSummaries(t *testing.T) {
	ledger, _, missing := seedTransactionOnlyTail(t)
	snapshot := ledger.Snapshot()
	require.Len(t, snapshot.RequestPeerSnapshots, 1)
	require.Equal(t, uint64(11), snapshot.RequestPeerSnapshots[0].PeerID)
	require.Equal(t, len(missing), snapshot.RequestPeerSnapshots[0].OutstandingNodes)
	require.Equal(t, snapshot.RequestPeerSnapshots[0].OldestRequest, snapshot.OldestRequest)
	require.NotZero(t, snapshot.OldestRequest)

	// Snapshot copies must not expose mutable backing storage.
	snapshot.RequestPeerSnapshots[0].PeerID = 99
	require.Equal(t, uint64(11), ledger.Snapshot().RequestPeerSnapshots[0].PeerID)
}
