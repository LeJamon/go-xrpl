package shamap

import (
	"container/list"
	"sync"
	"unsafe"
)

// CleanResidencyStats describes the retained, decoded records owned by a
// CleanResidency. Materialized nodes returned to a walk are operation-owned
// and are not included in UsedBytes or Nodes.
type CleanResidencyStats struct {
	Foreground       CleanResidencyClassStats
	Peer             CleanResidencyClassStats
	Limit            uint64
	UsedBytes        uint64
	Nodes            uint64
	Hits             uint64
	Misses           uint64
	Evictions        uint64
	AdmissionDenials uint64
}

// CleanResidencyClassStats reports one bounded access class. ReservationDenials
// counts failed budget attempts, including attempts recovered by eviction.
type CleanResidencyClassStats struct {
	Limit, UsedBytes, Nodes                        uint64
	Lookups, Hits, Admissions, Bypasses, Evictions uint64
	AdmissionDenials, ReservationDenials           uint64
}

type cleanResidencyCounters struct {
	lookups, hits, admissions, bypasses, evictions uint64
	admissionDenials, reservationDenials           uint64
}

type cleanResidencySegment struct {
	limit, used     uint64
	items           map[[32]byte]*list.Element
	order           list.List
	structuralSlots uint64
	counters        cleanResidencyCounters
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
	segments       [2]cleanResidencySegment
	closed         bool
}

// NewCleanResidency creates a bounded clean-node record cache. A zero limit
// creates a disabled cache, which is useful when a Family exposes the optional
// provider but has no cache allowance. The optional budget is charged only for
// retained records and conservative cache structure, and is never waited on.
// One eighth of the limit is reserved for peer records; peer activity never
// evicts or changes the recency of foreground records.
func NewCleanResidency(limit uint64, budget CacheMemoryBudget) *CleanResidency {
	c := &CleanResidency{generation: 1, limit: limit, budget: budget}
	peerLimit := limit / 8
	if peerLimit < cleanResidencyFixedBytes {
		peerLimit = 0
	}
	c.segments[ReadForeground].limit = limit - peerLimit
	c.segments[ReadPeer].limit = peerLimit
	for i := range c.segments {
		c.segments[i].items = make(map[[32]byte]*list.Element)
	}
	return c
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
		c.clearAllLocked(true)
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
	return c.lookupForPolicy(hash, ReadForeground)
}

func (c *CleanResidency) lookupForPolicy(hash [32]byte, policy ReadPolicy) (mapNode, bool) {
	if c == nil {
		return nil, false
	}
	c.generationGate.RLock()
	defer c.generationGate.RUnlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := &c.segments[policy].counters
	stats.lookups++
	if c.closed {
		return nil, false
	}
	for _, class := range []ReadPolicy{ReadForeground, ReadPeer} {
		segment := &c.segments[class]
		element, ok := segment.items[hash]
		if !ok {
			continue
		}
		entry := element.Value.(*cleanResidencyEntry)
		// Materialization keeps the payload charged until its operation-owned copy exists.
		node, err := materializePrefixNode(entry.body)
		if err != nil {
			return nil, false
		}
		if class == policy {
			segment.order.MoveToFront(element)
		} else if policy == ReadForeground {
			// A local reuse earns foreground residency. Reserve the new copy
			// before releasing the peer copy and its structural capacity. A
			// promotion cannot evict other foreground records.
			if c.insertLocked(&c.segments[ReadForeground], hash, entry.body, false) {
				c.removeLocked(segment, element, false)
			}
		}
		stats.hits++
		return node, true
	}
	return nil, false
}

func (c *CleanResidency) insertForGeneration(generation uint64, hash [32]byte, body prefixNodeBody) {
	c.insertForPolicy(generation, hash, body, ReadForeground)
}

func (c *CleanResidency) insertForPolicy(generation uint64, hash [32]byte, body prefixNodeBody, policy ReadPolicy) {
	if c == nil {
		return
	}
	c.generationGate.RLock()
	defer c.generationGate.RUnlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	segment := &c.segments[policy]
	if c.closed || generation != 0 && generation != c.generation {
		segment.counters.admissionDenials++
		return
	}
	// A concurrent foreground admission already provides peer reuse.
	if policy == ReadPeer {
		if _, exists := c.segments[ReadForeground].items[hash]; exists {
			segment.counters.bypasses++
			return
		}
	}
	if c.insertLocked(segment, hash, body, true) && policy == ReadForeground {
		peer := &c.segments[ReadPeer]
		if element, exists := peer.items[hash]; exists {
			c.removeLocked(peer, element, false)
		}
	}
}

