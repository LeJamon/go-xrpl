package nodestore

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// RotationConfig holds configuration for database rotation.
type RotationConfig struct {
	// RotationThreshold is the number of nodes after which rotation should occur.
	// When the primary backend exceeds this threshold, rotation is recommended.
	RotationThreshold int64

	// RetentionPeriod is how long to keep rotating backends before disposal.
	RetentionPeriod time.Duration

	// PrimaryConfig is the configuration for the primary backend.
	PrimaryConfig *Config

	// RotatingPath is the base path for rotating backends.
	// Rotating backends will be created at RotatingPath_N where N is a sequence number.
	RotatingPath string
}

// DefaultRotationConfig returns a RotationConfig with sensible defaults.
func DefaultRotationConfig() *RotationConfig {
	return &RotationConfig{
		RotationThreshold: 10_000_000, // 10 million nodes
		RetentionPeriod:   24 * time.Hour,
	}
}

// Validate checks if the rotation configuration is valid.
func (c *RotationConfig) Validate() error {
	if c.RotationThreshold <= 0 {
		return fmt.Errorf("rotation_threshold must be positive")
	}
	if c.RetentionPeriod <= 0 {
		return fmt.Errorf("retention_period must be positive")
	}
	if c.PrimaryConfig == nil {
		return fmt.Errorf("primary_config must be specified")
	}
	if c.RotatingPath == "" {
		return fmt.Errorf("rotating_path must be specified")
	}
	return nil
}

// rotatingBackend represents a backend in the rotation chain.
type rotatingBackend struct {
	backend   Backend
	createdAt time.Time
	sequence  int64
}

// RotatingDatabase wraps primary and rotating backends for database rotation.
// It stores new data in the primary backend and reads from both primary and rotating
// backends. The rotating backend contains older data that can be disposed of after
// the retention period.
type RotatingDatabase struct {
	mu sync.RWMutex

	config  *RotationConfig
	factory BackendFactory

	// Primary backend for new writes
	primary Backend

	// Chain of rotating backends (oldest first)
	rotating []*rotatingBackend

	// State management
	open     int64
	sequence int64

	stats struct {
		rotations        int64
		primaryReads     int64
		rotatingReads    int64
		primaryWrites    int64
		bytesWritten     int64
		bytesRead        int64
		disposedBackends int64
	}
}

// NewRotatingDatabase creates a new rotating database with the given configuration.
func NewRotatingDatabase(config *RotationConfig, factory BackendFactory) (*RotatingDatabase, error) {
	if config == nil {
		config = DefaultRotationConfig()
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid rotation config: %w", err)
	}

	if factory == nil {
		return nil, fmt.Errorf("backend factory must not be nil")
	}

	rd := &RotatingDatabase{
		config:   config,
		factory:  factory,
		rotating: make([]*rotatingBackend, 0),
	}

	return rd, nil
}

// Open opens the rotating database.
func (rd *RotatingDatabase) Open(createIfMissing bool) error {
	if !atomic.CompareAndSwapInt64(&rd.open, 0, 1) {
		return fmt.Errorf("rotating database already open")
	}

	primary, err := rd.factory(rd.config.PrimaryConfig)
	if err != nil {
		atomic.StoreInt64(&rd.open, 0)
		return fmt.Errorf("failed to create primary backend: %w", err)
	}

	if err := primary.Open(createIfMissing); err != nil {
		atomic.StoreInt64(&rd.open, 0)
		return fmt.Errorf("failed to open primary backend: %w", err)
	}

	rd.primary = primary
	return nil
}

// Close closes all backends in the rotating database.
func (rd *RotatingDatabase) Close() error {
	if !atomic.CompareAndSwapInt64(&rd.open, 1, 0) {
		return nil // Already closed
	}

	rd.mu.Lock()
	defer rd.mu.Unlock()

	var lastErr error

	// Close primary backend
	if rd.primary != nil {
		if err := rd.primary.Close(); err != nil {
			lastErr = err
		}
		rd.primary = nil
	}

	// Close all rotating backends
	for _, rb := range rd.rotating {
		if rb.backend != nil {
			if err := rb.backend.Close(); err != nil {
				lastErr = err
			}
		}
	}
	rd.rotating = nil

	return lastErr
}

