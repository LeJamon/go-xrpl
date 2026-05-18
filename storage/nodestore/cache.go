package nodestore

import (
	"container/list"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// cacheShardCount is the number of independent shards.  Power of two so
// the per-key shard selector is a cheap mask.  Hash256 keys are
// well-distributed by construction so any one byte makes a good index.
const cacheShardCount = 16

// cacheEntry represents an entry in the LRU cache.
type cacheEntry struct {
	key       Hash256   // The hash key
	node      *Node     // The cached node
	expiresAt time.Time // When this entry expires
	size      int       // Size of the node data in bytes
}

// isExpired checks if the cache entry has expired.
func (e *cacheEntry) isExpired() bool {
	return time.Now().After(e.expiresAt)
}

// cacheShard is one stripe of the sharded cache. Each shard owns its
// own LRU and mutex so Get/Put on disjoint hashes do not contend.
type cacheShard struct {
	mu sync.Mutex

	items map[Hash256]*list.Element
	lru   *list.List

	// Per-shard maxItems is the *whole* cache's maxSize divided by
	// cacheShardCount, rounded up. We do not enforce a global cap.
	maxItems int

	currentSize  int
	currentBytes int
}

// Cache implements a sharded LRU cache with TTL support for NodeStore.
//
// Before the sharding refactor every Get took a single write-lock to
// bump the LRU position, which serialised every cache read across the
// entire NodeStore. With cacheShardCount stripes a Get only contends
// with concurrent Get/Put on the same shard, reducing the critical
// section by roughly cacheShardCount in steady state.
//
// Read contract: the *Node returned by Get aliases the shard's entry
// and is shared with every other reader; per the Node contract it
// MUST NOT be mutated.
type Cache struct {
	shards [cacheShardCount]*cacheShard

	// maxSize / ttl are read-many, write-rare; protected by configMu.
	configMu sync.RWMutex
	maxSize  int
	ttl      time.Duration

	// Aggregate counters tracked lock-free.
	hits        atomic.Uint64
	misses      atomic.Uint64
	evictions   atomic.Uint64
	expirations atomic.Uint64
}

// NewCache creates a new sharded LRU cache.
func NewCache(maxSize int, ttl time.Duration) *Cache {
	c := &Cache{
		maxSize: maxSize,
		ttl:     ttl,
	}
	perShard := maxSize / cacheShardCount
	if perShard*cacheShardCount < maxSize {
		perShard++
	}
	for i := range c.shards {
		c.shards[i] = &cacheShard{
			items:    make(map[Hash256]*list.Element),
			lru:      list.New(),
			maxItems: perShard,
		}
	}
	return c
}

// shardFor returns the shard responsible for the given hash.
func (c *Cache) shardFor(h Hash256) *cacheShard {
	return c.shards[int(h[0])&(cacheShardCount-1)]
}

// Get retrieves a node from the cache.
// Returns the node and true if found, nil and false otherwise.
//
// The returned *Node aliases the cache entry and is shared with every
// other reader. Per the Node contract it MUST NOT be mutated; callers
// that need to modify the data must Clone() first.
func (c *Cache) Get(hash Hash256) (*Node, bool) {
	s := c.shardFor(hash)
	s.mu.Lock()
	element, found := s.items[hash]
	if !found {
		s.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}

	entry := element.Value.(*cacheEntry)
	if entry.isExpired() {
		s.removeElementLocked(element)
		s.mu.Unlock()
		c.expirations.Add(1)
		c.misses.Add(1)
		return nil, false
	}

	s.lru.MoveToFront(element)
	node := entry.node
	s.mu.Unlock()
	c.hits.Add(1)
	return node, true
}

// Put stores a node in the cache.
//
// The cache takes a defensive deep copy so that subsequent mutations of
// the caller's *Node — or of the buffer the backend decoder allocated —
// cannot bleed into the entry returned to other readers. From this
// point on the entry is owned by the cache and treated as immutable.
func (c *Cache) Put(node *Node) {
	if node == nil {
		return
	}

	owned := node.Clone()
	c.configMu.RLock()
	ttl := c.ttl
	c.configMu.RUnlock()

	s := c.shardFor(owned.Hash)
	s.mu.Lock()
	defer s.mu.Unlock()

	if element, found := s.items[owned.Hash]; found {
		entry := element.Value.(*cacheEntry)
		s.currentBytes = s.currentBytes - entry.size + owned.Size()
		entry.node = owned
		entry.expiresAt = time.Now().Add(ttl)
		entry.size = owned.Size()
		s.lru.MoveToFront(element)
		return
	}

	entry := &cacheEntry{
		key:       owned.Hash,
		node:      owned,
		expiresAt: time.Now().Add(ttl),
		size:      owned.Size(),
	}
	element := s.lru.PushFront(entry)
	s.items[owned.Hash] = element
	s.currentSize++
	s.currentBytes += entry.size

	for s.currentSize > s.maxItems {
		if !s.evictOldestLocked() {
			break
		}
		c.evictions.Add(1)
	}
}

// Remove removes a node from the cache.
func (c *Cache) Remove(hash Hash256) {
	s := c.shardFor(hash)
	s.mu.Lock()
	defer s.mu.Unlock()

	if element, found := s.items[hash]; found {
		s.removeElementLocked(element)
	}
}

// Clear removes all entries from the cache.
func (c *Cache) Clear() {
	for _, s := range c.shards {
		s.mu.Lock()
		s.items = make(map[Hash256]*list.Element)
		s.lru.Init()
		s.currentSize = 0
		s.currentBytes = 0
		s.mu.Unlock()
	}
}

// Sweep removes expired entries across every shard.
func (c *Cache) Sweep() int {
	removed := 0
	now := time.Now()
	for _, s := range c.shards {
		s.mu.Lock()
		for element := s.lru.Back(); element != nil; {
			entry := element.Value.(*cacheEntry)
			if now.After(entry.expiresAt) {
				next := element.Prev()
				s.removeElementLocked(element)
				c.expirations.Add(1)
				removed++
				element = next
			} else {
				// Entries are ordered by insertion time; once we hit a
				// fresh entry the rest are fresher still.
				break
			}
		}
		s.mu.Unlock()
	}
	return removed
}

// Stats returns cache statistics.
func (c *Cache) Stats() CacheStats {
	curSize, curBytes := c.sumSizes()

	c.configMu.RLock()
	maxSize, ttl := c.maxSize, c.ttl
	c.configMu.RUnlock()

	return CacheStats{
		Hits:         c.hits.Load(),
		Misses:       c.misses.Load(),
		Evictions:    c.evictions.Load(),
		Expirations:  c.expirations.Load(),
		CurrentSize:  curSize,
		CurrentBytes: curBytes,
		MaxSize:      maxSize,
		TTL:          ttl,
	}
}

// sumSizes aggregates per-shard size counters under each shard's lock.
func (c *Cache) sumSizes() (int, int) {
	size, bytes := 0, 0
	for _, s := range c.shards {
		s.mu.Lock()
		size += s.currentSize
		bytes += s.currentBytes
		s.mu.Unlock()
	}
	return size, bytes
}

// Size returns the current number of items in the cache.
func (c *Cache) Size() int {
	size, _ := c.sumSizes()
	return size
}

// ByteSize returns the current total bytes stored in the cache.
func (c *Cache) ByteSize() int {
	_, bytes := c.sumSizes()
	return bytes
}

// SetTTL updates the TTL for the cache.
// This only affects new entries; existing entries keep their original expiration.
func (c *Cache) SetTTL(ttl time.Duration) {
	c.configMu.Lock()
	c.ttl = ttl
	c.configMu.Unlock()
}

// SetMaxSize updates the maximum size of the cache.
// If the new size is smaller than the current size, oldest entries are evicted.
func (c *Cache) SetMaxSize(maxSize int) {
	c.configMu.Lock()
	c.maxSize = maxSize
	perShard := maxSize / cacheShardCount
	if perShard*cacheShardCount < maxSize {
		perShard++
	}
	c.configMu.Unlock()

	for _, s := range c.shards {
		s.mu.Lock()
		s.maxItems = perShard
		for s.currentSize > s.maxItems {
			if !s.evictOldestLocked() {
				break
			}
			c.evictions.Add(1)
		}
		s.mu.Unlock()
	}
}

// removeElementLocked removes an element from this shard.
// Caller must hold s.mu.
func (s *cacheShard) removeElementLocked(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	delete(s.items, entry.key)
	s.lru.Remove(element)
	s.currentSize--
	s.currentBytes -= entry.size
}

// evictOldestLocked removes the oldest (least recently used) entry
// from this shard. Caller must hold s.mu. Returns true iff an entry
// was evicted.
func (s *cacheShard) evictOldestLocked() bool {
	element := s.lru.Back()
	if element == nil {
		return false
	}
	s.removeElementLocked(element)
	return true
}

// CacheStats holds statistics about cache performance.
type CacheStats struct {
	Hits         uint64        // Number of cache hits
	Misses       uint64        // Number of cache misses
	Evictions    uint64        // Number of evictions due to size limit
	Expirations  uint64        // Number of expirations due to TTL
	CurrentSize  int           // Current number of items
	CurrentBytes int           // Current total bytes stored
	MaxSize      int           // Maximum number of items
	TTL          time.Duration // Time to live for entries
}

// HitRate returns the cache hit rate as a percentage.
func (s CacheStats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total) * 100
}

// String returns a string representation of the cache statistics.
func (s CacheStats) String() string {
	return fmt.Sprintf(`Cache Statistics:
  Size: %d/%d items (%d bytes)
  Hits: %d, Misses: %d (%.2f%% hit rate)
  Evictions: %d, Expirations: %d
  TTL: %v`,
		s.CurrentSize, s.MaxSize, s.CurrentBytes,
		s.Hits, s.Misses, s.HitRate(),
		s.Evictions, s.Expirations,
		s.TTL)
}
