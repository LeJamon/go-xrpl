package nodestore

import (
	"bytes"
	"container/heap"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

func TestCachePutKeepsCallerOwnership(t *testing.T) {
	cache := NewCache(1, time.Hour)
	node := NewNode(NodeTransaction, Blob("immutable payload"))
	want := append([]byte(nil), node.Data...)

	cache.Put(node)
	node.Data[0] ^= 0xff

	got, ok := cache.Get(node.Hash)
	if !ok {
		t.Fatal("cache miss")
	}
	if got == node {
		t.Fatal("Put stored caller-owned pointer")
	}
	if !bytes.Equal(got.Data, want) {
		t.Fatalf("cached data changed with caller mutation: got %x want %x", got.Data, want)
	}
}

func TestFetchTransfersDecodedNodeToCache(t *testing.T) {
	backend := memorydb.New()
	want := NewNode(NodeTransaction, Blob("stored payload"))
	encoded := encodeNodeData(want)
	if err := backend.Put(want.Hash[:], encoded); err != nil {
		releaseEncodeBuf(encoded)
		t.Fatalf("Put: %v", err)
	}
	releaseEncodeBuf(encoded)

	db := NewKVDatabaseWithConfig(backend, "test", &DatabaseConfig{
		CacheSize: 1,
		CacheTTL:  time.Hour,
	})
	t.Cleanup(func() { _ = db.Close() })

	first, err := db.Fetch(context.Background(), want.Hash)
	if err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	second, err := db.Fetch(context.Background(), want.Hash)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if first != second {
		t.Fatal("decoded node was cloned instead of transferred to the cache")
	}
	if !bytes.Equal(first.Data, want.Data) {
		t.Fatalf("Fetch data = %x, want %x", first.Data, want.Data)
	}
}

func TestFetchDataUncachedDoesNotPopulateNodeCache(t *testing.T) {
	backend := memorydb.New()
	want := NewNode(NodeTransaction, Blob("uncached traversal payload"))
	encoded := encodeNodeData(want)
	if err := backend.Put(want.Hash[:], encoded); err != nil {
		releaseEncodeBuf(encoded)
		t.Fatalf("Put: %v", err)
	}
	releaseEncodeBuf(encoded)

	db := NewKVDatabaseWithConfig(backend, "test", &DatabaseConfig{
		CacheSize: 1,
		CacheTTL:  time.Hour,
	})
	t.Cleanup(func() { _ = db.Close() })

	got, err := db.FetchDataUncached(context.Background(), want.Hash)
	if err != nil {
		t.Fatalf("FetchDataUncached: %v", err)
	}
	if !bytes.Equal(got, want.Data) {
		t.Fatalf("data = %x, want %x", got, want.Data)
	}
	if size := db.Stats().CacheSize; size != 0 {
		t.Fatalf("cache size = %d, want 0", size)
	}
}

func TestFetchCachedNeverFallsThroughToBackend(t *testing.T) {
	backend := memorydb.New()
	want := NewNode(NodeTransaction, Blob("cache-only payload"))
	encoded := encodeNodeData(want)
	if err := backend.Put(want.Hash[:], encoded); err != nil {
		releaseEncodeBuf(encoded)
		t.Fatalf("Put: %v", err)
	}
	releaseEncodeBuf(encoded)

	db := NewKVDatabaseWithConfig(backend, "test", &DatabaseConfig{
		CacheSize: 1,
		CacheTTL:  time.Hour,
	})
	t.Cleanup(func() { _ = db.Close() })

	got, err := db.FetchCached(t.Context(), want.Hash)
	if err != nil {
		t.Fatalf("FetchCached miss: %v", err)
	}
	if got != nil {
		t.Fatal("FetchCached read through to the backend")
	}

	if _, err := db.Fetch(t.Context(), want.Hash); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err = db.FetchCached(t.Context(), want.Hash)
	if err != nil {
		t.Fatalf("FetchCached hit: %v", err)
	}
	if got == nil || !bytes.Equal(got.Data, want.Data) {
		t.Fatalf("FetchCached data = %v, want %x", got, want.Data)
	}
}

func TestCacheSweepRemovesExpiredEntryAheadOfFreshTail(t *testing.T) {
	cache := NewCache(32, time.Hour)
	expired := &Node{Hash: Hash256{0x10}, Type: NodeTransaction, Data: Blob("expired")}
	fresh := &Node{Hash: Hash256{0x20}, Type: NodeTransaction, Data: Blob("fresh")}
	cache.putOwned(expired)
	cache.putOwned(fresh)
	if _, ok := cache.Get(expired.Hash); !ok {
		t.Fatal("expected cache hit while arranging recency")
	}

	shard := cache.shardFor(expired.Hash)
	shard.mu.Lock()
	setCacheEntryExpiryLocked(shard, expired.Hash, time.Now().Add(-time.Hour))
	setCacheEntryExpiryLocked(shard, fresh.Hash, time.Now().Add(time.Hour))
	shard.mu.Unlock()

	if removed := cache.Sweep(); removed != 1 {
		t.Fatalf("Sweep removed %d entries, want 1", removed)
	}
	shard.mu.Lock()
	_, hasExpired := shard.items[expired.Hash]
	_, hasFresh := shard.items[fresh.Hash]
	shard.mu.Unlock()
	if hasExpired || !hasFresh {
		t.Fatalf("cache entries after Sweep: expired=%t fresh=%t", hasExpired, hasFresh)
	}
}

func TestCacheCapacityPrefersExpiredEntryOverFreshLRU(t *testing.T) {
	cache := NewCache(32, time.Hour)
	expired := &Node{Hash: Hash256{0x10}, Type: NodeTransaction, Data: Blob("expired")}
	fresh := &Node{Hash: Hash256{0x20}, Type: NodeTransaction, Data: Blob("fresh")}
	newest := &Node{Hash: Hash256{0x30}, Type: NodeTransaction, Data: Blob("newest")}
	cache.putOwned(expired)
	cache.putOwned(fresh)
	if _, ok := cache.Get(expired.Hash); !ok {
		t.Fatal("expected cache hit while arranging recency")
	}

	shard := cache.shardFor(expired.Hash)
	shard.mu.Lock()
	setCacheEntryExpiryLocked(shard, expired.Hash, time.Now().Add(-time.Hour))
	setCacheEntryExpiryLocked(shard, fresh.Hash, time.Now().Add(time.Hour))
	shard.mu.Unlock()

	cache.putOwned(newest)
	shard.mu.Lock()
	_, hasExpired := shard.items[expired.Hash]
	_, hasFresh := shard.items[fresh.Hash]
	_, hasNewest := shard.items[newest.Hash]
	shard.mu.Unlock()
	if hasExpired || !hasFresh || !hasNewest {
		t.Fatalf("cache entries after capacity insert: expired=%t fresh=%t newest=%t", hasExpired, hasFresh, hasNewest)
	}
	stats := cache.Stats()
	if stats.Expirations != 1 || stats.Evictions != 0 {
		t.Fatalf("cache stats = expirations %d, evictions %d; want 1, 0", stats.Expirations, stats.Evictions)
	}
}

func TestCacheReplacementReordersExpiration(t *testing.T) {
	cache := NewCache(32, time.Hour)
	fresh := &Node{Hash: Hash256{0x10}, Type: NodeTransaction, Data: Blob("fresh")}
	replaced := &Node{Hash: Hash256{0x20}, Type: NodeTransaction, Data: Blob("replaced")}
	cache.putOwned(fresh)
	cache.putOwned(replaced)

	cache.SetTTL(-time.Hour)
	cache.putOwned(replaced)

	if removed := cache.Sweep(); removed != 1 {
		t.Fatalf("Sweep removed %d entries, want 1", removed)
	}
	if _, ok := cache.Get(replaced.Hash); ok {
		t.Fatal("replacement with elapsed TTL remained cached")
	}
	if _, ok := cache.Get(fresh.Hash); !ok {
		t.Fatal("fresh entry expired with replaced entry")
	}
}

func TestCacheConcurrentMutationPreservesIndexes(t *testing.T) {
	cache := NewCache(256, time.Hour)
	const workers = 8
	const operations = 500

	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := range workers {
		go func() {
			defer wg.Done()
			for operation := range operations {
				var hash Hash256
				hash[0] = byte((worker + operation) % cacheShardCount)
				hash[1] = byte(worker)
				hash[2] = byte(operation)
				node := &Node{Hash: hash, Type: NodeTransaction, Data: Blob{byte(operation)}}
				cache.putOwned(node)
				if operation%3 == 0 {
					cache.Get(hash)
				}
				if operation%5 == 0 {
					cache.Remove(hash)
				}
				if operation%17 == 0 {
					cache.Sweep()
				}
			}
		}()
	}
	wg.Wait()

	for _, shard := range cache.shards {
		shard.mu.Lock()
		if len(shard.items) != shard.currentSize || shard.lru.Len() != shard.currentSize || len(shard.expiry) != shard.currentSize {
			t.Fatalf("shard indexes disagree: items=%d lru=%d expiry=%d size=%d", len(shard.items), shard.lru.Len(), len(shard.expiry), shard.currentSize)
		}
		for position, entry := range shard.expiry {
			if entry.expiryPos != position {
				t.Fatalf("expiry position=%d, want %d", entry.expiryPos, position)
			}
			element, ok := shard.items[entry.key]
			if !ok || element.Value.(*cacheEntry) != entry {
				t.Fatalf("expiry entry %x absent from item index", entry.key[:4])
			}
		}
		shard.mu.Unlock()
	}
}

func setCacheEntryExpiryLocked(shard *cacheShard, hash Hash256, expiresAt time.Time) {
	entry := shard.items[hash].Value.(*cacheEntry)
	entry.expiresAt = expiresAt
	heap.Fix(&shard.expiry, entry.expiryPos)
}

func TestDecodeNodeDataTakesOwnership(t *testing.T) {
	want := NewNode(NodeTransaction, Blob("owned payload"))
	borrowed := encodeNodeData(want)
	encoded := append([]byte(nil), borrowed...)
	releaseEncodeBuf(borrowed)

	got, err := decodeNodeData(want.Hash, encoded)
	if err != nil {
		t.Fatalf("decodeNodeData: %v", err)
	}
	got.Data[0] ^= 0xff
	if encoded[nodeEncodingHeaderSize] != got.Data[0] {
		t.Fatal("decodeNodeData copied its caller-owned payload")
	}
}

func BenchmarkCacheInsertion(b *testing.B) {
	node := NewNode(NodeTransaction, make(Blob, 512))

	b.Run("DefensiveCopy", func(b *testing.B) {
		cache := NewCache(1, time.Hour)
		cache.Put(node)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			cache.Put(node)
		}
	})

	b.Run("OwnershipTransfer", func(b *testing.B) {
		cache := NewCache(1, time.Hour)
		cache.putOwned(node)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			cache.putOwned(node)
		}
	})
}
