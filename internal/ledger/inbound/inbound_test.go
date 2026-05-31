package inbound

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Regression for issue #395: after GotBase, NeedsMissingNodeIDs must
// surface path-based NodeIDs of the actual missing inner nodes, not
// just the root NodeID.
func TestNeedsMissingNodeIDs_RequestsActualMissingNodes(t *testing.T) {
	t.Parallel()
	source, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("new source map: %v", err)
	}
	for branch := byte(0); branch < 8; branch++ {
		for i := byte(0); i < 4; i++ {
			var key [32]byte
			key[0] = (branch << 4) | i
			if err := source.Put(key, make([]byte, 12)); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}

	hdr := header.LedgerHeader{
		LedgerIndex: 100,
		AccountHash: rootHash,
	}
	hdrBytes, ledgerHash := encodeHeader(hdr)

	il := New(ledgerHash, 100, 7, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := il.GotBase([]message.LedgerNode{
		{NodeData: hdrBytes},
		{NodeData: rootData},
	}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	ids := il.NeedsMissingNodeIDs()
	if len(ids) == 0 {
		t.Fatal("expected missing node IDs after GotBase, got none")
	}

	root := shamap.NewRootNodeID().Bytes()
	if len(ids) == 1 && bytes.Equal(ids[0], root) {
		t.Fatalf("regression: NeedsMissingNodeIDs returned only rootID; deep nodes can never be requested")
	}
	for i, raw := range ids {
		nid, err := shamap.UnmarshalBinary(raw)
		if err != nil {
			t.Fatalf("nodeID %d: malformed: %v", i, err)
		}
		if nid.IsRoot() {
			t.Errorf("nodeID %d: expected non-root NodeID, got root", i)
		}
		if err := nid.Validate(); err != nil {
			t.Errorf("nodeID %d: validate: %v", i, err)
		}
	}
}

// Regression for issue #413 applied to the state-map sync path:
// GotStateNodes must accept peer-supplied nodes that land beneath
// stubs we haven't yet expanded. The pre-fix AddKnownNodeUnchecked
// hash-search silently dropped them; AddKnownNodeByID descends by
// NodeID and succeeds.
func TestGotStateNodes_DeepNodesUnderStubs(t *testing.T) {
	t.Parallel()
	source, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("new source map: %v", err)
	}
	for branch := byte(0); branch < 4; branch++ {
		for sub := byte(0); sub < 4; sub++ {
			for i := byte(0); i < 4; i++ {
				var key [32]byte
				key[0] = (branch << 4) | sub
				key[1] = i << 4
				// TypeState rejects zero keys at the leaf; keep every key
				// non-zero by stuffing a sentinel tail.
				key[31] = 0xA5
				if err := source.Put(key, []byte{branch, sub, i, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}); err != nil {
					t.Fatalf("put: %v", err)
				}
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk wire nodes: %v", err)
	}
	if len(wireNodes) < 3 {
		t.Fatalf("test setup gave %d wire nodes; need a multi-level tree", len(wireNodes))
	}

	hdr := header.LedgerHeader{LedgerIndex: 200, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)
	il := New(ledgerHash, 200, 9, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := il.GotBase([]message.LedgerNode{
		{NodeData: hdrBytes},
		{NodeData: rootData},
	}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	nodes := make([]message.LedgerNode, 0, len(wireNodes))
	for _, w := range wireNodes {
		nodes = append(nodes, message.LedgerNode{NodeID: w.NodeID, NodeData: w.Data})
	}
	if err := il.GotStateNodes(nodes); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}

	if il.state != StateComplete {
		t.Fatalf("state = %d, want StateComplete", il.state)
	}
	gotHash, err := il.stateMap.Hash()
	if err != nil {
		t.Fatalf("dest hash: %v", err)
	}
	if gotHash != rootHash {
		t.Errorf("reconstructed state hash mismatch: want %x got %x", rootHash[:8], gotHash[:8])
	}
}

// Regression for issue #674: GotStateNodes/GotTransactionNodes must reject a
// peer reply whose node count exceeds hardMaxReplyNodes, mirroring rippled's
// PeerImp::onMessage(TMLedgerData) guard (PeerImp.cpp:1628, Tuning.h:42). The
// router translates the returned error into an IncPeerBadData charge.
func TestGotNodes_RejectOverHardMaxReplyNodes(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0x01}, 300, 11, slog.New(slog.NewTextHandler(io.Discard, nil)))

	over := make([]message.LedgerNode, hardMaxReplyNodes+1)
	for _, tc := range []struct {
		name string
		got  error
	}{
		{"GotStateNodes", il.GotStateNodes(over)},
		{"GotTransactionNodes", il.GotTransactionNodes(over)},
	} {
		if tc.got == nil || !strings.Contains(tc.got.Error(), "hardMaxReplyNodes") {
			t.Errorf("%s(over cap): got %v, want hardMaxReplyNodes rejection", tc.name, tc.got)
		}
	}

	// A reply exactly at the cap must pass the count guard (it may still fail
	// later for unrelated acquisition-state reasons, but not with the cap error).
	atCap := make([]message.LedgerNode, hardMaxReplyNodes)
	for _, tc := range []struct {
		name string
		got  error
	}{
		{"GotStateNodes", il.GotStateNodes(atCap)},
		{"GotTransactionNodes", il.GotTransactionNodes(atCap)},
	} {
		if tc.got != nil && strings.Contains(tc.got.Error(), "hardMaxReplyNodes") {
			t.Errorf("%s(at cap): unexpectedly rejected by count guard: %v", tc.name, tc.got)
		}
	}
}

// Regression for the empty-reply half of rippled's TMLedgerData guard
// (PeerImp.cpp:1628, nodes_size() <= 0): GotStateNodes/GotTransactionNodes must
// reject a peer reply carrying no nodes so the router charges badData.
func TestGotNodes_RejectEmptyReply(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0x02}, 300, 11, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, tc := range []struct {
		name string
		got  error
	}{
		{"GotStateNodes", il.GotStateNodes(nil)},
		{"GotTransactionNodes", il.GotTransactionNodes(nil)},
	} {
		if tc.got == nil || !strings.Contains(tc.got.Error(), "no nodes") {
			t.Errorf("%s(empty): got %v, want no-nodes rejection", tc.name, tc.got)
		}
	}
}

// GotBase must also enforce rippled's per-message node cap (PeerImp.cpp:1628):
// the guard runs at message ingress for every ledger info type, base included.
func TestGotBase_RejectOverHardMaxReplyNodes(t *testing.T) {
	t.Parallel()
	il := New([32]byte{0x03}, 300, 11, slog.New(slog.NewTextHandler(io.Discard, nil)))

	err := il.GotBase(make([]message.LedgerNode, hardMaxReplyNodes+1))
	if err == nil || !strings.Contains(err.Error(), "hardMaxReplyNodes") {
		t.Fatalf("GotBase(over cap): got %v, want hardMaxReplyNodes rejection", err)
	}
	if il.state != StateFailed {
		t.Errorf("GotBase(over cap): state = %d, want StateFailed", il.state)
	}
}

// Regression for issue #577: the classic acquisition path must recompute the
// header hash and reject a peer that returns a header whose true hash differs
// from the requested hash, mirroring rippled's takeHeader (InboundLedger.cpp:830).
func TestGotBase_RejectsHeaderHashMismatch(t *testing.T) {
	t.Parallel()
	source, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("new source map: %v", err)
	}
	var key [32]byte
	key[0] = 0x12
	key[31] = 0xA5
	if err := source.Put(key, make([]byte, 12)); err != nil {
		t.Fatalf("put: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}

	hdr := header.LedgerHeader{LedgerIndex: 300, AccountHash: rootHash}
	hdrBytes, trueHash := encodeHeader(hdr)

	// Ask for a hash that the supplied header does not hash to.
	wrongHash := trueHash
	wrongHash[0] ^= 0xFF

	il := New(wrongHash, 300, 11, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err = il.GotBase([]message.LedgerNode{
		{NodeData: hdrBytes},
		{NodeData: rootData},
	})
	if err == nil {
		t.Fatal("GotBase accepted a header whose hash does not match the requested hash")
	}
	if il.state != StateFailed {
		t.Fatalf("state = %d, want StateFailed", il.state)
	}
}

// A header that hashes to the requested value but reports a different sequence
// than the one we asked for must also be rejected (takeHeader seq check).
func TestGotBase_RejectsSeqMismatch(t *testing.T) {
	t.Parallel()
	source, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("new source map: %v", err)
	}
	var key [32]byte
	key[0] = 0x34
	key[31] = 0xA5
	if err := source.Put(key, make([]byte, 12)); err != nil {
		t.Fatalf("put: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}

	hdr := header.LedgerHeader{LedgerIndex: 400, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)

	// Request the correct hash but a sequence the header doesn't carry.
	il := New(ledgerHash, 401, 12, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err = il.GotBase([]message.LedgerNode{
		{NodeData: hdrBytes},
		{NodeData: rootData},
	})
	if err == nil {
		t.Fatal("GotBase accepted a header whose seq does not match the requested seq")
	}
	if il.state != StateFailed {
		t.Fatalf("state = %d, want StateFailed", il.state)
	}
}

// When acquiring by hash alone (seq 0), GotBase must adopt the verified header's
// sequence, mirroring rippled's takeHeader (InboundLedger.cpp:839-840).
func TestGotBase_AdoptsSeqWhenZero(t *testing.T) {
	t.Parallel()
	source, err := shamap.New(shamap.TypeState)
	if err != nil {
		t.Fatalf("new source map: %v", err)
	}
	var key [32]byte
	key[0] = 0x56
	key[31] = 0xA5
	if err := source.Put(key, make([]byte, 12)); err != nil {
		t.Fatalf("put: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}

	hdr := header.LedgerHeader{LedgerIndex: 500, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)

	il := New(ledgerHash, 0, 13, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := il.GotBase([]message.LedgerNode{
		{NodeData: hdrBytes},
		{NodeData: rootData},
	}); err != nil {
		t.Fatalf("GotBase rejected a valid header acquired by hash: %v", err)
	}
	if il.seq != 500 {
		t.Fatalf("seq = %d, want 500 adopted from header", il.seq)
	}
}
