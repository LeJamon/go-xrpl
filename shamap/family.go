package shamap

import (
	"context"
	"time"
)

// Family provides access to a persistent store for backed SHAMap instances.
// Each SHAMap independently fetches and deserializes nodes from the Family,
// ensuring no shared mutable state between SHAMap instances.
type Family interface {
	// Fetch returns the node's serialized data (prefix format) by SHAMap hash,
	// or (nil, nil) when absent.
	Fetch(ctx context.Context, hash [32]byte) ([]byte, error)

	// StoreBatch persists a batch of serialized nodes.
	StoreBatch(ctx context.Context, entries []FlushEntry) error
}

// PersistenceStats is an optional scalar-only view of an asynchronous Family.
// It lets acquisition diagnostics expose backpressure without fetching nodes.
type PersistenceStats struct {
	CapacityBytes  int64
	PendingBytes   int64
	CurrentBytes   int64
	PeakBytes      int64
	QueueWaits     uint64
	QueueWait      time.Duration
	EntriesWritten uint64
	BytesWritten   uint64
	StoreLatency   time.Duration
	StoreFailures  uint64
}

type fullBelowCacheProvider interface {
	FullBelowCache() *FullBelowCache
}

type durableFamily interface {
	FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error)
}

type nodePlacementFamily interface {
	FetchForNodePlacement(ctx context.Context, hash [32]byte) ([]byte, error)
}

// familyAccess caches the optional capabilities exposed by a Family. A value
// is bound once when the Family is installed on a map and is immutable after
// that.
type familyAccess struct {
	Family
	durable   durableFamily
	placement nodePlacementFamily
}

func bindFamily(family Family) *familyAccess {
	if family == nil {
		return nil
	}
	access := &familyAccess{Family: family}
	access.durable, _ = family.(durableFamily)
	access.placement, _ = family.(nodePlacementFamily)
	return access
}

func (a *familyAccess) available() bool {
	return a != nil && a.Family != nil
}

func (a *familyAccess) fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if !a.available() {
		return nil, nil
	}
	return a.Fetch(ctx, hash)
}

func (a *familyAccess) storeBatch(ctx context.Context, entries []FlushEntry) error {
	if !a.available() {
		return nil
	}
	return a.StoreBatch(ctx, entries)
}

func (a *familyAccess) fetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if a == nil {
		return nil, nil
	}
	if a.durable != nil {
		return a.durable.FetchDurable(ctx, hash)
	}
	return a.fetch(ctx, hash)
}

func (a *familyAccess) fetchPreferDurable(ctx context.Context, hash [32]byte) ([]byte, bool, error) {
	if a == nil {
		return nil, false, nil
	}
	if a.durable != nil {
		data, err := a.durable.FetchDurable(ctx, hash)
		if err != nil || len(data) > 0 {
			return data, len(data) > 0, err
		}
		data, err = a.fetch(ctx, hash)
		return data, false, err
	}
	if !a.available() {
		return nil, false, nil
	}
	data, err := a.fetch(ctx, hash)
	return data, len(data) > 0, err
}

func (a *familyAccess) fetchForPlacement(ctx context.Context, hash [32]byte) ([]byte, error) {
	if a == nil {
		return nil, nil
	}
	if a.placement != nil {
		return a.placement.FetchForNodePlacement(ctx, hash)
	}
	return a.fetchDurable(ctx, hash)
}

func familyFullBelowCache(family Family) *FullBelowCache {
	if provider, ok := family.(fullBelowCacheProvider); ok {
		if cache := provider.FullBelowCache(); cache != nil {
			return cache
		}
	}
	return NewFullBelowCache()
}
