package adaptor

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type chargedRetrySender struct {
	retryRecordingSender
	badDataMu    sync.Mutex
	badDataCalls []badDataCall
}

func (s *chargedRetrySender) IncPeerBadData(peerID uint64, reason string) {
	s.badDataMu.Lock()
	defer s.badDataMu.Unlock()
	s.badDataCalls = append(s.badDataCalls, badDataCall{peerID: peerID, reason: reason})
}

func (s *chargedRetrySender) getBadDataCalls() []badDataCall {
	s.badDataMu.Lock()
	defer s.badDataMu.Unlock()
	return append([]badDataCall(nil), s.badDataCalls...)
}

func newTxSetIngressRouter(t *testing.T) (*Router, *badDataRecordingSender, *mockEngine) {
	t.Helper()
	sender := &badDataRecordingSender{}
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Sender:        sender,
	})
	engine := &mockEngine{}
	return NewRouter(engine, a, make(chan *peermanagement.InboundMessage, 1)), sender, engine
}

func deliverTxSetData(t *testing.T, r *Router, peerID uint64, data *message.LedgerData) {
	t.Helper()
	r.handleLedgerData(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(peerID),
		Type:    uint16(message.TypeLedgerData),
		Payload: encodePayload(t, data),
	})
}

func TestTxSetIngress_RequestRecordedBeforeNetworkSend(t *testing.T) {
	var router *Router
	var observed bool
	sender := &requestObservationSender{
		observe: func(id consensus.TxSetID) {
			router.txSetAcquireMu.Lock()
			defer router.txSetAcquireMu.Unlock()
			state, ok := router.txSetAcquire[id]
			observed = ok && state.txMap == nil && !state.done
		},
	}
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Sender:        sender,
	})
	router = NewRouter(&mockEngine{}, a, make(chan *peermanagement.InboundMessage, 1))
	id := consensus.TxSetID{0x91}

	require.NoError(t, a.RequestTxSet(id))
	require.True(t, observed, "request intent must exist before TMGetLedger is emitted")
	require.Equal(t, 1, sender.calls)
}

func TestTxSetIngress_ZeroSetAvailableWithoutNetworkRequest(t *testing.T) {
	sender := &requestObservationSender{}
	a := New(Config{Sender: sender})

	local, err := a.BuildTxSet([][]byte{makeBlob(0x51)})
	require.NoError(t, err)
	require.NotEqual(t, consensus.TxSetID{}, local.ID(),
		"fixture represents our non-empty consensus position")

	peerSet, err := a.GetTxSet(consensus.TxSetID{})
	require.NoError(t, err)
	require.NotNil(t, peerSet)
	require.Zero(t, peerSet.Size(), "a peer's zero position is the canonical empty set")

	require.NoError(t, a.RequestTxSet(consensus.TxSetID{}))
	require.Zero(t, sender.calls, "the canonical zero set must never start network acquisition")
}

type requestObservationSender struct {
	noopSender
	observe func(consensus.TxSetID)
	calls   int
}

func (s *requestObservationSender) RequestTxSet(id consensus.TxSetID) error {
	s.calls++
	s.observe(id)
	return nil
}

func TestTxSetIngress_RejectsUnsolicitedCompleteAndPartialData(t *testing.T) {
	router, sender, engine := newTxSetIngressRouter(t)

	_, completeID, completeNodes := buildTxSetForTest(t, 4)
	deliverTxSetData(t, router, 11, ldFromWire(completeID, completeNodes))

	_, partialID, partialNodes := buildTxSetForTest(t, 8)
	require.Greater(t, len(partialNodes), 1)
	deliverTxSetData(t, router, 12, ldFromWire(partialID, partialNodes[:1]))

	assert.Equal(t, []badDataCall{
		{peerID: 11, reason: "txset-useless-unneeded"},
		{peerID: 12, reason: "txset-useless-unneeded"},
	}, sender.getBadDataCalls())

	router.txSetAcquireMu.Lock()
	_, completeAllocated := router.txSetAcquire[consensus.TxSetID(completeID)]
	_, partialAllocated := router.txSetAcquire[consensus.TxSetID(partialID)]
	router.txSetAcquireMu.Unlock()
	assert.False(t, completeAllocated)
	assert.False(t, partialAllocated)

	engine.mu.Lock()
	defer engine.mu.Unlock()
	assert.Empty(t, engine.txSets, "unsolicited data must not reach Engine.OnTxSet")
}

