package adaptor

import (
	"errors"
	"sync"
	"testing"

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

type missingNodeTarget struct {
	peerID      uint64
	transaction bool
}

type missingNodeSendCall struct {
	missingNodeTarget
	nodeIDs    [][]byte
	queryDepth uint32
}

type missingNodeChurnSender struct {
	noopSender
	mu           sync.Mutex
	errs         map[missingNodeTarget]error
	replacements []uint64
	calls        []missingNodeSendCall
}

func (s *missingNodeChurnSender) RequestStateNodes(
	peerID uint64,
	_ [32]byte,
	nodeIDs [][]byte,
	queryDepth uint32,
	_ bool,
) error {
	return s.record(peerID, false, nodeIDs, queryDepth)
}

func (s *missingNodeChurnSender) RequestTransactionNodes(
	peerID uint64,
	_ [32]byte,
	nodeIDs [][]byte,
	queryDepth uint32,
	_ bool,
) error {
	return s.record(peerID, true, nodeIDs, queryDepth)
}

func (s *missingNodeChurnSender) record(
	peerID uint64,
	transaction bool,
	nodeIDs [][]byte,
	queryDepth uint32,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := missingNodeTarget{peerID: peerID, transaction: transaction}
	s.calls = append(s.calls, missingNodeSendCall{
		missingNodeTarget: target,
		nodeIDs:           append([][]byte(nil), nodeIDs...),
		queryDepth:        queryDepth,
	})
	return s.errs[target]
}

func (s *missingNodeChurnSender) SelectLedgerPeers(
	_ [32]byte,
	_ uint32,
	excluded []uint64,
	maxPeers int,
) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	skip := make(map[uint64]struct{}, len(excluded))
	for _, peerID := range excluded {
		skip[peerID] = struct{}{}
	}
	selected := make([]uint64, 0, maxPeers)
	for _, peerID := range s.replacements {
		if _, excluded := skip[peerID]; excluded {
			continue
		}
		selected = append(selected, peerID)
		if len(selected) == maxPeers {
			break
		}
	}
	return selected
}

func (s *missingNodeChurnSender) snapshotCalls() []missingNodeSendCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]missingNodeSendCall(nil), s.calls...)
}

func newMissingNodeChurnRouter(t *testing.T, sender *missingNodeChurnSender) *Router {
	t.Helper()
	adaptor := newTestAdaptor(t)
	adaptor.sender = sender
	return NewRouter(nil, adaptor, nil)
}

