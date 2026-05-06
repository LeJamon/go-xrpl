package shamap

import (
	"testing"
)

func TestSyncFilter(t *testing.T) {
	// Test DefaultSyncFilter
	t.Run("DefaultSyncFilter", func(t *testing.T) {
		filter := &DefaultSyncFilter{}
		var hash [32]byte
		hash[0] = 1

		if !filter.ShouldFetch(hash) {
			t.Error("DefaultSyncFilter should always return true")
		}
	})

	// Test CachingSyncFilter
	t.Run("CachingSyncFilter", func(t *testing.T) {
		inner := &DefaultSyncFilter{}
		filter := NewCachingSyncFilter(inner, 100)

		var hash1, hash2 [32]byte
		hash1[0] = 1
		hash2[0] = 2

		// First call should hit inner
		result1 := filter.ShouldFetch(hash1)
		if !result1 {
			t.Error("First call should return true")
		}

		// Second call should hit cache
		result2 := filter.ShouldFetch(hash1)
		if !result2 {
			t.Error("Cached call should return true")
		}

		// Different hash
		result3 := filter.ShouldFetch(hash2)
		if !result3 {
			t.Error("New hash should return true")
		}
	})
}

func TestMissingNode(t *testing.T) {
	mn := &MissingNode{
		Hash:       [32]byte{1, 2, 3, 4, 5, 6, 7, 8},
		Depth:      5,
		ParentHash: [32]byte{9, 10, 11, 12, 13, 14, 15, 16},
		Branch:     3,
	}

	str := mn.String()
	if str == "" {
		t.Error("MissingNode.String() should return non-empty string")
	}
}

func TestGetMissingNodes(t *testing.T) {
	// Create a complete map
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add some items
	for i := byte(0); i < 10; i++ {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	// Complete map should have no missing nodes
	missing := sMap.GetMissingNodes(100, nil)
	if len(missing) != 0 {
		t.Errorf("Complete map should have no missing nodes, got %d", len(missing))
	}
}

func TestSyncState(t *testing.T) {
	state := NewSyncState()
	if state == nil {
		t.Fatal("NewSyncState should return non-nil")
	}
}

func TestStartAndFinishSync(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Start sync
	if err := sMap.StartSync(); err != nil {
		t.Fatalf("StartSync failed: %v", err)
	}

	if !sMap.IsSyncing() {
		t.Error("Map should be syncing after StartSync")
	}

	// Finish sync on empty map (which is complete)
	if err := sMap.FinishSync(); err != nil {
		t.Fatalf("FinishSync failed: %v", err)
	}

	if sMap.IsSyncing() {
		t.Error("Map should not be syncing after FinishSync")
	}
}

func TestIsComplete(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Empty map is complete
	if !sMap.IsComplete() {
		t.Error("Empty map should be complete")
	}

	// Add items
	var key [32]byte
	key[0] = 1
	if err := sMap.Put(key, make([]byte, 12)); err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	// Map with items should still be complete
	if !sMap.IsComplete() {
		t.Error("Map should be complete after adding items")
	}
}

func TestSyncProgress(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	present, total := sMap.SyncProgress()
	// Empty map should have root
	if total < 0 {
		t.Error("Total should be non-negative")
	}
	if present > total {
		t.Error("Present should not exceed total")
	}

	// Add items
	for i := byte(0); i < 5; i++ {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	present, total = sMap.SyncProgress()
	if present != total {
		t.Errorf("Complete map should have present == total, got %d vs %d", present, total)
	}
}

func TestAddRootNode(t *testing.T) {
	// Create a map and get its serialized root
	sourceMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create source map: %v", err)
	}

	var key [32]byte
	key[0] = 1
	if err := sourceMap.Put(key, make([]byte, 12)); err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	rootHash, err := sourceMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get hash: %v", err)
	}

	rootData, err := sourceMap.SerializeRoot()
	if err != nil {
		t.Fatalf("Failed to serialize root: %v", err)
	}

	// Create new map and add root
	destMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create dest map: %v", err)
	}

	if err := destMap.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode failed: %v", err)
	}

	// Verify root hash matches
	destHash, err := destMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get dest hash: %v", err)
	}

	if destHash != rootHash {
		t.Errorf("Root hash mismatch: expected %x, got %x", rootHash[:8], destHash[:8])
	}
}

func TestAddRootNodeErrors(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create map: %v", err)
	}

	// Empty data should fail
	if err := sMap.AddRootNode([32]byte{}, []byte{}); err == nil {
		t.Error("Empty data should fail")
	}

	// Invalid data should fail
	if err := sMap.AddRootNode([32]byte{}, []byte{1, 2, 3}); err == nil {
		t.Error("Invalid data should fail")
	}
}

// TestGetMissingNodes_PathNodeIDs is a regression test for issue #395.
// GetMissingNodes must populate MissingNode.NodeID with the path-based
// identifier of each missing inner node so the caller can request that
// exact subtree from a peer. Earlier the inbound-ledger acquisition
// only ever requested the root NodeID with QueryDepth=2, which caps
// observable depth at 2; any missing node deeper than that could never
// be filled and catch-up wedged with "1 missing node" forever.
func TestGetMissingNodes_PathNodeIDs(t *testing.T) {
	// Populate a source map with keys whose top nibble fans out across
	// several branches at the root, guaranteeing the root has multiple
	// inner-node children that get reported as missing once we sync
	// only the root into a destination map.
	source, err := New(TypeState)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	for branch := byte(0); branch < 8; branch++ {
		for i := byte(0); i < 4; i++ {
			var key [32]byte
			key[0] = (branch << 4) | i
			if err := source.Put(key, make([]byte, 12)); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}

	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}

	// Sync only the root into the destination — every non-empty branch
	// at depth 1 should now be reported as missing with a non-root
	// NodeID we can ship to a peer.
	dest, err := New(TypeState)
	if err != nil {
		t.Fatalf("New dest: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	missing := dest.GetMissingNodes(64, nil)
	if len(missing) == 0 {
		t.Fatal("expected missing nodes after syncing only root, got none")
	}

	seen := make(map[[33]byte]struct{}, len(missing))
	for _, m := range missing {
		if m.NodeID.IsRoot() {
			t.Errorf("missing node at depth %d should not have root NodeID", m.Depth)
		}
		if int(m.NodeID.Depth()) != m.Depth {
			t.Errorf("NodeID depth %d != MissingNode.Depth %d", m.NodeID.Depth(), m.Depth)
		}
		var k [33]byte
		copy(k[:], m.NodeID.Bytes())
		if _, dup := seen[k]; dup {
			t.Errorf("duplicate NodeID for missing node at depth %d", m.Depth)
		}
		seen[k] = struct{}{}
		if err := m.NodeID.Validate(); err != nil {
			t.Errorf("missing NodeID is malformed: %v", err)
		}
	}
}

func TestAddKnownNodeErrors(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create map: %v", err)
	}

	// Should fail when not syncing
	if err := sMap.AddKnownNode([32]byte{}, []byte{1, 2, 3}); err == nil {
		t.Error("AddKnownNode should fail when not syncing")
	}

	// Start sync
	if err := sMap.StartSync(); err != nil {
		t.Fatalf("StartSync failed: %v", err)
	}

	// Empty data should fail
	if err := sMap.AddKnownNode([32]byte{}, []byte{}); err == nil {
		t.Error("Empty data should fail")
	}
}
