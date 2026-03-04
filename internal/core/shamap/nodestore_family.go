package shamap

import (
	"context"
	"time"

	"github.com/LeJamon/goXRPLd/internal/storage/nodestore"
)

// NodeStoreFamily implements the Family interface by delegating to a nodestore.Database.
// This is the production-quality Family implementation, matching rippled's NodeFamily
// which wraps its NodeStore Database.
//
// Prefix-format serialized data (4-byte hash prefix + node content) is stored directly
// as Node.Data — the nodestore treats it as opaque bytes. This matches rippled's approach
// where the hash prefix is stored alongside the node data in the NodeStore.
//
// For tests: use NewPebbleNodeStoreFamily() with t.TempDir() — disk-backed, RAM bounded.
// For production: use NewPebbleNodeStoreFamily() with a persistent path.
type NodeStoreFamily struct {
	db nodestore.Database
}

// NewNodeStoreFamily creates a Family backed by the given nodestore.Database.
// The Database should already be opened and configured with caching.
func NewNodeStoreFamily(db nodestore.Database) *NodeStoreFamily {
	return &NodeStoreFamily{db: db}
}

// NewMemoryNodeStoreFamily creates a Family backed by an in-memory nodestore.
// Uses an unbounded MemoryBackend (matching geth's test pattern) wrapped with
// a DatabaseImpl providing LRU positive cache and negative cache.
func NewMemoryNodeStoreFamily() (*NodeStoreFamily, error) {
	backend := nodestore.NewMemoryBackend()
	if err := backend.Open(true); err != nil {
		return nil, err
	}

	db := nodestore.NewDatabase(backend, 2000, time.Hour)
	return NewNodeStoreFamily(db), nil
}

// NewPebbleNodeStoreFamily creates a Family backed by PebbleDB on disk.
// Data persists to disk; the LRU cache bounds RAM usage. For production.
func NewPebbleNodeStoreFamily(path string, cacheSize int) (*NodeStoreFamily, error) {
	config := &nodestore.Config{
		Backend:         "pebble",
		Path:            path,
		CacheSize:       cacheSize,
		Compressor:      "lz4",
		CreateIfMissing: true,
	}

	backend, err := nodestore.NewPebbleBackend(config)
	if err != nil {
		return nil, err
	}
	if err := backend.Open(true); err != nil {
		return nil, err
	}

	dbConfig := &nodestore.DatabaseConfig{
		CacheSize:            cacheSize,
		CacheTTL:             time.Hour,
		NegativeCacheTTL:     5 * time.Minute,
		NegativeCacheMaxSize: 100000,
	}
	db, err := nodestore.NewDatabaseWithConfig(backend, dbConfig)
	if err != nil {
		return nil, err
	}

	return NewNodeStoreFamily(db), nil
}

// Fetch retrieves a node's serialized data (prefix format) by its SHAMap hash.
// Returns nil, nil if the node is not found (matching the Family contract).
func (f *NodeStoreFamily) Fetch(hash [32]byte) ([]byte, error) {
	node, err := f.db.Fetch(context.Background(), nodestore.Hash256(hash))
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, nil
	}
	return node.Data, nil
}

// StoreBatch persists a batch of serialized nodes to the nodestore.
// Each FlushEntry's Data contains prefix-format bytes which are stored directly
// as Node.Data (opaque to the nodestore). The Hash is set from FlushEntry.Hash
// (SHA-512Half, NOT recomputed as SHA-256).
func (f *NodeStoreFamily) StoreBatch(entries []FlushEntry) error {
	if len(entries) == 0 {
		return nil
	}

	nodes := make([]*nodestore.Node, len(entries))
	for i, e := range entries {
		nodes[i] = &nodestore.Node{
			Hash: nodestore.Hash256(e.Hash),
			Data: e.Data,
			Type: nodestore.NodeAccount, // NodeStore treats data as opaque; type is for categorization only
		}
	}
	return f.db.StoreBatch(context.Background(), nodes)
}

// Sweep removes expired entries from the nodestore's caches.
// Should be called periodically (e.g., on each ledger close) to bound memory usage.
// This matches rippled's pattern of calling sweep() on NodeFamily.
func (f *NodeStoreFamily) Sweep() error {
	return f.db.Sweep()
}

// Stats returns performance statistics from the underlying nodestore,
// including cache hit rates, read/write counts, and cache sizes.
func (f *NodeStoreFamily) Stats() nodestore.Statistics {
	return f.db.Stats()
}

// Close gracefully shuts down the underlying nodestore, flushing any pending
// writes and releasing resources. Must be called on shutdown.
func (f *NodeStoreFamily) Close() error {
	return f.db.Close()
}
