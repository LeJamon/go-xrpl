package nodestore

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// RotatingKVDatabase keeps the decoded-node caches above a two-generation
// key-value store, so promotion never duplicates decoded nodes in memory.
type RotatingKVDatabase struct {
	*KVDatabase
	rotating kvstore.RotatingStore
}

// DeleteBefore is unsupported for generation stores. Destructive retention
// changes must use RotateGeneration so the durable manifest identity advances.
func (d *RotatingKVDatabase) DeleteBefore(context.Context, uint32, int) (uint64, error) {
	return 0, errors.New("nodestore: direct DeleteBefore is unsupported for rotating stores")
}

// NewRotatingKVDatabase constructs one logical NodeStore cache over a rotating
// key-value backend.
func NewRotatingKVDatabase(
	store kvstore.RotatingStore,
	config DatabaseConfig,
) (*RotatingKVDatabase, error) {
	database, err := NewKVDatabase(store, config)
	if err != nil {
		return nil, err
	}
	return &RotatingKVDatabase{
		KVDatabase: database,
		rotating:   store,
	}, nil
}

// FetchForPromotion bypasses the positive cache. RotatingStore.Get promotes an
// archive hit before returning, and decodeNodeData takes ownership of the
// returned bytes without an additional payload copy.
func (d *RotatingKVDatabase) FetchForPromotion(ctx context.Context, hash Hash256) (*Node, error) {
	if err := d.begin(ctx); err != nil {
		return nil, err
	}
	defer d.lifecycleMu.RUnlock()
	atomic.AddUint64(&d.stats.reads, 1)
	var storeGeneration uint64
	if d.negativeCache != nil {
		if d.negativeCache.IsMissing(hash) {
			return nil, nil
		}
		storeGeneration = d.storeGeneration.Load()
	}
	data, err := d.rotating.Promote(hash[:])
	if err != nil {
		if !errors.Is(err, kvstore.ErrNotFound) {
			return nil, fmt.Errorf("promote fetch failed: %w", err)
		}
		if d.negativeCache != nil {
			d.negativeCache.MarkMissing(hash)
			if d.storeGeneration.Load() != storeGeneration {
				d.negativeCache.Remove(hash)
			}
		}
		return nil, nil
	}
	node, err := decodeNodeData(hash, data)
	if err != nil {
		return nil, err
	}
	atomic.AddUint64(&d.stats.fetchHits, 1)
	atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	return node, nil
}

// CanRotateWithoutRefresh reports whether the archive generation is empty.
func (d *RotatingKVDatabase) CanRotateWithoutRefresh(ctx context.Context) (bool, error) {
	if err := d.begin(ctx); err != nil {
		return false, err
	}
	defer d.lifecycleMu.RUnlock()
	return d.rotating.CanRotateWithoutRefresh()
}

// RotateGeneration serializes the backend swap with stores and clears cache
// entries that may name records retired with the former archive.
func (d *RotatingKVDatabase) RotateGeneration(
	ctx context.Context,
	lastRotated, minimumOnline uint32,
) (bool, error) {
	return d.RotateGenerationWithPrune(ctx, lastRotated, minimumOnline, nil)
}

// RotateGenerationWithPrune acquires the durable mutation gate before
// invalidating SHAMap completeness proofs, preserving the global lock order.
func (d *RotatingKVDatabase) RotateGenerationWithPrune(
	ctx context.Context,
	lastRotated, minimumOnline uint32,
	beginPrune func() func(),
) (bool, error) {
	d.mutationMu.Lock()
	defer d.mutationMu.Unlock()
	if beginPrune != nil {
		finish := beginPrune()
		defer finish()
	}
	if err := d.begin(ctx); err != nil {
		return false, err
	}
	defer d.lifecycleMu.RUnlock()
	d.pruneMu.Lock()
	committed, err := d.rotating.Rotate(lastRotated, minimumOnline)
	if committed {
		d.cacheGeneration.Add(1)
		if d.cache != nil {
			d.cache.Clear()
		}
		if d.negativeCache != nil {
			d.negativeCache.Clear()
		}
		d.storeGeneration.Add(1)
	}
	d.pruneMu.Unlock()
	if err != nil {
		return committed, fmt.Errorf("rotate nodestore generation: %w", err)
	}
	return committed, nil
}

// GenerationState returns the boundary committed with the backend generation
// manifest.
func (d *RotatingKVDatabase) GenerationState() (uint32, uint32) {
	return d.rotating.RotationState()
}

var _ GenerationDatabase = (*RotatingKVDatabase)(nil)
