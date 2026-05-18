package nodestore

import (
	"container/list"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// cacheShardCount must be a power of two so shardFor can mask cheaply.
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

// cacheShard is one stripe of the sharded cache. Each shard owns its own
// LRU and mutex so Get/Put on disjoint hashes do not contend.
type cacheShard struct {
	mu sync.Mutex

	items map[Hash256]*list.Element
	lru   *list.List

	// maxItems is the whole cache's maxSize divided by cacheShardCount
	// (rounded up); no global cap is enforced.
	maxItems int

	currentSize  int
	currentBytes int
}

// Cache is a sharded LRU cache with TTL support for NodeStore.
//
// The *Node returned by Get aliases the shard's entry and is shared with
// every other reader. Per the Node contract it MUST NOT be mutated;
// callers that need to mutate must Clone() first.
type Cache struct {
	shards [cacheShardCount]*cacheShard

	configMu sync.RWMutex
	maxSize  int
	ttl      time.Duration

	hits        atomic.Uint64
	misses      atomic.Uint64
	evictions   atomic.Uint64
	expirations atomic.Uint64
}

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

func (c *Cache) shardFor(h Hash256) *cacheShard {
	return c.shards[int(h[0])&(cacheShardCount-1)]
}

// Get returns the cached *Node and true on hit, (nil, false) otherwise.
// The returned *Node aliases the cache entry; see the Cache doc for the
// no-mutation contract.
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

// Put stores a defensive deep copy of node. The cached entry is
// thereafter treated as immutable and shared with all readers.
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
				// LRU is insertion-ordered: a fresh tail implies a fresher head.
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

// removeElementLocked removes element. Caller must hold s.mu.
func (s *cacheShard) removeElementLocked(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	delete(s.items, entry.key)
	s.lru.Remove(element)
	s.currentSize--
	s.currentBytes -= entry.size
}

// evictOldestLocked evicts the LRU entry; returns false on empty shard.
// Caller must hold s.mu.
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
