package nodestore_test

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/storage/nodestore"
)

func TestDatabaseWithConfig(t *testing.T) {
	t.Run("DefaultConfig", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		db, err := nodestore.NewDatabaseWithConfig(backend, nil)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		// Should have negative cache
		if db.NegativeCache() == nil {
			t.Error("expected negative cache to be initialized")
		}

		// Should not have batch writer by default
		if db.BatchWriter() != nil {
			t.Error("expected batch writer to be nil by default")
		}
	})

	t.Run("WithNegativeCache", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		config := &nodestore.DatabaseConfig{
			CacheSize:            100,
			CacheTTL:             time.Minute,
			NegativeCacheTTL:     5 * time.Minute,
			NegativeCacheMaxSize: 1000,
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		ctx := context.Background()

		// Fetch non-existent node
		hash := nodestore.ComputeHash256(nodestore.Blob("non-existent"))
		node, err := db.Fetch(ctx, hash)
		if err != nil {
			t.Errorf("Fetch returned error: %v", err)
		}
		if node != nil {
			t.Error("expected nil node")
		}

		// Verify it's in the negative cache
		if !db.NegativeCache().IsMissing(hash) {
			t.Error("hash should be in negative cache")
		}

		// Second fetch should hit negative cache
		node, err = db.Fetch(ctx, hash)
		if err != nil {
			t.Errorf("second Fetch returned error: %v", err)
		}
		if node != nil {
			t.Error("expected nil node")
		}
	})

	t.Run("NegativeCacheClearedOnStore", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		config := &nodestore.DatabaseConfig{
			CacheSize:            100,
			CacheTTL:             time.Minute,
			NegativeCacheTTL:     5 * time.Minute,
			NegativeCacheMaxSize: 1000,
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		ctx := context.Background()

		// Create node but don't store yet
		data := nodestore.Blob("negative cache clear test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		// Fetch to add to negative cache
		fetched, _ := db.Fetch(ctx, node.Hash)
		if fetched != nil {
			t.Error("expected nil before store")
		}

		// Should be in negative cache
		if !db.NegativeCache().IsMissing(node.Hash) {
			t.Error("should be in negative cache")
		}

		// Store the node
		if err := db.Store(ctx, node); err != nil {
			t.Fatalf("Store returned error: %v", err)
		}

		// Should be removed from negative cache
		if db.NegativeCache().IsMissing(node.Hash) {
			t.Error("should not be in negative cache after store")
		}

		// Should be fetchable now
		fetched, err = db.Fetch(ctx, node.Hash)
		if err != nil {
			t.Errorf("Fetch returned error: %v", err)
		}
		if fetched == nil {
			t.Error("expected non-nil node after store")
		}
	})

	t.Run("NegativeCacheClearedOnStoreBatch", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		config := &nodestore.DatabaseConfig{
			CacheSize:            100,
			CacheTTL:             time.Minute,
			NegativeCacheTTL:     5 * time.Minute,
			NegativeCacheMaxSize: 1000,
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		ctx := context.Background()

		// Create nodes
		nodes := make([]*nodestore.Node, 3)
		for i := 0; i < 3; i++ {
			data := nodestore.Blob("batch clear test " + string(rune('A'+i)))
			nodes[i] = nodestore.NewNode(nodestore.NodeTransaction, data)

			// Add to negative cache
			db.Fetch(ctx, nodes[i].Hash)
		}

		// All should be in negative cache
		for _, node := range nodes {
			if !db.NegativeCache().IsMissing(node.Hash) {
				t.Error("should be in negative cache")
			}
		}

		// Store batch
		if err := db.StoreBatch(ctx, nodes); err != nil {
			t.Fatalf("StoreBatch returned error: %v", err)
		}

		// All should be removed from negative cache
		for _, node := range nodes {
			if db.NegativeCache().IsMissing(node.Hash) {
				t.Error("should not be in negative cache after store batch")
			}
		}
	})

	t.Run("WithBatchWriter", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		config := &nodestore.DatabaseConfig{
			CacheSize: 100,
			CacheTTL:  time.Minute,
			BatchWriteConfig: &nodestore.BatchWriteConfig{
				PreallocationSize: 10,
				LimitSize:         100,
				FlushInterval:     10 * time.Millisecond,
			},
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		if db.BatchWriter() == nil {
			t.Error("expected batch writer to be initialized")
		}
	})

	t.Run("StoreAsync", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		config := &nodestore.DatabaseConfig{
			CacheSize: 100,
			CacheTTL:  time.Minute,
			BatchWriteConfig: &nodestore.BatchWriteConfig{
				PreallocationSize: 10,
				LimitSize:         100,
				FlushInterval:     10 * time.Millisecond,
			},
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		ctx := context.Background()

		// Store async
		data := nodestore.Blob("async store test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		resultCh := db.StoreAsync(ctx, node)

		// Wait for result
		err = <-resultCh
		if err != nil {
			t.Errorf("StoreAsync returned error: %v", err)
		}

		// Give time for flush
		time.Sleep(50 * time.Millisecond)

		// Should be fetchable
		fetched, err := db.Fetch(ctx, node.Hash)
		if err != nil {
			t.Errorf("Fetch returned error: %v", err)
		}
		if fetched == nil {
			t.Error("expected non-nil node")
		}
	})

	t.Run("StoreAsyncWithoutBatchWriter", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// No batch writer
		config := &nodestore.DatabaseConfig{
			CacheSize: 100,
			CacheTTL:  time.Minute,
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		ctx := context.Background()

		// StoreAsync should fall back to sync store
		data := nodestore.Blob("async fallback test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		resultCh := db.StoreAsync(ctx, node)

		err = <-resultCh
		if err != nil {
			t.Errorf("StoreAsync returned error: %v", err)
		}

		// Should be fetchable
		fetched, err := db.Fetch(ctx, node.Hash)
		if err != nil {
			t.Errorf("Fetch returned error: %v", err)
		}
		if fetched == nil {
			t.Error("expected non-nil node")
		}
	})

	t.Run("ExtendedStats", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		config := &nodestore.DatabaseConfig{
			CacheSize:            100,
			CacheTTL:             time.Minute,
			NegativeCacheTTL:     5 * time.Minute,
			NegativeCacheMaxSize: 1000,
			BatchWriteConfig: &nodestore.BatchWriteConfig{
				PreallocationSize: 10,
				LimitSize:         100,
				FlushInterval:     10 * time.Millisecond,
			},
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		ctx := context.Background()

		// Perform some operations
		data := nodestore.Blob("extended stats test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)
		db.Store(ctx, node)
		db.Fetch(ctx, node.Hash)

		// Fetch non-existent to trigger negative cache
		db.Fetch(ctx, nodestore.ComputeHash256(nodestore.Blob("non-existent")))
		db.Fetch(ctx, nodestore.ComputeHash256(nodestore.Blob("non-existent"))) // Should hit negative cache

		stats := db.ExtendedStats()

		if stats.Reads < 2 {
			t.Errorf("expected at least 2 reads, got %d", stats.Reads)
		}

		if stats.Writes < 1 {
			t.Errorf("expected at least 1 write, got %d", stats.Writes)
		}

		if stats.NegativeCacheHits < 1 {
			t.Errorf("expected at least 1 negative cache hit, got %d", stats.NegativeCacheHits)
		}
	})

	t.Run("Sweep", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		config := &nodestore.DatabaseConfig{
			CacheSize:            100,
			CacheTTL:             50 * time.Millisecond,
			NegativeCacheTTL:     50 * time.Millisecond,
			NegativeCacheMaxSize: 1000,
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		ctx := context.Background()

		// Store a node to populate positive cache
		data := nodestore.Blob("sweep test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)
		db.Store(ctx, node)

		// Fetch non-existent to populate negative cache
		db.Fetch(ctx, nodestore.ComputeHash256(nodestore.Blob("non-existent for sweep")))

		// Wait for expiration
		time.Sleep(100 * time.Millisecond)

		// Sweep should remove expired entries
		if err := db.Sweep(); err != nil {
			t.Errorf("Sweep returned error: %v", err)
		}
	})

	t.Run("Close", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}

		config := &nodestore.DatabaseConfig{
			CacheSize:            100,
			CacheTTL:             time.Minute,
			NegativeCacheTTL:     5 * time.Minute,
			NegativeCacheMaxSize: 1000,
			BatchWriteConfig: &nodestore.BatchWriteConfig{
				PreallocationSize: 10,
				LimitSize:         100,
				FlushInterval:     10 * time.Millisecond,
			},
		}

		db, err := nodestore.NewDatabaseWithConfig(backend, config)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}

		// Close should close all components
		if err := db.Close(); err != nil {
			t.Errorf("Close returned error: %v", err)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		db, err := nodestore.NewDatabaseWithConfig(backend, nil)
		if err != nil {
			t.Fatalf("failed to create database: %v", err)
		}
		defer db.Close()

		// Create cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		data := nodestore.Blob("cancelled context test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		// Store should fail with cancelled context
		if err := db.Store(ctx, node); err == nil {
			t.Error("expected error for cancelled context")
		}

		// StoreAsync should fail with cancelled context
		resultCh := db.StoreAsync(ctx, node)
		if err := <-resultCh; err == nil {
			t.Error("expected error for cancelled context in StoreAsync")
		}
	})
}

func TestDatabaseConfig(t *testing.T) {
	t.Run("Defaults", func(t *testing.T) {
		config := nodestore.DefaultDatabaseConfig()

		if config.CacheSize <= 0 {
			t.Error("CacheSize should be positive")
		}

		if config.CacheTTL <= 0 {
			t.Error("CacheTTL should be positive")
		}

		if config.NegativeCacheTTL <= 0 {
			t.Error("NegativeCacheTTL should be positive")
		}

		if config.NegativeCacheMaxSize <= 0 {
			t.Error("NegativeCacheMaxSize should be positive")
		}

		// BatchWriteConfig should be nil by default
		if config.BatchWriteConfig != nil {
			t.Error("BatchWriteConfig should be nil by default")
		}
	})
}

// TestCacheIsolation pins the Node immutability contract at the cache
// boundary. Before the fix, Cache.Put stored the caller's *Node by
// reference, so a later mutation of the original Data slice silently
// corrupted every reader the cache subsequently handed the same
// pointer to. The fix takes a defensive deep copy on Put so subsequent
// caller-side mutations are invisible to other readers.
func TestCacheIsolation(t *testing.T) {
	cache := nodestore.NewCache(8, time.Minute)

	original := &nodestore.Node{
		Type: nodestore.NodeAccount,
		Hash: nodestore.Hash256{0x01},
		Data: nodestore.Blob{0xAA, 0xBB, 0xCC, 0xDD},
	}
	cache.Put(original)

	// Mutate the caller's buffer after Put. The cache must not observe
	// the mutation.
	original.Data[0] = 0xFF
	original.Data[1] = 0xFF

	got, ok := cache.Get(original.Hash)
	if !ok {
		t.Fatal("expected cache hit after Put")
	}
	expected := nodestore.Blob{0xAA, 0xBB, 0xCC, 0xDD}
	for i, b := range expected {
		if got.Data[i] != b {
			t.Fatalf("cache entry was corrupted by post-Put mutation: byte %d got %#x want %#x", i, got.Data[i], b)
		}
	}
}

// TestCacheConcurrentReadersImmutable verifies that two readers
// cache-hitting on the same hash see a stable Data buffer for the
// duration of their reads. Combined with the documented immutability
// contract this is the property NodeStore consumers depend on.
func TestCacheConcurrentReadersImmutable(t *testing.T) {
	cache := nodestore.NewCache(8, time.Minute)
	node := &nodestore.Node{
		Type: nodestore.NodeAccount,
		Hash: nodestore.Hash256{0x42},
		Data: nodestore.Blob{1, 2, 3, 4, 5, 6, 7, 8},
	}
	cache.Put(node)

	a, ok := cache.Get(node.Hash)
	if !ok {
		t.Fatal("expected cache hit (a)")
	}
	b, ok := cache.Get(node.Hash)
	if !ok {
		t.Fatal("expected cache hit (b)")
	}

	// The two readers can legitimately alias the same entry — the
	// contract says they must not mutate it. Verify both see identical
	// data and that the underlying buffer is not the caller's original
	// slice (Cache.Put took ownership via Clone).
	for i := range node.Data {
		if a.Data[i] != b.Data[i] || a.Data[i] != node.Data[i] {
			t.Fatalf("readers disagree at byte %d: a=%#x b=%#x orig=%#x", i, a.Data[i], b.Data[i], node.Data[i])
		}
	}
	if &a.Data[0] == &node.Data[0] {
		t.Fatal("Cache.Put must not retain caller's Data slice by reference")
	}
}
