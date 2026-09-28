package adaptor

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func mismatchedInnerNode(t *testing.T) []byte {
	t.Helper()
	_, _, nodes := buildTxSetForTest(t, 3)
	data := bytes.Clone(nodes[0].Data)
	data[0] ^= 1
	return data
}

type depthServeNetwork struct {
	*relayRecorder
	depth bool
}

func (s *depthServeNetwork) PeerSupportsNodeDepth(uint64) bool { return s.depth }

func TestLedgerNodeDepthRelay(t *testing.T) {
	_, hash, wire := buildTxSetForTest(t, 4)
	legacy := ldFromWire(hash, wire)
	modern := ldFromWire(hash, wire)
	require.NoError(t, useLedgerNodeDepth(modern.Nodes))
	for _, depth := range []bool{false, true} {
		t.Run(map[bool]string{false: "2.2", true: "2.3"}[depth], func(t *testing.T) {
			for _, input := range []*message.LedgerData{legacy, modern} {
				r, recorder := makeRouterWithRelayRecorder(t)
				r.serve = &depthServeNetwork{recorder, depth}
				incoming := *input
				incoming.RequestCookie = 77
				r.handleMessage(&peermanagement.InboundMessage{PeerID: 5, Type: message.TypeLedgerData, Payload: encodePayload(t, &incoming)})
				frames := recorder.sentFrames()
				require.Len(t, frames, 1)
				_, decoded := decodeFrame(t, frames[0].frame)
				output := decoded.(*message.LedgerData)
				require.False(t, output.HasRequestCookie())
				want := input.Nodes
				if !depth {
					want = legacy.Nodes
				}
				require.Equal(t, want, output.Nodes)
				require.Empty(t, recorder.badDataCalls())
			}
		})
	}
	innerDepth, excessDepth := uint32(1), uint32(65)
	for name, nodes := range map[string][]message.LedgerNode{
		"mixed":             {legacy.Nodes[0], modern.Nodes[1]},
		"empty data":        {{NodeID: wire[0].NodeID}},
		"missing reference": {{NodeData: wire[0].Data}},
		"inner depth":       {{NodeData: wire[0].Data, Depth: &innerDepth}},
		"excess depth":      {{NodeData: wire[len(wire)-1].Data, Depth: &excessDepth}},
	} {
		t.Run(name, func(t *testing.T) {
			r, recorder := makeRouterWithRelayRecorder(t)
			incoming := *modern
			incoming.RequestCookie = 77
			incoming.Nodes = nodes
			r.handleMessage(&peermanagement.InboundMessage{PeerID: 5, Type: message.TypeLedgerData, Payload: encodePayload(t, &incoming)})
			require.Empty(t, recorder.sentFrames())
			require.Equal(t, []badDataCall{{peerID: 5, reason: "ledger-data-node"}}, recorder.badDataCalls())
		})
	}
}

func TestLedgerNodeDepthTxSetAcquisition(t *testing.T) {
	for _, depth := range []bool{false, true} {
		t.Run(map[bool]string{false: "2.2", true: "2.3"}[depth], func(t *testing.T) {
			r, sender, engine := newTxSetIngressRouter(t)
			_, hash, wire := buildTxSetForTest(t, 16)
			id := consensus.TxSetID(hash)
			r.MarkTxSetStillNeeded(id)
			data := ldFromWire(hash, wire)
			if depth {
				require.NoError(t, useLedgerNodeDepth(data.Nodes))
			}
			deliverTxSetData(t, r, 5, data)
			require.Equal(t, []consensus.TxSetID{id}, engine.txSets)
			require.Empty(t, sender.getBadDataCalls())
		})
	}
}

func TestLedgerNodeDepthRejectsMalformedTxSetBeforeInsertion(t *testing.T) {
	r, sender, engine := newTxSetIngressRouter(t)
	_, hash, wire := buildTxSetForTest(t, 4)
	id := consensus.TxSetID(hash)
	r.MarkTxSetStillNeeded(id)
	data := ldFromWire(hash, wire)
	data.Nodes = append(data.Nodes, message.LedgerNode{NodeID: wire[0].NodeID, NodeData: []byte{255}})
	deliverTxSetData(t, r, 5, data)
	require.Empty(t, engine.txSets)
	require.Nil(t, r.txSetAcquire[id].txMap)
	require.Equal(t, []badDataCall{{peerID: 5, reason: "txset-baddata-nodeid"}}, sender.getBadDataCalls())
}

