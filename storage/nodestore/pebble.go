package nodestore

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"unsafe"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

const (
	// nodeHeaderSize is type(1) + ledgerSeq(4) = 5 bytes
	nodeHeaderSize = 5
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

	// Create directory if needed
	if createIfMissing {
		if err := os.MkdirAll(p.config.Path, 0755); err != nil {
			atomic.StoreInt64(&p.open, 0)
			return fmt.Errorf("failed to create directory %s: %w", p.config.Path, err)
		}
	}

	// Configure optimized PebbleDB options for XRPL workload
	opts := p.buildOptimizedOptions()

	// Open the database
	db, err := pebble.Open(p.config.Path, opts)
	if err != nil {
		atomic.StoreInt64(&p.open, 0)
		return fmt.Errorf("failed to open PebbleDB at %s: %w", p.config.Path, err)
	}

	p.db = db
	return nil
}

// buildOptimizedOptions creates optimized PebbleDB options for XRPL workload
func (p *PebbleBackend) buildOptimizedOptions() *pebble.Options {
	// Calculate memory budget based on available system memory
	var memBudget int64 = 256 << 20 // Default 256MB

	// Use up to 25% of system memory for cache, with reasonable bounds
	if mem := getSystemMemory(); mem > 0 {
		budget := mem / 4
		if budget > 1<<30 { // Max 1GB
			budget = 1 << 30
		}
		if budget < 128<<20 { // Min 128MB
			budget = 128 << 20
		}
		memBudget = budget
	}

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

// getSystemMemory returns available system memory in bytes
func getSystemMemory() int64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.Sys)
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
	keySlice := (*[32]byte)(unsafe.Pointer(&key[0]))[:]

	// Read from PebbleDB
	value, closer, err := p.db.Get(keySlice)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, NotFound
		}
		return nil, BackendError
	}
	defer closer.Close()

	// Decode the stored data
	node, err := p.decodeNode(key, value)
	if err != nil {
		return nil, DataCorrupt
	}

	// Update stats
	atomic.AddInt64(&p.stats.reads, 1)
	atomic.AddInt64(&p.stats.bytesRead, int64(len(value)))

	return node, OK
}

// FetchBatch retrieves multiple objects efficiently using individual gets.
func (p *PebbleBackend) FetchBatch(keys []Hash256) ([]*Node, Status) {
	if !p.IsOpen() {
		return nil, BackendError
	}

	results := make([]*Node, len(keys))

	for i, key := range keys {
		node, status := p.Fetch(key)
		if status == OK {
			results[i] = node
		} else if status != NotFound {
			return nil, status
		}
		// NotFound is OK - results[i] stays nil
	}
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

	// Encode the node
	value := encodeNode(node)

	keySlice := (*[32]byte)(unsafe.Pointer(&node.Hash[0]))[:]

	// Use NoSync for better performance, rely on WAL for durability
	if err := p.db.Set(keySlice, value, pebble.NoSync); err != nil {
		return BackendError
	}

	// Update stats
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

		value := encodeNode(node)

		keySlice := (*[32]byte)(unsafe.Pointer(&node.Hash[0]))[:]
		if err := batch.Set(keySlice, value, nil); err != nil {
			return BackendError
		}

		totalBytes += int64(len(value))
	}

	// Commit the batch with controlled sync
	syncMode := pebble.NoSync
	if len(nodes) > 1000 { // Sync large batches for durability
		syncMode = pebble.Sync
	}

	if err := batch.Commit(syncMode); err != nil {
		return BackendError
	}

	// Update stats
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

		// Decode the node
		node, err := p.decodeNode(hash, value)
		if err != nil {
			continue // Skip corrupted entries
		}

		// Call the callback function
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
func (p *PebbleBackend) Stats() map[string]interface{} {
	stats := make(map[string]interface{})
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

	startSlice := (*[32]byte)(unsafe.Pointer(&start[0]))[:]
	endSlice := (*[32]byte)(unsafe.Pointer(&end[0]))[:]

	size, err := p.db.EstimateDiskUsage(startSlice, endSlice)
	return size, err
}

// encodeNode serializes a node for storage.
// Format: [nodeType:1][ledgerSeq:4][data:N] = 5 bytes header + data
func encodeNode(n *Node) []byte {
	buf := make([]byte, nodeHeaderSize+len(n.Data))
	buf[0] = byte(n.Type)
	binary.BigEndian.PutUint32(buf[1:5], n.LedgerSeq)
	copy(buf[nodeHeaderSize:], n.Data)
	return buf
}

// decodeNode deserializes a node from storage.
func (p *PebbleBackend) decodeNode(hash Hash256, data []byte) (*Node, error) {
	if len(data) < nodeHeaderSize {
		return nil, fmt.Errorf("%w: data too short (%d bytes)", ErrDataCorrupt, len(data))
	}
	nodeType := NodeType(data[0])
	ledgerSeq := binary.BigEndian.Uint32(data[1:5])
	nodeData := make(Blob, len(data)-nodeHeaderSize)
	copy(nodeData, data[nodeHeaderSize:])
	return &Node{
		Type:      nodeType,
		Hash:      hash,
		Data:      nodeData,
		LedgerSeq: ledgerSeq,
	}, nil
}
