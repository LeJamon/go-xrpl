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

// FetchBatchUncached returns a hash-sorted prefix, including nil entries for
// missing nodes and retaining duplicates, without consulting node caches. Limits
// apply to node count and encoded bytes; the first value may exceed maxBytes
// to ensure progress. Backends without batch reads use individual lookups.
func (d *KVDatabase) FetchBatchUncached(ctx context.Context, hashes []Hash256, maxNodes, maxBytes int) ([]*Node, error) {
	if err := d.begin(ctx); err != nil {
		return nil, err
	}
	defer d.lifecycleMu.RUnlock()
	if len(hashes) == 0 {
		return nil, nil
	}
	if maxNodes <= 0 || maxBytes <= 0 {
		return nil, errors.New("nodestore: batch read limits must be positive")
	}
	ordered := slices.Clone(hashes)
	slices.SortFunc(ordered, func(a, b Hash256) int { return bytes.Compare(a[:], b[:]) })
	ordered = ordered[:min(len(ordered), maxNodes)]
	keys := make([][]byte, len(ordered))
	for i := range ordered {
		keys[i] = ordered[i][:]
	}
	var results []kvstore.ReadResult
	var err error
	if batch, ok := d.store.(kvstore.BatchReadingStore); ok {
		results, err = batch.GetBatch(ctx, keys, maxNodes, maxBytes)
	} else {
		results, err = d.readBatchFallback(ctx, keys, maxBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("batch fetch failed: %w", err)
	}
	if len(results) == 0 || len(results) > len(ordered) {
		return nil, fmt.Errorf("nodestore: invalid batch result count %d for %d requests", len(results), len(ordered))
	}
	nodes := make([]*Node, len(results))
	for i, result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !bytes.Equal(result.Key, keys[i]) {
			return nil, fmt.Errorf("nodestore: batch result %d has unexpected key", i)
		}
		atomic.AddUint64(&d.stats.reads, 1)
		if !result.Found {
			continue
		}
		node, err := decodeNodeData(ordered[i], result.Value)
		if err != nil {
			return nil, err
		}
		nodes[i] = node
		atomic.AddUint64(&d.stats.fetchHits, 1)
		atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}

func (d *KVDatabase) readBatchFallback(ctx context.Context, keys [][]byte, maxBytes int) ([]kvstore.ReadResult, error) {
	results := make([]kvstore.ReadResult, 0, len(keys))
	buffered := 0
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := d.store.Get(key)
		found := err == nil
		if err != nil && !errors.Is(err, kvstore.ErrNotFound) {
			return nil, err
		}
		if len(results) > 0 && len(value) > maxBytes-buffered {
			break
		}
		results = append(results, kvstore.ReadResult{Key: key, Value: value, Found: found})
		buffered += len(value)
		if buffered >= maxBytes {
			break
		}
	}
	return results, ctx.Err()
}
