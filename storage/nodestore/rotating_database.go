package nodestore

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// RotatingKVDatabaseImpl keeps the decoded-node caches above a two-generation
// key-value store, so promotion never duplicates decoded nodes in memory.
type RotatingKVDatabaseImpl struct {
	*KVDatabaseImpl
	rotating kvstore.RotatingStore
}

// NewRotatingKVDatabase constructs one logical NodeStore cache over a rotating
// key-value backend.
func NewRotatingKVDatabase(
	store kvstore.RotatingStore,
	name string,
	config *DatabaseConfig,
) *RotatingKVDatabaseImpl {
	return &RotatingKVDatabaseImpl{
		KVDatabaseImpl: NewKVDatabaseWithConfig(store, name, config),
		rotating:       store,
	}
}

// FetchForPromotion bypasses the positive cache. RotatingStore.Get promotes an
// archive hit before returning, and decodeNodeData takes ownership of the
// returned bytes without an additional payload copy.
func (d *RotatingKVDatabaseImpl) FetchForPromotion(ctx context.Context, hash Hash256) (*Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	atomic.AddUint64(&d.stats.reads, 1)
	var storeGeneration uint64
	if d.negativeCache != nil {
		if d.negativeCache.IsMissing(hash) {
			atomic.AddUint64(&d.stats.negativeCacheHits, 1)
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
func (d *RotatingKVDatabaseImpl) CanRotateWithoutRefresh(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return d.rotating.CanRotateWithoutRefresh()
}

// RotateGeneration serializes the backend swap with stores and clears cache
// entries that may name records retired with the former archive.
func (d *RotatingKVDatabaseImpl) RotateGeneration(
	ctx context.Context,
	lastRotated, minimumOnline uint32,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
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
func (d *RotatingKVDatabaseImpl) GenerationState() (uint32, uint32) {
	return d.rotating.RotationState()
}

var _ GenerationDatabase = (*RotatingKVDatabaseImpl)(nil)
