package nodestore

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// DatabaseImpl wraps a Backend to implement the Database interface.
type DatabaseImpl struct {
	backend        Backend
	cache          *Cache
	negativeCache  *NegativeCache
	batchWriter    *BatchWriter
	ioLatency      *IOLatencyTracker
	stats          struct {
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

	// BatchWriteConfig is kept for backwards compatibility but is ignored.
	BatchWriteConfig *BatchWriteConfig
}

// DefaultDatabaseConfig returns a DatabaseConfig with sensible defaults.
func DefaultDatabaseConfig() *DatabaseConfig {
	return &DatabaseConfig{
		CacheSize:            2000,
		CacheTTL:             time.Hour,
		NegativeCacheTTL:     5 * time.Minute,
		NegativeCacheMaxSize: 100000,
		BatchWriteConfig:     nil,
	}
}

// NewDatabase creates a new Database from a Backend.
func NewDatabase(backend Backend, cacheSize int, cacheTTL time.Duration) *DatabaseImpl {
	var cache *Cache
	if cacheSize > 0 {
		cache = NewCache(cacheSize, cacheTTL)
	}
	return &DatabaseImpl{
		backend:   backend,
		cache:     cache,
		ioLatency: NewIOLatencyTracker(0.1),
	}
}

// NewDatabaseWithConfig creates a new Database from a Backend with full configuration.
func NewDatabaseWithConfig(backend Backend, config *DatabaseConfig) (*DatabaseImpl, error) {
	if config == nil {
		config = DefaultDatabaseConfig()
	}

	db := &DatabaseImpl{
		backend:   backend,
		ioLatency: NewIOLatencyTracker(0.1),
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

	// Initialize batch writer if configured
	if config.BatchWriteConfig != nil {
		bw, err := NewBatchWriter(backend, config.BatchWriteConfig)
		if err != nil {
			return nil, err
		}
		db.batchWriter = bw
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

	start := time.Now()
	status := d.backend.Store(node)
	d.ioLatency.Record(time.Since(start))
	if status != OK {
		return errors.New("store failed: " + status.String())
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
func (d *DatabaseImpl) Fetch(ctx context.Context, hash Hash256) (*Node, error) {
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

	// Check negative cache - if node is known to be missing, skip backend lookup
	if d.negativeCache != nil {
		if d.negativeCache.IsMissing(hash) {
			atomic.AddUint64(&d.stats.negativeCacheHits, 1)
			return nil, nil
		}
	}

	start := time.Now()
	node, status := d.backend.Fetch(hash)
	d.ioLatency.Record(time.Since(start))
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
		// Update positive cache
		if d.cache != nil {
			d.cache.Put(node)
		}
	}

	return node, nil
}

// FetchBatch retrieves multiple nodes efficiently.
func (d *DatabaseImpl) FetchBatch(ctx context.Context, hashes []Hash256) ([]*Node, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	start := time.Now()
	nodes, status := d.backend.FetchBatch(hashes)
	d.ioLatency.Record(time.Since(start))
	if status != OK && status != NotFound {
		return nil, errors.New("fetch batch failed: " + status.String())
	}

	return nodes, nil
}

// FetchAsync retrieves a node asynchronously.
func (d *DatabaseImpl) FetchAsync(ctx context.Context, hash Hash256) <-chan Result {
	resultCh := make(chan Result, 1)

	go func() {
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

	start := time.Now()
	status := d.backend.StoreBatch(nodes)
	d.ioLatency.Record(time.Since(start))
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

	// Batch writer metrics (kept for backwards compatibility)
	BatchWriterPending int    // Number of pending batch writes
	BatchWriterFlushes uint64 // Number of batch flushes
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

	if d.batchWriter != nil {
		bwStats := d.batchWriter.Stats()
		stats.BatchWriterPending = bwStats.PendingCount
		stats.BatchWriterFlushes = uint64(bwStats.Flushes)
	}

	return stats
}

// Close gracefully closes the database.
func (d *DatabaseImpl) Close() error {
	var lastErr error

	// Close batch writer first
	if d.batchWriter != nil {
		if err := d.batchWriter.Close(); err != nil {
			lastErr = err
		}
	}

	// Close negative cache
	if d.negativeCache != nil {
		if err := d.negativeCache.Close(); err != nil {
			lastErr = err
		}
	}

	// Close backend last
	if err := d.backend.Close(); err != nil {
		lastErr = err
	}

	return lastErr
}

// StoreAsync stores a node asynchronously.
// Falls back to synchronous storage.
func (d *DatabaseImpl) StoreAsync(ctx context.Context, node *Node) <-chan error {
	result := make(chan error, 1)

	// Check context
	select {
	case <-ctx.Done():
		result <- ctx.Err()
		close(result)
		return result
	default:
	}

	// Fall back to synchronous storage
	go func() {
		err := d.Store(ctx, node)
		result <- err
		close(result)
	}()

	return result
}

// NegativeCache returns the negative cache (for advanced operations).
func (d *DatabaseImpl) NegativeCache() *NegativeCache {
	return d.negativeCache
}

// BatchWriter returns the batch writer (may be nil if not configured).
func (d *DatabaseImpl) BatchWriter() *BatchWriter {
	return d.batchWriter
}

// IOLatency returns the IO latency tracker for this database.
func (d *DatabaseImpl) IOLatency() *IOLatencyTracker {
	return d.ioLatency
}

// Sync forces pending writes to disk.
func (d *DatabaseImpl) Sync() error {
	status := d.backend.Sync()
	if status != OK {
		return errors.New("sync failed: " + status.String())
	}
	return nil
}
