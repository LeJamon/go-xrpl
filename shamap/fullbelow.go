package shamap

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	fullBelowCacheTarget     = 1 << 19
	fullBelowCacheExpiration = 10 * time.Minute
	fullBelowCacheShards     = 16
	// FullBelowCacheMaxDepth is the deepest level at which durable subtree
	// completeness proofs are retained. Startup verification uses the same
	// bound when warming the cache for the first network acquisition.
	FullBelowCacheMaxDepth = 4
)

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

type fullBelowShard struct {
	mu           sync.Mutex
	targetSize   int
	currentLimit int
	current      map[[32]byte]struct{}
	previous     map[[32]byte]struct{}
	lastRotation time.Time
}

func newFullBelowShard(targetSize int, now time.Time) fullBelowShard {
	limit := targetSize / 2
	if limit == 0 {
		limit = 1
	}
	return fullBelowShard{
		targetSize:   targetSize,
		currentLimit: limit,
		current:      make(map[[32]byte]struct{}),
		previous:     make(map[[32]byte]struct{}),
		lastRotation: now,
	}
}

func (s *fullBelowShard) advance(now time.Time, targetAge time.Duration) int {
	if now.Before(s.lastRotation) {
		s.lastRotation = now
		return 0
	}
	if now.Sub(s.lastRotation) >= targetAge {
		evicted := len(s.current) + len(s.previous)
		clear(s.current)
		clear(s.previous)
		s.lastRotation = now
		return evicted
	}
	if s.targetSize == 1 {
		return 0
	}
	if now.Sub(s.lastRotation) < targetAge/2 {
		return 0
	}
	evicted := len(s.previous)
	clear(s.previous)
	s.previous, s.current = s.current, s.previous
	s.lastRotation = now
	return evicted
}

func (s *fullBelowShard) makeRoom(now time.Time) int {
	if len(s.current) < s.currentLimit {
		return 0
	}
	if s.targetSize == 1 {
		evicted := len(s.current)
		clear(s.current)
		s.lastRotation = now
		return evicted
	}
	evicted := len(s.previous)
	clear(s.previous)
	s.previous, s.current = s.current, s.previous
	s.lastRotation = now
	return evicted
}

// FullBelowCache remembers durable SHAMap inner nodes whose descendants have
// all been proven present. It uses bounded two-generation recency sets because
// rippled's soft proportional-age target permits a fresh acquisition burst to
// retain millions of entries before they become old enough to sweep.
type FullBelowCache struct {
	gen atomic.Uint32

	// walks prevents a generation reset from overtaking an in-flight walk.
	walks   sync.RWMutex
	stateMu sync.RWMutex

	targetSize int
	targetAge  time.Duration
	now        func() time.Time
	shards     []fullBelowShard
	hits       atomic.Uint64
	misses     atomic.Uint64
	inserts    atomic.Uint64
	evictions  atomic.Uint64
	sweeps     atomic.Uint64
}

// NewFullBelowCache returns a cache at generation 1. Generation 0 is reserved
// for nodes that have never been proven full-below.
func NewFullBelowCache() *FullBelowCache {
	return newFullBelowCacheWithClock(
		fullBelowCacheTarget,
		fullBelowCacheExpiration,
		time.Now,
	)
}

func newFullBelowCacheWithClock(
	targetSize int,
	targetAge time.Duration,
	now func() time.Time,
) *FullBelowCache {
	if targetSize <= 0 {
		panic("shamap: FullBelowCache target size must be positive")
	}
	if targetAge <= 0 {
		panic("shamap: FullBelowCache target age must be positive")
	}
	if now == nil {
		panic("shamap: FullBelowCache clock must not be nil")
	}

	shardCount := min(fullBelowCacheShards, targetSize)
	shards := make([]fullBelowShard, shardCount)
	base, remainder := targetSize/shardCount, targetSize%shardCount
	createdAt := now()
	for i := range shards {
		shardTarget := base
		if i < remainder {
			shardTarget++
		}
		shards[i] = newFullBelowShard(shardTarget, createdAt)
	}

	c := &FullBelowCache{
		targetSize: targetSize,
		targetAge:  targetAge,
		now:        now,
		shards:     shards,
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
	unlock := c.BeginMutation()
	unlock()
}

// BeginMutation invalidates completeness marks and blocks missing-node walks
// until the returned function is called after the backing-store mutation.
func (c *FullBelowCache) BeginMutation() func() {
	c.walks.Lock()
	c.stateMu.Lock()
	if c.gen.Add(1) == 0 {
		c.gen.Store(1)
	}
	resetAt := c.now()
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		clear(shard.current)
		clear(shard.previous)
		shard.lastRotation = resetAt
		shard.mu.Unlock()
	}
	c.stateMu.Unlock()
	return c.walks.Unlock
}

