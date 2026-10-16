package nodestore

import (
	"container/heap"
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// cacheShardCount must be a power of two so shardFor can mask cheaply.
const cacheShardCount = 16

// Cache charges are deliberately conservative. They cover the retained node
// backing array, the cache entry and the list/heap bookkeeping; map bucket
// capacity is held separately until the shard is cleared and its maps are
// dropped.
const (
	nodeCacheEntryOverhead   = uint64(256)
	nodeCacheStructuralBytes = uint64(128)
)

// cacheEntry represents an entry in the LRU cache.
type cacheEntry struct {
	key       Hash256   // The hash key
	node      *Node     // The cached node
	expiresAt time.Time // When this entry expires
	expiryPos int
	charge    uint64
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

	currentSize      int
	structuralSlots  int
	structuralCharge uint64
	budget           MemoryBudget
	counters         *nodeCacheCounters
}

type nodeCacheCounters struct {
	admissions         atomic.Uint64
	evictions          atomic.Uint64
	admissionDenials   atomic.Uint64
	reservationDenials atomic.Uint64
}

// nodeCache is a sharded LRU cache with TTL support for NodeStore.
//
// The *Node returned by Get aliases the shard's entry and is shared with
// every other reader. Per the Node contract it MUST NOT be mutated;
// callers that need to mutate must Clone() first.
type nodeCache struct {
	shards   [cacheShardCount]*cacheShard
	ttl      time.Duration
	counters nodeCacheCounters
}

func newNodeCache(maxSize int, ttl time.Duration, budgets ...MemoryBudget) *nodeCache {
	c := &nodeCache{ttl: ttl}
	var budget MemoryBudget
	if len(budgets) != 0 {
		budget = budgets[0]
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
			budget:   budget,
			counters: &c.counters,
		}
	}
	return c
}

func (c *nodeCache) shardFor(h Hash256) *cacheShard {
	return c.shards[int(h[0])&(cacheShardCount-1)]
}

// Get returns the cached *Node and true on hit, (nil, false) otherwise.
func (c *nodeCache) Get(hash Hash256) (*Node, bool) {
	return c.lookup(hash, true)
}

// Lookup returns the cached *Node without changing its LRU position. It is
// used by peer reads, whose cache reuse must not displace foreground nodes.
func (c *nodeCache) Lookup(hash Hash256) (*Node, bool) {
	return c.lookup(hash, false)
}

func (c *nodeCache) lookup(hash Hash256, promote bool) (*Node, bool) {
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

	if promote {
		s.lru.MoveToFront(element)
	}
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
	s := c.shardFor(node.Hash)
	s.mu.Lock()
	if !s.reserveForNodeLocked(node, nodeCacheCloneCharge(node)) {
		c.counters.admissionDenials.Add(1)
		s.mu.Unlock()
		return
	}
	owned := node.Clone()
	s.putOwnedWithTTLLocked(owned, c.ttl)
	c.counters.admissions.Add(1)
	s.mu.Unlock()
}