func newDisconnectFrontier(
	t *testing.T,
	transaction bool,
) (*inbound.Ledger, []message.LedgerNode) {
	t.Helper()
	mapType := shamap.TypeState
	if transaction {
		mapType = shamap.TypeTransaction
	}
	source := shamap.New(mapType)
	for i, prefix := range []byte{0x10, 0xE0} {
		var key [32]byte
		key[0] = prefix
		key[31] = byte(i + 1)
		data := make([]byte, 12)
		data[0] = byte(i + 1)
		data[1] = 0xA5
		require.NoError(t, source.Put(key, data))
	}
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	ledgerHeader := header.LedgerHeader{LedgerIndex: 200}
	baseNodes := []message.LedgerNode{{}}
	var stateReplies []message.LedgerNode
	if transaction {
		state := shamap.New(shamap.TypeState)
		for i, prefix := range []byte{0x20, 0xD0} {
			var stateKey [32]byte
			stateKey[0] = prefix
			stateKey[31] = byte(i + 1)
			require.NoError(t, state.Put(stateKey, make([]byte, 12)))
		}
		stateHash, err := state.Hash()
		require.NoError(t, err)
		stateRoot, err := state.SerializeRoot()
		require.NoError(t, err)
		stateWire, err := state.WalkWireNodes()
		require.NoError(t, err)
		for _, node := range stateWire {
			if node.NodeID[32] > 0 {
				stateReplies = append(stateReplies, message.LedgerNode{
					NodeID: node.NodeID, NodeData: node.Data,
				})
			}
		}
		ledgerHeader.AccountHash = stateHash
		ledgerHeader.TxHash = rootHash
		baseNodes = append(baseNodes,
			message.LedgerNode{NodeData: stateRoot},
			message.LedgerNode{NodeData: rootData},
		)
	} else {
		ledgerHeader.AccountHash = rootHash
		baseNodes = append(baseNodes, message.LedgerNode{NodeData: rootData})
	}
	headerData := header.AddRaw(ledgerHeader, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	ledger := inbound.New(ledgerHash, ledgerHeader.LedgerIndex, 1, serveTestLogger())
	baseNodes[0].NodeData = headerData
	require.NoError(t, ledger.GotBase(baseNodes))
	require.Equal(t, inbound.StateWantState, ledger.State())
	if transaction {
		require.NoError(t, ledger.GotStateNodes(stateReplies))
		_, _, complete, err := ledger.CollectMissingRequestContext(t.Context(), false)
		require.NoError(t, err)
		require.False(t, complete)
	}

	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	replies := make([]message.LedgerNode, 0, len(wire)-1)
	for _, node := range wire {
		if node.NodeID[32] == 0 {
			continue
		}
		replies = append(replies, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}
	require.NotEmpty(t, replies)
	return ledger, replies
}

func TestAcquisitionWorkResult_RecoversStaleTimerTargets(t *testing.T) {
	t.Run("all stale retargets exact batch", func(t *testing.T) {
		sender := &missingNodeChurnSender{
			errs: map[missingNodeTarget]error{
				{peerID: 1}: peermanagement.ErrPeerNotFound,
			},
			replacements: []uint64{2},
		}
		router := newMissingNodeChurnRouter(t, sender)
		ledger, _ := newDisconnectFrontier(t, false)
		router.fetchTracker.Track(ledger)
		trackCatchupPeer(router, 1, ledger.Seq())
		stateIDs, _, complete, err := ledger.CollectMissingRequestContext(t.Context(), false)
		require.NoError(t, err)
		require.False(t, complete)
		require.NotEmpty(t, stateIDs)

		router.handleAcquisitionWorkResult(acquisitionWorkResult{
			ledger:   ledger,
			targets:  []uint64{1},
			stateIDs: stateIDs,
		})

		calls := sender.snapshotCalls()
		require.Len(t, calls, 2)
		assert.Equal(t, []uint64{1, 2}, []uint64{calls[0].peerID, calls[1].peerID})
		assert.Equal(t, calls[0].nodeIDs, calls[1].nodeIDs)
		assert.NotContains(t, ledger.Peers(), uint64(1))
		assert.Contains(t, ledger.Peers(), uint64(2))
		router.peersMu.RLock()
		_, stale := router.peerStates[1]
		router.peersMu.RUnlock()
		assert.False(t, stale)
	})

	t.Run("mixed fanout keeps live delivery", func(t *testing.T) {
		sender := &missingNodeChurnSender{
			errs: map[missingNodeTarget]error{
				{peerID: 1}: peermanagement.ErrConnectionClosed,
			},
		}
		router := newMissingNodeChurnRouter(t, sender)
		ledger, _ := newDisconnectFrontier(t, false)
		require.True(t, ledger.AddPeer(2))
		router.fetchTracker.Track(ledger)
		stateIDs, _, complete, err := ledger.CollectMissingRequestContext(t.Context(), false)
		require.NoError(t, err)
		require.False(t, complete)

		router.handleAcquisitionWorkResult(acquisitionWorkResult{
			ledger:   ledger,
			targets:  []uint64{1, 2},
			stateIDs: stateIDs,
		})

		calls := sender.snapshotCalls()
		require.Len(t, calls, 2)
		assert.Equal(t, []uint64{1, 2}, []uint64{calls[0].peerID, calls[1].peerID})
		assert.Equal(t, calls[0].nodeIDs, calls[1].nodeIDs)
		assert.NotContains(t, ledger.Peers(), uint64(1))
		assert.Contains(t, ledger.Peers(), uint64(2))
	})
}

func TestMissingReplyRequest_DisconnectErrorsReleaseAndRetarget(t *testing.T) {
	tests := []struct {
		name        string
		transaction bool
		err         error
	}{
		{name: "state peer not found", err: peermanagement.ErrPeerNotFound},
		{name: "transaction connection closed", transaction: true, err: peermanagement.ErrConnectionClosed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := missingNodeTarget{peerID: 1, transaction: tc.transaction}
			sender := &missingNodeChurnSender{
				errs:         map[missingNodeTarget]error{target: tc.err},
				replacements: []uint64{2},
			}
			router := newMissingNodeChurnRouter(t, sender)
			ledger, _ := newDisconnectFrontier(t, tc.transaction)
			router.fetchTracker.Track(ledger)
			trackCatchupPeer(router, 1, ledger.Seq())
			requests, complete, err := ledger.CollectMissingReplyRequestsContext(t.Context(), []uint64{1})
			require.NoError(t, err)
			require.False(t, complete)
			require.Len(t, requests, 1)
			require.Equal(t, tc.transaction, requests[0].Transaction)

			router.handleAcquisitionWorkResult(acquisitionWorkResult{
				ledger:   ledger,
				requests: requests,
			})

			calls := sender.snapshotCalls()
			require.Len(t, calls, 2)
			assert.Equal(t, []uint64{1, 2}, []uint64{calls[0].peerID, calls[1].peerID})
			assert.Equal(t, tc.transaction, calls[1].transaction)
			assert.Equal(t, calls[0].nodeIDs, calls[1].nodeIDs)
			assert.Equal(t, uint32(1), calls[1].queryDepth)
			assert.NotContains(t, ledger.Peers(), uint64(1))
			assert.Contains(t, ledger.Peers(), uint64(2))
			assert.Equal(t, 1, ledger.Snapshot().RequestPeers)
		})
	}
}

func TestRequestMissingAcquisitionNodes_MixedStaleLiveFanout(t *testing.T) {
	sender := &missingNodeChurnSender{
		errs: map[missingNodeTarget]error{
			{peerID: 1}: peermanagement.ErrPeerNotFound,
		},
	}
	router := newMissingNodeChurnRouter(t, sender)
	ledger, _ := newDisconnectFrontier(t, false)
	require.True(t, ledger.AddPeer(2))
	router.fetchTracker.Track(ledger)

	router.requestMissingAcquisitionNodes(ledger, 0)

	calls := sender.snapshotCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, []uint64{1, 2}, []uint64{calls[0].peerID, calls[1].peerID})
	assert.NotContains(t, ledger.Peers(), uint64(1))
	assert.Contains(t, ledger.Peers(), uint64(2))
}

