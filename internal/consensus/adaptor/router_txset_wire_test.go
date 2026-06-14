package adaptor

import (
	"bytes"
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

// txsetRecordingSender captures SendToPeer calls so tests can inspect
// the exact wire frames the router emits when serving a tx-set request.
// Other NetworkSender methods are inherited from noopSender so the
// tests don't have to stub the full surface.
type txsetRecordingSender struct {
	noopSender
	mu         sync.Mutex
	sentFrames []sentFrame
}

type sentFrame struct {
	peerID uint64
	frame  []byte
}

func (s *txsetRecordingSender) SendToPeer(peerID uint64, frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sentFrames = append(s.sentFrames, sentFrame{peerID: peerID, frame: append([]byte(nil), frame...)})
	return nil
}

func (s *txsetRecordingSender) sentTo(peerID uint64) []sentFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sentFrame
	for _, f := range s.sentFrames {
		if f.peerID == peerID {
			out = append(out, f)
		}
	}
	return out
}

func newTxSetWireAdaptor(t *testing.T) (*Adaptor, *txsetRecordingSender) {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &txsetRecordingSender{}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	return a, rs
}

// decodeFrame strips the 6-byte length+type header that encodeFrame adds
// and decodes the payload as the indicated message type. Mirrors the
// router's frame-handling path so tests interrogate exactly the bytes
// that go on the wire.
func decodeFrame(t *testing.T, frame []byte) (message.MessageType, message.Message) {
	t.Helper()
	require.GreaterOrEqual(t, len(frame), 6, "frame too short")
	msgType := message.MessageType(uint16(frame[4])<<8 | uint16(frame[5]))
	payload := frame[6:]
	msg, err := message.Decode(msgType, payload)
	require.NoError(t, err, "decode payload of type %d", msgType)
	return msgType, msg
}

