package inbound

import (
	"io"
	"log/slog"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

// buildDeepStateSource builds a multi-level state tree and returns its root
// hash, serialized root, and wire nodes.
func buildDeepStateSource(t *testing.T) ([32]byte, []byte, []shamap.WireNode) {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for branch := range byte(4) {
		for sub := range byte(4) {
			for i := range byte(4) {
				var key [32]byte
				key[0] = (branch << 4) | sub
				key[1] = i << 4
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
	return rootHash, rootData, wireNodes
}

// Issue #1143: out-of-order fat-reply nodes (those arriving before their
// ancestor stub is materialized) must NOT be counted as rejects. Delivered
// deepest-first they re-request rather than flood the reject log, and the
// acquisition still converges over a few passes.
func TestGotStateNodes_OutOfOrderNotRejected(t *testing.T) {
	t.Parallel()
	rootHash, rootData, wireNodes := buildDeepStateSource(t)

	hdr := header.LedgerHeader{LedgerIndex: 200, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)
	il := New(ledgerHash, 200, 9, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	nodes := make([]message.LedgerNode, 0, len(wireNodes))
	maxDepth := 0
	for _, w := range wireNodes {
		nid, err := shamap.UnmarshalBinary(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		if nid.IsRoot() {
			continue
		}
		if int(nid.Depth()) > maxDepth {
			maxDepth = int(nid.Depth())
		}
		nodes = append(nodes, message.LedgerNode{NodeID: w.NodeID, NodeData: w.Data})
	}
	if maxDepth < 2 {
		t.Fatalf("test setup too shallow: maxDepth=%d", maxDepth)
	}
	// Deepest-first: every non-frontier node is ahead of its parent stub.
	sort.SliceStable(nodes, func(i, j int) bool {
		a, _ := shamap.UnmarshalBinary(nodes[i].NodeID)
		b, _ := shamap.UnmarshalBinary(nodes[j].NodeID)
		return a.Depth() > b.Depth()
	})

	for round := 0; il.state != StateComplete; round++ {
		if round > maxDepth+2 {
			t.Fatalf("did not converge after %d passes (state=%d)", round, il.state)
		}
		if err := il.GotStateNodes(nodes); err != nil {
			t.Fatalf("GotStateNodes pass %d: %v", round, err)
		}
		if il.rejectCount != 0 {
			t.Fatalf("pass %d: ancestor-gap nodes counted as rejects: rejectCount=%d (%q)",
				round, il.rejectCount, il.lastRejectErr)
		}
	}

	gotHash, err := il.stateMap.Hash()
	if err != nil {
		t.Fatalf("dest hash: %v", err)
	}
	if gotHash != rootHash {
		t.Errorf("reconstructed state hash mismatch: want %x got %x", rootHash[:8], gotHash[:8])
	}
}

// Issue #1143: a genuinely invalid node stops the rest of the reply from being
// harvested, mirroring rippled receiveNode's stop-on-first-bad. Three corrupt
// nodes therefore tally exactly one reject, not three.
func TestGotStateNodes_StopsOnFirstInvalid(t *testing.T) {
	t.Parallel()
	rootHash, rootData, wireNodes := buildDeepStateSource(t)

	var d1 []shamap.WireNode
	for _, w := range wireNodes {
		nid, _ := shamap.UnmarshalBinary(w.NodeID)
		if nid.Depth() == 1 {
			d1 = append(d1, w)
		}
	}
	if len(d1) < 3 {
		t.Fatalf("need >=3 depth-1 nodes, got %d", len(d1))
	}

	hdr := header.LedgerHeader{LedgerIndex: 200, AccountHash: rootHash}
	hdrBytes, ledgerHash := encodeHeader(hdr)
	il := New(ledgerHash, 200, 9, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	// Each node carries a sibling's data: the hash never matches the parent's
	// stored child hash, so every entry is NodeInvalid.
	bad := []message.LedgerNode{
		{NodeID: d1[0].NodeID, NodeData: d1[1].Data},
		{NodeID: d1[1].NodeID, NodeData: d1[2].Data},
		{NodeID: d1[2].NodeID, NodeData: d1[0].Data},
	}
	if err := il.GotStateNodes(bad); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}
	if il.rejectCount != 1 {
		t.Fatalf("stop-on-first-bad: want rejectCount=1, got %d", il.rejectCount)
	}
}
