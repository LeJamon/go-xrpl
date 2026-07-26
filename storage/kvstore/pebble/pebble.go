// Package pebble implements the kvstore.KeyValueStore interface using CockroachDB/Pebble.
package pebble

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

// Store is a thin wrapper around CockroachDB/Pebble that implements kvstore.KeyValueStore.
//
// mu serialises every operation against Close: an op holds RLock across both its
// closed-check and the underlying s.db call, while Close takes the exclusive
// lock. Pebble panics ("pebble: closed") on any op against a closed DB, so a
// bare atomic flag — checked, then acted on — leaves a window where a racing
// Close turns the panic loose. The RWMutex closes that window.
type Store struct {
	mu       sync.RWMutex
	db       *pebble.DB
	options  Options
	cache    *pebble.Cache
	closed   bool
	readonly bool
}

// New opens a Pebble database at the given path.
// readonly opens the database in read-only mode if true.
func New(path string, options Options, readonly bool) (*Store, error) {
	resolved, err := options.Resolve()
	if err != nil {
		return nil, err
	}
	pebbleCache := pebble.NewCache(resolved.BlockCacheBytes)
	defer pebbleCache.Unref()
	return newWithCache(path, pebbleCache, resolved, readonly)
}

func newWithCache(path string, pebbleCache *pebble.Cache, options Options, readonly bool) (*Store, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("kvstore/pebble: failed to create directory %s: %w", path, err)
	}

	opts := &pebble.Options{
		Cache:                       pebbleCache,
		MaxOpenFiles:                options.MaxOpenFiles,
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

	return &Store{
		db:       db,
		options:  options,
		cache:    pebbleCache,
		readonly: readonly,
	}, nil
}

// Has returns true if the key exists in the store.
func (s *Store) Has(key []byte) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return kvstore.ErrClosed
	}
	return s.db.Set(key, value, pebble.NoSync)
}

// Delete removes the value for the given key.
func (s *Store) Delete(key []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return kvstore.ErrClosed
	}
	return s.db.Delete(key, pebble.NoSync)
}

// NewBatch returns a new batch for accumulating writes. On a closed store it
// returns a batch that reports ErrClosed rather than allocating one against
// the closed DB; the returned batch also re-checks on Write.
func (s *Store) NewBatch() kvstore.Batch {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return &errBatch{err: kvstore.ErrClosed}
	}
	return &batch{store: s, b: s.db.NewBatch()}
}

// NewIterator returns an iterator over key/value pairs with the given prefix,
// starting from start (or the first key >= start with the prefix).
// If the store is closed or the underlying iterator cannot be opened, the
// returned iterator is empty and reports the failure via Error.
func (s *Store) NewIterator(prefix []byte, start []byte) kvstore.Iterator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return "", kvstore.ErrClosed
	}
	if m := s.db.Metrics(); m != nil {
		return m.String(), nil
	}
	return "pebble: no metrics available", nil
}

// Compact compacts the database in the given key range.
func (s *Store) Compact(start []byte, limit []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return kvstore.ErrClosed
	}
	return s.db.Compact(start, limit, true)
}

// Sync makes all previously written data durable by appending a synced
// record to the WAL. Writes use pebble.NoSync, so this is the only point
// at which acknowledged writes are guaranteed to survive a crash.
func (s *Store) Sync() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return kvstore.ErrClosed
	}
	if s.readonly {
		return nil
	}
	return s.db.LogData(nil, pebble.Sync)
}

// Close closes the database, flushing pending writes first. The underlying
// handle is always closed, even if the flush fails.
//
// The exclusive lock is held across the whole close, so any in-flight op has
// already released its RLock (and finished touching s.db) before the handle is
// closed, and any op that arrives later observes closed == true.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil // already closed
	}
	s.closed = true
	var flushErr error
	if !s.readonly {
		flushErr = s.db.Flush()
	}
	return errors.Join(flushErr, s.db.Close())
}

// batch implements kvstore.Batch using a pebble.Batch. Put/Delete/Reset only
// touch the in-memory pebble.Batch buffer, so they are safe after Close; Write
// commits against s.db and therefore re-checks the closed state under the lock.
type batch struct {
	store *Store
	b     *pebble.Batch
	size  int
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
	b.store.mu.RLock()
	defer b.store.mu.RUnlock()
	if b.store.closed {
		return kvstore.ErrClosed
	}
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

// errBatch is a no-op batch that reports a fixed error, returned by NewBatch
// when the store is already closed so callers never buffer writes against a
// closed DB.
type errBatch struct {
	err error
}

func (b *errBatch) Put(key []byte, value []byte) error { return b.err }
func (b *errBatch) Delete(key []byte) error            { return b.err }
func (b *errBatch) ValueSize() int                     { return 0 }
func (b *errBatch) Write() error                       { return b.err }
func (b *errBatch) Reset()                             {}

// Ensure Store implements kvstore.KeyValueStore at compile time.
var _ kvstore.KeyValueStore = (*Store)(nil)
