package nodestore

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

// PebbleBackend implements a high-performance PebbleDB storage backend.
type PebbleBackend struct {
	// Core components
	db     *pebble.DB
	config *Config

	// State management (atomic for lock-free reads)
	open       int64 // Use atomic instead of mutex for simple state
	deletePath int64

	// Stats (atomic for lock-free updates)
	stats struct {
		reads        int64
		writes       int64
		bytesRead    int64
		bytesWritten int64
	}
}

// NewPebbleBackend creates a new optimized PebbleDB backend.
func NewPebbleBackend(config *Config) (Backend, error) {
	if config == nil {
		config = DefaultConfig()
	}

	p := &PebbleBackend{
		config: config,
	}

	return p, nil
}

// Name returns the name of this backend.
func (p *PebbleBackend) Name() string {
	return fmt.Sprintf("pebble(%s)", p.config.Path)
}

// Open opens the backend for use.
func (p *PebbleBackend) Open(createIfMissing bool) error {
	if !atomic.CompareAndSwapInt64(&p.open, 0, 1) {
		return fmt.Errorf("backend already open")
	}

	if createIfMissing {
		if err := os.MkdirAll(p.config.Path, 0755); err != nil {
			atomic.StoreInt64(&p.open, 0)
			return fmt.Errorf("failed to create directory %s: %w", p.config.Path, err)
		}
	}

	// Configure optimized PebbleDB options for XRPL workload
	opts := p.buildOptimizedOptions()

	db, err := pebble.Open(p.config.Path, opts)
	if err != nil {
		atomic.StoreInt64(&p.open, 0)
		return fmt.Errorf("failed to open PebbleDB at %s: %w", p.config.Path, err)
	}

	p.db = db
	return nil
}

// buildOptimizedOptions creates optimized PebbleDB options for XRPL
// workload. The cache size is fixed at 256MB; configurable sizing
// should be wired through DatabaseConfig rather than guessed from
// runtime.MemStats (which reports Go heap, not host RAM).
func (p *PebbleBackend) buildOptimizedOptions() *pebble.Options {
	const memBudget int64 = 256 << 20 // 256MB

	cache := pebble.NewCache(memBudget)

	opts := &pebble.Options{
		Cache:                       cache,
		MaxOpenFiles:                10000,    // High for SST file caching
		MemTableSize:                64 << 20, // 64MB memtables
		MemTableStopWritesThreshold: 4,        // Allow more memtables
		MaxConcurrentCompactions: func() int { // Scale with CPU cores
			return runtime.NumCPU()
		},

		// L0 settings optimized for high write throughput
		L0CompactionThreshold: 4,  // Allow more L0 files
		L0StopWritesThreshold: 20, // Higher threshold

		// Base level size - start larger for better space amplification
		LBaseMaxBytes: 256 << 20, // 256MB

		// Level-specific options with bloom filters for all levels
		Levels: make([]pebble.LevelOptions, 7),

		// Write options
		DisableWAL: false, // Keep WAL for durability

		// Compaction options
		TargetByteDeletionRate: 128 << 20, // 128MB/sec deletion rate
	}

	// Configure bloom filters and file sizes for each level
	for i := range opts.Levels {
		opts.Levels[i] = pebble.LevelOptions{
			BlockSize:      32 << 10,                 // 32KB blocks (good for large values)
			IndexBlockSize: 256 << 10,                // 256KB index blocks
			FilterPolicy:   bloom.FilterPolicy(10),   // 10 bits per key bloom filter
			FilterType:     pebble.TableFilter,       // Table-level filters
			TargetFileSize: int64(8<<20) << uint(i),  // Exponential file size growth
			Compression:    pebble.SnappyCompression, // Use Snappy (built-in Pebble compression)
		}

		// Cap max file size at 256MB
		if opts.Levels[i].TargetFileSize > 256<<20 {
			opts.Levels[i].TargetFileSize = 256 << 20
		}
	}

	return opts
}

// Close closes the backend and releases resources.
func (p *PebbleBackend) Close() error {
	if !atomic.CompareAndSwapInt64(&p.open, 1, 0) {
		return nil // Already closed
	}

	var err error
	if p.db != nil {
		// Flush any pending writes
		if syncErr := p.db.Flush(); syncErr != nil {
			err = syncErr
		}

		if closeErr := p.db.Close(); closeErr != nil {
			if err == nil {
				err = closeErr
			}
		}
		p.db = nil
	}

	// Delete path if requested
	if atomic.LoadInt64(&p.deletePath) != 0 && p.config.Path != "" {
		if removeErr := os.RemoveAll(p.config.Path); removeErr != nil {
			if err == nil {
				err = removeErr
			}
		}
	}

	return err
}

// IsOpen returns true if the backend is currently open.
func (p *PebbleBackend) IsOpen() bool {
	return atomic.LoadInt64(&p.open) != 0
}

// Fetch retrieves a single object by key - optimized for zero allocations.
func (p *PebbleBackend) Fetch(key Hash256) (*Node, Status) {
	if !p.IsOpen() {
		return nil, BackendError
	}

	// Use Hash256 directly as key - no allocation needed
	keySlice := key[:]

	value, closer, err := p.db.Get(keySlice)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, NotFound
		}
		return nil, BackendError
	}
	defer closer.Close()

	node, err := p.decodeNode(key, value)
	if err != nil {
		return nil, DataCorrupt
	}

	atomic.AddInt64(&p.stats.reads, 1)
	atomic.AddInt64(&p.stats.bytesRead, int64(len(value)))

	return node, OK
}

