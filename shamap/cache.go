package shamap

import (
	"sync"
	"sync/atomic"
)

// TreeNodeCache provides an LRU cache for frequently accessed SHAMap nodes.
// This improves performance by avoiding repeated deserialization and hash computation
// for nodes that are accessed multiple times during tree operations.
//
// Implementation note: the previous version used container/list under
// a sync.RWMutex with the lock held in write mode on every Get to
// bump the LRU position. That serialised every read across the whole
// cache. The new implementation uses a typed intrusive linked list
// (no per-Element heap alloc, no any-typed Value assertion) and
// atomic hit/miss counters so Stats does not contend with Get/Put.
type TreeNodeCache struct {
	mu      sync.Mutex
	maxSize int
	items   map[[32]byte]*lruElem[[32]byte, Node]
	lru     *lruList[[32]byte, Node]

	hits   atomic.Uint64
	misses atomic.Uint64
}

// NewTreeNodeCache creates a new TreeNodeCache with the specified maximum size.
// The cache uses an LRU eviction policy.
//
// Parameters:
//   - maxSize: maximum number of nodes to cache (must be > 0)
//
// Returns a new TreeNodeCache instance.
func NewTreeNodeCache(maxSize int) *TreeNodeCache {
	if maxSize <= 0 {
		maxSize = 1024 // Default size
	}
	return &TreeNodeCache{
		maxSize: maxSize,
		items:   make(map[[32]byte]*lruElem[[32]byte, Node], maxSize),
		lru:     newLRUList[[32]byte, Node](),
	}
}

// Get retrieves a node from the cache by its hash.
// Returns the node if found, nil otherwise.
// This operation moves the accessed node to the front of the LRU list.
func (c *TreeNodeCache) Get(hash [32]byte) Node {
	c.mu.Lock()
	elem, found := c.items[hash]
	if !found {
		c.mu.Unlock()
		c.misses.Add(1)
		return nil
	}
	c.lru.moveToFront(elem)
	node := elem.val
	c.mu.Unlock()
	c.hits.Add(1)
	return node
}

// Put adds a node to the cache.
// If the cache is full, the least recently used node is evicted.
// If a node with the same hash already exists, it is updated and moved to front.
func (c *TreeNodeCache) Put(hash [32]byte, node Node) {
	if node == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.items[hash]; found {
		elem.val = node
		c.lru.moveToFront(elem)
		return
	}

	elem := &lruElem[[32]byte, Node]{key: hash, val: node}
	c.lru.pushFront(elem)
	c.items[hash] = elem

	for c.lru.len > c.maxSize {
		oldest := c.lru.back()
		if oldest == nil {
			break
		}
		c.lru.remove(oldest)
		delete(c.items, oldest.key)
	}
}

// Evict removes a specific node from the cache.
func (c *TreeNodeCache) Evict(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.items[hash]; found {
		c.lru.remove(elem)
		delete(c.items, hash)
	}
}

// Clear removes all entries from the cache.
func (c *TreeNodeCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[[32]byte]*lruElem[[32]byte, Node], c.maxSize)
	c.lru = newLRUList[[32]byte, Node]()
}

// Size returns the current number of entries in the cache.
func (c *TreeNodeCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.len
}

// MaxSize returns the maximum capacity of the cache.
func (c *TreeNodeCache) MaxSize() int {
	return c.maxSize
}

// Stats returns cache statistics.
func (c *TreeNodeCache) Stats() (hits, misses uint64, size int) {
	c.mu.Lock()
	size = c.lru.len
	c.mu.Unlock()
	return c.hits.Load(), c.misses.Load(), size
}

