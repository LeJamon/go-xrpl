package nodestore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// FetchPeerBounded reuses cached nodes without promotion and bounds a backend
// copy before allocation. maxBytes covers the node payload, excluding the
// fixed storage header. Backends without strict reads fail closed.
func (d *KVDatabase) FetchPeerBounded(ctx context.Context, hash Hash256, maxBytes int) (*Node, error) {
	if err := d.begin(ctx); err != nil {
		return nil, err
	}
	defer d.lifecycleMu.RUnlock()
	if maxBytes < 0 || maxBytes > math.MaxInt-nodeEncodingHeaderSize {
		return nil, kvstore.ErrValueTooLarge
	}
	atomic.AddUint64(&d.stats.reads, 1)
	if d.cache != nil {
		node, found := d.cache.Lookup(hash)
		d.recordCacheLookup(ReadPeer, found)
		if found {
			atomic.AddUint64(&d.stats.cacheHits, 1)
			if len(node.Data) > maxBytes {
				return nil, kvstore.ErrValueTooLarge
			}
			atomic.AddUint64(&d.stats.fetchHits, 1)
			atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
			return node, nil
		}
		atomic.AddUint64(&d.stats.cacheMisses, 1)
	} else {
		d.recordCacheLookup(ReadPeer, false)
	}
	d.recordCacheBypass(ReadPeer)
	reader, ok := d.store.(kvstore.BoundedReadingStore)
	if !ok {
		return nil, fmt.Errorf("nodestore: bounded read: %w", errors.ErrUnsupported)
	}
	data, err := reader.GetBounded(ctx, hash[:], maxBytes+nodeEncodingHeaderSize)
	if errors.Is(err, kvstore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	node, err := decodeNodeData(hash, data)
	if err != nil {
		return nil, err
	}
	atomic.AddUint64(&d.stats.fetchHits, 1)
	atomic.AddUint64(&d.stats.readBytes, uint64(len(node.Data)))
	return node, nil
}