func (c *FullBelowCache) shard(hash [32]byte) *fullBelowShard {
	return &c.shards[int(hash[0])%len(c.shards)]
}

// Has reports whether hash is proven full-below and refreshes its recency.
func (c *FullBelowCache) Has(generation uint32, hash [32]byte) bool {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.gen.Load() != generation {
		return false
	}

	now := c.now()
	shard := c.shard(hash)
	shard.mu.Lock()
	if evicted := shard.advance(now, c.targetAge); evicted != 0 {
		c.evictions.Add(uint64(evicted))
	}
	if _, ok := shard.current[hash]; ok {
		if shard.targetSize == 1 {
			shard.lastRotation = now
		}
		shard.mu.Unlock()
		c.hits.Add(1)
		return true
	}
	if _, ok := shard.previous[hash]; ok {
		delete(shard.previous, hash)
		if evicted := shard.makeRoom(now); evicted != 0 {
			c.evictions.Add(uint64(evicted))
		}
		shard.current[hash] = struct{}{}
		shard.mu.Unlock()
		c.hits.Add(1)
		return true
	}
	shard.mu.Unlock()
	c.misses.Add(1)
	return false
}

// Insert records hash as full-below. Existing marks are promoted to the
// current generation and scan-cold marks are discarded on rotation.
func (c *FullBelowCache) Insert(generation uint32, hash [32]byte) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.gen.Load() != generation {
		return
	}

	now := c.now()
	shard := c.shard(hash)
	shard.mu.Lock()
	if evicted := shard.advance(now, c.targetAge); evicted != 0 {
		c.evictions.Add(uint64(evicted))
	}
	if _, ok := shard.current[hash]; ok {
		if shard.targetSize == 1 {
			shard.lastRotation = now
		}
		shard.mu.Unlock()
		return
	}
	if _, ok := shard.previous[hash]; ok {
		delete(shard.previous, hash)
	} else {
		c.inserts.Add(1)
	}
	if evicted := shard.makeRoom(now); evicted != 0 {
		c.evictions.Add(uint64(evicted))
	}
	shard.current[hash] = struct{}{}
	shard.mu.Unlock()
}

func cacheFullBelow(c *FullBelowCache, generation uint32, hash [32]byte, depth int) {
	if c != nil && depth <= FullBelowCacheMaxDepth {
		c.Insert(generation, hash)
	}
}

// Sweep advances the time-based generations without scanning every entry.
func (c *FullBelowCache) Sweep() {
	c.stateMu.RLock()
	now := c.now()
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		if evicted := shard.advance(now, c.targetAge); evicted != 0 {
			c.evictions.Add(uint64(evicted))
		}
		shard.mu.Unlock()
	}
	c.sweeps.Add(1)
	c.stateMu.RUnlock()
}

// Size reports the number of recorded hashes.
func (c *FullBelowCache) Size() int {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	size := 0
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		size += len(shard.current) + len(shard.previous)
		shard.mu.Unlock()
	}
	return size
}

// Stats returns cache size, target and cumulative activity counters.
func (c *FullBelowCache) Stats() FullBelowStats {
	return FullBelowStats{
		Size:       c.Size(),
		TargetSize: c.targetSize,
		Hits:       c.hits.Load(),
		Misses:     c.misses.Load(),
		Inserts:    c.inserts.Load(),
		Evictions:  c.evictions.Load(),
		Sweeps:     c.sweeps.Load(),
	}
}