// putOwned transfers node to the cache without copying it. The caller must not
// mutate node after the call; readers receive the same immutable pointer.
func (c *nodeCache) putOwned(node *Node) bool {
	if node == nil {
		return false
	}

	s := c.shardFor(node.Hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.reserveForNodeLocked(node, nodeCacheCharge(node)) {
		c.counters.admissionDenials.Add(1)
		return false
	}
	s.putOwnedWithTTLLocked(node, c.ttl)
	c.counters.admissions.Add(1)
	return true
}

// nodeCacheCharge accounts the retained backing capacity. putOwned aliases
// decoded payloads, so len(node.Data) would undercount a subslice.
func nodeCacheCharge(node *Node) uint64 {
	if node == nil {
		return 0
	}
	return nodeCacheEntryOverhead + uint64(cap(node.Data))
}

// nodeCacheCloneCharge accounts for the exact payload capacity allocated by
// Node.Clone. Put reserves this amount before allocating the defensive copy.
func nodeCacheCloneCharge(node *Node) uint64 {
	if node == nil {
		return 0
	}
	return nodeCacheEntryOverhead + uint64(len(node.Data))
}

// reserveForNodeLocked reserves the replacement before the caller allocates or
// transfers a node into the cache. It may shed this shard's expired/LRU values
// to make room, but it never evicts another shard or calls user code.
func (s *cacheShard) reserveForNodeLocked(node *Node, charge uint64) bool {
	now := time.Now()
	if s.currentSize >= s.maxItems {
		s.sweepExpiredLocked(now)
		if s.currentSize == 0 {
			s.rebuildEmptyLocked()
		}
	}
	_, found := s.items[node.Hash]
	if found {
		if s.tryReserveLocked(charge) {
			return true
		}
		// Keep a useful hit if a replacement cannot be admitted. Expired
		// entries elsewhere in this shard are still safe to discard.
		s.sweepExpiredLocked(now)
		if _, stillPresent := s.items[node.Hash]; stillPresent {
			return s.tryReserveLocked(charge)
		}
		return s.tryReserveNodeLocked(charge)
	}

	if s.tryReserveNodeLocked(charge) {
		return true
	}

	// An admission failure can be caused by this shard's cold entries. Shed
	// only local entries and retry; if the parent budget is full elsewhere the
	// retry still fails and the new node remains caller-owned.
	s.sweepExpiredLocked(now)
	if s.currentSize == 0 {
		s.rebuildEmptyLocked()
		if s.tryReserveNodeLocked(charge) {
			return true
		}
	}
	for s.currentSize > 0 {
		if !s.evictOldestLocked() {
			break
		}
		if s.currentSize == 0 {
			s.rebuildEmptyLocked()
		}
		if s.tryReserveNodeLocked(charge) {
			return true
		}
	}
	return false
}

func (s *cacheShard) tryReserveNodeLocked(charge uint64) bool {
	if s.structuralSlots < s.maxItems {
		charge += nodeCacheStructuralBytes
	}
	return s.tryReserveLocked(charge)
}

func (s *cacheShard) tryReserveLocked(charge uint64) bool {
	if s.budget == nil {
		return true
	}
	if s.budget.TryReserve(charge) {
		return true
	}
	s.counters.reservationDenials.Add(1)
	return false
}

func (s *cacheShard) releaseChargeLocked(charge uint64) {
	if s.budget != nil && charge != 0 {
		s.budget.Release(charge)
	}
}

func (s *cacheShard) putOwnedWithTTLLocked(node *Node, ttl time.Duration) {
	now := time.Now()
	charge := nodeCacheCharge(node)

	if element, found := s.items[node.Hash]; found {
		entry := element.Value.(*cacheEntry)
		oldCharge := entry.charge
		entry.node = node
		entry.charge = charge
		entry.expiresAt = now.Add(ttl)
		heap.Fix(&s.expiry, entry.expiryPos)
		s.lru.MoveToFront(element)
		s.releaseChargeLocked(oldCharge)
		return
	}
	if s.structuralSlots < s.maxItems {
		s.structuralSlots++
		s.structuralCharge += nodeCacheStructuralBytes
	}
	entry := &cacheEntry{
		key:       node.Hash,
		node:      node,
		expiresAt: now.Add(ttl),
		charge:    charge,
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
		s.clearLocked()
		s.mu.Unlock()
	}
}

// Close drops the shard maps and returns every cache-owned reservation. A
// closed cache is only used through its owning database, which serializes
// Close against all public operations.
func (c *nodeCache) Close() {
	for _, s := range c.shards {
		s.mu.Lock()
		s.clearLocked()
		s.items = nil
		s.expiry = nil
		s.structuralSlots = 0
		s.structuralCharge = 0
		s.mu.Unlock()
	}
}

func (s *cacheShard) clearLocked() {
	if s.budget != nil {
		var charge uint64
		for element := s.lru.Front(); element != nil; element = element.Next() {
			charge += element.Value.(*cacheEntry).charge
		}
		s.budget.Release(charge + s.structuralCharge)
	}
	s.items = make(map[Hash256]*list.Element)
	s.lru.Init()
	s.expiry = nil
	s.currentSize = 0
	s.structuralSlots = 0
	s.structuralCharge = 0
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

func (c *nodeCache) accessStats() CacheAccessStats {
	return CacheAccessStats{
		Admissions:         c.counters.admissions.Load(),
		Evictions:          c.counters.evictions.Load(),
		AdmissionDenials:   c.counters.admissionDenials.Load(),
		ReservationDenials: c.counters.reservationDenials.Load(),
	}
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
		s.releaseChargeLocked(entry.charge)
		removed++
	}
	return removed
}

// removeElementLocked removes element. Caller must hold s.mu.
func (s *cacheShard) removeElementLocked(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	heap.Remove(&s.expiry, entry.expiryPos)
	s.removeElementWithoutExpiryLocked(element)
	s.releaseChargeLocked(entry.charge)
}

func (s *cacheShard) removeElementWithoutExpiryLocked(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	delete(s.items, entry.key)
	s.lru.Remove(element)
	s.currentSize--
}

func (s *cacheShard) rebuildEmptyLocked() {
	if s.budget != nil && s.structuralCharge != 0 {
		s.budget.Release(s.structuralCharge)
	}
	s.items = make(map[Hash256]*list.Element)
	s.lru.Init()
	s.expiry = nil
	s.structuralSlots = 0
	s.structuralCharge = 0
}

// evictOldestLocked evicts the LRU entry; returns false on empty shard.
// Caller must hold s.mu.
func (s *cacheShard) evictOldestLocked() bool {
	element := s.lru.Back()
	if element == nil {
		return false
	}
	s.removeElementLocked(element)
	s.counters.evictions.Add(1)
	return true
}
