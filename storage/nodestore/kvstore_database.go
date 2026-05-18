package nodestore

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/LeJamon/goXRPLd/storage/kvstore"
)

// KVDatabaseImpl wraps a kvstore.KeyValueStore to implement the Database interface.
// This is the new preferred implementation that uses the generic KV layer.
type KVDatabaseImpl struct {
	store         kvstore.KeyValueStore
	cache         *Cache
	negativeCache *NegativeCache
	name          string
	asyncSem      chan struct{}
	stats         struct {
		reads             uint64
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
		store:    store,
		cache:    cache,
		name:     name,
		asyncSem: newAsyncSem(),
	}
}

// NewKVDatabaseWithConfig creates a new Database from a kvstore.KeyValueStore with full configuration.
func NewKVDatabaseWithConfig(store kvstore.KeyValueStore, name string, config *DatabaseConfig) (*KVDatabaseImpl, error) {
	if config == nil {
		config = DefaultDatabaseConfig()
	}

	db := &KVDatabaseImpl{
		store:    store,
		name:     name,
		asyncSem: newAsyncSem(),
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

	return db, nil
}

// Store persists a node to the store.
func (d *KVDatabaseImpl) Store(ctx context.Context, node *Node) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	encoded := encodeNodeData(node)
	if err := d.store.Put(node.Hash[:], encoded); err != nil {
		releaseEncodeBuf(encoded)
		return fmt.Errorf("store failed: %w", err)
	}
	releaseEncodeBuf(encoded)

	atomic.AddUint64(&d.stats.writes, 1)
	atomic.AddUint64(&d.stats.writeBytes, uint64(len(node.Data)))

	if d.cache != nil {
		d.cache.Put(node)
	}

	// Remove from negative cache since node now exists
	if d.negativeCache != nil {
		d.negativeCache.Remove(node.Hash)
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
			return node, nil
		}
		atomic.AddUint64(&d.stats.cacheMisses, 1)
	}

	if d.negativeCache != nil {
		if d.negativeCache.IsMissing(hash) {
			atomic.AddUint64(&d.stats.negativeCacheHits, 1)
			return nil, nil
		}
	}

	data, err := d.store.Get(hash[:])
	if err != nil {
		if errors.Is(err, kvstore.ErrNotFound) {
			// Mark as missing in negative cache
			if d.negativeCache != nil {
				d.negativeCache.MarkMissing(hash)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	node, err := decodeNodeData(hash, data)
	if err != nil {
		return nil, err
	}

	atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	if d.cache != nil {
		d.cache.Put(node)
	}

	return node, nil
}

// FetchBatch retrieves multiple nodes.
//
// Cache lookups are batched into the first pass so the call only pays
// for cache contention once per shard rather than re-entering d.Fetch
// per key (which used to dominate batch latency on warm caches). The
// kvstore.KeyValueStore interface has no multi-get primitive, so the
// miss pass is a sequential loop — but it skips the per-call cache
// reentry, the negative-cache reentry, and the stats counter shuffling
// of the old N × d.Fetch path.
func (d *KVDatabaseImpl) FetchBatch(ctx context.Context, hashes []Hash256) ([]*Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(hashes) == 0 {
		return nil, nil
	}

	results := make([]*Node, len(hashes))
	misses := make([]int, 0, len(hashes))

	if d.cache != nil {
		for i, h := range hashes {
			if node, ok := d.cache.Get(h); ok {
				atomic.AddUint64(&d.stats.cacheHits, 1)
				results[i] = node
			} else {
				atomic.AddUint64(&d.stats.cacheMisses, 1)
				misses = append(misses, i)
			}
		}
	} else {
		for i := range hashes {
			misses = append(misses, i)
		}
	}

	if len(misses) == 0 {
		atomic.AddUint64(&d.stats.reads, uint64(len(hashes)))
		return results, nil
	}

	for _, idx := range misses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h := hashes[idx]
		atomic.AddUint64(&d.stats.reads, 1)

		if d.negativeCache != nil && d.negativeCache.IsMissing(h) {
			atomic.AddUint64(&d.stats.negativeCacheHits, 1)
			continue
		}
		data, err := d.store.Get(h[:])
		if err != nil {
			if errors.Is(err, kvstore.ErrNotFound) {
				if d.negativeCache != nil {
					d.negativeCache.MarkMissing(h)
				}
				continue
			}
			return nil, fmt.Errorf("fetch batch failed: %w", err)
		}
		node, err := decodeNodeData(h, data)
		if err != nil {
			return nil, err
		}
		atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
		if d.cache != nil {
			d.cache.Put(node)
		}
		results[idx] = node
	}
	return results, nil
}

// FetchAsync retrieves a node asynchronously. Worker count is bounded
// by asyncWorkerLimit per database.
func (d *KVDatabaseImpl) FetchAsync(ctx context.Context, hash Hash256) <-chan Result {
	resultCh := make(chan Result, 1)
	select {
	case d.asyncSem <- struct{}{}:
	case <-ctx.Done():
		resultCh <- Result{Err: ctx.Err()}
		close(resultCh)
		return resultCh
	}
	go func() {
		defer func() { <-d.asyncSem }()
		node, err := d.Fetch(ctx, hash)
		resultCh <- Result{Node: node, Err: err}
		close(resultCh)
	}()
	return resultCh
}

// StoreBatch stores multiple nodes efficiently using a batch.
func (d *KVDatabaseImpl) StoreBatch(ctx context.Context, nodes []*Node) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	batch := d.store.NewBatch()
	// Reuse encode buffers across the batch. KVStore.Batch.Put is
	// required to copy the value into the batch's internal buffer
	// before returning, so we can recycle immediately.
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
	}
	if err := batch.Write(); err != nil {
		return fmt.Errorf("store batch commit failed: %w", err)
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
		if d.negativeCache != nil {
			d.negativeCache.Remove(node.Hash)
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
		Reads:       atomic.LoadUint64(&d.stats.reads),
		CacheHits:   atomic.LoadUint64(&d.stats.cacheHits),
		CacheMisses: atomic.LoadUint64(&d.stats.cacheMisses),
		ReadBytes:   atomic.LoadUint64(&d.stats.readBytes),
		Writes:      atomic.LoadUint64(&d.stats.writes),
		WriteBytes:  atomic.LoadUint64(&d.stats.writeBytes),
		BackendName: d.name,
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
	type syncer interface {
		Sync() error
	}
	s, ok := d.store.(syncer)
	if !ok {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- s.Sync() }()
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
