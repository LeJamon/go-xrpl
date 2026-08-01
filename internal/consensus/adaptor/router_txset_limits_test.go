package adaptor

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type txSetResourceRecordingSender struct {
	retryRecordingSender
	mu      sync.Mutex
	charges []txSetResourceCharge
}

type txSetResourceCharge struct {
	peerID uint64
	reason string
}

func (s *txSetResourceRecordingSender) IncPeerBadData(peerID uint64, reason string) {
	s.mu.Lock()
	s.charges = append(s.charges, txSetResourceCharge{peerID: peerID, reason: reason})
	s.mu.Unlock()
}

func (s *txSetResourceRecordingSender) badDataCharges() []txSetResourceCharge {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]txSetResourceCharge(nil), s.charges...)
}

func resourceTxSetID(value byte) consensus.TxSetID {
	var id consensus.TxSetID
	id[0] = value
	return id
}

func resourceLedgerData(id consensus.TxSetID, nodes []message.LedgerNode) *message.LedgerData {
	return &message.LedgerData{
		LedgerHash: append([]byte(nil), id[:]...),
		Nodes:      nodes,
	}
}

func resourceAcquireState(t *testing.T, retained int64) *txSetAcquireState {
	t.Helper()
	txMap := shamap.New(shamap.TypeTransaction)
	require.NoError(t, txMap.StartSync())
	return &txSetAcquireState{
		txMap:           txMap,
		startedAt:       time.Now(),
		lastUpdate:      time.Now(),
		retainedBytes:   retained,
		peerNonProgress: make(map[uint64]int),
	}
}

func TestTxSetAcquire_ActiveCountBound(t *testing.T) {
	router, _ := newRetryRouter(t)

	for i := 0; i < txSetAcquireMaxActive; i++ {
		id := resourceTxSetID(byte(i + 1))
		state := resourceAcquireState(t, int64(i+1))
		state.lastUpdate = time.Unix(int64(i+1), 0)
		router.txSetAcquire[id] = state
	}

	oldestID := resourceTxSetID(1)
	oldest := router.txSetAcquire[oldestID]
	admittedID := resourceTxSetID(0xff)
	assert.True(t, router.admitTxSetStillNeeded(admittedID))

	router.txSetAcquireMu.Lock()
	defer router.txSetAcquireMu.Unlock()
	assert.Len(t, router.txSetAcquire, txSetAcquireMaxActive)
	assert.NotContains(t, router.txSetAcquire, oldestID)
	assert.Contains(t, router.txSetAcquire, admittedID)
	assert.Nil(t, oldest.txMap)
	assert.Zero(t, oldest.retainedBytes)
}

func TestTxSetAcquire_AdmissionRejectsZeroID(t *testing.T) {
	router, _ := newRetryRouter(t)

	assert.False(t, router.admitTxSetStillNeeded(consensus.TxSetID{}))
	assert.Empty(t, router.txSetAcquire)
}

func TestTxSetAcquire_ActiveCountReclaimsDormantState(t *testing.T) {
	router, _ := newRetryRouter(t)
	dormantID := resourceTxSetID(1)
	dormant := resourceAcquireState(t, 1024)
	dormant.done = true
	dormant.dormant = true
	dormant.lastUpdate = time.Now().Add(-time.Second)
	router.txSetAcquire[dormantID] = dormant
	for i := 1; i < txSetAcquireMaxActive; i++ {
		id := resourceTxSetID(byte(i + 1))
		router.txSetAcquire[id] = resourceAcquireState(t, 0)
	}

	newID := resourceTxSetID(0xff)
	router.MarkTxSetStillNeeded(newID)
	router.handleTxSetData(resourceLedgerData(newID, nil), 1)

	router.txSetAcquireMu.Lock()
	defer router.txSetAcquireMu.Unlock()
	assert.Len(t, router.txSetAcquire, txSetAcquireMaxActive)
	assert.NotContains(t, router.txSetAcquire, dormantID)
	assert.Contains(t, router.txSetAcquire, newID)
	assert.Nil(t, dormant.txMap)
	assert.Zero(t, dormant.retainedBytes)
}

func TestTxSetAcquire_ReplyWorkBound(t *testing.T) {
	oversizedChunk := make([]byte, txSetAcquireMaxReplyBytes/8+1)
	oversizedNodes := make([]message.LedgerNode, 8)
	for i := range oversizedNodes {
		oversizedNodes[i].NodeData = oversizedChunk
	}
	tests := []struct {
		name  string
		nodes []message.LedgerNode
	}{
		{
			name:  "node count",
			nodes: make([]message.LedgerNode, txSetAcquireMaxReplyNodes+1),
		},
		{
			name:  "bytes",
			nodes: oversizedNodes,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, _ := newRetryRouter(t)
			sender := &txSetResourceRecordingSender{}
			router.adaptor.sender = sender
			id := resourceTxSetID(byte(i + 1))
			router.handleTxSetData(resourceLedgerData(id, tt.nodes), 7)

			router.txSetAcquireMu.Lock()
			assert.NotContains(t, router.txSetAcquire, id)
			router.txSetAcquireMu.Unlock()
			assert.Equal(t,
				[]txSetResourceCharge{{peerID: 7, reason: "txset-resource-limit"}},
				sender.badDataCharges(),
			)
		})
	}
}

