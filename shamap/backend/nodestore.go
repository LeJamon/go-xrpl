// Package backend adapts NodeStore databases for use by SHAMaps.
package backend

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// ErrStoreBelowMinimum reports a write rejected by the online-delete floor.
var ErrStoreBelowMinimum = errors.New("shamap: ledger is below the minimum online sequence")

// NodeStore adapts a nodestore.Database to the shamap.Family interface.
type NodeStore struct {
	db        nodestore.Database
	fullBelow *shamap.FullBelowCache
	durable   durableReadCoalescer
	storeMu   sync.RWMutex
	minimum   atomic.Uint32
}

var _ shamap.Family = (*NodeStore)(nil)

// New returns a SHAMap backend backed by db.
func New(db nodestore.Database) *NodeStore {
	return &NodeStore{db: db, fullBelow: shamap.NewFullBelowCache()}
}

// NewMemory returns a backend backed by an in-memory NodeStore.
func NewMemory() *NodeStore {
	store := memorydb.New()
	db, err := nodestore.NewKVDatabase(store, nodestore.DatabaseConfig{
		PositiveCache: nodestore.CacheConfig{
			Enabled:    true,
			MaxEntries: 2000,
			TTL:        time.Hour,
		},
	})
	if err != nil {
		panic("construct in-memory nodestore: " + err.Error())
	}
	return New(db)
}

// OpenPebble opens a persistent Pebble-backed NodeStore.
//
// blockCacheMB sizes Pebble's block cache in MiB. nodeCacheItems bounds the
// decoded-node cache by entry count.
func OpenPebble(path string, blockCacheMB, nodeCacheItems int) (*NodeStore, error) {
	return openPebble(path, blockCacheMB, nodeCacheItems, false)
}

// OpenPebbleReadOnly opens an existing persistent Pebble-backed NodeStore for
// concurrent readers.
func OpenPebbleReadOnly(path string, blockCacheMB, nodeCacheItems int) (*NodeStore, error) {
	return openPebble(path, blockCacheMB, nodeCacheItems, true)
}

func openPebble(path string, blockCacheMB, nodeCacheItems int, readOnly bool) (*NodeStore, error) {
	options, err := kvpebble.OptionsFromMiB(int64(blockCacheMB), kvpebble.DefaultMaxOpenFiles)
	if err != nil {
		return nil, err
	}
	var store *kvpebble.Store
	if readOnly {
		store, err = kvpebble.NewReadOnly(path, options)
	} else {
		store, err = kvpebble.New(path, options)
	}
	if err != nil {
		return nil, err
	}

	dbConfig := nodestore.DefaultDatabaseConfig()
	if nodeCacheItems > 0 {
		dbConfig.PositiveCache.MaxEntries = nodeCacheItems
	} else {
		dbConfig.PositiveCache = nodestore.CacheConfig{}
	}
	db, err := nodestore.NewKVDatabase(store, dbConfig)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	return New(db), nil
}

// FullBelowCache returns the cache shared by every SHAMap using this backend.
func (f *NodeStore) FullBelowCache() *shamap.FullBelowCache {
	return f.fullBelow
}

// Fetch retrieves serialized node data by its SHAMap hash.
func (f *NodeStore) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	node, err := f.db.Fetch(ctx, nodestore.Hash256(hash))
	if err != nil || node == nil {
		return nil, err
	}
	return node.Data, nil
}

// FetchCached returns serialized node data only when the decoded node is
// already resident in the NodeStore cache.
func (f *NodeStore) FetchCached(ctx context.Context, hash [32]byte) ([]byte, error) {
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
// decoded-node cache.
func (f *NodeStore) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.durable.fetch(ctx, hash, f.fetchDurable)
}

func (f *NodeStore) fetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if raw, ok := f.db.(interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	}); ok {
		return raw.FetchDataUncached(ctx, nodestore.Hash256(hash))
	}
	return f.Fetch(ctx, hash)
}

// StoreBatch persists serialized nodes to the NodeStore.
func (f *NodeStore) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if len(entries) == 0 {
		return nil
	}
	f.storeMu.RLock()
	defer f.storeMu.RUnlock()
	floor := f.minimum.Load()

	nodes := make([]*nodestore.Node, 0, len(entries))
	for _, entry := range entries {
		if floor != 0 && entry.LedgerSeq < floor {
			return ErrStoreBelowMinimum
		}
		nodeType := nodestore.NodeAccount
		if entry.MapType == shamap.TypeTransaction {
			nodeType = nodestore.NodeTransaction
		}
		nodes = append(nodes, &nodestore.Node{
			Hash:      nodestore.Hash256(entry.Hash),
			Data:      entry.Data,
			Type:      nodeType,
			LedgerSeq: entry.LedgerSeq,
		})
	}
	return f.db.StoreBatch(ctx, nodes)
}

// Sync persists all preceding writes to stable storage.
func (f *NodeStore) Sync(ctx context.Context) error {
	return f.db.Sync(ctx)
}

// SetMinimumLedgerSeq waits for older writes to finish, then rejects new
// writes below the online-delete boundary.
func (f *NodeStore) SetMinimumLedgerSeq(seq uint32) {
	f.storeMu.Lock()
	if seq > f.minimum.Load() {
		f.minimum.Store(seq)
	}
	f.storeMu.Unlock()
}

// Sweep removes expired entries from the NodeStore and SHAMap caches.
func (f *NodeStore) Sweep() error {
	f.fullBelow.Sweep()
	return f.db.Sweep()
}

// ResetFullBelow invalidates completeness marks after the backing store is
// rotated or otherwise replaces its node set.
func (f *NodeStore) ResetFullBelow() {
	f.fullBelow.Bump()
}

// BeginPrune invalidates completeness marks and blocks missing-node walks
// until the returned function is called after the backing-store mutation.
func (f *NodeStore) BeginPrune() func() {
	return f.fullBelow.BeginMutation()
}

// Close closes the underlying NodeStore.
func (f *NodeStore) Close() error {
	return f.db.Close()
}
