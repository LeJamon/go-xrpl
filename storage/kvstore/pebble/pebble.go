// Package pebble implements the kvstore.KeyValueStore interface using CockroachDB/Pebble.
package pebble

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/LeJamon/goXRPLd/storage/kvstore"
	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

// Store is a thin wrapper around CockroachDB/Pebble that implements kvstore.KeyValueStore.
type Store struct {
	db     *pebble.DB
	closed atomic.Bool
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

	return &Store{db: db}, nil
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
func (s *Store) NewIterator(prefix []byte, start []byte) kvstore.Iterator {
	opts := &pebble.IterOptions{}
	if len(prefix) > 0 {
		opts.LowerBound = prefix
		// Upper bound is the prefix incremented by 1 byte
		upper := prefixUpperBound(prefix)
		if upper != nil {
			opts.UpperBound = upper
		}
	}
	iter, _ := s.db.NewIter(opts)
	var seekKey []byte
	if len(start) > 0 {
		if len(prefix) > 0 {
			seekKey = append(prefix, start...)
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

	return &iterator{iter: iter, started: seekKey != nil}
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

// Close closes the database.
func (s *Store) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil // already closed
	}
	if err := s.db.Flush(); err != nil {
		return err
	}
	return s.db.Close()
}

// batch implements kvstore.Batch using a pebble.Batch.
type batch struct {
	b    *pebble.Batch
	size int
}

func (b *batch) Put(key []byte, value []byte) error {
	b.size += len(value)
	return b.b.Set(key, value, nil)
}

func (b *batch) Delete(key []byte) error {
	return b.b.Delete(key, nil)
}

func (b *batch) ValueSize() int {
	return b.size
}

func (b *batch) Write() error {
	return b.b.Commit(pebble.NoSync)
}

func (b *batch) Reset() {
	b.b.Reset()
	b.size = 0
}

// iterator implements kvstore.Iterator using a pebble.Iterator.
type iterator struct {
	iter    *pebble.Iterator
	started bool // whether the iterator has been positioned
}

func (i *iterator) Next() bool {
	if !i.started {
		i.started = true
		return i.iter.Valid()
	}
	return i.iter.Next()
}

func (i *iterator) Key() []byte {
	k := i.iter.Key()
	if k == nil {
		return nil
	}
	cp := make([]byte, len(k))
	copy(cp, k)
	return cp
}

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

func (i *iterator) Release() {
	i.iter.Close()
}

// Ensure Store implements kvstore.KeyValueStore at compile time.
var _ kvstore.KeyValueStore = (*Store)(nil)