func (c *CleanResidency) insertLocked(segment *cleanResidencySegment, hash [32]byte, body prefixNodeBody, evict bool) bool {
	if _, exists := segment.items[hash]; exists {
		return true
	}
	payload, ok := cleanResidencyPayloadBytes(body)
	if !ok || segment.limit == 0 || payload > segment.limit || cleanResidencyFixedBytes > segment.limit-payload {
		segment.counters.bypasses++
		return false
	}
	for {
		structural := uint64(0)
		if uint64(len(segment.items)) >= segment.structuralSlots {
			structural = cleanResidencyFixedBytes
		}
		additional := payload + structural
		if cleanResidencyExceeds(segment.limit, segment.used, additional) {
			if evict && c.evictOldestLocked(segment) {
				continue
			}
			segment.counters.admissionDenials++
			return false
		}
		if c.budget != nil && !c.budget.TryReserve(additional) {
			segment.counters.reservationDenials++
			if evict && c.evictOldestLocked(segment) {
				continue
			}
			segment.counters.admissionDenials++
			return false
		}
		storedPayload := make([]byte, len(body.payload))
		copy(storedPayload, body.payload)
		entry := &cleanResidencyEntry{
			hash:         hash,
			body:         prefixNodeBody{kind: body.kind, payload: storedPayload, key: body.key},
			payloadBytes: payload,
		}
		segment.items[hash] = segment.order.PushFront(entry)
		if structural != 0 {
			segment.structuralSlots++
		}
		segment.used += additional
		segment.counters.admissions++
		return true
	}
}

func (c *CleanResidency) evictOldestLocked(segment *cleanResidencySegment) bool {
	oldest := segment.order.Back()
	if oldest == nil {
		return false
	}
	c.removeLocked(segment, oldest, true)
	return true
}

func (c *CleanResidency) removeLocked(segment *cleanResidencySegment, element *list.Element, eviction bool) {
	entry := element.Value.(*cleanResidencyEntry)
	delete(segment.items, entry.hash)
	segment.order.Remove(element)
	// Map/list high-water capacity stays charged until the segment is empty.
	segment.used -= entry.payloadBytes
	if c.budget != nil && entry.payloadBytes != 0 {
		c.budget.Release(entry.payloadBytes)
	}
	if eviction {
		segment.counters.evictions++
	}
	if len(segment.items) == 0 {
		c.clearLocked(segment, true)
	}
}

func (c *CleanResidency) clearLocked(segment *cleanResidencySegment, reopen bool) {
	if c.budget != nil && segment.used != 0 {
		c.budget.Release(segment.used)
	}
	segment.used = 0
	segment.structuralSlots = 0
	segment.order.Init()
	if reopen {
		segment.items = make(map[[32]byte]*list.Element)
	} else {
		segment.items = nil
	}
}

func (c *CleanResidency) clearAllLocked(reopen bool) {
	for i := range c.segments {
		c.clearLocked(&c.segments[i], reopen)
	}
}

// Stats returns retained bytes and cumulative activity by access class.
func (c *CleanResidency) Stats() CleanResidencyStats {
	if c == nil {
		return CleanResidencyStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := CleanResidencyStats{Limit: c.limit}
	classes := []*CleanResidencyClassStats{&stats.Foreground, &stats.Peer}
	for i, class := range classes {
		segment := &c.segments[i]
		counters := &segment.counters
		*class = CleanResidencyClassStats{
			Limit: segment.limit, UsedBytes: segment.used, Nodes: uint64(len(segment.items)),
			Lookups: counters.lookups, Hits: counters.hits,
			Admissions: counters.admissions, Bypasses: counters.bypasses,
			Evictions: counters.evictions, AdmissionDenials: counters.admissionDenials,
			ReservationDenials: counters.reservationDenials,
		}
		stats.UsedBytes += class.UsedBytes
		stats.Nodes += class.Nodes
		stats.Hits += class.Hits
		stats.Misses += class.Lookups - class.Hits
		stats.Evictions += class.Evictions
		stats.AdmissionDenials += class.AdmissionDenials
	}
	return stats
}

// Close returns all cache-owned charges without invalidating operation-owned nodes.
func (c *CleanResidency) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.clearAllLocked(false)
	c.closed = true
}