func TestMissingNodeRecovery_ChurnIsBoundedAndCompletes(t *testing.T) {
	sender := &missingNodeChurnSender{
		errs: map[missingNodeTarget]error{
			{peerID: 1}: peermanagement.ErrPeerNotFound,
			{peerID: 2}: peermanagement.ErrConnectionClosed,
		},
		replacements: []uint64{2},
	}
	router := newMissingNodeChurnRouter(t, sender)
	ledger, replies := newDisconnectFrontier(t, false)
	router.fetchTracker.Track(ledger)
	stateIDs, _, complete, err := ledger.CollectMissingRequestContext(t.Context(), false)
	require.NoError(t, err)
	require.False(t, complete)

	router.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger:   ledger,
		targets:  []uint64{1},
		stateIDs: stateIDs,
	})

	calls := sender.snapshotCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, []uint64{1, 2}, []uint64{calls[0].peerID, calls[1].peerID})
	assert.Empty(t, ledger.Peers())
	assert.Len(t, sender.snapshotCalls(), 2)

	router.handlePeerConnect(3)

	calls = sender.snapshotCalls()
	require.Len(t, calls, 3)
	assert.Equal(t, uint64(3), calls[2].peerID)
	assert.Equal(t, calls[0].nodeIDs, calls[2].nodeIDs)
	require.NoError(t, ledger.GotStateNodes(replies))
	_, _, complete, err = ledger.CollectMissingRequestContext(t.Context(), false)
	require.NoError(t, err)
	assert.True(t, complete)
	assert.True(t, ledger.IsComplete())
}

func TestMissingNodeRecovery_LatePeerAfterNoReplacement(t *testing.T) {
	sender := &missingNodeChurnSender{
		errs: map[missingNodeTarget]error{
			{peerID: 1}: peermanagement.ErrPeerNotFound,
		},
	}
	router := newMissingNodeChurnRouter(t, sender)
	ledger, _ := newDisconnectFrontier(t, false)
	router.fetchTracker.Track(ledger)
	stateIDs, _, complete, err := ledger.CollectMissingRequestContext(t.Context(), false)
	require.NoError(t, err)
	require.False(t, complete)

	router.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger:   ledger,
		targets:  []uint64{1},
		stateIDs: stateIDs,
	})

	calls := sender.snapshotCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(1), calls[0].peerID)

	router.handlePeerConnect(2)

	calls = sender.snapshotCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, uint64(2), calls[1].peerID)
	assert.Equal(t, calls[0].nodeIDs, calls[1].nodeIDs)
}

func TestMissingReplyRequest_NonDisconnectErrorDoesNotEvictOrRetarget(t *testing.T) {
	sendErr := errors.New("temporary queue failure")
	sender := &missingNodeChurnSender{
		errs: map[missingNodeTarget]error{
			{peerID: 1}: sendErr,
		},
		replacements: []uint64{2},
	}
	router := newMissingNodeChurnRouter(t, sender)
	ledger, _ := newDisconnectFrontier(t, false)
	router.fetchTracker.Track(ledger)
	requests, complete, err := ledger.CollectMissingReplyRequestsContext(t.Context(), []uint64{1})
	require.NoError(t, err)
	require.False(t, complete)
	require.Len(t, requests, 1)

	router.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger:   ledger,
		requests: requests,
	})

	assert.Len(t, sender.snapshotCalls(), 1)
	assert.Contains(t, ledger.Peers(), uint64(1))
	assert.NotContains(t, ledger.Peers(), uint64(2))
	assert.Zero(t, ledger.Snapshot().RequestPeers)
}