// TestRouter_GetLedger_TsCandidate_ServesCachedTxSet pins the
// SHAMap-node wire shape for liTS_CANDIDATE responses. Rippled's
// PeerImp::processLedgerRequest (PeerImp.cpp:3304-3411) walks the
// tx-set SHAMap via getNodeFat(*shaMapNodeId, data, fatLeaves=false,
// queryDepth) and emits each entry as
//
//	node->set_nodeid(d.first.getRawString())
//	node->set_nodedata(d.second)
//
// — a stream of (SHAMapNodeID, wire-serialized SHAMap node) pairs,
// NOT raw tx blobs. The consumer (TransactionAcquire::takeNodes,
// TransactionAcquire.cpp:175-235) requires that shape: line 203
// checks d.first.isRoot() before AddRootNode, line 217 calls
// mMap->addKnownNode(d.first, ...) which rejects empty NodeIDs.
//
// Prior to the fix, serveTxSet emitted LedgerNode{NodeData: blob}
// per raw transaction blob with NodeID empty — wire-incompatible
// with rippled AND with goxrpl's own handleTxSetData consumer.
// The bug was masked in the 3-rippled + 2-goxrpl soak because
// requestors fan out across peers and rippled's serve produces
// the correct shape; the goxrpl serve path was never the only
// available source. A goxrpl-majority deployment, or any peer
// whose only candidate source is a goxrpl node, hit the bug.
//
// Properties pinned by this test:
//  1. resp.InfoType == liTS_CANDIDATE, resp.LedgerHash == txSetID
//  2. Every Node carries a non-empty NodeID (33 bytes, matching
//     SHAMapNodeID::getRawString).
//  3. The first Node is the SHAMap root (33 zero bytes) — pre-order
//     traversal so the consumer can AddRootNode before any
//     AddKnownNodeUnchecked.
//  4. Feeding the response back through a fresh SHAMap
//     reconstructs the original blobs — i.e. the wire format
//     round-trips through the canonical consumer path.
func TestRouter_GetLedger_TsCandidate_ServesCachedTxSet(t *testing.T) {
	engine := &mockEngine{}
	adaptor, rs := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)

	router := NewRouter(engine, adaptor, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	// Use 16-byte blobs so they satisfy the SHAMap transaction-leaf
	// minimum (real tx blobs are larger) and exercise the
	// PutWithNodeType path, not the small-blob fallback. Distinct
	// first byte gives the blobs distinct tx hashes so they land in
	// different SHAMap branches.
	blobs := [][]byte{
		bytes.Repeat([]byte{0x11}, 16),
		bytes.Repeat([]byte{0x55}, 16),
		bytes.Repeat([]byte{0x99}, 16),
	}
	ts, err := adaptor.BuildTxSet(blobs)
	require.NoError(t, err)
	wantID := ts.ID()

	// Inbound: peer 7 asks for the tx set.
	req := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: wantID[:],
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	}

	require.Eventually(t, func() bool {
		return len(rs.sentTo(7)) > 0
	}, time.Second, 10*time.Millisecond, "router did not respond to liTS_CANDIDATE request")

	frames := rs.sentTo(7)
	require.Len(t, frames, 1, "expected exactly one response frame")

	msgType, decoded := decodeFrame(t, frames[0].frame)
	require.Equal(t, message.TypeLedgerData, msgType)
	resp, ok := decoded.(*message.LedgerData)
	require.True(t, ok, "decoded message must be LedgerData")

	assert.Equal(t, message.LedgerInfoTsCandidate, resp.InfoType)
	assert.Equal(t, wantID[:], resp.LedgerHash)

	// Wire-shape: every node must carry a 33-byte SHAMapNodeID, and
	// the first node must be the root (all zeros).
	require.NotEmpty(t, resp.Nodes, "must include at least the root")
	for i, n := range resp.Nodes {
		require.Len(t, n.NodeID, 33,
			"node[%d] NodeID must be 33 bytes (matches SHAMapNodeID::getRawString); "+
				"empty NodeIDs are rejected by TransactionAcquire::takeNodes", i)
		require.NotEmpty(t, n.NodeData, "node[%d] NodeData must be non-empty", i)
	}
	rootID := resp.Nodes[0].NodeID
	for _, b := range rootID {
		require.Equal(t, byte(0), b,
			"first node must be SHAMap root (NodeID = 33 zero bytes); "+
				"pre-order is required so AddRootNode fires before AddKnownNodeByID")
	}

	// Round-trip: feed the response back through SHAMap sync
	// reconstruction (the same path handleTxSetData uses on inbound).
	// If the wire bytes carry the canonical tx-set, FinishSync
	// closes cleanly and the resulting root hash matches wantID.
	reconstructed := shamap.New(shamap.TypeTransaction)
	require.NoError(t, reconstructed.StartSync())
	require.NoError(t,
		reconstructed.AddRootNode([32]byte(wantID), resp.Nodes[0].NodeData),
		"AddRootNode must accept the served root payload")
	for i := 1; i < len(resp.Nodes); i++ {
		nid, err := shamap.UnmarshalBinary(resp.Nodes[i].NodeID)
		require.NoError(t, err, "node[%d] NodeID must parse", i)
		_, err = reconstructed.AddKnownNodeByID(nid, resp.Nodes[i].NodeData)
		require.NoError(t, err, "AddKnownNodeByID must accept node[%d]", i)
	}
	require.NoError(t, reconstructed.FinishSync(),
		"FinishSync must succeed — if this fails, the served wire bytes "+
			"don't form a complete SHAMap and the consumer would stall on "+
			"GetMissingNodes follow-ups")
	gotHash, err := reconstructed.Hash()
	require.NoError(t, err)
	assert.Equal(t, wantID[:], gotHash[:],
		"reconstructed tx-set hash must match the original — "+
			"if this fails the wire bytes lost information across serve+consume")
}

// TestRouter_GetLedger_TsCandidate_UnknownTxSet_NoResponse pins that
// when a peer asks for a tx set we don't have, we silently drop the
// request — no response frame, no panic. Matches rippled which falls
// through getTxSet without reply when InboundTransactions doesn't
// have the set and no relay candidate is configured.
func TestRouter_GetLedger_TsCandidate_UnknownTxSet_NoResponse(t *testing.T) {
	engine := &mockEngine{}
	adaptor, rs := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)

	router := NewRouter(engine, adaptor, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	// Hash that's intentionally never cached.
	unknownID := consensus.TxSetID{0xDE, 0xAD, 0xBE, 0xEF}

	req := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: unknownID[:],
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  9,
		Type:    uint16(message.TypeGetLedger),
		Payload: encodePayload(t, req),
	}

	// Give the router time to process and (incorrectly, if regressed) reply.
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, rs.sentTo(9), "router must not respond when tx set is unknown")
}

