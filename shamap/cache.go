package shamap

import (
	"sync"
	"sync/atomic"
)

// TreeNodeCache provides an LRU cache for frequently accessed SHAMap nodes.
type TreeNodeCache struct {
	mu      sync.Mutex
	maxSize int
	items   map[[32]byte]*lruElem[[32]byte, Node]
	lru     *lruList[[32]byte, Node]

	hits   atomic.Uint64
	misses atomic.Uint64
}

// NewTreeNodeCache returns an LRU cache with the given capacity.
// A non-positive maxSize is replaced by a default.
func NewTreeNodeCache(maxSize int) *TreeNodeCache {
	if maxSize <= 0 {
		maxSize = 1024
	}
	return &TreeNodeCache{
		maxSize: maxSize,
		items:   make(map[[32]byte]*lruElem[[32]byte, Node], maxSize),
		lru:     newLRUList[[32]byte, Node](),
	}
}

// Get returns the cached node for hash, or nil if absent. On a hit the
// entry is moved to the front of the LRU list.
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

// Put inserts node under hash, evicting the LRU entry when at capacity.
// A nil node is a no-op.
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

func (c *TreeNodeCache) Evict(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.items[hash]; found {
		c.lru.remove(elem)
		delete(c.items, hash)
	}
}

func (c *TreeNodeCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[[32]byte]*lruElem[[32]byte, Node], c.maxSize)
	c.lru = newLRUList[[32]byte, Node]()
}

func (c *TreeNodeCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.len
}

func (c *TreeNodeCache) MaxSize() int {
	return c.maxSize
}

func (c *TreeNodeCache) Stats() (hits, misses uint64, size int) {
	c.mu.Lock()
	size = c.lru.len
	c.mu.Unlock()
	return c.hits.Load(), c.misses.Load(), size
}

// HitRate returns hits/(hits+misses), or 0 if neither has occurred.
func (c *TreeNodeCache) HitRate() float64 {
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// Contains reports membership without touching LRU recency.
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

// NewFullBelowCache returns an LRU cache with the given capacity.
// A non-positive maxSize is replaced by a default.
func NewFullBelowCache(maxSize int) *FullBelowCache {
	if maxSize <= 0 {
		maxSize = 65536
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

// MarkFull marks the subtree rooted at hash as fully synced, evicting the
// LRU entry when at capacity.
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

// Unmark removes the full marking; call when a subtree becomes incomplete.
func (c *FullBelowCache) Unmark(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.items[hash]; found {
		c.lru.remove(elem)
		delete(c.items, hash)
	}
}

func (c *FullBelowCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[[32]byte]*lruElem[[32]byte, struct{}], c.maxSize)
	c.lru = newLRUList[[32]byte, struct{}]()
}

func (c *FullBelowCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.len
}

func (c *FullBelowCache) MaxSize() int {
	return c.maxSize
}

// Reset empties the cache and replaces its capacity.
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

// GetAllFull returns a snapshot of every hash currently marked full.
// Order is not guaranteed; recency is not touched.
func (c *FullBelowCache) GetAllFull() [][32]byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([][32]byte, 0, c.lru.len)
	for e := c.lru.front(); e != nil; e = c.lru.next(e) {
		result = append(result, e.key)
	}
	return result
}

// Touch marks hash as full iff every childHash is already full, propagating
// fullness up the tree during sync. Looked-up children have their LRU recency
// refreshed.
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
