package shamap

import (
	"testing"
)

func TestTreeNodeCache(t *testing.T) {
	t.Run("NewTreeNodeCache", func(t *testing.T) {
		cache := NewTreeNodeCache(100)
		if cache == nil {
			t.Fatal("NewTreeNodeCache should return non-nil")
		}

		if cache.Size() != 0 {
			t.Errorf("New cache should be empty, got size %d", cache.Size())
		}

		if cache.MaxSize() != 100 {
			t.Errorf("MaxSize should be 100, got %d", cache.MaxSize())
		}
	})

	t.Run("DefaultSize", func(t *testing.T) {
		cache := NewTreeNodeCache(0)
		if cache.MaxSize() != 1024 {
			t.Errorf("Default max size should be 1024, got %d", cache.MaxSize())
		}
	})

	t.Run("PutAndGet", func(t *testing.T) {
		cache := NewTreeNodeCache(100)

		// Create a test node
		item := NewItem([32]byte{1}, []byte{1, 2, 3})
		node, err := NewAccountStateLeafNode(item)
		if err != nil {
			t.Fatalf("Failed to create node: %v", err)
		}

		hash := node.Hash()

		// Put should work
		cache.Put(hash, node)

		if cache.Size() != 1 {
			t.Errorf("Cache size should be 1, got %d", cache.Size())
		}

		// Get should return the node
		retrieved := cache.Get(hash)
		if retrieved == nil {
			t.Fatal("Get should return the node")
		}

		if retrieved.Hash() != hash {
			t.Error("Retrieved node hash mismatch")
		}
	})

	t.Run("GetMiss", func(t *testing.T) {
		cache := NewTreeNodeCache(100)

		var hash [32]byte
		hash[0] = 1

		retrieved := cache.Get(hash)
		if retrieved != nil {
			t.Error("Get should return nil for missing key")
		}
	})

	t.Run("Evict", func(t *testing.T) {
		cache := NewTreeNodeCache(100)

		item := NewItem([32]byte{1}, []byte{1, 2, 3})
		node, _ := NewAccountStateLeafNode(item)
		hash := node.Hash()

		cache.Put(hash, node)
		if cache.Size() != 1 {
			t.Errorf("Cache size should be 1, got %d", cache.Size())
		}

		cache.Evict(hash)
		if cache.Size() != 0 {
			t.Errorf("Cache size should be 0 after evict, got %d", cache.Size())
		}

		if cache.Get(hash) != nil {
			t.Error("Evicted node should not be retrievable")
		}
	})

	t.Run("LRUEviction", func(t *testing.T) {
		cache := NewTreeNodeCache(3)

		// Add 3 nodes
		for i := byte(1); i <= 3; i++ {
			key := [32]byte{i}
			item := NewItem(key, []byte{i})
			node, _ := NewAccountStateLeafNode(item)
			cache.Put(node.Hash(), node)
		}

		if cache.Size() != 3 {
			t.Errorf("Cache size should be 3, got %d", cache.Size())
		}

		// Add 4th node - should evict oldest
		key4 := [32]byte{4}
		item4 := NewItem(key4, []byte{4})
		node4, _ := NewAccountStateLeafNode(item4)
		cache.Put(node4.Hash(), node4)

		if cache.Size() != 3 {
			t.Errorf("Cache size should still be 3, got %d", cache.Size())
		}
	})

	t.Run("Clear", func(t *testing.T) {
		cache := NewTreeNodeCache(100)

		for i := byte(1); i <= 5; i++ {
			key := [32]byte{i}
			item := NewItem(key, []byte{i})
			node, _ := NewAccountStateLeafNode(item)
			cache.Put(node.Hash(), node)
		}

		if cache.Size() != 5 {
			t.Errorf("Cache size should be 5, got %d", cache.Size())
		}

		cache.Clear()

		if cache.Size() != 0 {
			t.Errorf("Cache should be empty after clear, got %d", cache.Size())
		}
	})

	t.Run("Stats", func(t *testing.T) {
		cache := NewTreeNodeCache(100)

		item := NewItem([32]byte{1}, []byte{1})
		node, _ := NewAccountStateLeafNode(item)
		hash := node.Hash()

		// Miss
		cache.Get(hash)

		// Put
		cache.Put(hash, node)

		// Hit
		cache.Get(hash)
		cache.Get(hash)

		hits, misses, size := cache.Stats()
		if hits != 2 {
			t.Errorf("Expected 2 hits, got %d", hits)
		}
		if misses != 1 {
			t.Errorf("Expected 1 miss, got %d", misses)
		}
		if size != 1 {
			t.Errorf("Expected size 1, got %d", size)
		}
	})

	t.Run("HitRate", func(t *testing.T) {
		cache := NewTreeNodeCache(100)

		// Empty cache should have 0 hit rate
		if cache.HitRate() != 0 {
			t.Errorf("Empty cache hit rate should be 0, got %f", cache.HitRate())
		}

		item := NewItem([32]byte{1}, []byte{1})
		node, _ := NewAccountStateLeafNode(item)
		hash := node.Hash()

		cache.Get(hash) // Miss
		cache.Put(hash, node)
		cache.Get(hash) // Hit
		cache.Get(hash) // Hit

		// 2 hits, 1 miss = 2/3 = 0.666...
		rate := cache.HitRate()
		if rate < 0.65 || rate > 0.68 {
			t.Errorf("Hit rate should be ~0.667, got %f", rate)
		}
	})

	t.Run("Contains", func(t *testing.T) {
		cache := NewTreeNodeCache(100)

		item := NewItem([32]byte{1}, []byte{1})
		node, _ := NewAccountStateLeafNode(item)
		hash := node.Hash()

		if cache.Contains(hash) {
			t.Error("Should not contain hash before put")
		}

		cache.Put(hash, node)

		if !cache.Contains(hash) {
			t.Error("Should contain hash after put")
		}
	})

	t.Run("PutNilIgnored", func(t *testing.T) {
		cache := NewTreeNodeCache(100)

		var hash [32]byte
		hash[0] = 1

		cache.Put(hash, nil)

		if cache.Size() != 0 {
			t.Error("Putting nil should not add to cache")
		}
	})
}

