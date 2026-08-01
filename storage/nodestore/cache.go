package nodestore

import (
	"container/heap"
	"container/list"
	"sync"
	"time"
)

// cacheShardCount must be a power of two so shardFor can mask cheaply.
const cacheShardCount = 16

// cacheEntry represents an entry in the LRU cache.
type cacheEntry struct {
	key       Hash256   // The hash key
	node      *Node     // The cached node
	expiresAt time.Time // When this entry expires
	expiryPos int
}

// isExpired checks if the cache entry has expired.
func (e *cacheEntry) isExpired(now time.Time) bool {
	return now.After(e.expiresAt)
}

type expiryHeap []*cacheEntry

func (h expiryHeap) Len() int { return len(h) }

func (h expiryHeap) Less(i, j int) bool {
	return h[i].expiresAt.Before(h[j].expiresAt)
}

func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].expiryPos = i
	h[j].expiryPos = j
}

func (h *expiryHeap) Push(value any) {
	entry := value.(*cacheEntry)
	entry.expiryPos = len(*h)
	*h = append(*h, entry)
}

func (h *expiryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.expiryPos = -1
	*h = old[:last]
	return entry
}

// cacheShard is one stripe of the sharded cache. Each shard owns its own
// LRU and mutex so Get/Put on disjoint hashes do not contend.
type cacheShard struct {
	mu sync.Mutex

	items  map[Hash256]*list.Element
	lru    *list.List
	expiry expiryHeap

	// maxItems is the whole cache's maxSize divided by cacheShardCount
	// (rounded up); no global cap is enforced.
	maxItems int

	currentSize int
}

// nodeCache is a sharded LRU cache with TTL support for NodeStore.
//
// The *Node returned by Get aliases the shard's entry and is shared with
// every other reader. Per the Node contract it MUST NOT be mutated;
// callers that need to mutate must Clone() first.
type nodeCache struct {
	shards [cacheShardCount]*cacheShard
	ttl    time.Duration
}

func newNodeCache(maxSize int, ttl time.Duration) *nodeCache {
	c := &nodeCache{ttl: ttl}
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

func (c *nodeCache) shardFor(h Hash256) *cacheShard {
	return c.shards[int(h[0])&(cacheShardCount-1)]
}

// Get returns the cached *Node and true on hit, (nil, false) otherwise.
func (c *nodeCache) Get(hash Hash256) (*Node, bool) {
	s := c.shardFor(hash)
	s.mu.Lock()
	element, found := s.items[hash]
	if !found {
		s.mu.Unlock()
		return nil, false
	}

	entry := element.Value.(*cacheEntry)
	if entry.isExpired(time.Now()) {
		s.removeElementLocked(element)
		s.mu.Unlock()
		return nil, false
	}

	s.lru.MoveToFront(element)
	node := entry.node
	s.mu.Unlock()
	return node, true
}

// Put stores a defensive deep copy of node. The cached entry is
// thereafter treated as immutable and shared with all readers.
func (c *nodeCache) Put(node *Node) {
	if node == nil {
		return
	}
	c.putOwned(node.Clone())
}

// putOwned transfers node to the cache without copying it. The caller must not
// mutate node after the call; readers receive the same immutable pointer.
func (c *nodeCache) putOwned(node *Node) {
	if node == nil {
		return
	}
	ttl := c.ttl

	s := c.shardFor(node.Hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()

	if element, found := s.items[node.Hash]; found {
		entry := element.Value.(*cacheEntry)
		entry.node = node
		entry.expiresAt = now.Add(ttl)
		heap.Fix(&s.expiry, entry.expiryPos)
		s.lru.MoveToFront(element)
		return
	}
	if s.currentSize >= s.maxItems {
		s.sweepExpiredLocked(now)
	}

	entry := &cacheEntry{
		key:       node.Hash,
		node:      node,
		expiresAt: now.Add(ttl),
	}
	element := s.lru.PushFront(entry)
	s.items[node.Hash] = element
	heap.Push(&s.expiry, entry)
	s.currentSize++

	for s.currentSize > s.maxItems {
		if !s.evictOldestLocked() {
			break
		}
	}
}

// Remove removes a node from the cache.
func (c *nodeCache) Remove(hash Hash256) {
	s := c.shardFor(hash)
	s.mu.Lock()
	defer s.mu.Unlock()

	if element, found := s.items[hash]; found {
		s.removeElementLocked(element)
	}
}

// Clear removes all entries from the cache.
func (c *nodeCache) Clear() {
	for _, s := range c.shards {
		s.mu.Lock()
		s.items = make(map[Hash256]*list.Element)
		s.lru.Init()
		s.expiry = nil
		s.currentSize = 0
		s.mu.Unlock()
	}
}

func (c *nodeCache) Size() int {
	size := 0
	for _, s := range c.shards {
		s.mu.Lock()
		size += s.currentSize
		s.mu.Unlock()
	}
	return size
}

func (c *nodeCache) Sweep() int {
	removed := 0
	now := time.Now()
	for _, s := range c.shards {
		s.mu.Lock()
		n := s.sweepExpiredLocked(now)
		s.mu.Unlock()
		removed += n
	}
	return removed
}

// sweepExpiredLocked removes every expired entry from the expiration heap.
// Caller must hold s.mu.
func (s *cacheShard) sweepExpiredLocked(now time.Time) int {
	removed := 0
	for len(s.expiry) > 0 && s.expiry[0].isExpired(now) {
		entry := heap.Pop(&s.expiry).(*cacheEntry)
		s.removeElementWithoutExpiryLocked(s.items[entry.key])
		removed++
	}
	return removed
}

// removeElementLocked removes element. Caller must hold s.mu.
func (s *cacheShard) removeElementLocked(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	heap.Remove(&s.expiry, entry.expiryPos)
	s.removeElementWithoutExpiryLocked(element)
}

func (s *cacheShard) removeElementWithoutExpiryLocked(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	delete(s.items, entry.key)
	s.lru.Remove(element)
	s.currentSize--
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
