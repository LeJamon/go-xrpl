package shamap

import (
	"context"
	"testing"
)

// buildFetchPackTestMap builds a multi-level state SHAMap with deterministic,
// non-zero keys so the tree has inner nodes above the leaves.
func buildFetchPackTestMap(t *testing.T) *SHAMap {
	t.Helper()
	sm := New(TypeState)
	for branch := byte(0); branch < 4; branch++ {
		for sub := byte(0); sub < 4; sub++ {
			for i := byte(0); i < 4; i++ {
				var key [32]byte
				key[0] = (branch << 4) | sub
				key[1] = i << 4
				key[31] = 0xA5 // keep the leaf key non-zero (TypeState rejects zero)
				if err := sm.Put(key, []byte{branch, sub, i, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}); err != nil {
					t.Fatalf("put: %v", err)
				}
			}
		}
	}
	if _, err := sm.Hash(); err != nil {
		t.Fatalf("hash: %v", err)
	}
	return sm
}

// TestWalkFetchPackNodes_AllNodesVerify checks that every node the serve side
// emits round-trips through VerifyFetchPackNode — i.e. a consumer can verify each
// node against its advertised hash — and that tampering is rejected.
func TestWalkFetchPackNodes_AllNodesVerify(t *testing.T) {
	t.Parallel()
	sm := buildFetchPackTestMap(t)

	nodes, err := sm.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(nodes) < 3 {
		t.Fatalf("want a multi-level tree, got %d nodes", len(nodes))
	}
	for i, n := range nodes {
		if !VerifyFetchPackNode(n.Hash, n.Data) {
			t.Errorf("node %d failed VerifyFetchPackNode", i)
		}
	}

	// Wrong hash must be rejected.
	var bad [32]byte
	if VerifyFetchPackNode(bad, nodes[0].Data) {
		t.Error("VerifyFetchPackNode accepted a node under the wrong hash")
	}
	// Tampered data must be rejected.
	corrupt := append([]byte(nil), nodes[len(nodes)-1].Data...)
	corrupt[len(corrupt)-1] ^= 0xFF
	if VerifyFetchPackNode(nodes[len(nodes)-1].Hash, corrupt) {
		t.Error("VerifyFetchPackNode accepted tampered data")
	}
	// Empty data must be rejected.
	if VerifyFetchPackNode(nodes[0].Hash, nil) {
		t.Error("VerifyFetchPackNode accepted empty data")
	}
}

// TestWalkFetchPackNodes_RespectsCapAndOrder checks the maxNodes cap and that
// the walk is pre-order (root first), so a truncated pack is a connected prefix.
func TestWalkFetchPackNodes_RespectsCapAndOrder(t *testing.T) {
	t.Parallel()
	sm := buildFetchPackTestMap(t)

	all, err := sm.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatalf("walk all: %v", err)
	}
	capped, err := sm.WalkFetchPackNodes(3)
	if err != nil {
		t.Fatalf("walk capped: %v", err)
	}
	if len(capped) != 3 {
		t.Fatalf("cap not honored: got %d, want 3", len(capped))
	}
	for i := range capped {
		if capped[i].Hash != all[i].Hash {
			t.Errorf("node %d: capped walk diverges from full pre-order walk", i)
		}
	}
	rootHash, err := sm.Hash()
	if err != nil {
		t.Fatalf("root hash: %v", err)
	}
	if capped[0].Hash != rootHash {
		t.Errorf("first walked node is not the root: got %x want %x", capped[0].Hash[:8], rootHash[:8])
	}
}

func TestWalkFetchPackNodesContextBoundedStopsBeforeByteOverflow(t *testing.T) {
	sm := New(TypeState)
	for i := byte(1); i <= 4; i++ {
		var key [32]byte
		key[0] = i
		if err := sm.Put(key, []byte{i, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := sm.WalkFetchPackNodes(32)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 2 {
		t.Fatalf("walk returned %d nodes, want at least 2", len(all))
	}
	budget := int64(len(all[0].Data))
	got, complete, err := sm.WalkFetchPackNodesContextBounded(context.Background(), 32, budget)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("byte-limited walk reported complete")
	}
	if len(got) != 1 {
		t.Fatalf("byte-limited walk returned %d nodes, want 1", len(got))
	}
	if int64(len(got[0].Data)) > budget {
		t.Fatalf("walk exceeded byte budget: %d > %d", len(got[0].Data), budget)
	}
}

func TestWalkFetchPackNodesContextBoundedEmptyMap(t *testing.T) {
	nodes, complete, err := New(TypeState).WalkFetchPackNodesContextBounded(context.Background(), 32, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("empty map walk reported incomplete")
	}
	if len(nodes) != 0 {
		t.Fatalf("empty map walk returned %d nodes", len(nodes))
	}
}

// TestWalkFetchPackNodes_Bounds covers the degenerate inputs.
func TestWalkFetchPackNodes_Bounds(t *testing.T) {
	t.Parallel()
	sm := buildFetchPackTestMap(t)
	if nodes, err := sm.WalkFetchPackNodes(0); err != nil || nodes != nil {
		t.Fatalf("maxNodes=0: got (%v, %v), want (nil, nil)", nodes, err)
	}

	empty := New(TypeState)
	nodes, err := empty.WalkFetchPackNodes(10)
	if err != nil {
		t.Fatalf("walk empty: %v", err)
	}
	for i, n := range nodes {
		if !VerifyFetchPackNode(n.Hash, n.Data) {
			t.Errorf("empty-map node %d failed VerifyFetchPackNode", i)
		}
	}
}

func TestWalkFetchPackNodes_LoadsPersistedDescendants(t *testing.T) {
	source := buildFetchPackTestMap(t)
	expected, err := source.WalkFetchPackNodes(1 << 20)
	if err != nil {
		t.Fatalf("source walk: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	family := newMemoryFamily()
	if err := flushToFamily(source, family); err != nil {
		t.Fatalf("flush: %v", err)
	}
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}
	for branch := range BranchFactor {
		if child, _, present := backed.tree.root.LoadChild(branch); present && child != nil {
			t.Fatalf("branch %d was loaded before fetch-pack walk", branch)
		}
	}
	got, err := backed.WalkFetchPackNodesContext(context.Background(), 1<<20)
	if err != nil {
		t.Fatalf("backed walk: %v", err)
	}
	if len(got) != len(expected) {
		t.Fatalf("backed walk returned %d nodes, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i].Hash != expected[i].Hash {
			t.Fatalf("node %d hash = %x, want %x", i, got[i].Hash[:8], expected[i].Hash[:8])
		}
	}
}
