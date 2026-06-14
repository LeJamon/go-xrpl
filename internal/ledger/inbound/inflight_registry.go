// Copyright (c) 2024-2026. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package inbound

import (
	"maps"
	"sync"
)

// inFlightItem is the minimal behaviour an acquisition must expose for
// the registry to enforce its per-peer cap. Both *ReplayDelta and
// *SkipListAcquire satisfy it; everything else the registry needs is
// type-agnostic, hash-keyed map bookkeeping.
type inFlightItem interface {
	PeerID() uint64
}

// inFlightRegistry is a concurrency-safe set of in-flight acquisitions
// keyed by ledger hash, enforcing a global cap and a per-peer cap on
// admission. The replay-delta and skip-list paths each own one: keeping
// them separate makes the per-hash dedup precise (the same hash can be
// both a replay-delta target and a skip-list target) and stops a deep
// delta backlog from starving short-lived proof-path fetches. The caps
// are sized identically but accounted independently.
type inFlightRegistry[T inFlightItem] struct {
	mu          sync.Mutex
	items       map[[32]byte]T
	maxInFlight int
	maxPerPeer  int
}

// newInFlightRegistry returns an empty registry with the given caps.
func newInFlightRegistry[T inFlightItem](maxInFlight, maxPerPeer int) *inFlightRegistry[T] {
	return &inFlightRegistry[T]{
		items:       make(map[[32]byte]T),
		maxInFlight: maxInFlight,
		maxPerPeer:  maxPerPeer,
	}
}

// add registers a new acquisition for hash, enforcing in order: dedup
// (ErrAcquisitionExists), the global cap (ErrCapacityFull), and the
// per-peer cap against peerID (ErrPerPeerCapacityFull). factory is
// invoked only once every check passes, so a rejected admission never
// constructs an acquisition. The whole sequence runs under the lock so
// concurrent callers observe a consistent count. Returns the zero value
// of T and the sentinel error on rejection.
func (r *inFlightRegistry[T]) add(hash [32]byte, peerID uint64, factory func() T) (T, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var zero T
	if _, exists := r.items[hash]; exists {
		return zero, ErrAcquisitionExists
	}
	if len(r.items) >= r.maxInFlight {
		return zero, ErrCapacityFull
	}

	perPeer := 0
	for _, it := range r.items {
		if it.PeerID() == peerID {
			perPeer++
		}
	}
	if perPeer >= r.maxPerPeer {
		return zero, ErrPerPeerCapacityFull
	}

	item := factory()
	r.items[hash] = item
	return item, nil
}

// get returns the acquisition registered under hash, if any.
func (r *inFlightRegistry[T]) get(hash [32]byte) (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[hash]
	return it, ok
}

// remove drops the acquisition for hash. No-op on an unknown hash, so
// callers can call it unconditionally at the end of a handle path.
func (r *inFlightRegistry[T]) remove(hash [32]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, hash)
}

// has reports whether hash is currently registered.
func (r *inFlightRegistry[T]) has(hash [32]byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.items[hash]
	return ok
}

// count returns the number of in-flight acquisitions.
func (r *inFlightRegistry[T]) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

// drain empties the registry and returns the number of entries removed,
// so a shutdown caller has an observable "N still pending" count.
func (r *inFlightRegistry[T]) drain() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.items)
	r.items = make(map[[32]byte]T)
	return n
}

// snapshot returns a freshly-allocated copy of the hash→item map. The
// item values are shared, so callers must use the items' own
// concurrency-safe methods; the registry lock is released before the
// caller iterates the result.
func (r *inFlightRegistry[T]) snapshot() map[[32]byte]T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.items)
}