func TestTxSetAcquire_RetainedByteBounds(t *testing.T) {
	t.Run("per set", func(t *testing.T) {
		router, _ := newRetryRouter(t)
		id := resourceTxSetID(1)
		state := resourceAcquireState(t, txSetAcquireMaxSetBytes-4)
		router.txSetAcquire[id] = state

		nodes := []message.LedgerNode{{NodeID: []byte{1}, NodeData: make([]byte, 4)}}
		router.handleTxSetData(resourceLedgerData(id, nodes), 1)

		router.txSetAcquireMu.Lock()
		defer router.txSetAcquireMu.Unlock()
		assert.Equal(t, txSetAcquireMaxSetBytes-4, state.retainedBytes)
		assert.Same(t, state, router.txSetAcquire[id])
	})

	t.Run("global", func(t *testing.T) {
		router, _ := newRetryRouter(t)
		currentID := resourceTxSetID(1)
		current := resourceAcquireState(t, 0)
		router.txSetAcquire[currentID] = current
		for i := 0; i < int(txSetAcquireMaxGlobalBytes/txSetAcquireMaxSetBytes); i++ {
			id := resourceTxSetID(byte(i + 2))
			router.txSetAcquire[id] = resourceAcquireState(t, txSetAcquireMaxSetBytes)
		}

		nodes := []message.LedgerNode{{NodeID: []byte{1}, NodeData: []byte{1}}}
		router.handleTxSetData(resourceLedgerData(currentID, nodes), 1)

		router.txSetAcquireMu.Lock()
		defer router.txSetAcquireMu.Unlock()
		assert.Zero(t, current.retainedBytes)
		assert.Equal(t, txSetAcquireMaxGlobalBytes, router.txSetRetainedBytesLocked())
	})

	t.Run("global pressure reclaims oldest dormant map", func(t *testing.T) {
		router, _ := newRetryRouter(t)
		ld, txSetID := rootOnlyTxSetLedgerData(t, 8)
		var oldestID consensus.TxSetID
		for i := 0; i < int(txSetAcquireMaxGlobalBytes/txSetAcquireMaxSetBytes); i++ {
			id := resourceTxSetID(byte(0xa0 + i))
			if id == txSetID {
				id[1] = 1
			}
			if i == 0 {
				oldestID = id
			}
			state := resourceAcquireState(t, txSetAcquireMaxSetBytes)
			state.done = true
			state.dormant = true
			state.lastUpdate = time.Unix(int64(i+1), 0)
			router.txSetAcquire[id] = state
		}

		router.MarkTxSetStillNeeded(txSetID)
		router.handleTxSetData(ld, 1)

		router.txSetAcquireMu.Lock()
		defer router.txSetAcquireMu.Unlock()
		assert.NotContains(t, router.txSetAcquire, oldestID)
		state := router.txSetAcquire[txSetID]
		require.NotNil(t, state)
		assert.Positive(t, state.retainedBytes)
		assert.LessOrEqual(t, router.txSetRetainedBytesLocked(), txSetAcquireMaxGlobalBytes)
	})

	t.Run("rejected first reply leaves no state", func(t *testing.T) {
		for _, rootData := range [][]byte{nil, {1}} {
			router, _ := newRetryRouter(t)
			for i := 0; i < int(txSetAcquireMaxGlobalBytes/txSetAcquireMaxSetBytes); i++ {
				id := resourceTxSetID(byte(i + 2))
				router.txSetAcquire[id] = resourceAcquireState(t, txSetAcquireMaxSetBytes)
			}

			newID := resourceTxSetID(1)
			nodes := []message.LedgerNode{{NodeID: make([]byte, shamap.NodeIDSize), NodeData: rootData}}
			router.handleTxSetData(resourceLedgerData(newID, nodes), 1)

			router.txSetAcquireMu.Lock()
			assert.NotContains(t, router.txSetAcquire, newID)
			assert.Len(t, router.txSetAcquire, int(txSetAcquireMaxGlobalBytes/txSetAcquireMaxSetBytes))
			assert.Equal(t, txSetAcquireMaxGlobalBytes, router.txSetRetainedBytesLocked())
			router.txSetAcquireMu.Unlock()
		}
	})
}

func TestTxSetAcquire_DuplicateNodesDoNotInflateRetainedBytes(t *testing.T) {
	router, _ := newRetryRouter(t)
	ld, txSetID := rootOnlyTxSetLedgerData(t, 8)
	router.MarkTxSetStillNeeded(txSetID)

	router.handleTxSetData(ld, 1)
	router.txSetAcquireMu.Lock()
	state := router.txSetAcquire[txSetID]
	first := state.retainedBytes
	router.txSetAcquireMu.Unlock()
	require.Positive(t, first)

	router.handleTxSetData(ld, 1)
	router.txSetAcquireMu.Lock()
	second := state.retainedBytes
	router.txSetAcquireMu.Unlock()
	assert.Equal(t, first, second)
}

