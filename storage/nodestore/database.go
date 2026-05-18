package nodestore

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// asyncWorkerLimit caps the number of goroutines spawned by
// FetchAsync / StoreAsync per Database. Without this cap a hostile or
// buggy caller could fan out goroutines unboundedly by dropping the
// result channel before reading. We use a chan-of-tokens as a counting
// semaphore; the async call blocks (briefly) until a token is
// available.
const asyncWorkerLimit = 64

// newAsyncSem builds a token-bucket of capacity asyncWorkerLimit.
func newAsyncSem() chan struct{} {
	return make(chan struct{}, asyncWorkerLimit)
}

// DatabaseImpl wraps a Backend to implement the Database interface.
type DatabaseImpl struct {
	backend       Backend
	cache         *Cache
	negativeCache *NegativeCache
	asyncSem      chan struct{} // bounded goroutine pool for Async APIs
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

// NewDatabase creates a new Database from a Backend.
func NewDatabase(backend Backend, cacheSize int, cacheTTL time.Duration) *DatabaseImpl {
	var cache *Cache
	if cacheSize > 0 {
		cache = NewCache(cacheSize, cacheTTL)
	}
	return &DatabaseImpl{
		backend:  backend,
		cache:    cache,
		asyncSem: newAsyncSem(),
	}
}

// NewDatabaseWithConfig creates a new Database from a Backend with full configuration.
func NewDatabaseWithConfig(backend Backend, config *DatabaseConfig) (*DatabaseImpl, error) {
	if config == nil {
		config = DefaultDatabaseConfig()
	}

	db := &DatabaseImpl{
		backend:  backend,
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
func (d *DatabaseImpl) Store(ctx context.Context, node *Node) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	status := d.backend.Store(node)
	if status != OK {
		return errors.New("store failed: " + status.String())
	}

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
func (d *DatabaseImpl) Fetch(ctx context.Context, hash Hash256) (*Node, error) {
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

	// Check negative cache - if node is known to be missing, skip backend lookup
	if d.negativeCache != nil {
		if d.negativeCache.IsMissing(hash) {
			atomic.AddUint64(&d.stats.negativeCacheHits, 1)
			return nil, nil
		}
	}

	node, status := d.backend.Fetch(hash)
	if status == NotFound {
		// Mark as missing in negative cache
		if d.negativeCache != nil {
			d.negativeCache.MarkMissing(hash)
		}
		return nil, nil
	}
	if status != OK {
		return nil, errors.New("fetch failed: " + status.String())
	}

	if node != nil {
		atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
		if d.cache != nil {
			d.cache.Put(node)
		}
	}

	return node, nil
}

// FetchBatch retrieves multiple nodes efficiently.
//
// Cache + negative-cache lookups are batched into the first pass; only
// the remaining hashes are forwarded to the backend in a single
// FetchBatch call (which on Pebble probes under a consistent snapshot).
// The previous implementation forwarded every hash to the backend and
// missed cache entirely — a 30%+ wasted-IO regression on warm sync
// paths.
func (d *DatabaseImpl) FetchBatch(ctx context.Context, hashes []Hash256) ([]*Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(hashes) == 0 {
		return nil, nil
	}

	results := make([]*Node, len(hashes))
	misses := make([]Hash256, 0, len(hashes))
	missIdx := make([]int, 0, len(hashes))

	if d.cache != nil {
		for i, h := range hashes {
			if node, ok := d.cache.Get(h); ok {
				atomic.AddUint64(&d.stats.cacheHits, 1)
				results[i] = node
				continue
			}
			atomic.AddUint64(&d.stats.cacheMisses, 1)
			if d.negativeCache != nil && d.negativeCache.IsMissing(h) {
				atomic.AddUint64(&d.stats.negativeCacheHits, 1)
				continue
			}
			misses = append(misses, h)
			missIdx = append(missIdx, i)
		}
	} else {
		for i, h := range hashes {
			misses = append(misses, h)
			missIdx = append(missIdx, i)
		}
	}

	atomic.AddUint64(&d.stats.reads, uint64(len(hashes)))

	if len(misses) == 0 {
		return results, nil
	}

	fetched, status := d.backend.FetchBatch(misses)
	if status != OK && status != NotFound {
		return nil, errors.New("fetch batch failed: " + status.String())
	}

	for j, idx := range missIdx {
		node := fetched[j]
		if node == nil {
			if d.negativeCache != nil {
				d.negativeCache.MarkMissing(misses[j])
			}
			continue
		}
		atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
		if d.cache != nil {
			d.cache.Put(node)
		}
		results[idx] = node
	}

	return results, nil
}

// FetchAsync retrieves a node asynchronously. The number of in-flight
// async workers is bounded by asyncWorkerLimit; if the limit is
// reached the call blocks until a slot is available or ctx is
// cancelled.
func (d *DatabaseImpl) FetchAsync(ctx context.Context, hash Hash256) <-chan Result {
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

// StoreBatch stores multiple nodes efficiently.
func (d *DatabaseImpl) StoreBatch(ctx context.Context, nodes []*Node) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	status := d.backend.StoreBatch(nodes)
	if status != OK {
		return errors.New("store batch failed: " + status.String())
	}

	for _, node := range nodes {
		atomic.AddUint64(&d.stats.writes, 1)
		atomic.AddUint64(&d.stats.writeBytes, uint64(len(node.Data)))
		if d.cache != nil {
			d.cache.Put(node)
		}
		// Remove from negative cache since node now exists
		if d.negativeCache != nil {
			d.negativeCache.Remove(node.Hash)
		}
	}

	return nil
}

// Sweep removes expired entries from caches.
func (d *DatabaseImpl) Sweep() error {
	if d.cache != nil {
		d.cache.Sweep()
	}
	if d.negativeCache != nil {
		d.negativeCache.Sweep()
	}
	return nil
}

// Stats returns performance statistics.
func (d *DatabaseImpl) Stats() Statistics {
	stats := Statistics{
		Reads:       atomic.LoadUint64(&d.stats.reads),
		CacheHits:   atomic.LoadUint64(&d.stats.cacheHits),
		CacheMisses: atomic.LoadUint64(&d.stats.cacheMisses),
		ReadBytes:   atomic.LoadUint64(&d.stats.readBytes),
		Writes:      atomic.LoadUint64(&d.stats.writes),
		WriteBytes:  atomic.LoadUint64(&d.stats.writeBytes),
		BackendName: d.backend.Name(),
	}

	if d.cache != nil {
		cacheStats := d.cache.Stats()
		stats.CacheSize = uint64(cacheStats.CurrentSize)
		stats.CacheMaxSize = uint64(cacheStats.MaxSize)
	}

	return stats
}

// ExtendedStatistics holds extended performance metrics including negative cache stats.
type ExtendedStatistics struct {
	Statistics

	// Negative cache metrics
	NegativeCacheHits    uint64 // Number of negative cache hits
	NegativeCacheSize    uint64 // Current size of negative cache
	NegativeCacheMaxSize uint64 // Maximum size of negative cache
}

// ExtendedStats returns extended statistics including negative cache stats.
func (d *DatabaseImpl) ExtendedStats() ExtendedStatistics {
	stats := ExtendedStatistics{
		Statistics:        d.Stats(),
		NegativeCacheHits: atomic.LoadUint64(&d.stats.negativeCacheHits),
	}

	if d.negativeCache != nil {
		ncStats := d.negativeCache.Stats()
		stats.NegativeCacheSize = uint64(ncStats.Size)
		stats.NegativeCacheMaxSize = uint64(ncStats.MaxSize)
	}

	return stats
}

// Close gracefully closes the database.
func (d *DatabaseImpl) Close() error {
	var lastErr error

	if d.negativeCache != nil {
		if err := d.negativeCache.Close(); err != nil {
			lastErr = err
		}
	}

	if err := d.backend.Close(); err != nil {
		lastErr = err
	}

	return lastErr
}

// StoreAsync stores a node asynchronously. Bounded by asyncWorkerLimit.
func (d *DatabaseImpl) StoreAsync(ctx context.Context, node *Node) <-chan error {
	result := make(chan error, 1)
	select {
	case d.asyncSem <- struct{}{}:
	case <-ctx.Done():
		result <- ctx.Err()
		close(result)
		return result
	}
	go func() {
		defer func() { <-d.asyncSem }()
		result <- d.Store(ctx, node)
		close(result)
	}()
	return result
}

// NegativeCache returns the negative cache (for advanced operations).
func (d *DatabaseImpl) NegativeCache() *NegativeCache {
	return d.negativeCache
}

// Sync forces pending writes to disk. The flush itself is
// uninterruptible (a partial fsync would be worse than blocking),
// but ctx cancellation unblocks the caller while the flush continues
// in the background.
func (d *DatabaseImpl) Sync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan Status, 1)
	go func() { done <- d.backend.Sync() }()
	select {
	case status := <-done:
		if status != OK {
			return errors.New("sync failed: " + status.String())
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
