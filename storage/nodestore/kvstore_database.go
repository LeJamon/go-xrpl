package nodestore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// DatabaseConfig holds configuration for creating a Database.
type DatabaseConfig struct {
	// CacheSize is the maximum number of items in the positive cache.
	CacheSize int

	// CacheTTL is the time-to-live for positive cache entries.
	CacheTTL time.Duration

	// NegativeCacheTTL is the time-to-live for negative cache entries.
	// Set to 0 to disable negative caching.
	NegativeCacheTTL time.Duration

	// NegativeCacheMaxSize is the maximum number of entries in the negative cache.
	NegativeCacheMaxSize int
}

// DefaultDatabaseConfig returns a DatabaseConfig with sensible defaults.
func DefaultDatabaseConfig() *DatabaseConfig {
	return &DatabaseConfig{
		CacheSize:            2000,
		CacheTTL:             time.Hour,
		NegativeCacheTTL:     5 * time.Minute,
		NegativeCacheMaxSize: 100000,
	}
}

// KVDatabaseImpl wraps a kvstore.KeyValueStore to implement the Database interface.
type KVDatabaseImpl struct {
	// pruneMu serialises online-delete pruning against writers. Writers take
	// the read side; DeleteBefore's flush takes the write side so its
	// re-read-then-delete of each key is atomic w.r.t. a concurrent Store —
	// otherwise a key re-created live during a prune could be erased.
	pruneMu       sync.RWMutex
	store         kvstore.KeyValueStore
	cache         *Cache
	negativeCache *NegativeCache
	// Advances after a successful backend write and before negative-cache invalidation.
	storeGeneration atomic.Uint64
	name            string
	stats           struct {
		reads             uint64
		fetchHits         uint64
		cacheHits         uint64
		cacheMisses       uint64
		negativeCacheHits uint64
		writes            uint64
		readBytes         uint64
		writeBytes        uint64
	}
}

// NewKVDatabase creates a new Database from a kvstore.KeyValueStore.
func NewKVDatabase(store kvstore.KeyValueStore, name string, cacheSize int, cacheTTL time.Duration) *KVDatabaseImpl {
	var cache *Cache
	if cacheSize > 0 {
		cache = NewCache(cacheSize, cacheTTL)
	}
	return &KVDatabaseImpl{
		store: store,
		cache: cache,
		name:  name,
	}
}

// NewKVDatabaseWithConfig creates a new Database from a kvstore.KeyValueStore with full configuration.
func NewKVDatabaseWithConfig(store kvstore.KeyValueStore, name string, config *DatabaseConfig) *KVDatabaseImpl {
	if config == nil {
		config = DefaultDatabaseConfig()
	}

	db := &KVDatabaseImpl{
		store: store,
		name:  name,
	}

	if config.CacheSize > 0 {
		db.cache = NewCache(config.CacheSize, config.CacheTTL)
	}

	if config.NegativeCacheTTL > 0 {
		db.negativeCache = NewNegativeCacheWithConfig(&NegativeCacheConfig{
			TTL:     config.NegativeCacheTTL,
			MaxSize: config.NegativeCacheMaxSize,
		})
	}

	return db
}

// Store persists a node to the store.
func (d *KVDatabaseImpl) Store(ctx context.Context, node *Node) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	encoded := encodeNodeData(node)
	d.pruneMu.RLock()
	err := d.store.Put(node.Hash[:], encoded)
	d.pruneMu.RUnlock()
	releaseEncodeBuf(encoded)
	if err != nil {
		return fmt.Errorf("store failed: %w", err)
	}
	if d.negativeCache != nil {
		d.storeGeneration.Add(1)
		d.negativeCache.Remove(node.Hash)
	}

	atomic.AddUint64(&d.stats.writes, 1)
	atomic.AddUint64(&d.stats.writeBytes, uint64(len(node.Data)))

	if d.cache != nil {
		d.cache.Put(node)
	}

	return nil
}

// Fetch retrieves a node by its hash.
func (d *KVDatabaseImpl) Fetch(ctx context.Context, hash Hash256) (*Node, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	atomic.AddUint64(&d.stats.reads, 1)

	if d.cache != nil {
		if node, found := d.cache.Get(hash); found {
			atomic.AddUint64(&d.stats.cacheHits, 1)
			atomic.AddUint64(&d.stats.fetchHits, 1)
			atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
			return node, nil
		}
		atomic.AddUint64(&d.stats.cacheMisses, 1)
	}

	data, err := d.fetchBackend(hash)
	if err != nil || data == nil {
		return nil, err
	}

	node, err := decodeNodeData(hash, data)
	if err != nil {
		return nil, err
	}

	atomic.AddUint64(&d.stats.fetchHits, 1)
	atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	if d.cache != nil {
		d.cache.putOwned(node)
	}

	return node, nil
}

