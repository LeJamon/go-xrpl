package nodestore

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

type cacheTestBudget struct {
	mu    sync.Mutex
	limit uint64
	used  uint64
}

func (b *cacheTestBudget) TryReserve(bytes uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.limit-b.used {
		return false
	}
	b.used += bytes
	return true
}

func (b *cacheTestBudget) Release(bytes uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.used {
		panic("cache test budget release exceeds reservation")
	}
	b.used -= bytes
}

func (b *cacheTestBudget) Used() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func TestNodeCacheBudgetWeightsAndClose(t *testing.T) {
	budget := &cacheTestBudget{limit: 1 << 20}
	cache := newNodeCache(1, time.Hour, budget)
	first := testNode(NodeTransaction, []byte("first"), 0)
	cache.Put(first)
	if budget.Used() == 0 {
		t.Fatal("cache admission did not reserve payload and metadata")
	}
	firstCharge := budget.Used()

	// A replacement is reserved before the old entry is released. With no
	// spare allowance it is denied and the useful old alias remains available.
	budget.mu.Lock()
	budget.limit = firstCharge
	budget.mu.Unlock()
	replacement := &Node{Hash: first.Hash, Type: NodeTransaction, Data: []byte("larger replacement")}
	cache.Put(replacement)
	got, ok := cache.Get(first.Hash)
	if !ok || string(got.Data) != string(first.Data) {
		t.Fatalf("denied replacement changed cache: got=%q ok=%t", got.Data, ok)
	}

	cache.Remove(first.Hash)
	if budget.Used() == 0 {
		t.Fatal("entry eviction released retained structural capacity prematurely")
	}
	cache.Close()
	if got := budget.Used(); got != 0 {
		t.Fatalf("cache close leaked reservation: %d bytes", got)
	}
}

func TestNodeCacheDeniedGrowthEvictsOnlyLocalColdEntry(t *testing.T) {
	budget := &cacheTestBudget{limit: nodeCacheEntryOverhead + nodeCacheStructuralBytes + 8}
	cache := newNodeCache(1, time.Hour, budget)
	first := &Node{Hash: Hash256{0x01}, Type: NodeTransaction, Data: []byte("first")}
	second := &Node{Hash: Hash256{0x01, 0x01}, Type: NodeTransaction, Data: []byte("second")}
	cache.Put(first)
	cache.Put(second)
	if _, ok := cache.Get(first.Hash); ok {
		t.Fatal("old local entry survived a denied-growth replacement")
	}
	if got, ok := cache.Get(second.Hash); !ok || string(got.Data) != "second" {
		t.Fatalf("new admitted entry = %q, ok=%t", got.Data, ok)
	}
	cache.Close()
}

func TestNodeCachePutDefensiveCopyAndFetchAlias(t *testing.T) {
	cache := newNodeCache(1, time.Hour)
	node := testNode(NodeTransaction, []byte("immutable"), 0)
	cache.Put(node)
	node.Data[0] ^= 0xff
	got, ok := cache.Get(node.Hash)
	if !ok {
		t.Fatal("cache miss after Put")
	}
	if string(got.Data) != "immutable" {
		t.Fatalf("cached defensive copy changed: %q", got.Data)
	}
	alias, ok := cache.Get(node.Hash)
	if !ok || alias != got {
		t.Fatal("cache lookup did not preserve immutable alias semantics")
	}
}

func TestNodeCacheExpiryReturnsPayloadBeforeClose(t *testing.T) {
	budget := &cacheTestBudget{limit: 1 << 20}
	cache := newNodeCache(1, time.Nanosecond, budget)
	node := testNode(NodeTransaction, []byte("expires"), 0)
	cache.Put(node)
	if budget.Used() == 0 {
		t.Fatal("expiry test cache did not admit node")
	}
	time.Sleep(time.Millisecond)
	if _, ok := cache.Get(node.Hash); ok {
		t.Fatal("expired node remained resident")
	}
	if got := budget.Used(); got != nodeCacheStructuralBytes {
		t.Fatalf("expiry charge = %d, want retained structural charge %d", got, nodeCacheStructuralBytes)
	}
	cache.Close()
	if got := budget.Used(); got != 0 {
		t.Fatalf("expiry close leaked charge: %d", got)
	}
}

func TestDatabaseRejectsBudgetOnNegativeCache(t *testing.T) {
	budget := &cacheTestBudget{limit: 1 << 20}
	_, err := NewKVDatabase(memorydb.New(), DatabaseConfig{
		NegativeCache: CacheConfig{
			Enabled:      true,
			MaxEntries:   1,
			TTL:          time.Hour,
			MemoryBudget: budget,
		},
	})
	if err == nil {
		t.Fatal("negative cache accepted positive-cache memory budget")
	}
}
