package nodestore

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

const defaultDeleteBatch = 65536

// DeleteBefore removes nodes whose ledger sequence is below boundary.
func (d *KVDatabase) DeleteBefore(
	ctx context.Context,
	boundary uint32,
	batchSize int,
) (deleted uint64, err error) {
	if boundary == 0 {
		return 0, nil
	}
	d.mutationMu.Lock()
	defer d.mutationMu.Unlock()
	if err := d.bumpDurableMutation(ctx); err != nil {
		return 0, fmt.Errorf("delete-before durable generation: %w", err)
	}
	if err := d.begin(ctx); err != nil {
		return 0, err
	}
	defer d.lifecycleMu.RUnlock()
	if batchSize <= 0 {
		batchSize = defaultDeleteBatch
	}

	iterator, err := d.store.NewIterator(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("delete-before iterator: %w", err)
	}
	defer func() {
		if closeErr := iterator.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("delete-before iterator close: %w", closeErr))
		}
	}()

	pending := make([][]byte, 0, batchSize)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		count, flushErr := d.deletePendingBefore(pending, boundary)
		deleted += count
		pending = pending[:0]
		return flushErr
	}
	finishWith := func(scanErr error) (uint64, error) {
		flushErr := flush()
		return deleted, errors.Join(scanErr, flushErr)
	}

	for iterator.Next() {
		if err := ctx.Err(); err != nil {
			return finishWith(err)
		}

		key := iterator.Key()
		if len(key) != len(Hash256{}) {
			return finishWith(fmt.Errorf("%w: invalid key length %d", ErrDataCorrupt, len(key)))
		}
		var hash Hash256
		copy(hash[:], key)
		node, decodeErr := decodeNodeData(hash, iterator.Value())
		if decodeErr != nil {
			return finishWith(decodeErr)
		}
		if node.LedgerSeq >= boundary {
			continue
		}

		pending = append(pending, append([]byte(nil), key...))
		if len(pending) >= batchSize {
			if err := flush(); err != nil {
				return deleted, err
			}
		}
	}
	if scanErr := iterator.Error(); scanErr != nil {
		return finishWith(fmt.Errorf("delete-before scan: %w", scanErr))
	}
	if err := flush(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (d *KVDatabase) deletePendingBefore(keys [][]byte, boundary uint32) (deleted uint64, err error) {
	d.pruneMu.Lock()
	defer d.pruneMu.Unlock()

	batch, err := d.store.NewBatch()
	if err != nil {
		return 0, fmt.Errorf("delete-before batch: %w", err)
	}
	defer func() {
		if closeErr := batch.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("delete-before batch close: %w", closeErr))
		}
	}()

	deletedKeys := make([]Hash256, 0, len(keys))
	for _, key := range keys {
		current, getErr := d.store.Get(key)
		if errors.Is(getErr, kvstore.ErrNotFound) {
			continue
		}
		if getErr != nil {
			return 0, fmt.Errorf("delete-before read current: %w", getErr)
		}
		var hash Hash256
		copy(hash[:], key)
		node, decodeErr := decodeNodeData(hash, current)
		if decodeErr != nil {
			return 0, fmt.Errorf("delete-before read current: %w", decodeErr)
		}
		if node.LedgerSeq >= boundary {
			continue
		}
		if err := batch.Delete(key); err != nil {
			return 0, fmt.Errorf("delete-before batch: %w", err)
		}
		deletedKeys = append(deletedKeys, hash)
	}
	if len(deletedKeys) == 0 {
		return 0, nil
	}
	if err := batch.Write(); err != nil {
		return 0, fmt.Errorf("delete-before commit: %w", err)
	}

	d.cacheGeneration.Add(1)
	for _, hash := range deletedKeys {
		if d.cache != nil {
			d.cache.Remove(hash)
		}
		if d.negativeCache != nil {
			d.negativeCache.MarkMissing(hash)
		}
	}
	return uint64(len(deletedKeys)), nil
}
