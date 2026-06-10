package nodestore

import (
	"context"
	"encoding/binary"
	"fmt"
)

// defaultDeleteBatch is the number of keys removed per kvstore batch when no
// explicit batch size is supplied. It bounds the in-memory key list and the
// per-batch write size so a single prune pass over a large backend never holds
// the whole keyspace in memory at once.
const defaultDeleteBatch = 65536

// PrunableDatabase is a Database that supports online deletion of ledgers below
// a retention boundary. It is a capability interface: a Database may implement
// it to participate in online_delete rotation. Backends that cannot enumerate
// their keyspace simply do not implement it.
type PrunableDatabase interface {
	Database

	// DeleteBefore removes every stored node whose LedgerSeq is strictly below
	// boundary, returning the number of nodes deleted. Nodes still referenced by
	// the live state tree are immune: the ledger persistence path re-writes every
	// live state node on each ledger with the current sequence, so a node that is
	// still part of recent state always carries a LedgerSeq at or above the
	// retained range. Only superseded state nodes, old ledger headers, and old
	// transaction blobs carry a LedgerSeq below the boundary.
	//
	// Deletion is performed in batches of at most batchSize keys (a non-positive
	// batchSize selects a default). The context is honoured between batches so a
	// shutdown unblocks the caller promptly. A best-effort partial deletion is
	// reported even when the context is cancelled mid-pass.
	DeleteBefore(ctx context.Context, boundary uint32, batchSize int) (deleted uint64, err error)
}

// DeleteBefore implements PrunableDatabase. It scans the underlying kvstore,
// decodes the inline ledger sequence from each stored value, and batch-deletes
// the keys below boundary, evicting them from the in-memory caches as it goes.
func (d *KVDatabaseImpl) DeleteBefore(ctx context.Context, boundary uint32, batchSize int) (uint64, error) {
	if boundary == 0 {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = defaultDeleteBatch
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	var deleted uint64
	pending := make([][]byte, 0, batchSize)

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		batch := d.store.NewBatch()
		for _, k := range pending {
			if err := batch.Delete(k); err != nil {
				return fmt.Errorf("delete-before batch: %w", err)
			}
		}
		if err := batch.Write(); err != nil {
			return fmt.Errorf("delete-before commit: %w", err)
		}
		for _, k := range pending {
			var h Hash256
			copy(h[:], k)
			if d.cache != nil {
				d.cache.Remove(h)
			}
			if d.negativeCache != nil {
				d.negativeCache.MarkMissing(h)
			}
		}
		deleted += uint64(len(pending))
		pending = pending[:0]
		return nil
	}

	it := d.store.NewIterator(nil, nil)
	defer it.Release()

	for it.Next() {
		if err := ctx.Err(); err != nil {
			// Persist what we have already queued so the pass makes forward
			// progress across cancellations, then report the cancellation.
			_ = flush()
			return deleted, err
		}

		value := it.Value()
		if len(value) < nodeEncodingHeaderSize {
			// Not a node blob this scanner understands; leave it untouched.
			continue
		}
		seq := binary.BigEndian.Uint32(value[1:5])
		if seq >= boundary {
			continue
		}

		key := append([]byte(nil), it.Key()...)
		pending = append(pending, key)
		if len(pending) >= batchSize {
			if err := flush(); err != nil {
				return deleted, err
			}
		}
	}
	if err := it.Error(); err != nil {
		_ = flush()
		return deleted, fmt.Errorf("delete-before scan: %w", err)
	}
	if err := flush(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

var _ PrunableDatabase = (*KVDatabaseImpl)(nil)