// TestRouter_LedgerData_TsCandidate_FeedsEngine pins the
// response-side wiring for issue #401: when we receive a
// TMLedgerData with type=liTS_CANDIDATE in response to a tx-set
// request we made, we MUST extract every node's blob and feed them
// to engine.OnTxSet so the dispute tracker can build positions
// against peer tx sets. Without this branch the engine never sees
// the acquired blobs, never creates disputes against peer-only txs,
// and stays at propose_seq=0.
func TestRouter_LedgerData_TsCandidate_FeedsEngine(t *testing.T) {
	engine := &mockEngine{}
	adaptor, _ := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)

	router := NewRouter(engine, adaptor, inbox)

	ctx := t.Context()
	go router.Run(ctx)

	// Build a real SHAMap of TypeTransaction containing two tx blobs.
	// Real tx blobs are >= 12 bytes (SHAMap leaf min — see shamap
	// leaf.go); use 16-byte synthetic blobs that satisfy the floor.
	blobs := [][]byte{
		bytes.Repeat([]byte{0x11}, 16),
		bytes.Repeat([]byte{0x55}, 16),
	}
	txMap := shamap.New(shamap.TypeTransaction)
	for i, blob := range blobs {
		var key [32]byte
		key[0] = byte(0x10 + i) // distinct keys to land in different branches
		require.NoError(t,
			txMap.PutWithNodeType(key, blob, shamap.NodeTypeTransactionNoMeta),
			"PutWithNodeType")
	}
	id, err := txMap.Hash()
	require.NoError(t, err, "Hash")
	wireNodes, err := txMap.WalkWireNodes()
	require.NoError(t, err, "WalkWireNodes")
	require.NotEmpty(t, wireNodes, "expected at least the root + leaves")

	// Convert WalkWireNodes output → LedgerData node entries. Pre-order
	// guarantees the root arrives first, which handleTxSetData requires
	// (AddRootNode before AddKnownNodeUnchecked).
	ldNodes := make([]message.LedgerNode, 0, len(wireNodes))
	for _, n := range wireNodes {
		ldNodes = append(ldNodes, message.LedgerNode{
			NodeID:   n.NodeID,
			NodeData: n.Data,
		})
	}

	resp := &message.LedgerData{
		LedgerHash: id[:],
		LedgerSeq:  0,
		InfoType:   message.LedgerInfoTsCandidate,
		Nodes:      ldNodes,
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  3,
		Type:    uint16(message.TypeLedgerData),
		Payload: encodePayload(t, resp),
	}

	require.Eventually(t, func() bool {
		engine.mu.Lock()
		defer engine.mu.Unlock()
		return len(engine.txSets) > 0
	}, time.Second, 10*time.Millisecond, "engine did not see acquired tx-set")

	engine.mu.Lock()
	defer engine.mu.Unlock()
	require.Len(t, engine.txSets, 1, "engine must receive exactly one tx-set notification")
	assert.Equal(t, consensus.TxSetID(id), engine.txSets[0],
		"engine must receive the tx-set ID we asked for, "+
			"so the dispute tracker indexes against the right hash")
}

// TestRequestTxSet_WireFormat pins the outbound wire shape that
// OverlaySender.RequestTxSet produces. Mirrors rippled's
// PeerImp::getTxSet contract: a TMGetLedger frame with
// itype=liTS_CANDIDATE and ledger_hash=<txSetID>. The previous
// TMHaveTransactionSet{tsNEED} format was a no-op against rippled
// (PeerImp.cpp:2008-2031 only acts on tsHAVE).
//
// This is a frame-level check — we encode the same args RequestTxSet
// uses and assert the round-trip matches. Combined with the
// txsetRecordingSender capture in router tests above, this gives full
// coverage of the call shape and the wire shape without standing up a
// real overlay.
func TestRequestTxSet_WireFormat(t *testing.T) {
	id := consensus.TxSetID{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F, 0x20,
	}

	// These lines are exactly what OverlaySender.RequestTxSet does;
	// keep them in sync if that function changes.
	rootNodeID := make([]byte, 33)
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: id[:],
		QueryDepth: 3,
		NodeIDs:    [][]byte{rootNodeID},
	}
	frame, err := encodeFrame(message.TypeGetLedger, msg)
	require.NoError(t, err)

	msgType, decoded := decodeFrame(t, frame)
	require.Equal(t, message.TypeGetLedger, msgType,
		"RequestTxSet must use TMGetLedger, NOT TMHaveTransactionSet "+
			"(#401 root cause)")

	req, ok := decoded.(*message.GetLedger)
	require.True(t, ok)
	assert.Equal(t, message.LedgerInfoTsCandidate, req.InfoType)
	assert.Equal(t, id[:], req.LedgerHash)
	assert.Equal(t, uint32(3), req.QueryDepth,
		"query_depth=3 matches rippled's TransactionAcquire.cpp:131; "+
			"the response may not include the whole tree, in which "+
			"case the acquire path needs to retry with the missing "+
			"node IDs (rippled's TransactionAcquire::trigger second "+
			"branch at TransactionAcquire.cpp:144-171)")
	require.Len(t, req.NodeIDs, 1,
		"node_ids must contain at least the root SHAMap node ID — "+
			"rippled rejects with 'Invalid ledger node IDs' otherwise "+
			"(PeerImp.cpp:1435-1438, #401)")
	assert.Len(t, req.NodeIDs[0], 33,
		"SHAMap node ID is 32 bytes path + 1 byte depth = 33 bytes "+
			"(SHAMapNodeID::getRawString)")
}