// HitRate returns the cache hit rate as a fraction between 0 and 1.
func (c *TreeNodeCache) HitRate() float64 {
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// Contains checks if a hash is in the cache without affecting LRU order.
func (c *TreeNodeCache) Contains(hash [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, found := c.items[hash]
	return found
}

// FullBelowCache tracks which subtrees are fully synchronized.
// When a subtree is marked as "full", we know all its nodes are present locally,
// which allows skipping sync checks for that entire subtree.
// This significantly improves sync performance for large trees.
//
// The cache uses an LRU eviction policy: when at capacity, the least
// recently touched entry is evicted. Both reads (IsFull) and writes
// (MarkFull, Touch) update recency.
type FullBelowCache struct {
	mu      sync.Mutex
	maxSize int
	items   map[[32]byte]*lruElem[[32]byte, struct{}]
	lru     *lruList[[32]byte, struct{}]
}

// NewFullBelowCache creates a new FullBelowCache.
//
// Parameters:
//   - maxSize: maximum number of hashes to track (0 = use default size)
//
// Returns a new FullBelowCache instance.
func NewFullBelowCache(maxSize int) *FullBelowCache {
	if maxSize <= 0 {
		maxSize = 65536 // Default size
	}
	return &FullBelowCache{
		maxSize: maxSize,
		items:   make(map[[32]byte]*lruElem[[32]byte, struct{}], maxSize),
		lru:     newLRUList[[32]byte, struct{}](),
	}
}

// IsFull returns true if the subtree rooted at the given hash is fully synced.
// On a hit, the entry is moved to the front of the LRU list (touched).
func (c *FullBelowCache) IsFull(hash [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, found := c.items[hash]
	if !found {
		return false
	}
	c.lru.moveToFront(elem)
	return true
}

// MarkFull marks the subtree rooted at the given hash as fully synced.
// If the cache is at capacity, the least recently used entry is evicted.
// If the hash is already present, it is moved to the front of the LRU list.
func (c *FullBelowCache) MarkFull(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.markFullLocked(hash)
}

// markFullLocked inserts hash into the cache or refreshes its recency.
// Caller must hold c.mu.
func (c *FullBelowCache) markFullLocked(hash [32]byte) {
	if elem, found := c.items[hash]; found {
		c.lru.moveToFront(elem)
		return
	}

	for c.lru.len >= c.maxSize {
		oldest := c.lru.back()
		if oldest == nil {
			break
		}
		c.lru.remove(oldest)
		delete(c.items, oldest.key)
	}

	elem := &lruElem[[32]byte, struct{}]{key: hash}
	c.lru.pushFront(elem)
	c.items[hash] = elem
}

// Unmark removes the full marking for a hash.
// This should be called when a subtree becomes incomplete (e.g., after modification).
func (c *FullBelowCache) Unmark(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.items[hash]; found {
		c.lru.remove(elem)
		delete(c.items, hash)
	}
}

// Clear removes all entries from the cache.
func (c *FullBelowCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[[32]byte]*lruElem[[32]byte, struct{}], c.maxSize)
	c.lru = newLRUList[[32]byte, struct{}]()
}

// Size returns the current number of entries in the cache.
func (c *FullBelowCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.len
}

// MaxSize returns the maximum capacity of the cache.
func (c *FullBelowCache) MaxSize() int {
	return c.maxSize
}

// Reset resets the cache to empty state with a new maximum size.
func (c *FullBelowCache) Reset(maxSize int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if maxSize <= 0 {
		maxSize = 65536
	}
	c.items = make(map[[32]byte]*lruElem[[32]byte, struct{}], maxSize)
	c.lru = newLRUList[[32]byte, struct{}]()
	c.maxSize = maxSize
}

// GetAllFull returns a copy of all hashes currently marked as full.
// This is useful for debugging or persisting cache state.
// Order is not guaranteed and this method does not affect LRU recency.
func (c *FullBelowCache) GetAllFull() [][32]byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([][32]byte, 0, c.lru.len)
	for e := c.lru.front(); e != nil; e = c.lru.next(e) {
		result = append(result, e.key)
	}
	return result
}

// Touch marks a hash as full if and only if all its children are also full.
// This is used to propagate "fullness" up the tree during sync.
// Looking up children also refreshes their LRU recency.
//
// Parameters:
//   - hash: the hash to potentially mark
//   - childHashes: hashes of all children that must be full
//
// Returns true if the hash was marked as full.
func (c *FullBelowCache) Touch(hash [32]byte, childHashes [][32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.items[hash]; found {
		c.lru.moveToFront(elem)
		return true
	}

	for _, childHash := range childHashes {
		elem, found := c.items[childHash]
		if !found {
			return false
		}
		c.lru.moveToFront(elem)
	}

	c.markFullLocked(hash)
	return true
}
