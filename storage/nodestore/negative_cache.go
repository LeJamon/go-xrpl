package nodestore

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// NegativeCache tracks nodes that are known to be missing from the store.
// This optimization prevents repeated backend lookups for non-existent nodes.
type NegativeCache struct {
	mu      sync.RWMutex
	entries map[Hash256]time.Time // Hash -> expiration time
	ttl     time.Duration

	// Statistics
	stats struct {
		hits        int64 // Number of cache hits (confirmed missing)
		misses      int64 // Number of cache misses (not in negative cache)
		insertions  int64 // Number of entries added
		expirations int64 // Number of entries expired
		evictions   int64 // Number of entries evicted
	}

	// Configuration
	maxSize int // Maximum number of entries (0 = unlimited)
	closed  int64
}

// NegativeCacheConfig holds configuration for the negative cache.
type NegativeCacheConfig struct {
	// TTL is the time-to-live for negative cache entries.
	TTL time.Duration

	// MaxSize is the maximum number of entries to cache (0 = unlimited).
	MaxSize int

	// SweepInterval is how often to sweep expired entries (0 = manual sweep only).
	SweepInterval time.Duration
}

// DefaultNegativeCacheConfig returns a NegativeCacheConfig with sensible defaults.
func DefaultNegativeCacheConfig() *NegativeCacheConfig {
	return &NegativeCacheConfig{
		TTL:           5 * time.Minute,
		MaxSize:       100000, // 100k entries
		SweepInterval: time.Minute,
	}
}

// NewNegativeCache creates a new negative cache with the given TTL.
func NewNegativeCache(ttl time.Duration) *NegativeCache {
	return NewNegativeCacheWithConfig(&NegativeCacheConfig{
		TTL:     ttl,
		MaxSize: 100000,
	})
}

// NewNegativeCacheWithConfig creates a new negative cache with the given configuration.
func NewNegativeCacheWithConfig(config *NegativeCacheConfig) *NegativeCache {
	if config == nil {
		config = DefaultNegativeCacheConfig()
	}

	nc := &NegativeCache{
		entries: make(map[Hash256]time.Time),
		ttl:     config.TTL,
		maxSize: config.MaxSize,
	}

	return nc
}

// MarkMissing records that a node is not present in the store.
func (nc *NegativeCache) MarkMissing(hash Hash256) {
	if atomic.LoadInt64(&nc.closed) != 0 {
		return
	}

	nc.mu.Lock()
	defer nc.mu.Unlock()

	// Evict if at capacity
	if nc.maxSize > 0 && len(nc.entries) >= nc.maxSize {
		nc.evictOldestLocked()
	}

	// Add or update entry
	_, exists := nc.entries[hash]
	nc.entries[hash] = time.Now().Add(nc.ttl)

	if !exists {
		atomic.AddInt64(&nc.stats.insertions, 1)
	}
}

// IsMissing checks if a node is known to be missing.
// Returns true if the node is in the negative cache and not expired.
func (nc *NegativeCache) IsMissing(hash Hash256) bool {
	if atomic.LoadInt64(&nc.closed) != 0 {
		return false
	}

	nc.mu.RLock()
	expiresAt, found := nc.entries[hash]
	nc.mu.RUnlock()

	if !found {
		atomic.AddInt64(&nc.stats.misses, 1)
		return false
	}

	// Check if expired
	if time.Now().After(expiresAt) {
		// Entry expired, remove it
		nc.mu.Lock()
		// Double-check under write lock
		if exp, ok := nc.entries[hash]; ok && time.Now().After(exp) {
			delete(nc.entries, hash)
			atomic.AddInt64(&nc.stats.expirations, 1)
		}
		nc.mu.Unlock()

		atomic.AddInt64(&nc.stats.misses, 1)
		return false
	}

	atomic.AddInt64(&nc.stats.hits, 1)
	return true
}

// Remove removes an entry from the negative cache.
// This should be called when a node is added to the store.
func (nc *NegativeCache) Remove(hash Hash256) {
	if atomic.LoadInt64(&nc.closed) != 0 {
		return
	}

	nc.mu.Lock()
	delete(nc.entries, hash)
	nc.mu.Unlock()
}

// Clear removes all entries from the negative cache.
func (nc *NegativeCache) Clear() {
	nc.mu.Lock()
	nc.entries = make(map[Hash256]time.Time)
	nc.mu.Unlock()
}

// Sweep removes all expired entries from the cache.
func (nc *NegativeCache) Sweep() int {
	if atomic.LoadInt64(&nc.closed) != 0 {
		return 0
	}

	nc.mu.Lock()
	defer nc.mu.Unlock()

	now := time.Now()
	removed := 0

	for hash, expiresAt := range nc.entries {
		if now.After(expiresAt) {
			delete(nc.entries, hash)
			removed++
		}
	}

	atomic.AddInt64(&nc.stats.expirations, int64(removed))
	return removed
}

