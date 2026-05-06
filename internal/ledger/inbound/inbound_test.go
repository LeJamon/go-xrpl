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

// TestNeedsMissingNodeIDs_RequestsActualMissingNodes is a regression
// test for issue #395. After GotBase has installed the state-map root,
// NeedsMissingNodeIDs must return path-based NodeIDs of the actual
// missing inner nodes — not just the root NodeID. The previous
// implementation always returned [rootID], which left any subtree
// rooted at depth ≥3 permanently unrequestable (peer responses to a
// root request top out at QueryDepth=2), wedging legacy catch-up with
// the "1 missing node" symptom in the bug report.
func TestNeedsMissingNodeIDs_RequestsActualMissingNodes(t *testing.T) {
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

	// Build a header that points at the source map's root so GotBase
	// will install it as the AccountHash anchor.
	hdr := header.LedgerHeader{
		LedgerIndex: 100,
		AccountHash: rootHash,
	}
	hdrBytes, err := header.AddRaw(hdr, false)
	if err != nil {
		t.Fatalf("addraw header: %v", err)
	}

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

	// Pre-fix this returned a single rootID; the fix must surface real
	// non-root subtree NodeIDs so we can pull deep nodes from peers.
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
