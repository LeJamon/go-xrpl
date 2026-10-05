package inbound

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type replyFixture struct {
	ledger *Ledger
	wire   map[string]message.LedgerNode
}

func newReplyFixture(t *testing.T, withTransactions bool) replyFixture {
	return newReplyFixtureWithAncestors(t, withTransactions, true)
}

func newReplyFixtureWithAncestors(t *testing.T, withTransactions, attachAncestors bool) replyFixture {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for first := byte(0); first < 1; first++ {
		for second := byte(0); second < 16; second++ {
			for third := byte(0); third < 16; third++ {
				for fourth := byte(0); fourth < 8; fourth++ {
					var key [32]byte
					key[0] = first<<4 | second
					key[1] = third<<4 | fourth
					key[31] = 0xA5
					value := make([]byte, 12)
					value[0] = first
					value[1] = second
					value[2] = third
					value[3] = fourth
					if err := source.Put(key, value); err != nil {
						t.Fatalf("put source leaf: %v", err)
					}
				}
			}
		}
	}
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	txHash := [32]byte{}
	if withTransactions {
		txHash = rootHash
	}
	headerData, ledgerHash := encodeHeader(header.LedgerHeader{
		LedgerIndex: 101,
		AccountHash: rootHash,
		TxHash:      txHash,
	})
	ledger := New(ledgerHash, 101, 11, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := []message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}
	if withTransactions {
		base = append(base, message.LedgerNode{NodeData: rootData})
	}
	require.NoError(t, ledger.GotBase(base))
	wireNodes, err := source.WalkWireNodes()
	require.NoError(t, err)
	if attachAncestors {
		ancestors := make([]message.LedgerNode, 0, len(wireNodes))
		for _, node := range wireNodes {
			parsed, parseErr := shamap.ParseNodeID(node.NodeID)
			require.NoError(t, parseErr)
			if parsed.Depth() >= 1 && parsed.Depth() <= 3 {
				ancestors = append(ancestors, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
			}
		}
		_, err = ledger.GotStateNodesUseful(ancestors)
		require.NoError(t, err)
		if withTransactions {
			_, err = ledger.GotTransactionNodesUseful(ancestors)
			require.NoError(t, err)
		}
	}
	wire := make(map[string]message.LedgerNode, len(wireNodes))
	for _, node := range wireNodes {
		wire[string(node.NodeID)] = message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data}
	}
	return replyFixture{ledger: ledger, wire: wire}
}

func replyWireNode(t *testing.T, wire map[string]message.LedgerNode, nodeID []byte) message.LedgerNode {
	t.Helper()
	node, ok := wire[string(nodeID)]
	require.True(t, ok, "source is missing requested NodeID %x", nodeID)
	return node
}

