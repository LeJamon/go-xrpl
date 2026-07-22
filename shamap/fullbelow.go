package shamap

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	fullBelowCacheTarget        = 1 << 19
	fullBelowCacheExpiration    = 10 * time.Minute
	fullBelowCacheSweepInterval = 30 * time.Second
	fullBelowMinimumAge         = time.Second
	fullBelowCacheMaxDepth      = 4
)

type fullBelowEntry struct {
	lastAccess time.Time
}

// FullBelowStats is a point-in-time cache snapshot.
type FullBelowStats struct {
	Size       int
	TargetSize int
	Hits       uint64
	Misses     uint64
	Inserts    uint64
	Evictions  uint64
	Sweeps     uint64
}

// FullBelowCache remembers durable SHAMap inner nodes whose descendants have
// all been proven present. The target is deliberately soft: entries age out
// during periodic sweeps instead of being evicted synchronously at insertion,
// so one large acquisition can retain the subtrees it has already completed.
type FullBelowCache struct {
	gen atomic.Uint32

	// walks prevents a generation reset from overtaking an in-flight walk.
	walks sync.RWMutex
	mu    sync.Mutex

	targetSize int
	targetAge  time.Duration
	now        func() time.Time
	entries    map[[32]byte]fullBelowEntry
	hits       uint64
	misses     uint64
	inserts    uint64
	evictions  uint64
	sweeps     uint64
}

// NewFullBelowCache returns a cache at generation 1. Generation 0 is reserved
// for nodes that have never been proven full-below.
func NewFullBelowCache() *FullBelowCache {
	return newFullBelowCacheWithClock(
		fullBelowCacheTarget,
		fullBelowCacheExpiration,
		fullBelowCacheSweepInterval,
		time.Now,
	)
}

func newFullBelowCacheWithClock(
	targetSize int,
	targetAge time.Duration,
	sweepEvery time.Duration,
	now func() time.Time,
) *FullBelowCache {
	if targetSize <= 0 {
		panic("shamap: FullBelowCache target size must be positive")
	}
	if targetAge <= 0 {
		panic("shamap: FullBelowCache target age must be positive")
	}
	if sweepEvery <= 0 {
		panic("shamap: FullBelowCache sweep interval must be positive")
	}
	if now == nil {
		panic("shamap: FullBelowCache clock must not be nil")
	}
	c := &FullBelowCache{
		targetSize: targetSize,
		targetAge:  targetAge,
		now:        now,
		entries:    make(map[[32]byte]fullBelowEntry),
	}
	c.gen.Store(1)
	return c
}

// Generation returns the current generation.
func (c *FullBelowCache) Generation() uint32 {
	return c.gen.Load()
}

// Begin pins the current generation for one missing-node walk. The returned
// function must be called when the walk finishes.
func (c *FullBelowCache) Begin() (uint32, func()) {
	c.walks.RLock()
	return c.gen.Load(), c.walks.RUnlock
}

// Bump invalidates every outstanding mark after a backing-store replacement.
func (c *FullBelowCache) Bump() {
	unlock := c.invalidateAndLock()
	unlock()
}

func (c *FullBelowCache) invalidateAndLock() func() {
	c.walks.Lock()

	c.mu.Lock()
	c.entries = make(map[[32]byte]fullBelowEntry)
	if c.gen.Add(1) == 0 {
		c.gen.Store(1)
	}
	c.mu.Unlock()
	return c.walks.Unlock
}

// Has reports whether hash is proven full-below and refreshes its age.
func (c *FullBelowCache) Has(generation uint32, hash [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen.Load() != generation {
		return false
	}
	entry, ok := c.entries[hash]
	if !ok {
		c.misses++
		return false
	}
	entry.lastAccess = c.now()
	c.entries[hash] = entry
	c.hits++
	return true
}

// Insert records hash as full-below. Existing marks are refreshed. The cache
// may grow beyond its target between sweeps.
func (c *FullBelowCache) Insert(generation uint32, hash [32]byte) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen.Load() != generation {
		return
	}
	if _, exists := c.entries[hash]; !exists {
		c.inserts++
	}
	c.entries[hash] = fullBelowEntry{lastAccess: now}
}

func cacheFullBelow(c *FullBelowCache, generation uint32, hash [32]byte, depth int) {
	if c != nil && depth <= fullBelowCacheMaxDepth {
		c.Insert(generation, hash)
	}
}

// Sweep expires old entries. At or below target size, entries retain the full
// target age. Above target, the age is reduced proportionally but never below
// one second.
func (c *FullBelowCache) Sweep() {
	now := c.now()
	c.mu.Lock()
	c.sweepLocked(now)
	c.mu.Unlock()
}

func (c *FullBelowCache) sweepLocked(now time.Time) {
	age := c.targetAge
	if size := len(c.entries); size > c.targetSize {
		age = time.Duration(int64(c.targetAge) * int64(c.targetSize) / int64(size))
		if age < fullBelowMinimumAge {
			age = fullBelowMinimumAge
		}
	}
	cutoff := now.Add(-age)
	for hash, entry := range c.entries {
		if !entry.lastAccess.After(cutoff) {
			delete(c.entries, hash)
			c.evictions++
		}
	}
	c.sweeps++
}

// Size reports the number of recorded hashes.
func (c *FullBelowCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Stats returns cache size, target and cumulative activity counters.
func (c *FullBelowCache) Stats() FullBelowStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return FullBelowStats{
		Size:       len(c.entries),
		TargetSize: c.targetSize,
		Hits:       c.hits,
		Misses:     c.misses,
		Inserts:    c.inserts,
		Evictions:  c.evictions,
		Sweeps:     c.sweeps,
	}
}
