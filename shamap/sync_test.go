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
		if err := sMap.Put(key, []byte{i}); err != nil {
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
	if err := sMap.Put(key, []byte{1}); err != nil {
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
		if err := sMap.Put(key, []byte{i}); err != nil {
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
	if err := sourceMap.Put(key, []byte{1, 2, 3}); err != nil {
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