func TestTxSetIngress_ChargesMalformedInvalidAndNonProgressData(t *testing.T) {
	tests := []struct {
		name   string
		nodes  []message.LedgerNode
		reason string
	}{
		{
			name:   "missing node data",
			nodes:  []message.LedgerNode{{NodeID: make([]byte, shamap.NodeIDSize)}},
			reason: "ledger-data-decode",
		},
		{
			name:   "invalid node id",
			nodes:  []message.LedgerNode{{NodeID: []byte{1}, NodeData: []byte{1}}},
			reason: "txset-baddata-nodeid",
		},
		{
			name:   "empty reply",
			reason: "ledger-data-count",
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router, sender, engine := newTxSetIngressRouter(t)
			id := consensus.TxSetID{byte(i + 1), 0xa5}
			router.MarkTxSetStillNeeded(id)

			deliverTxSetData(t, router, 20+uint64(i), &message.LedgerData{
				LedgerHash: id[:],
				InfoType:   message.LedgerInfoTsCandidate,
				Nodes:      test.nodes,
			})

			assert.Equal(t, []badDataCall{{
				peerID: 20 + uint64(i),
				reason: test.reason,
			}}, sender.getBadDataCalls())
			router.txSetAcquireMu.Lock()
			state := router.txSetAcquire[id]
			router.txSetAcquireMu.Unlock()
			require.NotNil(t, state)
			assert.Nil(t, state.txMap, "rejected framing must not allocate a SHAMap")
			engine.mu.Lock()
			assert.Empty(t, engine.txSets)
			engine.mu.Unlock()
		})
	}
}

func TestTxSetIngress_ChargesInvalidSHAMapReply(t *testing.T) {
	router, sender, engine := newTxSetIngressRouter(t)
	_, rawID, wireNodes := buildTxSetForTest(t, 4)
	require.Greater(t, len(wireNodes), 1)
	id := consensus.TxSetID(rawID)
	router.MarkTxSetStillNeeded(id)

	deliverTxSetData(t, router, 31, &message.LedgerData{
		LedgerHash: id[:],
		InfoType:   message.LedgerInfoTsCandidate,
		Nodes: []message.LedgerNode{
			{NodeID: wireNodes[0].NodeID, NodeData: wireNodes[0].Data},
			{NodeID: wireNodes[1].NodeID, NodeData: []byte{0xde, 0xad}},
		},
	})

	assert.Equal(t, []badDataCall{{
		peerID: 31,
		reason: "txset-useless-nonprogress",
	}}, sender.getBadDataCalls())
	engine.mu.Lock()
	defer engine.mu.Unlock()
	assert.Empty(t, engine.txSets)
}

func TestTxSetIngress_ChargesPostCompletionStraggler(t *testing.T) {
	router, sender, engine := newTxSetIngressRouter(t)
	_, rawID, wireNodes := buildTxSetForTest(t, 4)
	id := consensus.TxSetID(rawID)
	router.MarkTxSetStillNeeded(id)
	complete := ldFromWire(rawID, wireNodes)

	deliverTxSetData(t, router, 40, complete)
	engine.mu.Lock()
	require.Len(t, engine.txSets, 1)
	engine.mu.Unlock()
	require.Empty(t, sender.getBadDataCalls())

	deliverTxSetData(t, router, 41, complete)

	assert.Equal(t, []badDataCall{{
		peerID: 41,
		reason: "txset-useless-unneeded",
	}}, sender.getBadDataCalls())
	engine.mu.Lock()
	defer engine.mu.Unlock()
	assert.Len(t, engine.txSets, 1, "a straggler must not re-feed Engine.OnTxSet")
}

