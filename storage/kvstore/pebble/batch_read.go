package pebble

import (
	"bytes"
	"context"
	"errors"
	"sort"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

type batchReadFunc func(key []byte, remaining int, allowOversized bool) ([]byte, bool, bool, error)

func validateBatchReadLimits(keys [][]byte, maxNodes, maxBytes int) error {
	if len(keys) == 0 {
		return nil
	}
	if maxNodes <= 0 {
		return errors.New("kvstore/pebble: batch node limit must be positive")
	}
	if maxBytes <= 0 {
		return errors.New("kvstore/pebble: batch byte limit must be positive")
	}
	return nil
}

func sortedBatchReadKeys(ctx context.Context, keys [][]byte) ([][]byte, error) {
	if ctx == nil {
		return nil, errors.New("kvstore/pebble: nil batch read context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sorted := append([][]byte(nil), keys...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i], sorted[j]) < 0
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sorted, nil
}

func readBatch(
	ctx context.Context,
	keys [][]byte,
	maxNodes, maxBytes int,
	read batchReadFunc,
) ([]kvstore.ReadResult, error) {
	if err := validateBatchReadLimits(keys, maxNodes, maxBytes); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		if ctx == nil {
			return nil, errors.New("kvstore/pebble: nil batch read context")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	sorted, err := sortedBatchReadKeys(ctx, keys)
	if err != nil {
		return nil, err
	}

	results := make([]kvstore.ReadResult, 0, min(maxNodes, len(sorted)))
	bytesRead := 0
	for _, key := range sorted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(results) >= maxNodes || bytesRead >= maxBytes {
			break
		}
		value, found, tooLarge, err := read(key, maxBytes-bytesRead, len(results) == 0)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if tooLarge {
			break
		}

		result := kvstore.ReadResult{
			Key:   cloneBatchBytes(key),
			Found: found,
		}
		if found {
			result.Value = value
			bytesRead += len(value)
		}
		results = append(results, result)
	}
	return results, nil
}

func cloneBatchBytes(value []byte) []byte {
	copyValue := make([]byte, len(value))
	copy(copyValue, value)
	return copyValue
}
