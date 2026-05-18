package inbound

import (
	"bytes"
	"io"
	"log/slog"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/shamap"
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
	hdrBytes := header.AddRaw(hdr, false)

	var ledgerHash [32]byte
	ledgerHash[0] = 0xAA

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
	hdrBytes := header.AddRaw(hdr, false)

	var ledgerHash [32]byte
	ledgerHash[0] = 0xBB
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
