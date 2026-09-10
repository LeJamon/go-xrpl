// Package sigcache caches positive signature verdicts by transaction ID and
// signing namespace. Legacy nested signatures have a separate namespace because
// their signed bytes change when fixCleanup3_4_0 is enabled. Only successful
// cryptographic verification may populate the cache.
package sigcache

import (
	"sync"
	"time"
)

const (
	// defaultMaxEntries caps a single generation before it rotates. Two
	// generations are retained, so live memory is bounded at ~2× this. Sized
	// to comfortably hold several full blocks worth of transactions.
	defaultMaxEntries = 1 << 17
	// defaultTTL bounds how long a verdict is retained without a size-driven
	// rotation, mirroring rippled's HashRouter hold time. An entry lives
	// between one and two TTLs (or is evicted earlier under size pressure).
	defaultTTL = 5 * time.Minute
)

// Cache is a bounded positive set of verified-good transaction IDs. It uses a
// two-generation rotation (current + previous) so lookups and inserts are O(1),
// memory is bounded without per-entry timestamps, and eviction approximates LRU
// with a TTL floor. Safe for concurrent use.
type Cache struct {
	mu         sync.Mutex
	cur        map[cacheKey]struct{}
	prev       map[cacheKey]struct{}
	maxEntries int
	ttl        time.Duration
	lastRotate time.Time
	now        func() time.Time
}

// cacheKey keeps the pre-cleanup role-signature namespace separate from the
// normal signing namespace. Ordinary transactions and role-bearing
// transactions after fixCleanup3_4_0 intentionally share the normal namespace.
type cacheKey struct {
	id         [32]byte
	legacyRole bool
}

// NewCache builds a cache with the given per-generation size cap and TTL. A nil
// clock defaults to time.Now. Exposed for unit tests; production code uses the
// process-wide global via VerifiedWithRules/MarkVerifiedWithRules.
func NewCache(maxEntries int, ttl time.Duration, clock func() time.Time) *Cache {
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if clock == nil {
		clock = time.Now
	}
	return &Cache{
		cur:        make(map[cacheKey]struct{}),
		prev:       make(map[cacheKey]struct{}),
		maxEntries: maxEntries,
		ttl:        ttl,
		lastRotate: clock(),
		now:        clock,
	}
}

// Has reports whether id is a known verified-good transaction.
func (c *Cache) Has(id [32]byte) bool {
	return c.HasWithRules(id, false)
}

// HasWithRules reports whether id is a known verified-good transaction in the
// requested signature namespace. legacyRole is true only for a transaction
// carrying a role signature while fixCleanup3_4_0 is disabled.
func (c *Cache) HasWithRules(id [32]byte, legacyRole bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maybeRotateLocked()
	key := cacheKey{id: id, legacyRole: legacyRole}
	if _, ok := c.cur[key]; ok {
		return true
	}
	_, ok := c.prev[key]
	return ok
}

// Add records id as verified-good.
func (c *Cache) Add(id [32]byte) {
	c.AddWithRules(id, false)
}

// AddWithRules records id as verified-good in the requested signature
// namespace. Callers must only add an id after genuine cryptographic
// verification of the exact blob represented by that id.
func (c *Cache) AddWithRules(id [32]byte, legacyRole bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maybeRotateLocked()
	c.cur[cacheKey{id: id, legacyRole: legacyRole}] = struct{}{}
}

// Reset empties both generations. Intended for test isolation.
func (c *Cache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = make(map[cacheKey]struct{})
	c.prev = make(map[cacheKey]struct{})
	c.lastRotate = c.now()
}

// maybeRotateLocked ages out the previous generation and promotes the current
// one when the current generation fills up or the TTL elapses. Caller holds mu.
func (c *Cache) maybeRotateLocked() {
	if len(c.cur) < c.maxEntries && c.now().Sub(c.lastRotate) < c.ttl {
		return
	}
	c.prev = c.cur
	c.cur = make(map[cacheKey]struct{})
	c.lastRotate = c.now()
}

// global is the process-wide cache, analogous to rippled's app-wide HashRouter.
var global = NewCache(defaultMaxEntries, defaultTTL, time.Now)

// Verified reports whether the transaction id has a cached verified-good
// signature verdict; a hit lets the caller skip re-verification.
func Verified(id [32]byte) bool {
	return VerifiedWithRules(id, false)
}

// VerifiedWithRules reports whether id has a cached verified-good signature
// verdict in the requested signature namespace.
func VerifiedWithRules(id [32]byte, legacyRole bool) bool {
	return global.HasWithRules(id, legacyRole)
}

// MarkVerified records that the transaction id's signature was verified good.
// Callers MUST only invoke this after a successful cryptographic verification
// of the exact blob that hashes to id — this upholds the positive-cache
// security invariant.
func MarkVerified(id [32]byte) {
	MarkVerifiedWithRules(id, false)
}

// MarkVerifiedWithRules records that id's signature was verified good in the
// requested signature namespace. Callers MUST only invoke this after a
// successful cryptographic verification of the exact blob that hashes to id.
func MarkVerifiedWithRules(id [32]byte, legacyRole bool) {
	global.AddWithRules(id, legacyRole)
}

// Reset clears the process-wide cache. Intended for test isolation.
func Reset() {
	global.Reset()
}