// IsOpen returns true if the rotating database is open.
func (rd *RotatingDatabase) IsOpen() bool {
	return atomic.LoadInt64(&rd.open) != 0
}

// Fetch retrieves a node by its hash.
// It tries the primary backend first, then the rotating backends from newest to oldest.
//
// The RLock is only held long enough to snapshot the backend slice;
// the actual Fetch calls run outside the lock so a slow-IO miss does
// not block a concurrent Rotate() (which needs the write lock).
func (rd *RotatingDatabase) Fetch(key Hash256) (*Node, Status) {
	if !rd.IsOpen() {
		return nil, BackendError
	}

	rd.mu.RLock()
	primary := rd.primary
	rotating := append([]*rotatingBackend(nil), rd.rotating...)
	rd.mu.RUnlock()

	if primary != nil {
		node, status := primary.Fetch(key)
		if status == OK {
			atomic.AddInt64(&rd.stats.primaryReads, 1)
			atomic.AddInt64(&rd.stats.bytesRead, int64(len(node.Data)))
			return node, OK
		}
		if status != NotFound {
			return nil, status
		}
	}

	for i := len(rotating) - 1; i >= 0; i-- {
		rb := rotating[i]
		if rb.backend == nil {
			continue
		}
		node, status := rb.backend.Fetch(key)
		if status == OK {
			atomic.AddInt64(&rd.stats.rotatingReads, 1)
			atomic.AddInt64(&rd.stats.bytesRead, int64(len(node.Data)))
			return node, OK
		}
		if status != NotFound {
			return nil, status
		}
	}

	return nil, NotFound
}

// FetchBatch retrieves multiple nodes efficiently.
//
// As with Fetch, only the slice-snapshot phase holds rd.mu — the
// backend Fetches run unlocked so Rotate() is not starved.
func (rd *RotatingDatabase) FetchBatch(keys []Hash256) ([]*Node, Status) {
	if !rd.IsOpen() {
		return nil, BackendError
	}

	results := make([]*Node, len(keys))
	remaining := make(map[int]Hash256, len(keys))
	for i, key := range keys {
		remaining[i] = key
	}

	rd.mu.RLock()
	primary := rd.primary
	rotating := append([]*rotatingBackend(nil), rd.rotating...)
	rd.mu.RUnlock()

	if primary != nil && len(remaining) > 0 {
		for idx, key := range remaining {
			node, status := primary.Fetch(key)
			if status == OK {
				results[idx] = node
				delete(remaining, idx)
				atomic.AddInt64(&rd.stats.primaryReads, 1)
				atomic.AddInt64(&rd.stats.bytesRead, int64(len(node.Data)))
			}
		}
	}

	for i := len(rotating) - 1; i >= 0 && len(remaining) > 0; i-- {
		rb := rotating[i]
		if rb.backend == nil {
			continue
		}
		for idx, key := range remaining {
			node, status := rb.backend.Fetch(key)
			if status == OK {
				results[idx] = node
				delete(remaining, idx)
				atomic.AddInt64(&rd.stats.rotatingReads, 1)
				atomic.AddInt64(&rd.stats.bytesRead, int64(len(node.Data)))
			}
		}
	}

	return results, OK
}

// Store saves a node to the primary backend only.
func (rd *RotatingDatabase) Store(node *Node) Status {
	if node == nil {
		return BackendError
	}

	if !rd.IsOpen() {
		return BackendError
	}

	rd.mu.RLock()
	primary := rd.primary
	rd.mu.RUnlock()

	if primary == nil {
		return BackendError
	}

	status := primary.Store(node)
	if status == OK {
		atomic.AddInt64(&rd.stats.primaryWrites, 1)
		atomic.AddInt64(&rd.stats.bytesWritten, int64(len(node.Data)))
	}

	return status
}

