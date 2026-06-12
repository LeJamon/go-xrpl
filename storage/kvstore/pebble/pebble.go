// Package pebble implements the kvstore.KeyValueStore interface using CockroachDB/Pebble.
package pebble

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

// Store is a thin wrapper around CockroachDB/Pebble that implements kvstore.KeyValueStore.
type Store struct {
	db       *pebble.DB
	closed   atomic.Bool
	readonly bool
}

// New opens a Pebble database at the given path.
// cache is the block cache size in bytes (0 for default).
// handles is the number of open file handles allowed (0 for default).
// readonly opens the database in read-only mode if true.
func New(path string, cache int, handles int, readonly bool) (*Store, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("kvstore/pebble: failed to create directory %s: %w", path, err)
	}

	if cache <= 0 {
		cache = 256 << 20 // 256MB default
	}
	if handles <= 0 {
		handles = 500
	}

	pebbleCache := pebble.NewCache(int64(cache))

	opts := &pebble.Options{
		Cache:                       pebbleCache,
		MaxOpenFiles:                handles,
		MemTableSize:                64 << 20, // 64MB memtables
		MemTableStopWritesThreshold: 4,
		MaxConcurrentCompactions: func() int {
			return runtime.NumCPU()
		},
		L0CompactionThreshold: 4,
		L0StopWritesThreshold: 20,
		LBaseMaxBytes:         256 << 20,
		Levels:                make([]pebble.LevelOptions, 7),
		DisableWAL:            false,
		ReadOnly:              readonly,
	}

	for i := range opts.Levels {
		opts.Levels[i] = pebble.LevelOptions{
			BlockSize:      32 << 10,
			IndexBlockSize: 256 << 10,
			FilterPolicy:   bloom.FilterPolicy(10),
			FilterType:     pebble.TableFilter,
			TargetFileSize: int64(8<<20) << uint(i),
			Compression:    pebble.SnappyCompression,
		}
		if opts.Levels[i].TargetFileSize > 256<<20 {
			opts.Levels[i].TargetFileSize = 256 << 20
		}
	}

	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, fmt.Errorf("kvstore/pebble: failed to open %s: %w", path, err)
	}

	return &Store{db: db, readonly: readonly}, nil
}

// Has returns true if the key exists in the store.
func (s *Store) Has(key []byte) (bool, error) {
	if s.closed.Load() {
		return false, kvstore.ErrClosed
	}
	_, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	closer.Close()
	return true, nil
}

// Get retrieves the value for the given key.
// Returns kvstore.ErrNotFound if the key does not exist.
func (s *Store) Get(key []byte) ([]byte, error) {
	if s.closed.Load() {
		return nil, kvstore.ErrClosed
	}
	val, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, kvstore.ErrNotFound
		}
		return nil, err
	}
	defer closer.Close()
	// Copy because the slice is only valid until closer.Close()
	result := make([]byte, len(val))
	copy(result, val)
	return result, nil
}

// Put stores the value for the given key.
func (s *Store) Put(key []byte, value []byte) error {
	if s.closed.Load() {
		return kvstore.ErrClosed
	}
	return s.db.Set(key, value, pebble.NoSync)
}

// Delete removes the value for the given key.
func (s *Store) Delete(key []byte) error {
	if s.closed.Load() {
		return kvstore.ErrClosed
	}
	return s.db.Delete(key, pebble.NoSync)
}

// NewBatch returns a new batch for accumulating writes.
func (s *Store) NewBatch() kvstore.Batch {
	return &batch{b: s.db.NewBatch()}
}

// NewIterator returns an iterator over key/value pairs with the given prefix,
// starting from start (or the first key >= start with the prefix).
// If the store is closed or the underlying iterator cannot be opened, the
// returned iterator is empty and reports the failure via Error.
func (s *Store) NewIterator(prefix []byte, start []byte) kvstore.Iterator {
	if s.closed.Load() {
		return &errIterator{err: kvstore.ErrClosed}
	}
	opts := &pebble.IterOptions{}
	if len(prefix) > 0 {
		opts.LowerBound = prefix
		// Upper bound is the prefix incremented by 1 byte
		upper := prefixUpperBound(prefix)
		if upper != nil {
			opts.UpperBound = upper
		}
	}
	iter, err := s.db.NewIter(opts)
	if err != nil {
		return &errIterator{err: err}
	}
	var seekKey []byte
	if len(start) > 0 {
		if len(prefix) > 0 {
			// Concatenate into a fresh slice; appending onto the caller's
			// prefix could clobber its backing array.
			seekKey = make([]byte, 0, len(prefix)+len(start))
			seekKey = append(seekKey, prefix...)
			seekKey = append(seekKey, start...)
		} else {
			seekKey = start
		}
	} else if len(prefix) > 0 {
		seekKey = prefix
	}

	if seekKey != nil {
		iter.SeekGE(seekKey)
	} else {
		iter.First()
	}

	// started stays false: the iterator is now positioned on its first
	// element, so the first Next() must report it without advancing.
	return &iterator{iter: iter}
}

