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
	fullBelowEntryBytes      = uint64(64)
	fullBelowStructuralBytes = uint64(128)
	// FullBelowCacheMemoryBytes is the fixed working-set commitment for the
	// default cache, including one structural reservation per retained mark.
	FullBelowCacheMemoryBytes = uint64(fullBelowCacheTarget) *
		(fullBelowEntryBytes + fullBelowStructuralBytes)
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

// CacheMemoryBudget is the narrow admission contract used by the optional
// shared FullBelow cache. The cache owns a reservation while it retains a
// completeness mark and releases it when that mark is evicted or the cache is
// closed.
type CacheMemoryBudget interface {
	TryReserve(uint64) bool
	Release(uint64)
}

type fullBelowShard struct {
	mu               sync.Mutex
	targetSize       int
	currentLimit     int
	current          map[[32]byte]struct{}
	previous         map[[32]byte]struct{}
	lastRotation     time.Time
	budget           CacheMemoryBudget
	structuralSlots  int
	structuralCharge uint64
}

func newFullBelowShard(targetSize int, now time.Time, budget CacheMemoryBudget) fullBelowShard {
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
		budget:       budget,
	}
}

func (s *fullBelowShard) releaseEntriesLocked(count int) {
	if s.budget != nil && count > 0 {
		s.budget.Release(uint64(count) * fullBelowEntryBytes)
	}
}

func (s *fullBelowShard) clearMapLocked(entries map[[32]byte]struct{}) int {
	count := len(entries)
	clear(entries)
	s.releaseEntriesLocked(count)
	return count
}

func (s *fullBelowShard) dropMapsLocked() {
	entries := len(s.current) + len(s.previous)
	s.releaseEntriesLocked(entries)
	if s.budget != nil && s.structuralCharge != 0 {
		s.budget.Release(s.structuralCharge)
	}
	s.current = nil
	s.previous = nil
	s.structuralSlots = 0
	s.structuralCharge = 0
}

func (s *fullBelowShard) reserveEntryLocked() bool {
	charge := fullBelowEntryBytes
	structural := uint64(0)
	if s.structuralSlots < s.targetSize {
		structural = fullBelowStructuralBytes
	}
	if s.budget != nil && !s.budget.TryReserve(charge+structural) {
		return false
	}
	if structural != 0 {
		s.structuralSlots++
		s.structuralCharge += structural
	}
	return true
}

func (s *fullBelowShard) advance(now time.Time, targetAge time.Duration) int {
	if now.Before(s.lastRotation) {
		s.lastRotation = now
		return 0
	}
	if now.Sub(s.lastRotation) >= targetAge {
		evicted := len(s.current) + len(s.previous)
		s.clearMapLocked(s.current)
		s.clearMapLocked(s.previous)
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
	s.clearMapLocked(s.previous)
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
		s.clearMapLocked(s.current)
		s.lastRotation = now
		return evicted
	}
	evicted := len(s.previous)
	s.clearMapLocked(s.previous)
	s.previous, s.current = s.current, s.previous
	s.lastRotation = now
	return evicted
}

// FullBelowCache remembers durable SHAMap inner nodes whose descendants have
// all been proven present. It uses bounded two-generation recency sets because
// rippled's soft proportional-age target permits a fresh acquisition burst to
// retain millions of entries before they become old enough to sweep.
type FullBelowCache struct {
	gen    atomic.Uint32
	closed atomic.Bool

	// walks prevents a generation reset from overtaking an in-flight walk.
	walks   sync.RWMutex
	stateMu sync.RWMutex
	// durableFingerprint identifies the NodeStore generation in which the
	// current cache marks were proven. A cache generation alone is local to the
	// process and cannot detect a durable mutation performed without a cache
	// hook.
	durableFingerprint [32]byte
	durableGeneration  uint32

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
	return newFullBelowCacheWithClockAndBudget(
		fullBelowCacheTarget,
		fullBelowCacheExpiration,
		time.Now,
		nil,
	)
}

// NewFullBelowCacheWithBudget constructs the default cache with an optional
// node-wide admission account. Admission failures simply leave a subtree
// eligible for recomputation; they never affect map correctness.
func NewFullBelowCacheWithBudget(budget CacheMemoryBudget) *FullBelowCache {
	return newFullBelowCacheWithClockAndBudget(
		fullBelowCacheTarget,
		fullBelowCacheExpiration,
		time.Now,
		budget,
	)
}

func newFullBelowCacheWithClock(
	targetSize int,
	targetAge time.Duration,
	now func() time.Time,
) *FullBelowCache {
	return newFullBelowCacheWithClockAndBudget(targetSize, targetAge, now, nil)
}

