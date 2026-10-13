package shamap

import (
	"container/list"
	"sync"
	"sync/atomic"
	"unsafe"
)

// CleanResidencyStats describes the retained, decoded records owned by a
// CleanResidency. Materialized nodes returned to a walk are operation-owned
// and are not included in UsedBytes or Nodes.
type CleanResidencyStats struct {
	Limit            uint64
	UsedBytes        uint64
	Nodes            uint64
	Hits             uint64
	Misses           uint64
	Evictions        uint64
	AdmissionDenials uint64
}

type cleanResidencyEntry struct {
	hash         [32]byte
	body         prefixNodeBody
	payloadBytes uint64
}

// The charge includes the entry, its list element, and a conservative amount
// for map/list bookkeeping. It remains reserved while the cache retains any
// entries: Go maps and lists retain bucket/high-water capacity after
// individual entries are evicted, so releasing it per eviction would
// under-account memory. An empty cache discards those structures.
const cleanResidencyFixedBytes = uint64(unsafe.Sizeof(cleanResidencyEntry{})) +
	uint64(unsafe.Sizeof(list.Element{})) + 128

// CleanResidency bounds immutable records decoded from a Family. Records are
// keyed by their content hash and never contain child pointers, so evicting a
// record cannot invalidate a node currently used by a SHAMap walk. A cache
// admission failure only disables reuse for that node; it never blocks or
// changes map correctness.
type CleanResidency struct {
	generationGate sync.RWMutex
	generation     uint64
	mu             sync.Mutex
	limit          uint64
	budget         CacheMemoryBudget
	used           uint64
	items          map[[32]byte]*list.Element
	order          list.List
	closed         bool

	// structuralSlots is the high-water number of cache entries. Its
	// conservative reservation remains in used while entries still occupy the
	// map/list; an empty cache resets those structures and releases it.
	structuralSlots uint64

	hits             atomic.Uint64
	misses           atomic.Uint64
	evictions        atomic.Uint64
	admissionDenials atomic.Uint64
}

// NewCleanResidency creates a bounded clean-node record cache. A zero limit
// creates a disabled cache, which is useful when a Family exposes the optional
// provider but has no cache allowance. The optional budget is charged only for
// retained records and conservative cache structure, and is never waited on.
func NewCleanResidency(limit uint64, budget CacheMemoryBudget) *CleanResidency {
	return &CleanResidency{
		generation: 1,
		limit:      limit,
		budget:     budget,
		items:      make(map[[32]byte]*list.Element),
	}
}

// BeginMutation invalidates all retained records and blocks cache operations
// until the returned function is called. A read that is already fetching from
// the Family may finish, but its earlier generation cannot reinsert a retired
// record after the mutation starts.
func (c *CleanResidency) BeginMutation() func() {
	if c == nil {
		return func() {}
	}
	c.generationGate.Lock()
	c.mu.Lock()
	if !c.closed {
		c.clearLocked(true)
		c.generation++
		if c.generation == 0 {
			c.generation = 1
		}
	}
	c.mu.Unlock()
	return c.generationGate.Unlock
}

func (c *CleanResidency) beginRead() uint64 {
	if c == nil {
		return 0
	}
	c.generationGate.RLock()
	c.mu.Lock()
	generation := c.generation
	c.mu.Unlock()
	c.generationGate.RUnlock()
	return generation
}

func cleanResidencyPayloadBytes(body prefixNodeBody) (uint64, bool) {
	payload := uint64(len(body.payload))
	if payload > ^uint64(0)-cleanResidencyFixedBytes {
		return 0, false
	}
	return payload, true
}

func cleanResidencyExceeds(limit, used, additional uint64) bool {
	return additional > limit || used > limit-additional
}

func (c *CleanResidency) lookup(hash [32]byte) (mapNode, bool) {
	if c == nil {
		return nil, false
	}
	c.generationGate.RLock()
	defer c.generationGate.RUnlock()
	return c.lookupLocked(hash)
}

func (c *CleanResidency) lookupLocked(hash [32]byte) (mapNode, bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}
	element, ok := c.items[hash]
	if !ok {
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}
	c.order.MoveToFront(element)
	entry := element.Value.(*cleanResidencyEntry)
	// Keep the cache lock while materializing so the payload remains covered by
	// the cache's budget reservation until the operation-owned node is complete.
	node, err := materializePrefixNode(entry.body)
	c.mu.Unlock()
	if err != nil {
		// Entries are created only from already-validated records. Treat an
		// invariant violation as a miss rather than returning unverifiable data.
		c.misses.Add(1)
		return nil, false
	}
	c.hits.Add(1)
	return node, true
}