// FetchBatch retrieves multiple objects under a single Pebble snapshot
// so every Get sees a consistent LSM view.
func (p *PebbleBackend) FetchBatch(keys []Hash256) ([]*Node, Status) {
	if !p.IsOpen() {
		return nil, BackendError
	}
	if len(keys) == 0 {
		return nil, OK
	}

	results := make([]*Node, len(keys))

	snap := p.db.NewSnapshot()
	defer snap.Close()

	var totalBytes int64
	for i, key := range keys {
		k := key
		value, closer, err := snap.Get(k[:])
		if err != nil {
			if errors.Is(err, pebble.ErrNotFound) {
				continue // NotFound is OK — results[i] stays nil
			}
			return nil, BackendError
		}
		node, decodeErr := decodeNodeData(k, value)
		closer.Close()
		if decodeErr != nil {
			return nil, DataCorrupt
		}
		results[i] = node
		totalBytes += int64(len(value))
	}

	atomic.AddInt64(&p.stats.reads, int64(len(keys)))
	atomic.AddInt64(&p.stats.bytesRead, totalBytes)

	return results, OK
}

// Store saves a single object synchronously.
func (p *PebbleBackend) Store(node *Node) Status {
	if node == nil {
		return BackendError
	}

	if !p.IsOpen() {
		return BackendError
	}

	value := encodeNodeData(node)
	defer releaseEncodeBuf(value)

	// Use NoSync for better performance, rely on WAL for durability.
	if err := p.db.Set(node.Hash[:], value, pebble.NoSync); err != nil {
		return BackendError
	}

	atomic.AddInt64(&p.stats.writes, 1)
	atomic.AddInt64(&p.stats.bytesWritten, int64(len(value)))

	return OK
}

// StoreBatch saves multiple objects efficiently using batched writes.
func (p *PebbleBackend) StoreBatch(nodes []*Node) Status {
	if !p.IsOpen() {
		return BackendError
	}

	if len(nodes) == 0 {
		return OK
	}

	// Use indexed batch for better performance
	batch := p.db.NewIndexedBatch()
	defer batch.Close()

	var totalBytes int64

	for _, node := range nodes {
		if node == nil {
			continue
		}

		value := encodeNodeData(node)
		if err := batch.Set(node.Hash[:], value, nil); err != nil {
			releaseEncodeBuf(value)
			return BackendError
		}
		totalBytes += int64(len(value))
		// pebble.Batch.Set copies into the batch immediately.
		releaseEncodeBuf(value)
	}

	// Commit the batch with controlled sync
	syncMode := pebble.NoSync
	if len(nodes) > 1000 { // Sync large batches for durability
		syncMode = pebble.Sync
	}

	if err := batch.Commit(syncMode); err != nil {
		return BackendError
	}

	atomic.AddInt64(&p.stats.writes, int64(len(nodes)))
	atomic.AddInt64(&p.stats.bytesWritten, totalBytes)

	return OK
}

// Sync forces pending writes to be flushed.
func (p *PebbleBackend) Sync() Status {
	if !p.IsOpen() {
		return BackendError
	}

	if err := p.db.Flush(); err != nil {
		return BackendError
	}

	return OK
}

// ForEach iterates over all objects in the backend.
func (p *PebbleBackend) ForEach(fn func(*Node) error) error {
	if !p.IsOpen() {
		return ErrBackendClosed
	}

	opts := &pebble.IterOptions{}

	iter, _ := p.db.NewIter(opts)
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		value := iter.Value()

		// Convert key bytes to Hash256
		if len(key) != 32 {
			continue // Skip invalid keys
		}

		var hash Hash256
		copy(hash[:], key)

		node, err := p.decodeNode(hash, value)
		if err != nil {
			continue // Skip corrupted entries
		}

		if err := fn(node); err != nil {
			return err
		}
	}

	return iter.Error()
}

// GetWriteLoad returns 0 (no async write queue).
func (p *PebbleBackend) GetWriteLoad() int {
	return 0
}

// SetDeletePath marks the backend for deletion when closed.
func (p *PebbleBackend) SetDeletePath() {
	atomic.StoreInt64(&p.deletePath, 1)
}

// FdRequired returns the number of file descriptors needed.
func (p *PebbleBackend) FdRequired() int {
	return 500
}

// BackendInfo returns information about this backend.
func (p *PebbleBackend) BackendInfo() BackendInfo {
	return BackendInfo{
		Name:            "pebble",
		Description:     "High-performance LSM-tree database backend optimized for XRPL",
		FileDescriptors: p.FdRequired(),
		Persistent:      true,
		Compression:     true,
	}
}

// Stats returns performance statistics.
func (p *PebbleBackend) Stats() map[string]any {
	stats := make(map[string]any)
	stats["reads"] = atomic.LoadInt64(&p.stats.reads)
	stats["writes"] = atomic.LoadInt64(&p.stats.writes)
	stats["bytes_read"] = atomic.LoadInt64(&p.stats.bytesRead)
	stats["bytes_written"] = atomic.LoadInt64(&p.stats.bytesWritten)

	if p.db != nil {
		if metrics := p.db.Metrics(); metrics != nil {
			stats["pebble_metrics"] = *metrics
		}
	}

	return stats
}

// Compact triggers manual compaction of the database.
func (p *PebbleBackend) Compact() error {
	if !p.IsOpen() {
		return ErrBackendClosed
	}
	return p.db.Compact(nil, nil, true)
}

// EstimateSize returns an estimate of the total size of data in the given range.
func (p *PebbleBackend) EstimateSize(start, end Hash256) (uint64, error) {
	if !p.IsOpen() {
		return 0, ErrBackendClosed
	}

	startSlice := start[:]
	endSlice := end[:]

	size, err := p.db.EstimateDiskUsage(startSlice, endSlice)
	return size, err
}

func (p *PebbleBackend) decodeNode(hash Hash256, data []byte) (*Node, error) {
	return decodeNodeData(hash, data)
}
