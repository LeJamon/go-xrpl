package nodestore_test

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

func TestNegativeCache(t *testing.T) {
	t.Run("Creation", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)
		if cache == nil {
			t.Fatal("NewNegativeCache returned nil")
		}

		if cache.Size() != 0 {
			t.Errorf("expected empty cache, got size %d", cache.Size())
		}
	})

	t.Run("MarkMissingAndCheck", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)

		hash := nodestore.ComputeHash256(nodestore.Blob("missing node"))

		// Should not be missing initially
		if cache.IsMissing(hash) {
			t.Error("hash should not be marked as missing initially")
		}

		// Mark as missing
		cache.MarkMissing(hash)

		// Should be missing now
		if !cache.IsMissing(hash) {
			t.Error("hash should be marked as missing")
		}

		// Size should be 1
		if cache.Size() != 1 {
			t.Errorf("expected size 1, got %d", cache.Size())
		}
	})

	t.Run("Remove", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)

		hash := nodestore.ComputeHash256(nodestore.Blob("to be removed"))

		cache.MarkMissing(hash)
		if !cache.IsMissing(hash) {
			t.Fatal("hash should be marked as missing")
		}

		// Remove
		cache.Remove(hash)

		if cache.IsMissing(hash) {
			t.Error("hash should not be missing after removal")
		}
	})

	t.Run("Expiration", func(t *testing.T) {
		// Use a very short TTL
		cache := nodestore.NewNegativeCache(50 * time.Millisecond)

		hash := nodestore.ComputeHash256(nodestore.Blob("expiring"))

		cache.MarkMissing(hash)
		if !cache.IsMissing(hash) {
			t.Fatal("hash should be marked as missing")
		}

		// Wait for expiration
		time.Sleep(100 * time.Millisecond)

		// Should be expired now
		if cache.IsMissing(hash) {
			t.Error("hash should have expired")
		}
	})

	t.Run("Sweep", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(50 * time.Millisecond)

		// Add multiple entries
		for i := range 5 {
			hash := nodestore.ComputeHash256(nodestore.Blob("sweep test " + string(rune('A'+i))))
			cache.MarkMissing(hash)
		}

		if cache.Size() != 5 {
			t.Fatalf("expected 5 entries, got %d", cache.Size())
		}

		// Wait for expiration
		time.Sleep(100 * time.Millisecond)

		// Sweep
		removed := cache.Sweep()

		if removed != 5 {
			t.Errorf("expected to remove 5 entries, removed %d", removed)
		}

		if cache.Size() != 0 {
			t.Errorf("expected 0 entries after sweep, got %d", cache.Size())
		}
	})

	t.Run("Clear", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)

		// Add multiple entries
		for i := range 5 {
			hash := nodestore.ComputeHash256(nodestore.Blob("clear test " + string(rune('A'+i))))
			cache.MarkMissing(hash)
		}

		if cache.Size() == 0 {
			t.Fatal("cache should have entries")
		}

		// Clear
		cache.Clear()

		if cache.Size() != 0 {
			t.Errorf("expected 0 entries after clear, got %d", cache.Size())
		}
	})

	t.Run("MaxSizeEviction", func(t *testing.T) {
		config := &nodestore.NegativeCacheConfig{
			TTL:     5 * time.Minute,
			MaxSize: 10,
		}
		cache := nodestore.NewNegativeCacheWithConfig(config)

		// Add more entries than max size
		for i := range 20 {
			hash := nodestore.ComputeHash256(nodestore.Blob("eviction test " + string(rune(i))))
			cache.MarkMissing(hash)
		}

		// Size should be at or near max size
		if cache.Size() > 10 {
			t.Errorf("expected size <= 10, got %d", cache.Size())
		}
	})

	t.Run("Stats", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)

		hash1 := nodestore.ComputeHash256(nodestore.Blob("stats test 1"))
		hash2 := nodestore.ComputeHash256(nodestore.Blob("stats test 2"))

		// Mark one as missing
		cache.MarkMissing(hash1)

		// Check for missing (hit)
		cache.IsMissing(hash1)

		// Check for not missing (miss)
		cache.IsMissing(hash2)

		stats := cache.Stats()

		if stats.Insertions < 1 {
			t.Error("expected at least 1 insertion")
		}

		if stats.Hits < 1 {
			t.Error("expected at least 1 hit")
		}

		if stats.Misses < 1 {
			t.Error("expected at least 1 miss")
		}

		if stats.Size != 1 {
			t.Errorf("expected size 1, got %d", stats.Size)
		}
	})

	t.Run("HitRate", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)

		hash := nodestore.ComputeHash256(nodestore.Blob("hit rate test"))
		cache.MarkMissing(hash)

		// 2 hits
		cache.IsMissing(hash)
		cache.IsMissing(hash)

		// 2 misses
		cache.IsMissing(nodestore.ComputeHash256(nodestore.Blob("miss1")))
		cache.IsMissing(nodestore.ComputeHash256(nodestore.Blob("miss2")))

		stats := cache.Stats()

		// Should be 50% hit rate
		hitRate := stats.HitRate()
		if hitRate < 45 || hitRate > 55 {
			t.Errorf("expected hit rate around 50%%, got %.2f%%", hitRate)
		}
	})

	t.Run("SetTTL", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)

		// Change TTL
		cache.SetTTL(10 * time.Minute)

		stats := cache.Stats()
		if stats.TTL != 10*time.Minute {
			t.Errorf("expected TTL 10m, got %v", stats.TTL)
		}
	})

	t.Run("SetMaxSize", func(t *testing.T) {
		config := &nodestore.NegativeCacheConfig{
			TTL:     5 * time.Minute,
			MaxSize: 100,
		}
		cache := nodestore.NewNegativeCacheWithConfig(config)

		// Add some entries
		for i := range 50 {
			hash := nodestore.ComputeHash256(nodestore.Blob("maxsize test " + string(rune(i))))
			cache.MarkMissing(hash)
		}

		// Reduce max size
		cache.SetMaxSize(20)

		// Should evict entries
		if cache.Size() > 20 {
			t.Errorf("expected size <= 20 after SetMaxSize, got %d", cache.Size())
		}
	})

	t.Run("Close", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)

		hash := nodestore.ComputeHash256(nodestore.Blob("close test"))
		cache.MarkMissing(hash)

		// Close
		if err := cache.Close(); err != nil {
			t.Errorf("Close returned error: %v", err)
		}

		// Operations after close should not panic
		if cache.IsMissing(hash) {
			t.Error("IsMissing should return false after close")
		}

		// MarkMissing should be a no-op
		cache.MarkMissing(hash)
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)

		const goroutines = 10
		const opsPerGoroutine = 100

		var wg sync.WaitGroup
		wg.Add(goroutines)

		for g := range goroutines {
			go func(id int) {
				defer wg.Done()

				for i := range opsPerGoroutine {
					hash := nodestore.ComputeHash256(nodestore.Blob("concurrent " + string(rune('A'+id)) + string(rune('0'+i%10))))

					// Mix of operations
					cache.MarkMissing(hash)
					cache.IsMissing(hash)
					if i%10 == 0 {
						cache.Remove(hash)
					}
				}
			}(g)
		}

		wg.Wait()

		// Cache should be in a consistent state
		_ = cache.Size()
		_ = cache.Stats()
	})
}