// StoreBatch saves multiple nodes to the primary backend only.
func (rd *RotatingDatabase) StoreBatch(nodes []*Node) Status {
	if !rd.IsOpen() {
		return BackendError
	}

	rd.mu.RLock()
	primary := rd.primary
	rd.mu.RUnlock()

	if primary == nil {
		return BackendError
	}

	status := primary.StoreBatch(nodes)
	if status == OK {
		var totalBytes int64
		for _, node := range nodes {
			if node != nil {
				totalBytes += int64(len(node.Data))
			}
		}
		atomic.AddInt64(&rd.stats.primaryWrites, int64(len(nodes)))
		atomic.AddInt64(&rd.stats.bytesWritten, totalBytes)
	}

	return status
}

// Rotate performs a hot-swap of backends.
// The current primary becomes a rotating backend, and a new primary is created.
func (rd *RotatingDatabase) Rotate() error {
	if !rd.IsOpen() {
		return ErrBackendClosed
	}

	rd.mu.Lock()
	defer rd.mu.Unlock()

	// Sync the current primary before rotation
	if rd.primary != nil {
		rd.primary.Sync()
	}

	// Move current primary to rotating chain
	if rd.primary != nil {
		rb := &rotatingBackend{
			backend:   rd.primary,
			createdAt: time.Now(),
			sequence:  atomic.AddInt64(&rd.sequence, 1),
		}
		rd.rotating = append(rd.rotating, rb)
	}

	newConfig := rd.config.PrimaryConfig.Clone()
	newConfig.Path = fmt.Sprintf("%s_%d", rd.config.RotatingPath, time.Now().UnixNano())

	newPrimary, err := rd.factory(newConfig)
	if err != nil {
		return fmt.Errorf("failed to create new primary backend: %w", err)
	}

	if err := newPrimary.Open(true); err != nil {
		return fmt.Errorf("failed to open new primary backend: %w", err)
	}

	rd.primary = newPrimary
	atomic.AddInt64(&rd.stats.rotations, 1)

	// Clean up old rotating backends that have exceeded retention period
	rd.cleanupExpiredBackendsLocked()

	return nil
}

// cleanupExpiredBackendsLocked removes rotating backends that have exceeded the retention period.
// Must be called with the mutex held.
func (rd *RotatingDatabase) cleanupExpiredBackendsLocked() {
	now := time.Now()
	cutoff := now.Add(-rd.config.RetentionPeriod)

	// Find backends to remove
	var remaining []*rotatingBackend
	for _, rb := range rd.rotating {
		if rb.createdAt.Before(cutoff) {
			// Backend has exceeded retention period, close it
			if rb.backend != nil {
				rb.backend.SetDeletePath() // Mark for deletion
				rb.backend.Close()
			}
			atomic.AddInt64(&rd.stats.disposedBackends, 1)
		} else {
			remaining = append(remaining, rb)
		}
	}

	rd.rotating = remaining
}

// ShouldRotate returns true if the primary backend has exceeded the rotation threshold.
func (rd *RotatingDatabase) ShouldRotate() bool {
	if !rd.IsOpen() {
		return false
	}

	rd.mu.RLock()
	primary := rd.primary
	rd.mu.RUnlock()

	if primary == nil {
		return false
	}

	// Estimate node count based on write operations
	// In a real implementation, you might want to track this more precisely
	writes := atomic.LoadInt64(&rd.stats.primaryWrites)
	return writes >= rd.config.RotationThreshold
}

// Sync forces pending writes to be flushed to all backends.
func (rd *RotatingDatabase) Sync() Status {
	if !rd.IsOpen() {
		return BackendError
	}

	rd.mu.RLock()
	defer rd.mu.RUnlock()

	if rd.primary != nil {
		if status := rd.primary.Sync(); status != OK {
			return status
		}
	}

	for _, rb := range rd.rotating {
		if rb.backend != nil {
			if status := rb.backend.Sync(); status != OK {
				return status
			}
		}
	}

	return OK
}

// ForEach iterates over all objects in all backends.
func (rd *RotatingDatabase) ForEach(fn func(*Node) error) error {
	if !rd.IsOpen() {
		return ErrBackendClosed
	}

	rd.mu.RLock()
	defer rd.mu.RUnlock()

	if rd.primary != nil {
		if err := rd.primary.ForEach(fn); err != nil {
			return err
		}
	}

	for _, rb := range rd.rotating {
		if rb.backend != nil {
			if err := rb.backend.ForEach(fn); err != nil {
				return err
			}
		}
	}

	return nil
}