func TestTxSetIngress_BadRootReRequestsWithoutPenalty(t *testing.T) {
	sender := &chargedRetrySender{}
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Sender:        sender,
	})
	router := NewRouter(&mockEngine{}, a, make(chan *peermanagement.InboundMessage, 1))
	id := consensus.TxSetID{0x61}
	router.MarkTxSetStillNeeded(id)

	withRetryKnobs(router, time.Hour, 20, 1, func() {
		router.handleTxSetData(&message.LedgerData{
			LedgerHash: id[:],
			InfoType:   message.LedgerInfoTsCandidate,
			Nodes: []message.LedgerNode{{
				NodeID:   make([]byte, shamap.NodeIDSize),
				NodeData: []byte{0xde, 0xad},
			}},
		}, 62)
	})

	assert.Empty(t, sender.getBadDataCalls(), "rippled treats bad root data as useful")
	require.Equal(t, 1, sender.calledN(), "bad root data must trigger another root request")
	assert.Equal(t, uint64(62), sender.lastCall().peerID)
	require.Len(t, sender.lastCall().nodeIDs, 1)
	assert.True(t, isShamapRootNodeID(sender.lastCall().nodeIDs[0]))

	router.txSetAcquireMu.Lock()
	defer router.txSetAcquireMu.Unlock()
	state := router.txSetAcquire[id]
	require.NotNil(t, state)
	assert.Zero(t, state.peerNonProgress[62], "bad root must not deprioritize its peer")
	assert.Zero(t, state.stallTicks, "a useful reply resets consecutive stall accounting")
	assert.False(t, state.haveRoot)
}

func TestTxSetIngress_BadRootThenValidRootContinues(t *testing.T) {
	sender := &chargedRetrySender{}
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Sender:        sender,
	})
	engine := &mockEngine{}
	router := NewRouter(engine, a, make(chan *peermanagement.InboundMessage, 1))
	_, rawID, wireNodes := buildTxSetForTest(t, 8)
	id := consensus.TxSetID(rawID)
	router.MarkTxSetStillNeeded(id)

	reply := ldFromWire(rawID, wireNodes)
	reply.Nodes = append([]message.LedgerNode{{
		NodeID:   make([]byte, shamap.NodeIDSize),
		NodeData: []byte{0xde, 0xad},
	}}, reply.Nodes...)
	router.handleTxSetData(reply, 63)

	assert.Empty(t, sender.getBadDataCalls())
	engine.mu.Lock()
	defer engine.mu.Unlock()
	require.Equal(t, []consensus.TxSetID{id}, engine.txSets,
		"a valid root later in the reply must recover from an earlier bad root")
}

func TestTxSetIngress_BadRootThenNonRootIsInvalid(t *testing.T) {
	sender := &chargedRetrySender{}
	a := New(Config{
		LedgerService: newTestLedgerService(t),
		Sender:        sender,
	})
	engine := &mockEngine{}
	router := NewRouter(engine, a, make(chan *peermanagement.InboundMessage, 1))
	_, rawID, wireNodes := buildTxSetForTest(t, 8)
	require.Greater(t, len(wireNodes), 1)
	id := consensus.TxSetID(rawID)
	router.MarkTxSetStillNeeded(id)

	router.handleTxSetData(&message.LedgerData{
		LedgerHash: id[:],
		InfoType:   message.LedgerInfoTsCandidate,
		Nodes: []message.LedgerNode{
			{NodeID: make([]byte, shamap.NodeIDSize), NodeData: []byte{0xde, 0xad}},
			{NodeID: wireNodes[1].NodeID, NodeData: wireNodes[1].Data},
		},
	}, 64)

	require.Equal(t, []badDataCall{{
		peerID: 64,
		reason: "txset-useless-nonprogress",
	}}, sender.getBadDataCalls())
	require.Equal(t, 1, sender.calledN(), "the invalid rootless reply still re-requests the root")
	assert.True(t, isShamapRootNodeID(sender.lastCall().nodeIDs[0]))

	router.txSetAcquireMu.Lock()
	state := router.txSetAcquire[id]
	router.txSetAcquireMu.Unlock()
	require.NotNil(t, state)
	assert.False(t, state.haveRoot)
	assert.Equal(t, 1, state.peerNonProgress[64])
	engine.mu.Lock()
	defer engine.mu.Unlock()
	assert.Empty(t, engine.txSets)
}
