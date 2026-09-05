package nodestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// RotatingKVDatabase keeps the decoded-node caches above a two-generation
// key-value store, so promotion never duplicates decoded nodes in memory.
type RotatingKVDatabase struct {
	*KVDatabase
	rotating kvstore.RotatingStore
}

// PromotionCacheMetrics returns backend cache counters for refresh benchmarks.
func (d *RotatingKVDatabase) PromotionCacheMetrics() kvstore.CacheMetrics {
	if store, ok := d.rotating.(kvstore.CacheMetricsStore); ok {
		return store.CacheMetrics()
	}
	return kvstore.CacheMetrics{}
}

// PromotionIOMetrics returns optional backend persistence counters for refresh
// benchmark instrumentation.
func (d *RotatingKVDatabase) PromotionIOMetrics() kvstore.IOMetrics {
	if store, ok := d.rotating.(kvstore.IOMetricsStore); ok {
		return store.IOMetrics()
	}
	return kvstore.IOMetrics{}
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

// FetchBatchForPromotion fetches a bounded hash-sorted group without caching decoded nodes.
// Scalar-only backends report Requested, Consumed and BufferedBytes; source-hit
// and write counters are unavailable. They may promote the first unreturned
// record before discovering that it exceeds the remaining byte budget.
func (d *RotatingKVDatabase) FetchBatchForPromotion(
	ctx context.Context,
	hashes []Hash256,
	maxBytes int,
) ([]*Node, kvstore.PromotionStats, error) {
	batchStore, ok := d.rotating.(kvstore.BatchPromotingStore)
	if !ok {
		return d.fetchBatchForPromotionFallback(ctx, hashes, maxBytes)
	}
	if err := d.begin(ctx); err != nil {
		return nil, kvstore.PromotionStats{}, err
	}
	defer d.lifecycleMu.RUnlock()
	keys := make([][]byte, len(hashes))
	for i := range hashes {
		keys[i] = hashes[i][:]
	}
	storeGeneration := d.storeGeneration.Load()
	promotions, stats, err := batchStore.PromoteBatch(keys, maxBytes)
	atomic.AddUint64(&d.stats.reads, uint64(stats.Consumed))
	if err != nil {
		return nil, stats, fmt.Errorf("batch promote fetch failed: %w", err)
	}
	nodes := make([]*Node, len(promotions))
	for i, promotion := range promotions {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		if len(promotion.Key) != len(Hash256{}) {
			return nil, stats, fmt.Errorf("batch promote fetch returned invalid key length %d", len(promotion.Key))
		}
		var hash Hash256
		copy(hash[:], promotion.Key)
		if !promotion.Found {
			if d.negativeCache != nil {
				d.negativeCache.MarkMissing(hash)
				if d.storeGeneration.Load() != storeGeneration {
					d.negativeCache.Remove(hash)
				}
			}
			continue
		}
		node, err := decodeNodeData(hash, promotion.Value)
		if err != nil {
			return nil, stats, err
		}
		nodes[i] = node
		atomic.AddUint64(&d.stats.fetchHits, 1)
		atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	}
	return nodes, stats, nil
}

func (d *RotatingKVDatabase) fetchBatchForPromotionFallback(
	ctx context.Context,
	hashes []Hash256,
	maxBytes int,
) ([]*Node, kvstore.PromotionStats, error) {
	stats := kvstore.PromotionStats{Requested: len(hashes)}
	if len(hashes) == 0 {
		return nil, stats, nil
	}
	if maxBytes <= 0 {
		return nil, stats, errors.New("nodestore: promotion byte limit must be positive")
	}
	sorted := append([]Hash256(nil), hashes...)
	slices.SortFunc(sorted, func(a, b Hash256) int { return bytes.Compare(a[:], b[:]) })
	nodes := make([]*Node, 0, len(sorted))
	for _, hash := range sorted {
		node, err := d.FetchForPromotion(ctx, hash)
		if err != nil {
			return nil, stats, err
		}
		size := 0
		if node != nil {
			size = len(node.Data) + nodeEncodingHeaderSize
		}
		if len(nodes) > 0 && stats.BufferedBytes+size > maxBytes {
			break
		}
		stats.BufferedBytes += size
		stats.Consumed++
		nodes = append(nodes, node)
	}
	return nodes, stats, nil
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