// GetWriteLoad returns an estimate of pending write operations.
func (rd *RotatingDatabase) GetWriteLoad() int {
	rd.mu.RLock()
	defer rd.mu.RUnlock()

	if rd.primary != nil {
		return rd.primary.GetWriteLoad()
	}
	return 0
}

// SetDeletePath marks all backends for deletion when closed.
func (rd *RotatingDatabase) SetDeletePath() {
	rd.mu.Lock()
	defer rd.mu.Unlock()

	if rd.primary != nil {
		rd.primary.SetDeletePath()
	}

	for _, rb := range rd.rotating {
		if rb.backend != nil {
			rb.backend.SetDeletePath()
		}
	}
}

// FdRequired returns the total number of file descriptors needed.
func (rd *RotatingDatabase) FdRequired() int {
	rd.mu.RLock()
	defer rd.mu.RUnlock()

	total := 0
	if rd.primary != nil {
		total += rd.primary.FdRequired()
	}

	for _, rb := range rd.rotating {
		if rb.backend != nil {
			total += rb.backend.FdRequired()
		}
	}

	return total
}

// Name returns the name of this backend.
func (rd *RotatingDatabase) Name() string {
	return "rotating"
}

// Stats returns statistics about the rotating database.
func (rd *RotatingDatabase) Stats() RotatingDatabaseStats {
	rd.mu.RLock()
	defer rd.mu.RUnlock()

	stats := RotatingDatabaseStats{
		Rotations:        atomic.LoadInt64(&rd.stats.rotations),
		PrimaryReads:     atomic.LoadInt64(&rd.stats.primaryReads),
		RotatingReads:    atomic.LoadInt64(&rd.stats.rotatingReads),
		PrimaryWrites:    atomic.LoadInt64(&rd.stats.primaryWrites),
		BytesWritten:     atomic.LoadInt64(&rd.stats.bytesWritten),
		BytesRead:        atomic.LoadInt64(&rd.stats.bytesRead),
		DisposedBackends: atomic.LoadInt64(&rd.stats.disposedBackends),
		RotatingCount:    len(rd.rotating),
	}

	return stats
}

// PrimaryBackend returns the primary backend (for advanced operations).
func (rd *RotatingDatabase) PrimaryBackend() Backend {
	rd.mu.RLock()
	defer rd.mu.RUnlock()
	return rd.primary
}

// RotatingBackends returns the rotating backends (for advanced operations).
func (rd *RotatingDatabase) RotatingBackends() []Backend {
	rd.mu.RLock()
	defer rd.mu.RUnlock()

	backends := make([]Backend, len(rd.rotating))
	for i, rb := range rd.rotating {
		backends[i] = rb.backend
	}
	return backends
}

// RotatingDatabaseStats holds statistics for the rotating database.
type RotatingDatabaseStats struct {
	Rotations        int64 // Number of rotation operations performed
	PrimaryReads     int64 // Number of reads from primary backend
	RotatingReads    int64 // Number of reads from rotating backends
	PrimaryWrites    int64 // Number of writes to primary backend
	BytesWritten     int64 // Total bytes written
	BytesRead        int64 // Total bytes read
	DisposedBackends int64 // Number of backends disposed after retention
	RotatingCount    int   // Current number of rotating backends
}

// String returns a formatted string representation of the statistics.
func (s RotatingDatabaseStats) String() string {
	return fmt.Sprintf(`RotatingDatabase Statistics:
  Rotations: %d
  Primary Reads: %d
  Rotating Reads: %d
  Primary Writes: %d
  Bytes Read: %d
  Bytes Written: %d
  Disposed Backends: %d
  Current Rotating Backends: %d`,
		s.Rotations,
		s.PrimaryReads,
		s.RotatingReads,
		s.PrimaryWrites,
		s.BytesRead,
		s.BytesWritten,
		s.DisposedBackends,
		s.RotatingCount)
}
