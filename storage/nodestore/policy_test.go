package nodestore

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

func putTestNode(t *testing.T, store *memorydb.MemDatabase, node *Node) {
	t.Helper()
	encoded := encodeNodeData(node)
	if err := store.Put(node.Hash[:], encoded); err != nil {
		releaseEncodeBuf(encoded)
		t.Fatalf("Put: %v", err)
	}
	releaseEncodeBuf(encoded)
}

func policyNode(data string, first byte) *Node {
	node := &Node{
		Type: NodeTransaction,
		Hash: Hash256{first},
		Data: []byte(data),
	}
	return node
}

func policyDatabase(t *testing.T, store *memorydb.MemDatabase, maxEntries int) *KVDatabase {
	t.Helper()
	db := testDatabase(t, store, positiveCacheConfig(maxEntries))
	return db
}

func TestFetchWithPolicyPeerMissDoesNotAdmit(t *testing.T) {
	store := memorydb.New()
	node := policyNode("peer miss", 0x31)
	putTestNode(t, store, node)
	db := policyDatabase(t, store, 16)

	got, err := db.FetchWithPolicy(context.Background(), node.Hash, ReadPeer)
	if err != nil {
		t.Fatalf("FetchWithPolicy: %v", err)
	}
	if got == nil || !bytes.Equal(got.Data, node.Data) {
		t.Fatalf("peer fetch = %#v, want node", got)
	}
	if got := db.Stats(); got.Peer.Lookups != 1 || got.Peer.Hits != 0 || got.Peer.Bypasses != 1 {
		t.Fatalf("peer stats = %+v, want one miss and bypass", got.Peer)
	}
	if got := db.Stats().CacheSize; got != 0 {
		t.Fatalf("peer miss cache size = %d, want 0", got)
	}
}

func TestFetchWithPolicyCountsDisabledCacheLookups(t *testing.T) {
	store := memorydb.New()
	db := testDatabase(t, store, noCacheConfig())
	missing := Hash256{0x33}

	for _, policy := range []ReadPolicy{ReadForegroundUncached, ReadPeer} {
		if node, err := db.FetchWithPolicy(context.Background(), missing, policy); err != nil || node != nil {
			t.Fatalf("FetchWithPolicy(%d) = (%#v, %v), want miss", policy, node, err)
		}
	}
	stats := db.Stats()
	if stats.Foreground.Lookups != 1 || stats.Foreground.Bypasses != 1 || stats.Peer.Lookups != 1 || stats.Peer.Bypasses != 1 {
		t.Fatalf("disabled-cache policy stats = foreground=%+v peer=%+v", stats.Foreground, stats.Peer)
	}
}

func TestFetchWithPolicyForegroundUncachedDoesNotAdmit(t *testing.T) {
	store := memorydb.New()
	node := policyNode("uncached", 0x32)
	putTestNode(t, store, node)
	db := policyDatabase(t, store, 16)

	for range 2 {
		got, err := db.FetchWithPolicy(context.Background(), node.Hash, ReadForegroundUncached)
		if err != nil {
			t.Fatalf("FetchWithPolicy: %v", err)
		}
		if got == nil {
			t.Fatal("FetchWithPolicy returned nil")
		}
	}
	stats := db.Stats()
	if stats.Foreground.Lookups != 2 || stats.Foreground.Hits != 0 || stats.Foreground.Bypasses != 2 {
		t.Fatalf("foreground uncached stats = %+v, want two misses and bypasses", stats.Foreground)
	}
	if stats.Foreground.Admissions != 0 || stats.CacheSize != 0 {
		t.Fatalf("foreground uncached admitted node: stats=%+v size=%d", stats.Foreground, stats.CacheSize)
	}
}

func TestFetchWithPolicyPeerHitDoesNotPromote(t *testing.T) {
	store := memorydb.New()
	first := policyNode("first", 0x40)
	second := policyNode("second", 0x40)
	second.Hash[1] = 1
	third := policyNode("third", 0x40)
	third.Hash[1] = 2
	for _, node := range []*Node{first, second, third} {
		putTestNode(t, store, node)
	}
	db := policyDatabase(t, store, 32) // two entries per shard

	for _, node := range []*Node{first, second} {
		if _, err := db.Fetch(context.Background(), node.Hash); err != nil {
			t.Fatalf("foreground Fetch(%q): %v", node.Data, err)
		}
	}
	if _, err := db.FetchWithPolicy(context.Background(), first.Hash, ReadPeer); err != nil {
		t.Fatalf("peer FetchWithPolicy: %v", err)
	}
	if _, err := db.Fetch(context.Background(), third.Hash); err != nil {
		t.Fatalf("foreground Fetch(third): %v", err)
	}
	if _, found := db.cache.Get(first.Hash); found {
		t.Fatal("peer hit promoted the old LRU entry")
	}
	if _, found := db.cache.Get(second.Hash); !found {
		t.Fatal("peer hit displaced the newer foreground entry")
	}
}

func TestFetchWithPolicyBudgetStatsSeparateRecoveryAndTerminalDenial(t *testing.T) {
	t.Run("recovered", func(t *testing.T) {
		payload := []byte("first")
		budget := &cacheTestBudget{limit: nodeCacheEntryOverhead + nodeCacheStructuralBytes + uint64(len(payload)) + 1}
		store := memorydb.New()
		first := policyNode(string(payload), 0x50)
		second := policyNode("second", 0x50)
		second.Hash[1] = 1
		putTestNode(t, store, first)
		putTestNode(t, store, second)
		db, err := NewKVDatabase(store, DatabaseConfig{PositiveCache: CacheConfig{
			Enabled: true, MaxEntries: 16, TTL: time.Hour, MemoryBudget: budget,
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Fetch(context.Background(), first.Hash); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Fetch(context.Background(), second.Hash); err != nil {
			t.Fatal(err)
		}
		stats := db.Stats().Foreground
		if stats.ReservationDenials == 0 || stats.AdmissionDenials != 0 || stats.Evictions == 0 {
			t.Fatalf("recovered budget stats = %+v", stats)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		budget := &cacheTestBudget{limit: nodeCacheEntryOverhead + nodeCacheStructuralBytes + 5}
		store := memorydb.New()
		first := policyNode("first", 0x51)
		second := policyNode("payload larger than budget", 0x51)
		second.Hash[1] = 1
		putTestNode(t, store, first)
		putTestNode(t, store, second)
		db, err := NewKVDatabase(store, DatabaseConfig{PositiveCache: CacheConfig{
			Enabled: true, MaxEntries: 16, TTL: time.Hour, MemoryBudget: budget,
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Fetch(context.Background(), first.Hash); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Fetch(context.Background(), second.Hash); err != nil {
			t.Fatal(err)
		}
		stats := db.Stats().Foreground
		if stats.ReservationDenials == 0 || stats.Admissions != 1 || stats.AdmissionDenials != 1 || stats.Evictions != 1 {
			t.Fatalf("terminal budget stats = %+v", stats)
		}
	})
}