// prefixUpperBound returns the upper bound for the given prefix (exclusive).
// Returns nil if the prefix is all 0xFF bytes.
func prefixUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper
		}
	}
	return nil // overflow: all bytes were 0xFF
}

// Stat returns a string with database statistics.
func (s *Store) Stat() (string, error) {
	if s.closed.Load() {
		return "", kvstore.ErrClosed
	}
	if m := s.db.Metrics(); m != nil {
		return m.String(), nil
	}
	return "pebble: no metrics available", nil
}

// Compact compacts the database in the given key range.
func (s *Store) Compact(start []byte, limit []byte) error {
	if s.closed.Load() {
		return kvstore.ErrClosed
	}
	return s.db.Compact(start, limit, true)
}

// Sync makes all previously written data durable by appending a synced
// record to the WAL. Writes use pebble.NoSync, so this is the only point
// at which acknowledged writes are guaranteed to survive a crash.
func (s *Store) Sync() error {
	if s.closed.Load() {
		return kvstore.ErrClosed
	}
	if s.readonly {
		return nil
	}
	return s.db.LogData(nil, pebble.Sync)
}

// Close closes the database, flushing pending writes first. The underlying
// handle is always closed, even if the flush fails.
func (s *Store) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil // already closed
	}
	var flushErr error
	if !s.readonly {
		flushErr = s.db.Flush()
	}
	return errors.Join(flushErr, s.db.Close())
}

// batch implements kvstore.Batch using a pebble.Batch.
type batch struct {
	b    *pebble.Batch
	size int
}

// Put queues a key/value write.
func (b *batch) Put(key []byte, value []byte) error {
	b.size += len(value)
	return b.b.Set(key, value, nil)
}

// Delete queues deletion of a key.
func (b *batch) Delete(key []byte) error {
	return b.b.Delete(key, nil)
}

// ValueSize returns an estimate of the queued write size in bytes.
func (b *batch) ValueSize() int {
	return b.size
}

func (b *batch) Write() error {
	return b.b.Commit(pebble.NoSync)
}

// Reset clears the accumulated writes.
func (b *batch) Reset() {
	b.b.Reset()
	b.size = 0
}

// iterator implements kvstore.Iterator using a pebble.Iterator.
type iterator struct {
	iter    *pebble.Iterator
	started bool // whether the iterator has been positioned
}

// Next advances the iterator and reports whether a pair is available.
func (i *iterator) Next() bool {
	if !i.started {
		i.started = true
		return i.iter.Valid()
	}
	return i.iter.Next()
}

// Key returns the key at the current position.
func (i *iterator) Key() []byte {
	k := i.iter.Key()
	if k == nil {
		return nil
	}
	cp := make([]byte, len(k))
	copy(cp, k)
	return cp
}

// Value returns the value at the current position.
func (i *iterator) Value() []byte {
	v := i.iter.Value()
	if v == nil {
		return nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp
}

func (i *iterator) Error() error {
	return i.iter.Error()
}

// Release closes the underlying pebble iterator.
func (i *iterator) Release() {
	i.iter.Close()
}

// errIterator is an empty iterator that reports a fixed error, returned when
// an iterator cannot be opened (e.g. the store is closed).
type errIterator struct {
	err error
}

func (i *errIterator) Next() bool    { return false }
func (i *errIterator) Key() []byte   { return nil }
func (i *errIterator) Value() []byte { return nil }
func (i *errIterator) Error() error  { return i.err }
func (i *errIterator) Release()      {}

// Ensure Store implements kvstore.KeyValueStore at compile time.
var _ kvstore.KeyValueStore = (*Store)(nil)