func TestFullBelowCache(t *testing.T) {
	t.Run("NewFullBelowCache", func(t *testing.T) {
		cache := NewFullBelowCache(1000)
		if cache == nil {
			t.Fatal("NewFullBelowCache should return non-nil")
		}

		if cache.Size() != 0 {
			t.Errorf("New cache should be empty, got size %d", cache.Size())
		}

		if cache.MaxSize() != 1000 {
			t.Errorf("MaxSize should be 1000, got %d", cache.MaxSize())
		}
	})

	t.Run("DefaultSize", func(t *testing.T) {
		cache := NewFullBelowCache(0)
		if cache.MaxSize() != 65536 {
			t.Errorf("Default max size should be 65536, got %d", cache.MaxSize())
		}
	})

	t.Run("MarkFullAndIsFull", func(t *testing.T) {
		cache := NewFullBelowCache(100)

		var hash [32]byte
		hash[0] = 1

		if cache.IsFull(hash) {
			t.Error("Hash should not be marked full initially")
		}

		cache.MarkFull(hash)

		if !cache.IsFull(hash) {
			t.Error("Hash should be marked full after MarkFull")
		}
	})

	t.Run("Unmark", func(t *testing.T) {
		cache := NewFullBelowCache(100)

		var hash [32]byte
		hash[0] = 1

		cache.MarkFull(hash)
		if !cache.IsFull(hash) {
			t.Error("Hash should be full")
		}

		cache.Unmark(hash)
		if cache.IsFull(hash) {
			t.Error("Hash should not be full after Unmark")
		}
	})

	t.Run("Clear", func(t *testing.T) {
		cache := NewFullBelowCache(100)

		for i := byte(0); i < 10; i++ {
			cache.MarkFull([32]byte{i})
		}

		if cache.Size() != 10 {
			t.Errorf("Cache size should be 10, got %d", cache.Size())
		}

		cache.Clear()

		if cache.Size() != 0 {
			t.Errorf("Cache should be empty after clear, got %d", cache.Size())
		}
	})

	t.Run("Reset", func(t *testing.T) {
		cache := NewFullBelowCache(100)

		for i := byte(0); i < 10; i++ {
			cache.MarkFull([32]byte{i})
		}

		cache.Reset(200)

		if cache.Size() != 0 {
			t.Error("Cache should be empty after reset")
		}

		if cache.MaxSize() != 200 {
			t.Errorf("MaxSize should be 200, got %d", cache.MaxSize())
		}
	})

	t.Run("GetAllFull", func(t *testing.T) {
		cache := NewFullBelowCache(100)

		hashes := make([][32]byte, 5)
		for i := range hashes {
			hashes[i] = [32]byte{byte(i + 1)}
			cache.MarkFull(hashes[i])
		}

		all := cache.GetAllFull()
		if len(all) != 5 {
			t.Errorf("Expected 5 hashes, got %d", len(all))
		}
	})

	t.Run("Touch", func(t *testing.T) {
		cache := NewFullBelowCache(100)

		var parent, child1, child2 [32]byte
		parent[0] = 1
		child1[0] = 2
		child2[0] = 3

		// Touch with missing children should fail
		if cache.Touch(parent, [][32]byte{child1, child2}) {
			t.Error("Touch should fail when children not full")
		}

		// Mark children as full
		cache.MarkFull(child1)
		cache.MarkFull(child2)

		// Touch should now succeed
		if !cache.Touch(parent, [][32]byte{child1, child2}) {
			t.Error("Touch should succeed when all children are full")
		}

		if !cache.IsFull(parent) {
			t.Error("Parent should be marked full after Touch")
		}
	})

	t.Run("TouchAlreadyFull", func(t *testing.T) {
		cache := NewFullBelowCache(100)

		var hash [32]byte
		hash[0] = 1

		cache.MarkFull(hash)

		// Touch on already-full hash should return true
		if !cache.Touch(hash, nil) {
			t.Error("Touch should return true for already-full hash")
		}
	})

	t.Run("EvictionOnOverflow", func(t *testing.T) {
		cache := NewFullBelowCache(10)

		// Fill the cache
		for i := byte(0); i < 10; i++ {
			cache.MarkFull([32]byte{i})
		}

		if cache.Size() != 10 {
			t.Errorf("Cache size should be 10, got %d", cache.Size())
		}

		// Add one more - should trigger eviction
		cache.MarkFull([32]byte{100})

		// Size should be around 5-6 after evicting half
		if cache.Size() > 6 {
			t.Errorf("Cache size should be reduced after eviction, got %d", cache.Size())
		}
	})

	t.Run("MarkFullIdempotent", func(t *testing.T) {
		cache := NewFullBelowCache(100)

		var hash [32]byte
		hash[0] = 1

		cache.MarkFull(hash)
		sizeBefore := cache.Size()

		cache.MarkFull(hash)
		sizeAfter := cache.Size()

		if sizeBefore != sizeAfter {
			t.Error("Marking full twice should not change size")
		}
	})
}
