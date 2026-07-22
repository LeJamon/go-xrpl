package nodestore

import (
	"bytes"
	"context"
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
	shard.items[expired.Hash].Value.(*cacheEntry).expiresAt = time.Now().Add(-time.Hour)
	shard.items[fresh.Hash].Value.(*cacheEntry).expiresAt = time.Now().Add(time.Hour)
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
	shard.items[expired.Hash].Value.(*cacheEntry).expiresAt = time.Now().Add(-time.Hour)
	shard.items[fresh.Hash].Value.(*cacheEntry).expiresAt = time.Now().Add(time.Hour)
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
