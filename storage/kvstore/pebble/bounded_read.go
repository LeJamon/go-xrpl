package pebble

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// GetBounded checks the lazy value length before materializing or copying it.
func (s *Store) GetBounded(ctx context.Context, key []byte, maxBytes int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxBytes < 0 {
		return nil, kvstore.ErrValueTooLarge
	}
	iterator, err := s.newPointIterator()
	if err != nil {
		return nil, err
	}
	value, found, tooLarge, readErr := iterator.get(key, maxBytes, false)
	if err := errors.Join(readErr, iterator.Close(), ctx.Err()); err != nil {
		return nil, err
	}
	if tooLarge {
		return nil, kvstore.ErrValueTooLarge
	}
	if !found {
		return nil, kvstore.ErrNotFound
	}
	return value, nil
}

// GetBounded reads the current generations without promoting archive records.
func (r *RotatingStore) GetBounded(ctx context.Context, key []byte, maxBytes int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, kvstore.ErrClosed
	}
	r.archiveMu.RLock()
	defer r.archiveMu.RUnlock()
	data, err := r.writable.GetBounded(ctx, key, maxBytes)
	if !errors.Is(err, kvstore.ErrNotFound) {
		return data, err
	}
	return r.archive.GetBounded(ctx, key, maxBytes)
}