func TestTxSetAcquire_DuplicateHeavyReplyCanUseRemainingSetBudget(t *testing.T) {
	router, _ := newRetryRouter(t)
	_, rawID, wireNodes := buildTxSetForTest(t, 8)
	txSetID := consensus.TxSetID(rawID)
	root := message.LedgerNode{NodeID: wireNodes[0].NodeID, NodeData: wireNodes[0].Data}
	router.MarkTxSetStillNeeded(txSetID)
	router.handleTxSetData(resourceLedgerData(txSetID, []message.LedgerNode{root}), 1)

	findWireNode := func(nodeID []byte) message.LedgerNode {
		for _, node := range wireNodes[1:] {
			if bytes.Equal(node.NodeID, nodeID) {
				return message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data}
			}
		}
		return message.LedgerNode{}
	}

	router.txSetAcquireMu.Lock()
	state := router.txSetAcquire[txSetID]
	require.NotNil(t, state)
	missing := state.txMap.GetMissingNodes(256, nil)
	require.NotEmpty(t, missing)
	duplicate := findWireNode(missing[0].NodeID.Bytes())
	router.txSetAcquireMu.Unlock()
	require.NotEmpty(t, duplicate.NodeData)
	router.handleTxSetData(resourceLedgerData(txSetID, []message.LedgerNode{duplicate}), 1)

	router.txSetAcquireMu.Lock()
	missing = state.txMap.GetMissingNodes(256, nil)
	require.NotEmpty(t, missing)
	targetID := missing[0].NodeID.Bytes()
	target := findWireNode(targetID)
	require.NotEmpty(t, target.NodeData)
	targetBytes := int64(len(target.NodeID)) + int64(len(target.NodeData))
	state.retainedBytes = txSetAcquireMaxSetBytes - targetBytes
	router.txSetAcquireMu.Unlock()

	nodes := make([]message.LedgerNode, 0, 9)
	for range 8 {
		nodes = append(nodes, duplicate)
	}
	nodes = append(nodes, target)
	router.handleTxSetData(resourceLedgerData(txSetID, nodes), 1)

	router.txSetAcquireMu.Lock()
	defer router.txSetAcquireMu.Unlock()
	require.Same(t, state, router.txSetAcquire[txSetID])
	assert.Equal(t, txSetAcquireMaxSetBytes, state.retainedBytes)
	for _, node := range state.txMap.GetMissingNodes(256, nil) {
		assert.False(t, bytes.Equal(node.NodeID.Bytes(), targetID),
			"the useful node must attach even when duplicates make total reply bytes exceed remaining set budget")
	}
}

func TestTxSetAcquire_CleanupReleasesRetainedBytes(t *testing.T) {
	t.Run("completion", func(t *testing.T) {
		router, _ := newRetryRouter(t)
		_, rawID, wireNodes := buildTxSetForTest(t, 4)
		txSetID := consensus.TxSetID(rawID)
		router.MarkTxSetStillNeeded(txSetID)

		router.handleTxSetData(ldFromWire(rawID, wireNodes), 1)

		router.txSetAcquireMu.Lock()
		defer router.txSetAcquireMu.Unlock()
		state := router.txSetAcquire[txSetID]
		require.NotNil(t, state)
		assert.True(t, state.done)
		assert.Nil(t, state.txMap)
		assert.Zero(t, state.retainedBytes)
		assert.Zero(t, router.txSetRetainedBytesLocked())
	})

	t.Run("terminal failure", func(t *testing.T) {
		router, _ := newRetryRouter(t)
		id := resourceTxSetID(1)
		state := resourceAcquireState(t, 1024)
		router.txSetAcquire[id] = state

		router.markTxSetDone(id)

		router.txSetAcquireMu.Lock()
		defer router.txSetAcquireMu.Unlock()
		assert.True(t, state.done)
		assert.Nil(t, state.txMap)
		assert.Zero(t, router.txSetRetainedBytesLocked())
	})

	t.Run("ttl sweep", func(t *testing.T) {
		router, _ := newRetryRouter(t)
		id := resourceTxSetID(1)
		state := resourceAcquireState(t, 1024)
		state.lastUpdate = time.Now().Add(-txSetAcquireTTL - time.Second)
		router.txSetAcquire[id] = state

		router.txSetAcquireMu.Lock()
		router.sweepStaleTxSetAcquireLocked()
		_, exists := router.txSetAcquire[id]
		total := router.txSetRetainedBytesLocked()
		router.txSetAcquireMu.Unlock()

		assert.False(t, exists)
		assert.Zero(t, total)
		assert.Nil(t, state.txMap)
		assert.Zero(t, state.retainedBytes)
	})
}
