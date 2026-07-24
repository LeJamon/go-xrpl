package shamap

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// ErrStoreBelowMinimum reports a write rejected by the online-delete floor.
var ErrStoreBelowMinimum = errors.New("shamap: ledger is below the minimum online sequence")

// NodeStoreFamily implements the Family interface by delegating to a nodestore.Database.
// This is the production-quality Family implementation, matching rippled's NodeFamily
// which wraps its NodeStore Database.
//
// Prefix-format serialized data (4-byte hash prefix + node content) is stored directly
// as Node.Data — the nodestore treats it as opaque bytes. This matches rippled's approach
// where the hash prefix is stored alongside the node data in the NodeStore.
//
// For tests: use NewMemoryNodeStoreFamily() — in-memory, zero disk I/O.
// For production: use NewPebbleNodeStoreFamily() with a persistent path.
type NodeStoreFamily struct {
	db        nodestore.Database
	fullBelow *FullBelowCache
	durable   durableReadCoalescer
	storeMu   sync.RWMutex
	minimum   atomic.Uint32
}

// NewNodeStoreFamily creates a Family backed by the given nodestore.Database.
// The Database should already be opened and configured with caching.
func NewNodeStoreFamily(db nodestore.Database) *NodeStoreFamily {
	return &NodeStoreFamily{db: db, fullBelow: NewFullBelowCache()}
}

// FullBelowCache returns the cache shared by every SHAMap backed by this
// family.
func (f *NodeStoreFamily) FullBelowCache() *FullBelowCache {
	return f.fullBelow
}

// NewMemoryNodeStoreFamily creates a Family backed by an in-memory kvstore.
// Uses a MemDatabase (matching geth's test pattern) wrapped with
// a KVDatabaseImpl providing LRU positive cache and negative cache.
func NewMemoryNodeStoreFamily() *NodeStoreFamily {
	store := memorydb.New()
	db := nodestore.NewKVDatabase(store, "memory", 2000, time.Hour)
	return NewNodeStoreFamily(db)
}

// NewPebbleNodeStoreFamily creates a Family backed by PebbleDB on disk.
// Data persists to disk; the caches bound RAM usage. For production.
//
// The two cache budgets are independent units and must be passed separately:
//   - blockCacheMB sizes Pebble's block cache (decompressed SSTable blocks) in
//     MiB, avoiding disk reads.
//   - nodeCacheItems caps the positive LRU of decoded nodes as a COUNT of
//     entries (not bytes), avoiding the Pebble lookup + deserialize on a hit.
func NewPebbleNodeStoreFamily(path string, blockCacheMB, nodeCacheItems int) (*NodeStoreFamily, error) {
	store, err := kvpebble.New(path, blockCacheMB*1024*1024, 500, false)
	if err != nil {
		return nil, err
	}

	dbConfig := &nodestore.DatabaseConfig{
		CacheSize:            nodeCacheItems,
		CacheTTL:             time.Hour,
		NegativeCacheTTL:     5 * time.Minute,
		NegativeCacheMaxSize: 100000,
	}
	db := nodestore.NewKVDatabaseWithConfig(store, "pebble("+path+")", dbConfig)
	return NewNodeStoreFamily(db), nil
}

// Fetch retrieves a node's serialized data (prefix format) by its SHAMap hash.
// Returns nil, nil if the node is not found (matching the Family contract).
func (f *NodeStoreFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	node, err := f.db.Fetch(ctx, nodestore.Hash256(hash))
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, nil
	}
	return node.Data, nil
}

// FetchCached returns serialized node data only when the decoded node is
// already resident in the NodeStore cache.
func (f *NodeStoreFamily) FetchCached(ctx context.Context, hash [32]byte) ([]byte, error) {
	cached, ok := f.db.(interface {
		FetchCached(context.Context, nodestore.Hash256) (*nodestore.Node, error)
	})
	if !ok {
		return nil, nil
	}
	node, err := cached.FetchCached(ctx, nodestore.Hash256(hash))
	if err != nil || node == nil {
		return nil, err
	}
	return node.Data, nil
}

// FetchDurable reads directly from the backing store without populating the
// decoded-node cache. Missing-node verification has a multi-million-node
// one-shot working set, for which LRU insertion only displaces useful entries.
func (f *NodeStoreFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.durable.fetch(ctx, hash, f.fetchDurable)
}

func (f *NodeStoreFamily) fetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if raw, ok := f.db.(interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	}); ok {
		return raw.FetchDataUncached(ctx, nodestore.Hash256(hash))
	}
	return f.Fetch(ctx, hash)
}

// StoreBatch persists a batch of serialized nodes to the nodestore.
// Each FlushEntry's Data contains prefix-format bytes which are stored directly
// as Node.Data (opaque to the nodestore). The Hash is set from FlushEntry.Hash
// (SHA-512Half, NOT recomputed as SHA-256).
func (f *NodeStoreFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	if len(entries) == 0 {
		return nil
	}
	f.storeMu.RLock()
	defer f.storeMu.RUnlock()
	floor := f.minimum.Load()

	nodes := make([]*nodestore.Node, 0, len(entries))
	for _, e := range entries {
		if floor != 0 && e.LedgerSeq < floor {
			return ErrStoreBelowMinimum
		}
		nodeType := nodestore.NodeAccount
		if e.MapType == TypeTransaction {
			nodeType = nodestore.NodeTransaction
		}
		nodes = append(nodes, &nodestore.Node{
			Hash:      nodestore.Hash256(e.Hash),
			Data:      e.Data,
			Type:      nodeType,
			LedgerSeq: e.LedgerSeq,
		})
	}
	return f.db.StoreBatch(ctx, nodes)
}

// SetMinimumLedgerSeq waits for older family writes to finish, then rejects
// new writes below the online-delete boundary.
func (f *NodeStoreFamily) SetMinimumLedgerSeq(seq uint32) {
	f.storeMu.Lock()
	if seq > f.minimum.Load() {
		f.minimum.Store(seq)
	}
	f.storeMu.Unlock()
}

// Sweep removes expired entries from the nodestore's caches.
// Should be called periodically (e.g., on each ledger close) to bound memory usage.
// This matches rippled's pattern of calling sweep() on NodeFamily.
func (f *NodeStoreFamily) Sweep() error {
	f.fullBelow.Sweep()
	return f.db.Sweep()
}

// ResetFullBelow invalidates completeness marks after the backing store is
// rotated or otherwise replaces its node set.
func (f *NodeStoreFamily) ResetFullBelow() {
	f.fullBelow.Bump()
}

// BeginPrune invalidates full-below marks and blocks missing-node walks until
// the returned function is called after the backing-store mutation.
func (f *NodeStoreFamily) BeginPrune() func() {
	return f.fullBelow.invalidateAndLock()
}

// Close gracefully shuts down the underlying nodestore, flushing any pending
// writes and releasing resources. Must be called on shutdown.
func (f *NodeStoreFamily) Close() error {
	return f.db.Close()
}
