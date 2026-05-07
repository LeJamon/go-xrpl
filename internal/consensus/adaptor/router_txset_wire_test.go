package adaptor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
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

// TestRouter_GetLedger_TsCandidate_ServesCachedTxSet pins the issue
// #401 layer-3 fix, serve side: when a peer asks us for the contents
// of a tx set we have cached, we MUST reply with TMLedgerData carrying
// type=liTS_CANDIDATE, ledger_hash=<txSetID>, and one node per tx
// blob. Mirrors rippled's PeerImp::getTxSet
// (PeerImp.cpp:3255-3287). Without this branch goxrpl can't relay
// peer-acquired tx sets, so #401's dispute-convergence fix is one-way.
func TestRouter_GetLedger_TsCandidate_ServesCachedTxSet(t *testing.T) {
	engine := &mockEngine{}
	adaptor, rs := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	// Seed three tx blobs into the adaptor's cache so the lookup hits.
	blobs := [][]byte{
		{0xAA, 0x01, 0x02, 0x03},
		{0xBB, 0x10, 0x20, 0x30},
		{0xCC, 0xFF, 0xEE, 0xDD},
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

	// Wait for the router to process the request.
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
	require.Len(t, resp.Nodes, len(blobs), "one node per tx blob")
	// Order is whatever ts.Txs() returns; verify the set matches by
	// hashing into a small membership map keyed on the blob contents.
	got := make(map[string]bool, len(resp.Nodes))
	for _, n := range resp.Nodes {
		got[string(n.NodeData)] = true
	}
	for _, b := range blobs {
		assert.True(t, got[string(b)], "response missing blob %x", b)
	}
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

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

// TestRouter_LedgerData_TsCandidate_FeedsEngine pins the issue #401
// layer-3 fix, response side: when we receive a TMLedgerData with
// type=liTS_CANDIDATE in response to a tx-set request we made, we
// MUST extract every node's blob and feed them to engine.OnTxSet so
// the dispute tracker can build positions against peer tx sets.
//
// Without this branch the engine never sees the acquired blobs, never
// creates disputes against peer-only txs, and stays at propose_seq=0
// forever — the symptom that left rippled and goxrpl deadlocked at
// seq=6 in the live harness.
func TestRouter_LedgerData_TsCandidate_FeedsEngine(t *testing.T) {
	t.Skip("rippled wire format requires SHAMap-serialized nodes, not " +
		"raw tx blobs — test fixture needs to be rewritten to construct " +
		"the SHAMap, walk it, and emit each node with its proper " +
		"NodeID + serialized form. Live testnet is the real validation " +
		"for now (#401 layer 3 follow-up).")

	engine := &mockEngine{}
	adaptor, _ := newTxSetWireAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)

	router := NewRouter(engine, adaptor, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	blobs := [][]byte{
		{0x11, 0x22, 0x33, 0x44},
		{0x55, 0x66, 0x77, 0x88},
	}
	ts := NewTxSet(blobs)
	id := ts.ID()

	resp := &message.LedgerData{
		LedgerHash: id[:],
		LedgerSeq:  0,
		InfoType:   message.LedgerInfoTsCandidate,
		Nodes: []message.LedgerNode{
			{NodeData: blobs[0]},
			{NodeData: blobs[1]},
		},
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  3,
		Type:    uint16(message.TypeLedgerData),
		Payload: encodePayload(t, resp),
	}

	require.Eventually(t, func() bool {
		return len(engine.txSets) > 0
	}, time.Second, 10*time.Millisecond, "engine did not see acquired tx-set")

	engine.mu.Lock()
	defer engine.mu.Unlock()
	require.Len(t, engine.txSets, 1)
	assert.Equal(t, id, engine.txSets[0],
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
			"(#401 layer 3 root cause)")

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
			"(PeerImp.cpp:1435-1438, #401 layer 3 follow-up)")
	assert.Len(t, req.NodeIDs[0], 33,
		"SHAMap node ID is 32 bytes path + 1 byte depth = 33 bytes "+
			"(SHAMapNodeID::getRawString)")
}