// evictOldestLocked evicts the oldest entries to make room.
// Must be called with mu held.
func (nc *NegativeCache) evictOldestLocked() {
	// Find oldest entries (evict 10% of max size)
	evictCount := nc.maxSize / 10
	if evictCount < 1 {
		evictCount = 1
	}

	// Simple approach: find oldest entries
	type entry struct {
		hash      Hash256
		expiresAt time.Time
	}

	oldest := make([]entry, 0, evictCount)

	for hash, exp := range nc.entries {
		if len(oldest) < evictCount {
			oldest = append(oldest, entry{hash, exp})
		} else {
			// Find the newest in our oldest list
			maxIdx := 0
			for i := 1; i < len(oldest); i++ {
				if oldest[i].expiresAt.After(oldest[maxIdx].expiresAt) {
					maxIdx = i
				}
			}
			// Replace if this entry is older
			if exp.Before(oldest[maxIdx].expiresAt) {
				oldest[maxIdx] = entry{hash, exp}
			}
		}
	}

	// Delete the oldest entries
	for _, e := range oldest {
		delete(nc.entries, e.hash)
		atomic.AddInt64(&nc.stats.evictions, 1)
	}
}

// Size returns the current number of entries in the cache.
func (nc *NegativeCache) Size() int {
	nc.mu.RLock()
	size := len(nc.entries)
	nc.mu.RUnlock()
	return size
}

// SetTTL updates the TTL for new entries.
func (nc *NegativeCache) SetTTL(ttl time.Duration) {
	nc.mu.Lock()
	nc.ttl = ttl
	nc.mu.Unlock()
}

// SetMaxSize updates the maximum size of the cache.
func (nc *NegativeCache) SetMaxSize(maxSize int) {
	nc.mu.Lock()
	nc.maxSize = maxSize
	// Evict if we're now over the limit
	for nc.maxSize > 0 && len(nc.entries) > nc.maxSize {
		nc.evictOldestLocked()
	}
	nc.mu.Unlock()
}

// Close closes the negative cache.
func (nc *NegativeCache) Close() error {
	if !atomic.CompareAndSwapInt64(&nc.closed, 0, 1) {
		return nil // Already closed
	}

	nc.mu.Lock()
	nc.entries = nil
	nc.mu.Unlock()

	return nil
}

// Stats returns statistics about the negative cache.
func (nc *NegativeCache) Stats() NegativeCacheStats {
	nc.mu.RLock()
	size := len(nc.entries)
	nc.mu.RUnlock()

	return NegativeCacheStats{
		Hits:        atomic.LoadInt64(&nc.stats.hits),
		Misses:      atomic.LoadInt64(&nc.stats.misses),
		Insertions:  atomic.LoadInt64(&nc.stats.insertions),
		Expirations: atomic.LoadInt64(&nc.stats.expirations),
		Evictions:   atomic.LoadInt64(&nc.stats.evictions),
		Size:        size,
		MaxSize:     nc.maxSize,
		TTL:         nc.ttl,
	}
}

// NegativeCacheStats holds statistics for the negative cache.
type NegativeCacheStats struct {
	Hits        int64         // Number of cache hits
	Misses      int64         // Number of cache misses
	Insertions  int64         // Number of entries added
	Expirations int64         // Number of entries expired
	Evictions   int64         // Number of entries evicted
	Size        int           // Current number of entries
	MaxSize     int           // Maximum number of entries
	TTL         time.Duration // Time-to-live for entries
}

// HitRate returns the cache hit rate as a percentage.
func (s NegativeCacheStats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total) * 100
}

// String returns a formatted string representation of the statistics.
func (s NegativeCacheStats) String() string {
	return fmt.Sprintf(`NegativeCache Statistics:
  Size: %d/%d entries
  Hits: %d, Misses: %d (%.2f%% hit rate)
  Insertions: %d
  Expirations: %d, Evictions: %d
  TTL: %v`,
		s.Size, s.MaxSize,
		s.Hits, s.Misses, s.HitRate(),
		s.Insertions,
		s.Expirations, s.Evictions,
		s.TTL)
}

// NegativeCacheSweeper automatically sweeps expired entries from a negative cache.
type NegativeCacheSweeper struct {
	cache    *NegativeCache
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// NewNegativeCacheSweeper creates a new sweeper for the given cache.
func NewNegativeCacheSweeper(cache *NegativeCache, interval time.Duration) *NegativeCacheSweeper {
	return &NegativeCacheSweeper{
		cache:    cache,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start starts the sweeper background goroutine.
func (s *NegativeCacheSweeper) Start() {
	s.wg.Add(1)
	go s.run()
}

// Stop stops the sweeper background goroutine.
func (s *NegativeCacheSweeper) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

// run is the sweeper background goroutine.
func (s *NegativeCacheSweeper) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cache.Sweep()
		}
	}
}
