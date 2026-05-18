package nodestore

import (
	"container/list"
	"fmt"
	"sync"
	"time"
)

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

// Cache implements an LRU cache with TTL support for NodeStore.
type Cache struct {
	mu sync.RWMutex

	maxSize int           // Maximum number of items
	ttl     time.Duration // Time to live for entries

	// LRU implementation
	items map[Hash256]*list.Element // Hash to list element mapping
	lru   *list.List                // LRU list (most recent at front)

	hits         uint64 // Number of cache hits
	misses       uint64 // Number of cache misses
	evictions    uint64 // Number of evictions due to size limit
	expirations  uint64 // Number of expirations due to TTL
	currentSize  int    // Current number of items
	currentBytes int    // Current total bytes stored
}

// NewCache creates a new LRU cache with the specified configuration.
func NewCache(maxSize int, ttl time.Duration) *Cache {
	return &Cache{
		maxSize: maxSize,
		ttl:     ttl,
		items:   make(map[Hash256]*list.Element),
		lru:     list.New(),
	}
}

// Get retrieves a node from the cache.
// Returns the node and true if found, nil and false otherwise.
//
// The returned *Node aliases the cache entry and is shared with every
// other reader. Per the Node contract it MUST NOT be mutated; callers
// that need to modify the data must Clone() first.
func (c *Cache) Get(hash Hash256) (*Node, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, found := c.items[hash]
	if !found {
		c.misses++
		return nil, false
	}

	entry := element.Value.(*cacheEntry)

	if entry.isExpired() {
		c.removeElementLocked(element)
		c.expirations++
		c.misses++
		return nil, false
	}

	// Move to front (most recently used)
	c.lru.MoveToFront(element)
	c.hits++

	return entry.node, true
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

	c.mu.Lock()
	defer c.mu.Unlock()

	if element, found := c.items[owned.Hash]; found {
		entry := element.Value.(*cacheEntry)
		c.currentBytes = c.currentBytes - entry.size + owned.Size()
		entry.node = owned
		entry.expiresAt = time.Now().Add(c.ttl)
		entry.size = owned.Size()
		c.lru.MoveToFront(element)
		return
	}

	entry := &cacheEntry{
		key:       owned.Hash,
		node:      owned,
		expiresAt: time.Now().Add(c.ttl),
		size:      owned.Size(),
	}

	element := c.lru.PushFront(entry)
	c.items[owned.Hash] = element
	c.currentSize++
	c.currentBytes += entry.size

	// Evict oldest items if cache is full
	for c.currentSize > c.maxSize {
		c.evictOldestLocked()
	}
}

// Remove removes a node from the cache.
func (c *Cache) Remove(hash Hash256) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, found := c.items[hash]; found {
		c.removeElementLocked(element)
	}
}

// Clear removes all entries from the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[Hash256]*list.Element)
	c.lru.Init()
	c.currentSize = 0
	c.currentBytes = 0
}

// Sweep removes expired entries from the cache.
func (c *Cache) Sweep() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	now := time.Now()

	for element := c.lru.Back(); element != nil; {
		entry := element.Value.(*cacheEntry)
		if now.After(entry.expiresAt) {
			next := element.Prev()
			c.removeElementLocked(element)
			c.expirations++
			removed++
			element = next
		} else {
			// Since entries are ordered by insertion time and we're going
			// from oldest to newest, we can stop here
			break
		}
	}

	return removed
}

// Stats returns cache statistics.
func (c *Cache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return CacheStats{
		Hits:         c.hits,
		Misses:       c.misses,
		Evictions:    c.evictions,
		Expirations:  c.expirations,
		CurrentSize:  c.currentSize,
		CurrentBytes: c.currentBytes,
		MaxSize:      c.maxSize,
		TTL:          c.ttl,
	}
}

// Size returns the current number of items in the cache.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentSize
}

// ByteSize returns the current total bytes stored in the cache.
func (c *Cache) ByteSize() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentBytes
}

// SetTTL updates the TTL for the cache.
// This only affects new entries; existing entries keep their original expiration.
func (c *Cache) SetTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
}

// SetMaxSize updates the maximum size of the cache.
// If the new size is smaller than the current size, oldest entries are evicted.
func (c *Cache) SetMaxSize(maxSize int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.maxSize = maxSize

	// Evict entries if necessary
	for c.currentSize > c.maxSize {
		c.evictOldestLocked()
	}
}

// removeElementLocked removes an element from the cache.
// This method must be called with the mutex held.
func (c *Cache) removeElementLocked(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	delete(c.items, entry.key)
	c.lru.Remove(element)
	c.currentSize--
	c.currentBytes -= entry.size
}

// evictOldestLocked removes the oldest (least recently used) entry.
// This method must be called with the mutex held.
func (c *Cache) evictOldestLocked() {
	if element := c.lru.Back(); element != nil {
		c.removeElementLocked(element)
		c.evictions++
	}
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
