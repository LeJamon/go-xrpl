package shamap

import "testing"

// buildFetchPackTestMap builds a multi-level state SHAMap with deterministic,
// non-zero keys so the tree has inner nodes above the leaves.
func buildFetchPackTestMap(t *testing.T) *SHAMap {
	t.Helper()
	sm, err := New(TypeState)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
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
// emits round-trips through VerifyWireNode — i.e. a consumer can verify each
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
		if !VerifyWireNode(n.Hash, n.Data) {
			t.Errorf("node %d failed VerifyWireNode", i)
		}
	}

	// Wrong hash must be rejected.
	var bad [32]byte
	if VerifyWireNode(bad, nodes[0].Data) {
		t.Error("VerifyWireNode accepted a node under the wrong hash")
	}
	// Tampered data must be rejected.
	corrupt := append([]byte(nil), nodes[len(nodes)-1].Data...)
	corrupt[len(corrupt)-1] ^= 0xFF
	if VerifyWireNode(nodes[len(nodes)-1].Hash, corrupt) {
		t.Error("VerifyWireNode accepted tampered data")
	}
	// Empty data must be rejected.
	if VerifyWireNode(nodes[0].Hash, nil) {
		t.Error("VerifyWireNode accepted empty data")
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

// TestWalkFetchPackNodes_Bounds covers the degenerate inputs.
func TestWalkFetchPackNodes_Bounds(t *testing.T) {
	t.Parallel()
	sm := buildFetchPackTestMap(t)
	if nodes, err := sm.WalkFetchPackNodes(0); err != nil || nodes != nil {
		t.Fatalf("maxNodes=0: got (%v, %v), want (nil, nil)", nodes, err)
	}

	empty, err := New(TypeState)
	if err != nil {
		t.Fatalf("new empty: %v", err)
	}
	nodes, err := empty.WalkFetchPackNodes(10)
	if err != nil {
		t.Fatalf("walk empty: %v", err)
	}
	for i, n := range nodes {
		if !VerifyWireNode(n.Hash, n.Data) {
			t.Errorf("empty-map node %d failed VerifyWireNode", i)
		}
	}
}