func (c *CleanResidency) insert(hash [32]byte, body prefixNodeBody) {
	if c == nil {
		return
	}
	c.insertForGeneration(0, hash, body)
}

func (c *CleanResidency) insertForGeneration(generation uint64, hash [32]byte, body prefixNodeBody) {
	if c == nil {
		return
	}
	c.generationGate.RLock()
	defer c.generationGate.RUnlock()
	payload, ok := cleanResidencyPayloadBytes(body)
	if !ok || c.limit == 0 || payload > c.limit || cleanResidencyFixedBytes > c.limit-payload {
		c.admissionDenials.Add(1)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || generation != 0 && generation != c.generation {
		c.admissionDenials.Add(1)
		return
	}
	if _, exists := c.items[hash]; exists {
		return
	}

	for {
		structural := uint64(0)
		if uint64(len(c.items)) >= c.structuralSlots {
			structural = cleanResidencyFixedBytes
		}
		additional := payload + structural
		if cleanResidencyExceeds(c.limit, c.used, additional) {
			if c.evictOldestLocked() {
				continue
			}
			if len(c.items) == 0 && c.used != 0 {
				c.resetEmptyLocked()
				continue
			}
			c.admissionDenials.Add(1)
			return
		}
		if c.budget != nil && !c.budget.TryReserve(additional) {
			// A different cache may be consuming the shared pool. Give this
			// cache a chance to free cold payloads before denying admission.
			if c.evictOldestLocked() {
				continue
			}
			if len(c.items) == 0 && c.used != 0 {
				c.resetEmptyLocked()
				continue
			}
			c.admissionDenials.Add(1)
			return
		}
		storedPayload := make([]byte, len(body.payload))
		copy(storedPayload, body.payload)
		entry := &cleanResidencyEntry{
			hash:         hash,
			body:         prefixNodeBody{kind: body.kind, payload: storedPayload, key: body.key},
			payloadBytes: payload,
		}
		element := c.order.PushFront(entry)
		c.items[hash] = element
		if structural != 0 {
			c.structuralSlots++
		}
		c.used += additional
		return
	}
}

func (c *CleanResidency) evictOldestLocked() bool {
	oldest := c.order.Back()
	if oldest == nil {
		return false
	}
	entry := oldest.Value.(*cleanResidencyEntry)
	delete(c.items, entry.hash)
	c.order.Remove(oldest)
	// Keep structural capacity reserved while the cache remains populated;
	// resetEmptyLocked releases it when the backing map/list are discarded.
	c.used -= entry.payloadBytes
	if c.budget != nil && entry.payloadBytes != 0 {
		c.budget.Release(entry.payloadBytes)
	}
	c.evictions.Add(1)
	if len(c.items) == 0 {
		c.resetEmptyLocked()
	}
	return true
}

func (c *CleanResidency) clearLocked(reopen bool) {
	if c.budget != nil && c.used != 0 {
		c.budget.Release(c.used)
	}
	c.used = 0
	c.structuralSlots = 0
	c.order.Init()
	if reopen {
		c.items = make(map[[32]byte]*list.Element)
	} else {
		c.items = nil
	}
}

func (c *CleanResidency) resetEmptyLocked() {
	if len(c.items) == 0 && c.used != 0 {
		c.clearLocked(true)
	}
}

// Stats returns a point-in-time view of retained clean records and cumulative
// cache activity.
func (c *CleanResidency) Stats() CleanResidencyStats {
	if c == nil {
		return CleanResidencyStats{}
	}
	c.mu.Lock()
	stats := CleanResidencyStats{
		Limit:     c.limit,
		UsedBytes: c.used,
		Nodes:     uint64(len(c.items)),
	}
	c.mu.Unlock()
	stats.Hits = c.hits.Load()
	stats.Misses = c.misses.Load()
	stats.Evictions = c.evictions.Load()
	stats.AdmissionDenials = c.admissionDenials.Load()
	return stats
}

// Close drops all retained records and returns their cache-budget charges. It
// is safe to call more than once and does not affect in-flight materialized
// nodes returned by an earlier lookup.
func (c *CleanResidency) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.clearLocked(false)
	c.closed = true
}
