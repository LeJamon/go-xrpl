package nodestore

import (
	"context"
	"errors"
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
		store: store,
		cache: cache,
		name:  name,
	}
}

// NewKVDatabaseWithConfig creates a new Database from a kvstore.KeyValueStore with full configuration.
func NewKVDatabaseWithConfig(store kvstore.KeyValueStore, name string, config *DatabaseConfig) (*KVDatabaseImpl, error) {
	if config == nil {
		config = DefaultDatabaseConfig()
	}

	db := &KVDatabaseImpl{
		store: store,
		name:  name,
	}

	// Initialize positive cache
	if config.CacheSize > 0 {
		db.cache = NewCache(config.CacheSize, config.CacheTTL)
	}

	// Initialize negative cache
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
		return errors.New("store failed: " + err.Error())
	}

	atomic.AddUint64(&d.stats.writes, 1)
	atomic.AddUint64(&d.stats.writeBytes, uint64(len(node.Data)))

	// Update positive cache
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

	// Check positive cache first
	if d.cache != nil {
		if node, found := d.cache.Get(hash); found {
			atomic.AddUint64(&d.stats.cacheHits, 1)
			return node, nil
		}
		atomic.AddUint64(&d.stats.cacheMisses, 1)
	}

	// Check negative cache
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
		return nil, errors.New("fetch failed: " + err.Error())
	}

	node, err := decodeNodeData(hash, data)
	if err != nil {
		return nil, err
	}

	atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	// Update positive cache
	if d.cache != nil {
		d.cache.Put(node)
	}

	return node, nil
}

// FetchBatch retrieves multiple nodes, going through the cache for each.
func (d *KVDatabaseImpl) FetchBatch(ctx context.Context, hashes []Hash256) ([]*Node, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	results := make([]*Node, len(hashes))
	for i, hash := range hashes {
		node, err := d.Fetch(ctx, hash)
		if err != nil {
			return nil, err
		}
		results[i] = node
	}
	return results, nil
}

// FetchAsync retrieves a node asynchronously.
func (d *KVDatabaseImpl) FetchAsync(ctx context.Context, hash Hash256) <-chan Result {
	resultCh := make(chan Result, 1)
	go func() {
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
	for _, node := range nodes {
		if node == nil {
			continue
		}
		encoded := encodeNodeData(node)
		if err := batch.Put(node.Hash[:], encoded); err != nil {
			return errors.New("store batch failed: " + err.Error())
		}
	}
	if err := batch.Write(); err != nil {
		return errors.New("store batch commit failed: " + err.Error())
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

// Sync forces pending writes to disk.
func (d *KVDatabaseImpl) Sync() error {
	type syncer interface {
		Sync() error
	}
	if s, ok := d.store.(syncer); ok {
		return s.Sync()
	}
	return nil
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

// ForEach iterates over all stored nodes, decoding each entry.
// The callback receives a fully decoded Node. Return a non-nil error
// from fn to stop iteration early.
func (d *KVDatabaseImpl) ForEach(fn func(*Node) error) error {
	iter := d.store.NewIterator(nil, nil)
	defer iter.Release()

	for iter.Next() {
		key := iter.Key()
		if len(key) != 32 {
			continue // skip non-hash keys
		}
		var hash Hash256
		copy(hash[:], key)

		node, err := decodeNodeData(hash, iter.Value())
		if err != nil {
			continue // skip corrupt entries
		}
		if err := fn(node); err != nil {
			return err
		}
	}
	return iter.Error()
}

// Ensure KVDatabaseImpl implements Database at compile time.
var _ Database = (*KVDatabaseImpl)(nil)
