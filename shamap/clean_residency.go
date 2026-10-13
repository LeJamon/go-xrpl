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
// for map/list bookkeeping. It remains reserved until Close: Go maps and
// lists retain bucket/high-water capacity after individual entries are
// evicted, so releasing this charge per eviction would under-account memory.
const cleanResidencyFixedBytes = uint64(unsafe.Sizeof(cleanResidencyEntry{})) +
	uint64(unsafe.Sizeof(list.Element{})) + 128

// CleanResidency bounds immutable records decoded from a Family. Records are
// keyed by their content hash and never contain child pointers, so evicting a
// record cannot invalidate a node currently used by a SHAMap walk. A cache
// admission failure only disables reuse for that node; it never blocks or
// changes map correctness.
type CleanResidency struct {
	mu     sync.Mutex
	limit  uint64
	budget CacheMemoryBudget
	used   uint64
	items  map[[32]byte]*list.Element
	order  list.List
	closed bool

	// structuralSlots is the high-water number of cache entries. The
	// corresponding structuralCharge is retained until Close even when an
	// entry is evicted, conservatively covering map/list capacity.
	structuralSlots  uint64
	structuralCharge uint64

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
		limit:  limit,
		budget: budget,
		items:  make(map[[32]byte]*list.Element),
	}
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
	payload, ok := cleanResidencyPayloadBytes(body)
	if !ok || c.limit == 0 || payload > c.limit || cleanResidencyFixedBytes > c.limit-payload {
		c.admissionDenials.Add(1)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		c.admissionDenials.Add(1)
		return
	}
	if _, exists := c.items[hash]; exists {
		return
	}

	var structural uint64
	for {
		structural = 0
		if uint64(len(c.items)) >= c.structuralSlots {
			structural = cleanResidencyFixedBytes
		}
		additional := payload + structural
		if !cleanResidencyExceeds(c.limit, c.used, additional) {
			break
		}
		oldest := c.order.Back()
		if oldest == nil {
			c.admissionDenials.Add(1)
			return
		}
		entry := oldest.Value.(*cleanResidencyEntry)
		delete(c.items, entry.hash)
		c.order.Remove(oldest)
		// Keep structuralCharge reserved. Only the exact payload ceases to be
		// live when the entry is removed.
		c.used -= entry.payloadBytes
		if c.budget != nil && entry.payloadBytes != 0 {
			c.budget.Release(entry.payloadBytes)
		}
		c.evictions.Add(1)
	}
	additional := payload + structural
	if c.budget != nil && !c.budget.TryReserve(additional) {
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
		c.structuralCharge += structural
	}
	c.used += additional
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
	if c.budget != nil && c.used != 0 {
		c.budget.Release(c.used)
	}
	c.used = 0
	c.structuralSlots = 0
	c.structuralCharge = 0
	c.items = nil
	c.order.Init()
	c.closed = true
}