func TestApplyMissingReply_PartialReplyReleasesPeerAndRetainsSuffix(t *testing.T) {
	fixture := newReplyFixture(t, false)
	requests, complete, err := fixture.ledger.CollectMissingReplyRequestsContext(t.Context(), []uint64{11})
	require.NoError(t, err)
	require.False(t, complete)
	require.Len(t, requests, 1)
	require.GreaterOrEqual(t, len(requests[0].NodeIDs), 2)
	request := requests[0]

	result, err := fixture.ledger.ApplyMissingReplyContext(t.Context(), request.PeerID, false, []message.LedgerNode{
		replyWireNode(t, fixture.wire, request.NodeIDs[0]),
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Consumed)
	require.Equal(t, 1, result.Fulfilled)
	require.Equal(t, len(request.NodeIDs)-1, result.Unfulfilled)

	fixture.ledger.mu.Lock()
	require.NotContains(t, fixture.ledger.requestPeers, request.PeerID)
	for _, hash := range request.NodeHashes[1:] {
		_, queued := fixture.ledger.readySet[hash]
		_, leased := fixture.ledger.requestLeases[hash]
		require.True(t, queued || leased, "unreturned hash %x was dropped", hash[:8])
	}
	fixture.ledger.mu.Unlock()

	retry, complete, err := fixture.ledger.CollectMissingReplyRequestsContext(t.Context(), []uint64{22})
	require.NoError(t, err)
	require.False(t, complete)
	require.Len(t, retry, 1)
	require.NotContains(t, retry[0].NodeHashes, request.NodeHashes[0])
}

func TestApplyMissingReply_LateReplyFulfillsCurrentLeaseWithoutReleasingNewerPeer(t *testing.T) {
	fixture := newReplyFixture(t, false)
	requests, complete, err := fixture.ledger.CollectMissingReplyRequestsContext(t.Context(), []uint64{11})
	require.NoError(t, err)
	require.False(t, complete)
	require.Len(t, requests, 1)
	oldRequest := requests[0]
	require.GreaterOrEqual(t, len(oldRequest.NodeIDs), 2)

	newerRequest := MissingRequest{
		PeerID:     22,
		NodeIDs:    oldRequest.NodeIDs[:2],
		NodeHashes: oldRequest.NodeHashes[:2],
	}
	fixture.ledger.mu.Lock()
	for _, hash := range newerRequest.NodeHashes {
		lease, ok := fixture.ledger.requestLeases[hash]
		require.True(t, ok)
		lease.peerID = newerRequest.PeerID
		fixture.ledger.requestLeases[hash] = lease
		fixture.ledger.recentNodes[hash] = newerRequest.PeerID
	}
	fixture.ledger.requestPeers[newerRequest.PeerID] = struct{}{}
	fixture.ledger.mu.Unlock()

	result, err := fixture.ledger.ApplyMissingReplyContext(t.Context(), oldRequest.PeerID, false, []message.LedgerNode{
		replyWireNode(t, fixture.wire, oldRequest.NodeIDs[0]),
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Fulfilled,
		"a verified late node must fulfill the current NodeID/hash lease")

	fixture.ledger.mu.Lock()
	require.Contains(t, fixture.ledger.requestPeers, newerRequest.PeerID,
		"a late reply must not release the newer peer's send credit")
	remaining, ok := fixture.ledger.requestLeases[newerRequest.NodeHashes[1]]
	require.True(t, ok)
	require.Equal(t, newerRequest.PeerID, remaining.peerID)
	fixture.ledger.mu.Unlock()
}

func TestApplyMissingReply_NodeReRequestDoesNotFulfillLease(t *testing.T) {
	fixture := newReplyFixtureWithAncestors(t, false, false)
	var target message.LedgerNode
	var targetID shamap.NodeID
	for _, node := range fixture.wire {
		parsed, err := shamap.ParseNodeID(node.NodeID)
		require.NoError(t, err)
		if parsed.Depth() >= 2 {
			target, targetID = node, parsed
			break
		}
	}
	require.NotEmpty(t, target.NodeData)

	leaseHash := [32]byte{0xD1}
	fixture.ledger.mu.Lock()
	fixture.ledger.requestLeases[leaseHash] = missingRequestLease{
		peerID:  11,
		nodeID:  targetID,
		expires: time.Now().Add(missingRequestLeaseTTL),
	}
	fixture.ledger.requestByNode[requestNodeKey{nodeID: targetID}] = leaseHash
	fixture.ledger.recentNodes[leaseHash] = 11
	fixture.ledger.requestPeers[11] = struct{}{}
	fixture.ledger.mu.Unlock()

	result, err := fixture.ledger.ApplyMissingReplyContext(t.Context(), 11, false, []message.LedgerNode{target})
	require.NoError(t, err)
	require.Equal(t, 1, result.Consumed)
	require.Zero(t, result.Fulfilled)
	require.Equal(t, 1, result.Unfulfilled)

	fixture.ledger.mu.Lock()
	_, queued := fixture.ledger.readySet[leaseHash]
	_, leased := fixture.ledger.requestLeases[leaseHash]
	require.True(t, queued || leased, "NodeReRequest must remain retryable")
	fixture.ledger.mu.Unlock()
}

func TestApplyMissingReply_StateAndTransactionNodeIDsAreScoped(t *testing.T) {
	fixture := newReplyFixture(t, true)
	var target message.LedgerNode
	var targetID shamap.NodeID
	for _, node := range fixture.wire {
		parsed, err := shamap.ParseNodeID(node.NodeID)
		require.NoError(t, err)
		if parsed.Depth() >= 4 {
			target, targetID = node, parsed
			break
		}
	}
	require.NotEmpty(t, target.NodeData)

	stateHash := [32]byte{0xE1}
	txHash := [32]byte{0xE2}
	fixture.ledger.mu.Lock()
	fixture.ledger.requestLeases[stateHash] = missingRequestLease{
		peerID:  11,
		nodeID:  targetID,
		expires: time.Now().Add(missingRequestLeaseTTL),
	}
	fixture.ledger.requestLeases[txHash] = missingRequestLease{
		peerID:      11,
		transaction: true,
		nodeID:      targetID,
		expires:     time.Now().Add(missingRequestLeaseTTL),
	}
	fixture.ledger.requestByNode[requestNodeKey{nodeID: targetID}] = stateHash
	fixture.ledger.requestByNode[requestNodeKey{transaction: true, nodeID: targetID}] = txHash
	fixture.ledger.recentNodes[stateHash] = 11
	fixture.ledger.recentNodes[txHash] = 11
	fixture.ledger.requestPeers[11] = struct{}{}
	fixture.ledger.mu.Unlock()

	stateResult, err := fixture.ledger.ApplyMissingReplyContext(t.Context(), 11, false, []message.LedgerNode{target})
	require.NoError(t, err)
	require.Equal(t, 1, stateResult.Fulfilled)

	fixture.ledger.mu.Lock()
	_, stateLeaseStillPresent := fixture.ledger.requestLeases[stateHash]
	_, txLeaseStillPresent := fixture.ledger.requestLeases[txHash]
	require.False(t, stateLeaseStillPresent)
	require.True(t, txLeaseStillPresent)
	fixture.ledger.mu.Unlock()

	txResult, err := fixture.ledger.ApplyMissingReplyContext(t.Context(), 11, true, []message.LedgerNode{target})
	require.NoError(t, err)
	require.Equal(t, 1, txResult.Fulfilled)
	fixture.ledger.mu.Lock()
	_, txLeaseStillPresent = fixture.ledger.requestLeases[txHash]
	fixture.ledger.mu.Unlock()
	require.False(t, txLeaseStillPresent)
}
