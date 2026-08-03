package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestRouter_LateAccountStateNodesEnterFetchPackOnly(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	closed := r.adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, closed)
	stateMap, err := closed.StateMapSnapshot()
	require.NoError(t, err)
	wire, err := stateMap.WalkWireNodes()
	require.NoError(t, err)
	require.NotEmpty(t, wire)
	entry, err := shamap.FlushEntryFromWire(wire[0].Data, closed.Sequence(), shamap.TypeState)
	require.NoError(t, err)
	hash := [32]byte{0xA1}
	stateReply := &message.LedgerData{
		LedgerHash: hash[:],
		LedgerSeq:  closed.Sequence(),
		InfoType:   message.LedgerInfoAsNode,
		Nodes: []message.LedgerNode{
			{NodeID: wire[0].NodeID, NodeData: wire[0].Data},
			{NodeID: wire[0].NodeID, NodeData: []byte{0xFF}},
		},
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeLedgerData,
		Payload: encodePayload(t, stateReply),
	})

	stored, ok := r.fetchPacks.get(entry.Hash, time.Now())
	require.True(t, ok)
	assert.Equal(t, entry.Data, stored)
	calls := sender.getBadDataCalls()
	assert.Empty(t, calls)

	txHash := [32]byte{0xA2}
	txReply := &message.LedgerData{
		LedgerHash: txHash[:],
		LedgerSeq:  closed.Sequence(),
		InfoType:   message.LedgerInfoTxNode,
		Nodes:      []message.LedgerNode{{NodeID: wire[0].NodeID, NodeData: wire[0].Data}},
	}
	r.fetchPacks = newFetchPackCache()
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  8,
		Type:    message.TypeLedgerData,
		Payload: encodePayload(t, txReply),
	})
	_, ok = r.fetchPacks.get(entry.Hash, time.Now())
	assert.False(t, ok, "late transaction nodes must not enter the stale state cache")
}

func TestRouter_RejectsInvalidLedgerDataNodeCountsBeforeCaching(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	closed := r.adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, closed)
	stateMap, err := closed.StateMapSnapshot()
	require.NoError(t, err)
	wire, err := stateMap.WalkWireNodes()
	require.NoError(t, err)
	require.NotEmpty(t, wire)
	entry, err := shamap.FlushEntryFromWire(wire[0].Data, closed.Sequence(), shamap.TypeState)
	require.NoError(t, err)
	hash := [32]byte{0xB2}

	reply := &message.LedgerData{
		LedgerHash: hash[:],
		LedgerSeq:  closed.Sequence(),
		InfoType:   message.LedgerInfoAsNode,
	}
	emptyPayload := encodePayload(t, reply)
	oversizedPayload := append([]byte(nil), emptyPayload...)
	for range 12289 {
		oversizedPayload = protowire.AppendTag(oversizedPayload, 4, protowire.BytesType)
		oversizedPayload = protowire.AppendVarint(oversizedPayload, 0)
	}
	for i, payload := range [][]byte{emptyPayload, oversizedPayload} {
		r.handleMessage(&peermanagement.InboundMessage{
			PeerID:  peermanagement.PeerID(20 + i),
			Type:    message.TypeLedgerData,
			Payload: payload,
		})
	}

	_, ok := r.fetchPacks.get(entry.Hash, time.Now())
	assert.False(t, ok, "oversized reply must be rejected before any node is cached")
	calls := sender.getBadDataCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, "ledger-data-count", calls[0].reason)
	assert.Equal(t, "ledger-data-decode", calls[1].reason)
}

func TestRouter_LateAccountStateNodeRequiresNodeID(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	closed := r.adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, closed)
	stateMap, err := closed.StateMapSnapshot()
	require.NoError(t, err)
	wire, err := stateMap.WalkWireNodes()
	require.NoError(t, err)
	require.NotEmpty(t, wire)
	entry, err := shamap.FlushEntryFromWire(wire[0].Data, closed.Sequence(), shamap.TypeState)
	require.NoError(t, err)
	hash := [32]byte{0xB3}

	reply := &message.LedgerData{
		LedgerHash: hash[:],
		LedgerSeq:  closed.Sequence(),
		InfoType:   message.LedgerInfoAsNode,
		Nodes:      []message.LedgerNode{{NodeData: wire[0].Data}},
	}
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  30,
		Type:    message.TypeLedgerData,
		Payload: encodePayload(t, reply),
	})

	_, ok := r.fetchPacks.get(entry.Hash, time.Now())
	assert.False(t, ok)
	calls := sender.getBadDataCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "ledger-data-node", calls[0].reason)
}
