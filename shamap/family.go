package shamap

import (
	"context"
	"errors"
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

// ReadPolicy separates cache admission intent from tree attachment.
type ReadPolicy uint8

const (
	// ReadForeground admits records for local ledger work.
	ReadForeground ReadPolicy = iota
	// ReadPeer uses bounded peer residency without promoting foreground entries.
	ReadPeer
)

type policyFamily interface {
	FetchWithPolicy(context.Context, [32]byte, ReadPolicy) ([]byte, error)
}

// boundedFamily optionally provides a strict, prefix-byte bounded read. The
// implementation must reject an oversized record before copying it into a
// caller-owned buffer and report that case with exceeded=true. A missing node
// is (nil, false, nil). Families without this capability cannot safely serve
// a cold node to a bounded wire walk.
type boundedFamily interface {
	FetchBounded(context.Context, [32]byte, int) ([]byte, bool, error)
}

type fullBelowCacheProvider interface {
	FullBelowCache() *FullBelowCache
}

type cleanResidencyProvider interface {
	SHAMapCleanResidency() *CleanResidency
}

type durableFamily interface {
	FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error)
}

type durableSnapshotFamily interface {
	AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
}

type nodePlacementFamily interface {
	FetchForNodePlacement(ctx context.Context, hash [32]byte) ([]byte, error)
}

// familyAccess caches the optional capabilities exposed by a Family. A value
// is bound once when the Family is installed on a map and is immutable after
// that.
type familyAccess struct {
	Family
	durable         durableFamily
	durableSnapshot durableSnapshotFamily
	placement       nodePlacementFamily
	policy          policyFamily
	bounded         boundedFamily
	residency       *CleanResidency
}

func bindFamily(family Family) *familyAccess {
	if family == nil {
		return nil
	}
	access := &familyAccess{Family: family}
	access.durable, _ = family.(durableFamily)
	access.durableSnapshot, _ = family.(durableSnapshotFamily)
	access.placement, _ = family.(nodePlacementFamily)
	access.policy, _ = family.(policyFamily)
	access.bounded, _ = family.(boundedFamily)
	if provider, ok := family.(cleanResidencyProvider); ok {
		access.residency = provider.SHAMapCleanResidency()
	}
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

func (a *familyAccess) fetchWithPolicy(ctx context.Context, hash [32]byte, policy ReadPolicy) ([]byte, error) {
	if a != nil && a.policy != nil {
		return a.policy.FetchWithPolicy(ctx, hash, policy)
	}
	return a.fetch(ctx, hash)
}

func (a *familyAccess) fetchBounded(ctx context.Context, hash [32]byte, maxPrefixBytes int) ([]byte, bool, error) {
	if !a.available() {
		return nil, false, nil
	}
	if a.bounded == nil {
		return nil, false, errors.ErrUnsupported
	}
	return a.bounded.FetchBounded(ctx, hash, maxPrefixBytes)
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

func (a *familyAccess) acquireDurableSnapshot(ctx context.Context) ([32]byte, func(), bool, error) {
	if a == nil || a.durableSnapshot == nil {
		return [32]byte{}, nil, false, nil
	}
	fingerprint, release, err := a.durableSnapshot.AcquireDurableSnapshot(ctx)
	if errors.Is(err, errors.ErrUnsupported) {
		return [32]byte{}, nil, false, nil
	}
	return fingerprint, release, true, err
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
