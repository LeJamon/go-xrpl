package shamap

import (
	"container/list"
	"sync"
)

// TreeNodeCache provides an LRU cache for frequently accessed SHAMap nodes.
// This improves performance by avoiding repeated deserialization and hash computation
// for nodes that are accessed multiple times during tree operations.
type TreeNodeCache struct {
	mu      sync.RWMutex
	maxSize int
	cache   map[[32]byte]*list.Element
	lruList *list.List
	hits    uint64
	misses  uint64
}

// cacheEntry represents an entry in the node cache.
type cacheEntry struct {
	hash [32]byte
	node Node
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
		cache:   make(map[[32]byte]*list.Element, maxSize),
		lruList: list.New(),
	}
}

// Get retrieves a node from the cache by its hash.
// Returns the node if found, nil otherwise.
// This operation moves the accessed node to the front of the LRU list.
func (c *TreeNodeCache) Get(hash [32]byte) Node {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.cache[hash]; found {
		c.hits++
		c.lruList.MoveToFront(elem)
		return elem.Value.(*cacheEntry).node
	}

	c.misses++
	return nil
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

	if elem, found := c.cache[hash]; found {
		c.lruList.MoveToFront(elem)
		elem.Value.(*cacheEntry).node = node
		return
	}

	// Evict if necessary
	for c.lruList.Len() >= c.maxSize {
		c.evictOldest()
	}

	entry := &cacheEntry{hash: hash, node: node}
	elem := c.lruList.PushFront(entry)
	c.cache[hash] = elem
}

// Evict removes a specific node from the cache.
func (c *TreeNodeCache) Evict(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.cache[hash]; found {
		c.lruList.Remove(elem)
		delete(c.cache, hash)
	}
}

// evictOldest removes the least recently used entry from the cache.
// Caller must hold the write lock.
func (c *TreeNodeCache) evictOldest() {
	elem := c.lruList.Back()
	if elem != nil {
		entry := elem.Value.(*cacheEntry)
		c.lruList.Remove(elem)
		delete(c.cache, entry.hash)
	}
}

// Clear removes all entries from the cache.
func (c *TreeNodeCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = make(map[[32]byte]*list.Element, c.maxSize)
	c.lruList = list.New()
}

// Size returns the current number of entries in the cache.
func (c *TreeNodeCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lruList.Len()
}

// MaxSize returns the maximum capacity of the cache.
func (c *TreeNodeCache) MaxSize() int {
	return c.maxSize
}

// Stats returns cache statistics.
func (c *TreeNodeCache) Stats() (hits, misses uint64, size int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses, c.lruList.Len()
}

// HitRate returns the cache hit rate as a fraction between 0 and 1.
func (c *TreeNodeCache) HitRate() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.hits + c.misses
	if total == 0 {
		return 0
	}
	return float64(c.hits) / float64(total)
}

// Contains checks if a hash is in the cache without affecting LRU order.
func (c *TreeNodeCache) Contains(hash [32]byte) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, found := c.cache[hash]
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
	fullSet map[[32]byte]*list.Element
	lruList *list.List
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
		fullSet: make(map[[32]byte]*list.Element, maxSize),
		lruList: list.New(),
		maxSize: maxSize,
	}
}

// IsFull returns true if the subtree rooted at the given hash is fully synced.
// On a hit, the entry is moved to the front of the LRU list (touched).
func (c *FullBelowCache) IsFull(hash [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, found := c.fullSet[hash]
	if !found {
		return false
	}
	c.lruList.MoveToFront(elem)
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
	if elem, found := c.fullSet[hash]; found {
		c.lruList.MoveToFront(elem)
		return
	}

	for c.lruList.Len() >= c.maxSize {
		c.evictOldestLocked()
	}

	elem := c.lruList.PushFront(hash)
	c.fullSet[hash] = elem
}

// evictOldestLocked removes the least recently used entry.
// Caller must hold c.mu.
func (c *FullBelowCache) evictOldestLocked() {
	elem := c.lruList.Back()
	if elem == nil {
		return
	}
	hash := elem.Value.([32]byte)
	c.lruList.Remove(elem)
	delete(c.fullSet, hash)
}

// Unmark removes the full marking for a hash.
// This should be called when a subtree becomes incomplete (e.g., after modification).
func (c *FullBelowCache) Unmark(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.fullSet[hash]; found {
		c.lruList.Remove(elem)
		delete(c.fullSet, hash)
	}
}

// Clear removes all entries from the cache.
func (c *FullBelowCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fullSet = make(map[[32]byte]*list.Element, c.maxSize)
	c.lruList = list.New()
}

// Size returns the current number of entries in the cache.
func (c *FullBelowCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lruList.Len()
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

	c.fullSet = make(map[[32]byte]*list.Element, maxSize)
	c.lruList = list.New()
	c.maxSize = maxSize
}

// GetAllFull returns a copy of all hashes currently marked as full.
// This is useful for debugging or persisting cache state.
// Order is not guaranteed and this method does not affect LRU recency.
func (c *FullBelowCache) GetAllFull() [][32]byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([][32]byte, 0, c.lruList.Len())
	for elem := c.lruList.Front(); elem != nil; elem = elem.Next() {
		result = append(result, elem.Value.([32]byte))
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

	if elem, found := c.fullSet[hash]; found {
		c.lruList.MoveToFront(elem)
		return true
	}

	for _, childHash := range childHashes {
		elem, found := c.fullSet[childHash]
		if !found {
			return false
		}
		c.lruList.MoveToFront(elem)
	}

	c.markFullLocked(hash)
	return true
}