// FetchDataUncached retrieves a node's opaque payload without populating the
// decoded-node LRU. It is used by one-shot content-addressed traversals whose
// working set is much larger than that cache.
func (d *KVDatabaseImpl) FetchDataUncached(ctx context.Context, hash Hash256) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	atomic.AddUint64(&d.stats.reads, 1)
	data, err := d.fetchBackend(hash)
	if err != nil || data == nil {
		return nil, err
	}
	if len(data) < nodeEncodingHeaderSize {
		return nil, fmt.Errorf("%w: data too short (%d bytes)", ErrDataCorrupt, len(data))
	}
	payload := data[nodeEncodingHeaderSize:len(data):len(data)]
	atomic.AddUint64(&d.stats.fetchHits, 1)
	atomic.AddUint64(&d.stats.readBytes, uint64(len(payload)))
	return payload, nil
}

func (d *KVDatabaseImpl) fetchBackend(hash Hash256) ([]byte, error) {
	var storeGeneration uint64
	if d.negativeCache != nil {
		if d.negativeCache.IsMissing(hash) {
			atomic.AddUint64(&d.stats.negativeCacheHits, 1)
			return nil, nil
		}
		storeGeneration = d.storeGeneration.Load()
	}

	data, err := d.store.Get(hash[:])
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, kvstore.ErrNotFound) {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	if d.negativeCache != nil {
		d.negativeCache.MarkMissing(hash)
		if d.storeGeneration.Load() != storeGeneration {
			d.negativeCache.Remove(hash)
		}
	}
	return nil, nil
}

// StoreBatch stores multiple nodes efficiently using a batch.
func (d *KVDatabaseImpl) StoreBatch(ctx context.Context, nodes []*Node) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	batch := d.store.NewBatch()
	wroteNode := false
	for _, node := range nodes {
		if node == nil {
			continue
		}
		encoded := encodeNodeData(node)
		err := batch.Put(node.Hash[:], encoded)
		releaseEncodeBuf(encoded)
		if err != nil {
			return fmt.Errorf("store batch failed: %w", err)
		}
		wroteNode = true
	}
	d.pruneMu.RLock()
	err := batch.Write()
	d.pruneMu.RUnlock()
	if err != nil {
		return fmt.Errorf("store batch commit failed: %w", err)
	}
	if d.negativeCache != nil && wroteNode {
		d.storeGeneration.Add(1)
		for _, node := range nodes {
			if node != nil {
				d.negativeCache.Remove(node.Hash)
			}
		}
	}

	for _, node := range nodes {
		if node == nil {
			continue
		}
		atomic.AddUint64(&d.stats.writes, 1)
		atomic.AddUint64(&d.stats.writeBytes, uint64(len(node.Data)))
		if d.cache != nil {
			d.cache.Put(node)
		}
	}

	return nil
}

// Sweep removes expired entries from caches.
func (d *KVDatabaseImpl) Sweep() error {
	if d.cache != nil {
		d.cache.Sweep()
	}
	if d.negativeCache != nil {
		d.negativeCache.Sweep()
	}
	return nil
}

// Stats returns performance statistics.
func (d *KVDatabaseImpl) Stats() Statistics {
	stats := Statistics{
		Reads:             atomic.LoadUint64(&d.stats.reads),
		FetchHits:         atomic.LoadUint64(&d.stats.fetchHits),
		CacheHits:         atomic.LoadUint64(&d.stats.cacheHits),
		CacheMisses:       atomic.LoadUint64(&d.stats.cacheMisses),
		NegativeCacheHits: atomic.LoadUint64(&d.stats.negativeCacheHits),
		ReadBytes:         atomic.LoadUint64(&d.stats.readBytes),
		Writes:            atomic.LoadUint64(&d.stats.writes),
		WriteBytes:        atomic.LoadUint64(&d.stats.writeBytes),
		BackendName:       d.name,
	}

	if d.cache != nil {
		cacheStats := d.cache.Stats()
		stats.CacheSize = uint64(cacheStats.CurrentSize)
		stats.CacheMaxSize = uint64(cacheStats.MaxSize)
	}

	return stats
}

// Sync forces pending writes to disk. The flush itself is
// uninterruptible; ctx cancellation unblocks the caller while the
// underlying store flush continues in the background.
func (d *KVDatabaseImpl) Sync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- d.store.Sync() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close closes the database.
func (d *KVDatabaseImpl) Close() error {
	var lastErr error
	if d.negativeCache != nil {
		if err := d.negativeCache.Close(); err != nil {
			lastErr = err
		}
	}
	if err := d.store.Close(); err != nil {
		lastErr = err
	}
	return lastErr
}

// Ensure KVDatabaseImpl implements Database at compile time.
var _ Database = (*KVDatabaseImpl)(nil)
