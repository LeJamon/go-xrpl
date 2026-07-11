package shamap

import (
	"sync"
	"sync/atomic"
)

// fullBelowPartitionMax bounds each of the FullBelowCache's two hash
// partitions. When the live partition fills, it is demoted to the stale
// slot and a fresh one takes its place, capping resident memory at roughly
// 2*fullBelowPartitionMax [32]byte keys without per-entry timestamps. This
// is the idiomatic-Go stand-in for rippled's time-partitioned KeyCache
// (FullBelowCache backed by KeyCache), which sweeps on a wall clock.
const fullBelowPartitionMax = 1 << 17

// FullBelowCache remembers which SHAMap inner nodes have all of their
// descendants resident, so a missing-node walk can prune whole subtrees
// instead of re-descending them on every peer reply. It is the go-xrpl
// analogue of rippled's FullBelowCache (xrpld/shamap/FullBelowCache.h):
//
//   - A monotonically increasing generation tags the marks. Every inner
//     node caches the generation it was last proven full-below at
//     (innerNode.fullBelowGen); a mark counts only while it equals the
//     cache's current generation, so Bump invalidates every outstanding
//     mark in O(1). Fresh and wire/store-deserialized nodes carry
//     generation 0, which never equals a live generation (>= 1), so an
//     unproven node is never mistaken for full-below.
//
//   - A bounded hash set records the node hashes proven full-below. The
//     per-node generation is the in-memory fast path (nodes stay resident
//     across the replies of one acquisition, so it carries the walk); the
//     hash set covers the backed case where a proven subtree's child
//     pointers were released after a flush — a hash-only child whose hash
//     is in the set is skipped without re-fetching and re-materializing its
//     entire subtree from the store.
//
// A node hash is only ever inserted after its whole subtree has been
// verified resident, and SHAMap trees are content-addressed and
// path-copy-persistent, so a recorded mark can never become a false
// positive: the subtree a hash names is immutable.
type FullBelowCache struct {
	gen atomic.Uint32

	mu   sync.Mutex
	live map[[32]byte]struct{}
	old  map[[32]byte]struct{}
}

// NewFullBelowCache returns a cache at generation 1 (the first live
// generation; 0 is reserved for "never proven").
func NewFullBelowCache() *FullBelowCache {
	c := &FullBelowCache{live: make(map[[32]byte]struct{})}
	c.gen.Store(1)
	return c
}

// Generation returns the current generation. A missing-node walk reads it
// once and compares it against each inner node's cached generation.
func (c *FullBelowCache) Generation() uint32 {
	return c.gen.Load()
}

// Bump invalidates every outstanding full-below mark by advancing the
// generation and dropping the recorded hashes. rippled bumps only on a
// NodeFamily reset (never per acquisition, since content-addressing keeps
// marks valid across ledgers); go-xrpl mirrors that — an acquisition gets a
// fresh cache rather than bumping a shared one — and exposes Bump for that
// reset path and for tests that need to defeat the cache.
func (c *FullBelowCache) Bump() {
	c.mu.Lock()
	c.live = make(map[[32]byte]struct{})
	c.old = nil
	c.mu.Unlock()
	c.gen.Add(1)
}

// Has reports whether hash was proven full-below in the current generation.
func (c *FullBelowCache) Has(hash [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.live[hash]; ok {
		return true
	}
	_, ok := c.old[hash]
	return ok
}

// Insert records hash as full-below. When the live partition fills it is
// demoted so recently-inserted hashes survive one more rotation, bounding
// memory without per-entry expiry.
func (c *FullBelowCache) Insert(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.live) >= fullBelowPartitionMax {
		c.old = c.live
		c.live = make(map[[32]byte]struct{})
	}
	c.live[hash] = struct{}{}
}

// Size reports the number of recorded hashes across both partitions.
func (c *FullBelowCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.live) + len(c.old)
}
