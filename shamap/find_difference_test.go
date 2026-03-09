package shamap

import (
	"testing"
)

func TestFindDifference(t *testing.T) {
	t.Run("IdenticalMaps", func(t *testing.T) {
		map1, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create map1: %v", err)
		}

		map2, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create map2: %v", err)
		}

		// Add same items to both
		for i := byte(0); i < 5; i++ {
			var key [32]byte
			key[0] = i
			if err := map1.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put to map1: %v", err)
			}
			if err := map2.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put to map2: %v", err)
			}
		}

		keys, err := map1.FindDifference(map2)
		if err != nil {
			t.Fatalf("FindDifference failed: %v", err)
		}

		if len(keys) != 0 {
			t.Errorf("Identical maps should have no differences, got %d", len(keys))
		}
	})

	t.Run("EmptyMaps", func(t *testing.T) {
		map1, _ := New(TypeState)
		map2, _ := New(TypeState)

		keys, err := map1.FindDifference(map2)
		if err != nil {
			t.Fatalf("FindDifference failed: %v", err)
		}

		if len(keys) != 0 {
			t.Errorf("Empty maps should have no differences, got %d", len(keys))
		}
	})

	t.Run("OneEmpty", func(t *testing.T) {
		map1, _ := New(TypeState)
		map2, _ := New(TypeState)

		// Add items to map1 only
		for i := byte(0); i < 3; i++ {
			var key [32]byte
			key[0] = i
			if err := map1.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		keys, err := map1.FindDifference(map2)
		if err != nil {
			t.Fatalf("FindDifference failed: %v", err)
		}

		if len(keys) != 3 {
			t.Errorf("Expected 3 differences, got %d", len(keys))
		}
	})

	t.Run("DisjointSets", func(t *testing.T) {
		map1, _ := New(TypeState)
		map2, _ := New(TypeState)

		// Add different items to each
		for i := byte(0); i < 3; i++ {
			var key1, key2 [32]byte
			key1[0] = i
			key2[0] = i + 10

			if err := map1.Put(key1, []byte{i}); err != nil {
				t.Fatalf("Failed to put to map1: %v", err)
			}
			if err := map2.Put(key2, []byte{i + 10}); err != nil {
				t.Fatalf("Failed to put to map2: %v", err)
			}
		}

		keys, err := map1.FindDifference(map2)
		if err != nil {
			t.Fatalf("FindDifference failed: %v", err)
		}

		// 3 from map1 + 3 from map2 = 6 differences
		if len(keys) != 6 {
			t.Errorf("Expected 6 differences, got %d", len(keys))
		}
	})

	t.Run("ModifiedValue", func(t *testing.T) {
		map1, _ := New(TypeState)
		map2, _ := New(TypeState)

		var key [32]byte
		key[0] = 1

		if err := map1.Put(key, []byte{1, 2, 3}); err != nil {
			t.Fatalf("Failed to put to map1: %v", err)
		}
		if err := map2.Put(key, []byte{4, 5, 6}); err != nil {
			t.Fatalf("Failed to put to map2: %v", err)
		}

		keys, err := map1.FindDifference(map2)
		if err != nil {
			t.Fatalf("FindDifference failed: %v", err)
		}

		if len(keys) != 1 {
			t.Errorf("Expected 1 difference (modified value), got %d", len(keys))
		}

		if len(keys) > 0 && keys[0] != key {
			t.Error("Difference should be the modified key")
		}
	})

	t.Run("MixedDifferences", func(t *testing.T) {
		map1, _ := New(TypeState)
		map2, _ := New(TypeState)

		// Common keys
		for i := byte(0); i < 3; i++ {
			var key [32]byte
			key[0] = i
			if err := map1.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
			if err := map2.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		// Only in map1
		var onlyMap1 [32]byte
		onlyMap1[0] = 100
		if err := map1.Put(onlyMap1, []byte{100}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}

		// Only in map2
		var onlyMap2 [32]byte
		onlyMap2[0] = 200
		if err := map2.Put(onlyMap2, []byte{200}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}

		// Modified
		var modified [32]byte
		modified[0] = 50
		if err := map1.Put(modified, []byte{1}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
		if err := map2.Put(modified, []byte{2}); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}

		keys, err := map1.FindDifference(map2)
		if err != nil {
			t.Fatalf("FindDifference failed: %v", err)
		}

		// 1 only in map1 + 1 only in map2 + 1 modified = 3
		if len(keys) != 3 {
			t.Errorf("Expected 3 differences, got %d", len(keys))
		}
	})

	t.Run("NilOther", func(t *testing.T) {
		map1, _ := New(TypeState)

		_, err := map1.FindDifference(nil)
		if err == nil {
			t.Error("FindDifference with nil should fail")
		}
	})

	t.Run("LargerMaps", func(t *testing.T) {
		map1, _ := New(TypeState)
		map2, _ := New(TypeState)

		// Add 50 items to each with some overlap
		for i := byte(0); i < 50; i++ {
			var key [32]byte
			key[0] = i
			if err := map1.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		for i := byte(25); i < 75; i++ {
			var key [32]byte
			key[0] = i
			if err := map2.Put(key, []byte{i}); err != nil {
				t.Fatalf("Failed to put: %v", err)
			}
		}

		keys, err := map1.FindDifference(map2)
		if err != nil {
			t.Fatalf("FindDifference failed: %v", err)
		}

		// 0-24 only in map1 (25 items)
		// 50-74 only in map2 (25 items)
		// 25-49 common (no diff)
		// Total: 50 differences
		if len(keys) != 50 {
			t.Errorf("Expected 50 differences, got %d", len(keys))
		}
	})
}

func TestFindDifferenceSymmetry(t *testing.T) {
	// FindDifference should return same keys regardless of order
	map1, _ := New(TypeState)
	map2, _ := New(TypeState)

	var key1, key2 [32]byte
	key1[0] = 1
	key2[0] = 2

	map1.Put(key1, []byte{1})
	map2.Put(key2, []byte{2})

	diff1, err := map1.FindDifference(map2)
	if err != nil {
		t.Fatalf("FindDifference 1 failed: %v", err)
	}

	diff2, err := map2.FindDifference(map1)
	if err != nil {
		t.Fatalf("FindDifference 2 failed: %v", err)
	}

	if len(diff1) != len(diff2) {
		t.Errorf("Symmetric calls should return same number of keys: %d vs %d", len(diff1), len(diff2))
	}

	// Both should have same keys (order may differ)
	keys1 := make(map[[32]byte]bool)
	for _, k := range diff1 {
		keys1[k] = true
	}

	for _, k := range diff2 {
		if !keys1[k] {
			t.Errorf("Key %x in diff2 but not in diff1", k[:8])
		}
	}
}

func TestCollectAllKeys(t *testing.T) {
	sMap, _ := New(TypeState)

	// Add items
	for i := byte(0); i < 10; i++ {
		var key [32]byte
		key[0] = i
		sMap.Put(key, []byte{i})
	}

	sMap.mu.RLock()
	defer sMap.mu.RUnlock()

	keys, err := sMap.collectAllKeysUnsafe(sMap.root)
	if err != nil {
		t.Fatalf("collectAllKeysUnsafe failed: %v", err)
	}

	if len(keys) != 10 {
		t.Errorf("Expected 10 keys, got %d", len(keys))
	}
}

func TestCollectAllKeysExcept(t *testing.T) {
	sMap, _ := New(TypeState)

	// Add items
	for i := byte(0); i < 5; i++ {
		var key [32]byte
		key[0] = i
		sMap.Put(key, []byte{i})
	}

	var exceptKey [32]byte
	exceptKey[0] = 2

	sMap.mu.RLock()
	defer sMap.mu.RUnlock()

	keys, err := sMap.collectAllKeysExceptUnsafe(sMap.root, exceptKey)
	if err != nil {
		t.Fatalf("collectAllKeysExceptUnsafe failed: %v", err)
	}

	if len(keys) != 4 {
		t.Errorf("Expected 4 keys, got %d", len(keys))
	}

	// Verify except key is not in result
	for _, k := range keys {
		if k == exceptKey {
			t.Error("Except key should not be in result")
		}
	}
}