func newFullBelowCacheWithClockAndBudget(
	targetSize int,
	targetAge time.Duration,
	now func() time.Time,
	budget CacheMemoryBudget,
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
		shards[i] = newFullBelowShard(shardTarget, createdAt, budget)
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
		shard.clearMapLocked(shard.current)
		shard.clearMapLocked(shard.previous)
		shard.lastRotation = resetAt
		shard.mu.Unlock()
	}
	c.durableFingerprint = [32]byte{}
	c.durableGeneration = 0
	c.stateMu.Unlock()
	return c.walks.Unlock
}

// EnsureDurableFingerprint atomically clears stale completeness marks and
// binds the resulting cache generation to fingerprint. The caller must hold
// the corresponding durable snapshot first, matching the store mutation lock
// order.
func (c *FullBelowCache) EnsureDurableFingerprint(fingerprint [32]byte) uint32 {
	if c == nil || fingerprint == ([32]byte{}) {
		return 0
	}
	c.walks.Lock()
	c.stateMu.Lock()
	generation := c.gen.Load()
	if c.durableGeneration == generation && c.durableFingerprint == fingerprint {
		c.stateMu.Unlock()
		c.walks.Unlock()
		return generation
	}
	if c.gen.Add(1) == 0 {
		c.gen.Store(1)
	}
	generation = c.gen.Load()
	resetAt := c.now()
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		shard.clearMapLocked(shard.current)
		shard.clearMapLocked(shard.previous)
		shard.lastRotation = resetAt
		shard.mu.Unlock()
	}
	c.durableGeneration = generation
	c.durableFingerprint = fingerprint
	c.stateMu.Unlock()
	c.walks.Unlock()
	return generation
}

// BindDurableFingerprint associates a cache generation with the durable
// NodeStore generation pinned for its walk. A complete subtree mark still
// establishes the proof; this binding only prevents it crossing mutations.
// It returns false when the cache generation changed or the fingerprint is
// empty.
func (c *FullBelowCache) BindDurableFingerprint(generation uint32, fingerprint [32]byte) bool {
	if c == nil || generation == 0 || fingerprint == ([32]byte{}) {
		return false
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.gen.Load() != generation {
		return false
	}
	if c.durableGeneration == generation && c.durableFingerprint != ([32]byte{}) &&
		c.durableFingerprint != fingerprint {
		return false
	}
	c.durableGeneration = generation
	c.durableFingerprint = fingerprint
	return true
}

// DurableFingerprint reports the durable generation bound to the requested
// cache generation, if a complete walk established one.
func (c *FullBelowCache) DurableFingerprint(generation uint32) ([32]byte, bool) {
	if c == nil || generation == 0 {
		return [32]byte{}, false
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.gen.Load() != generation || c.durableGeneration != generation ||
		c.durableFingerprint == ([32]byte{}) {
		return [32]byte{}, false
	}
	return c.durableFingerprint, true
}

func (c *FullBelowCache) shard(hash [32]byte) *fullBelowShard {
	return &c.shards[int(hash[0])%len(c.shards)]
}

// Has reports whether hash is proven full-below and refreshes its recency.
func (c *FullBelowCache) Has(generation uint32, hash [32]byte) bool {
	if c == nil {
		return false
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.closed.Load() {
		return false
	}
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
	if c == nil {
		return
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.closed.Load() {
		return
	}
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
		if evicted := shard.makeRoom(now); evicted != 0 {
			c.evictions.Add(uint64(evicted))
		}
	} else {
		if evicted := shard.makeRoom(now); evicted != 0 {
			c.evictions.Add(uint64(evicted))
		}
		if !shard.reserveEntryLocked() {
			shard.mu.Unlock()
			return
		}
		c.inserts.Add(1)
	}
	shard.current[hash] = struct{}{}
	shard.mu.Unlock()
}

func cacheFullBelow(c *FullBelowCache, generation uint32, hash [32]byte, depth int) {
	if c != nil && depth <= FullBelowCacheMaxDepth {
		c.Insert(generation, hash)
	}
}

// Close drops both map generations and returns all cache-owned reservations.
// It is idempotent and safe to call after Clear or a prior Close.
func (c *FullBelowCache) Close() {
	if c == nil {
		return
	}
	c.walks.Lock()
	c.stateMu.Lock()
	if !c.closed.Load() {
		for i := range c.shards {
			shard := &c.shards[i]
			shard.mu.Lock()
			shard.dropMapsLocked()
			shard.mu.Unlock()
		}
		c.closed.Store(true)
	}
	c.stateMu.Unlock()
	c.walks.Unlock()
}

// Sweep advances the time-based generations without scanning every entry.
func (c *FullBelowCache) Sweep() {
	if c == nil {
		return
	}
	c.stateMu.RLock()
	if c.closed.Load() {
		c.stateMu.RUnlock()
		return
	}
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
	if c == nil {
		return 0
	}
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.closed.Load() {
		return 0
	}
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
	if c == nil {
		return FullBelowStats{}
	}
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
