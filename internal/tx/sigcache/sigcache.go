// Package sigcache implements a process-wide, bounded positive cache of
// transaction IDs whose cryptographic signature has already been verified good.
// It is the go-xrpl analog of rippled's SF_SIGGOOD HashRouter flag
// (rippled apply.cpp:78 checkValidity): a signature verdict is keyed by the tx
// ID (SHA-512Half of the signed blob), so it survives the re-parse the
// consensus closed-ledger build performs on the agreed tx set. Without it the
// build re-runs ECDSA/EdDSA verification on every already-verified transaction,
// which does not scale to a full block.
//
// Security invariant: this is a POSITIVE cache only. An entry exists solely
// after a genuine signature verification succeeded for that exact blob. A miss
// therefore always triggers a full verification, so an unknown, never-verified,
// or forged transaction can never skip the crypto check. The tx ID commits to
// the entire signed blob (signature and public key included), so a cache hit
// proves the signature over that blob was previously verified good.
package sigcache

import (
	"sync"
	"sync/atomic"
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
	cur        map[[32]byte]struct{}
	prev       map[[32]byte]struct{}
	maxEntries int
	ttl        time.Duration
	lastRotate time.Time
	now        func() time.Time
}

// NewCache builds a cache with the given per-generation size cap and TTL. A nil
// clock defaults to time.Now. Exposed for unit tests; production code uses the
// process-wide global via Verified/MarkVerified.
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
		cur:        make(map[[32]byte]struct{}),
		prev:       make(map[[32]byte]struct{}),
		maxEntries: maxEntries,
		ttl:        ttl,
		lastRotate: clock(),
		now:        clock,
	}
}

// Has reports whether id is a known verified-good transaction.
func (c *Cache) Has(id [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maybeRotateLocked()
	if _, ok := c.cur[id]; ok {
		return true
	}
	_, ok := c.prev[id]
	return ok
}

// Add records id as verified-good.
func (c *Cache) Add(id [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maybeRotateLocked()
	c.cur[id] = struct{}{}
}

// Reset empties both generations. Intended for test isolation.
func (c *Cache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = make(map[[32]byte]struct{})
	c.prev = make(map[[32]byte]struct{})
	c.lastRotate = c.now()
}

// maybeRotateLocked ages out the previous generation and promotes the current
// one when the current generation fills up or the TTL elapses. Caller holds mu.
func (c *Cache) maybeRotateLocked() {
	if len(c.cur) < c.maxEntries && c.now().Sub(c.lastRotate) < c.ttl {
		return
	}
	c.prev = c.cur
	c.cur = make(map[[32]byte]struct{})
	c.lastRotate = c.now()
}

// global is the process-wide cache, analogous to rippled's app-wide HashRouter.
var global = NewCache(defaultMaxEntries, defaultTTL, time.Now)

// Instrumentation counters (issue-keepup). skipped counts signature
// verifications avoided via a cache hit; verified counts genuine verifications
// recorded. Snapshotted around a build to confirm the redundant-verify savings.
var (
	skipped  atomic.Uint64
	verified atomic.Uint64
)

// Verified reports whether the transaction id has a cached verified-good
// signature verdict; a hit lets the caller skip re-verification. A hit also
// bumps the skipped counter for the issue-keepup instrumentation.
func Verified(id [32]byte) bool {
	if global.Has(id) {
		skipped.Add(1)
		return true
	}
	return false
}

// MarkVerified records that the transaction id's signature was verified good.
// Callers MUST only invoke this after a successful cryptographic verification
// of the exact blob that hashes to id — this upholds the positive-cache
// security invariant. It also bumps the verified counter (issue-keepup).
func MarkVerified(id [32]byte) {
	verified.Add(1)
	global.Add(id)
}

// Reset clears the process-wide cache and instrumentation counters. Intended
// for test isolation.
func Reset() {
	global.Reset()
	skipped.Store(0)
	verified.Store(0)
}

// Stats returns the cumulative counts of skipped (cache-hit) and genuine
// (verified) signature checks. Used by the issue-keepup instrumentation to log
// per-build savings.
func Stats() (skippedCount, verifiedCount uint64) {
	return skipped.Load(), verified.Load()
}