func TestLedgerNodeDepthServesAndAcquiresLedger(t *testing.T) {
	for _, depth := range []bool{false, true} {
		t.Run(map[bool]string{false: "2.2", true: "2.3"}[depth], func(t *testing.T) {
			r, recorder := makeRouterWithRelayRecorder(t)
			r.serve = &depthServeNetwork{recorder, depth}
			ledger := r.adaptor.LedgerService().GetClosedLedger()
			hash, seq := ledger.Hash(), ledger.Sequence()
			acquisition := inbound.New(hash, seq, 7, serveTestLogger())
			request := func(kind message.LedgerInfoType, ids [][]byte) *message.LedgerData {
				t.Helper()
				before := len(recorder.sentFrames())
				r.handleGetLedger(&peermanagement.InboundMessage{PeerID: 7, Type: message.TypeGetLedger, Payload: encodePayload(t, &message.GetLedger{LedgerHash: hash[:], LedgerSeq: seq, InfoType: kind, NodeIDs: ids})})
				frames := recorder.sentFrames()
				require.Len(t, frames, before+1)
				_, decoded := decodeFrame(t, frames[before].frame)
				return decoded.(*message.LedgerData)
			}
			base := request(message.LedgerInfoBase, nil)
			for _, node := range base.Nodes {
				require.Nil(t, node.NodeID)
				require.Nil(t, node.ID)
				require.Nil(t, node.Depth)
			}
			require.NoError(t, acquisition.GotBase(base.Nodes))
			for i := 0; i < 200 && !acquisition.IsComplete(); i++ {
				ids, _ := acquisition.CollectMissingRequest(false)
				if acquisition.IsComplete() {
					break
				}
				reply := request(message.LedgerInfoAsNode, ids)
				for _, node := range reply.Nodes {
					if depth {
						require.Nil(t, node.NodeID)
						require.True(t, node.ID != nil || node.Depth != nil)
					} else {
						require.Len(t, node.NodeID, 33)
						require.Nil(t, node.ID)
						require.Nil(t, node.Depth)
					}
				}
				require.NoError(t, acquisition.GotStateNodes(reply.Nodes))
			}
			require.True(t, acquisition.IsComplete())
			gotHeader, state, _, err := acquisition.Result()
			require.NoError(t, err)
			require.Equal(t, hash, gotHeader.Hash)
			gotHash, err := state.Hash()
			require.NoError(t, err)
			wantHash, err := ledger.StateMapHash()
			require.NoError(t, err)
			require.Equal(t, wantHash, gotHash)
		})
	}
}

func TestLedgerNodeDepthServesTxSet(t *testing.T) {
	for _, depth := range []bool{false, true} {
		t.Run(map[bool]string{false: "2.2", true: "2.3"}[depth], func(t *testing.T) {
			r, recorder := makeRouterWithRelayRecorder(t)
			r.serve = &depthServeNetwork{recorder, depth}
			txSet, err := r.adaptor.BuildTxSet([][]byte{bytes.Repeat([]byte{1}, 40), bytes.Repeat([]byte{2}, 40)})
			require.NoError(t, err)
			id := txSet.ID()
			consumer, sender, engine := newTxSetIngressRouter(t)
			consumer.MarkTxSetStillNeeded(id)
			ids := [][]byte{make([]byte, 33)}
			for i := 0; i < 100 && len(engine.txSets) == 0; i++ {
				r.serveTxSet(7, &message.GetLedger{LedgerHash: id[:], InfoType: message.LedgerInfoTsCandidate, NodeIDs: ids, QueryDepth: 64, QueryDepthSet: true})
				frames := recorder.sentFrames()
				require.Len(t, frames, i+1)
				_, decoded := decodeFrame(t, frames[i].frame)
				data := decoded.(*message.LedgerData)
				for _, node := range data.Nodes {
					if depth {
						require.Nil(t, node.NodeID)
						require.True(t, node.ID != nil || node.Depth != nil)
					} else {
						require.Len(t, node.NodeID, 33)
					}
				}
				deliverTxSetData(t, consumer, 7, data)
				if len(engine.txSets) == 0 {
					state := consumer.txSetAcquire[id]
					require.NotNil(t, state)
					ids = missingNodeIDs(state.txMap.GetMissingNodes(100, nil))
					require.NotEmpty(t, ids)
				}
			}
			require.Equal(t, []consensus.TxSetID{id}, engine.txSets)
			require.Empty(t, sender.getBadDataCalls())
		})
	}
}

func TestLedgerNodeDepthCountsReferenceBytes(t *testing.T) {
	data := &message.LedgerData{Nodes: []message.LedgerNode{{NodeData: []byte{1}, ID: make([]byte, 33)}}}
	got, ok := txSetReplyBytes(data)
	require.True(t, ok)
	require.Equal(t, int64(34), got)
	depth := uint32(64)
	data.Nodes[0].ID = nil
	data.Nodes[0].Depth = &depth
	got, ok = txSetReplyBytes(data)
	require.True(t, ok)
	require.Equal(t, int64(5), got)
}