func TestNegativeCacheSweeper(t *testing.T) {
	t.Run("AutomaticSweep", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(50 * time.Millisecond)

		// Add some entries
		for i := range 5 {
			hash := nodestore.ComputeHash256(nodestore.Blob("sweeper test " + string(rune('A'+i))))
			cache.MarkMissing(hash)
		}

		if cache.Size() != 5 {
			t.Fatalf("expected 5 entries, got %d", cache.Size())
		}

		// Start sweeper
		sweeper := nodestore.NewNegativeCacheSweeper(cache, 30*time.Millisecond)
		sweeper.Start()

		// Wait for entries to expire and be swept
		time.Sleep(150 * time.Millisecond)

		// Stop sweeper
		sweeper.Stop()

		// Entries should be swept
		if cache.Size() != 0 {
			t.Errorf("expected 0 entries after automatic sweep, got %d", cache.Size())
		}
	})

	t.Run("StopSweeper", func(t *testing.T) {
		cache := nodestore.NewNegativeCache(5 * time.Minute)
		sweeper := nodestore.NewNegativeCacheSweeper(cache, 10*time.Millisecond)

		sweeper.Start()
		time.Sleep(50 * time.Millisecond)
		sweeper.Stop()

		// Should not panic when stopping again or accessing cache
		_ = cache.Size()
	})
}

func TestNegativeCacheStats(t *testing.T) {
	t.Run("String", func(t *testing.T) {
		stats := nodestore.NegativeCacheStats{
			Hits:        100,
			Misses:      50,
			Insertions:  200,
			Expirations: 10,
			Evictions:   5,
			Size:        185,
			MaxSize:     1000,
			TTL:         5 * time.Minute,
		}

		s := stats.String()

		if s == "" {
			t.Error("Stats.String() should not be empty")
		}

		// Should contain key metrics
		if !containsString(s, "185") {
			t.Error("String should contain size")
		}
		if !containsString(s, "100") {
			t.Error("String should contain hits")
		}
	})
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[0:len(substr)] == substr || containsString(s[1:], substr)))
}
