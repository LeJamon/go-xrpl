// Package pebble implements the kvstore.KeyValueStore interface using CockroachDB/Pebble.
package pebble

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
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
	closed   bool
	readOnly bool
}

// readOnlyFS suppresses Pebble's exclusive database lock. Callers may use it
// only for immutable stores that were closed before publication.
type readOnlyFS struct{ vfs.FS }

type noOpCloser struct{}

func (noOpCloser) Close() error { return nil }

func (readOnlyFS) Lock(string) (io.Closer, error) { return noOpCloser{}, nil }

// New opens a Pebble database at the given path.
func New(path string, options Options) (*Store, error) {
	resolved, err := options.Resolve()
	if err != nil {
		return nil, err
	}
	pebbleCache := pebble.NewCache(resolved.BlockCacheBytes)
	defer pebbleCache.Unref()
	return newWithCache(path, pebbleCache, resolved)
}

// NewReadOnly opens an existing Pebble store without taking its exclusive
// writer lock.
func NewReadOnly(path string, options Options) (*Store, error) {
	resolved, err := options.Resolve()
	if err != nil {
		return nil, err
	}
	pebbleCache := pebble.NewCache(resolved.BlockCacheBytes)
	defer pebbleCache.Unref()
	return openWithCache(path, pebbleCache, resolved, true)
}

func newWithCache(path string, pebbleCache *pebble.Cache, options Options) (*Store, error) {
	return openWithCache(path, pebbleCache, options, false)
}

func openWithCache(path string, pebbleCache *pebble.Cache, options Options, readOnly bool) (*Store, error) {
	if !readOnly {
		if err := os.MkdirAll(path, 0755); err != nil {
			return nil, fmt.Errorf("kvstore/pebble: failed to create directory %s: %w", path, err)
		}
	}

	pebbleOptions := makePebbleOptions(options, pebbleCache)
	pebbleOptions.ReadOnly = readOnly
	if readOnly {
		pebbleOptions.FS = readOnlyFS{FS: vfs.Default}
	}
	db, err := pebble.Open(path, pebbleOptions)
	if err != nil {
		return nil, fmt.Errorf("kvstore/pebble: failed to open %s: %w", path, err)
	}

	return &Store{db: db, readOnly: readOnly}, nil
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

// NewBatch returns a new batch for accumulating writes.
func (s *Store) NewBatch() (kvstore.Batch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, kvstore.ErrClosed
	}
	return &batch{store: s, batch: s.db.NewBatch()}, nil
}

// NewIterator returns an iterator over key/value pairs with the given prefix,
// starting from start (or the first key >= start with the prefix).
func (s *Store) NewIterator(prefix []byte, start []byte) (kvstore.Iterator, error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, kvstore.ErrClosed
	}
	opts := &pebble.IterOptions{}
	if len(prefix) > 0 {
		opts.LowerBound = append([]byte(nil), prefix...)
		upper := prefixUpperBound(opts.LowerBound)
		if upper != nil {
			opts.UpperBound = upper
		}
	}
	iter, err := s.db.NewIter(opts)
	if err != nil {
		s.mu.RUnlock()
		return nil, err
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
			seekKey = append([]byte(nil), start...)
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
	return &iterator{store: s, iter: iter}, nil
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

// Sync makes all previously written data durable by appending a synced
// record to the WAL. Writes use pebble.NoSync, so this is the only point
// at which acknowledged writes are guaranteed to survive a crash.
func (s *Store) Sync() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return kvstore.ErrClosed
	}
	if s.readOnly {
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
	if s.readOnly {
		return s.db.Close()
	}
	flushErr := s.db.Flush()
	return errors.Join(flushErr, s.db.Close())
}

type batch struct {
	store  *Store
	batch  *pebble.Batch
	size   int
	closed bool
}

// Put queues a key/value write.
func (b *batch) Put(key []byte, value []byte) error {
	if b.closed {
		return kvstore.ErrClosed
	}
	b.size += len(value)
	return b.batch.Set(key, value, nil)
}

// Delete queues deletion of a key.
func (b *batch) Delete(key []byte) error {
	if b.closed {
		return kvstore.ErrClosed
	}
	return b.batch.Delete(key, nil)
}

// ValueSize returns an estimate of the queued write size in bytes.
func (b *batch) ValueSize() int {
	return b.size
}

func (b *batch) Write() error {
	if b.closed {
		return kvstore.ErrClosed
	}
	b.store.mu.RLock()
	defer b.store.mu.RUnlock()
	if b.store.closed {
		return kvstore.ErrClosed
	}
	return b.batch.Commit(pebble.NoSync)
}

// Reset clears the accumulated writes.
func (b *batch) Reset() {
	if b.closed {
		return
	}
	b.batch.Reset()
	b.size = 0
}

func (b *batch) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	b.size = 0
	return b.batch.Close()
}

// iterator implements kvstore.Iterator using a pebble.Iterator.
type iterator struct {
	store   *Store
	iter    *pebble.Iterator
	started bool // whether the iterator has been positioned
	closed  bool
}

type pointIterator struct {
	store  *Store
	iter   *pebble.Iterator
	closed bool
}

func (s *Store) newPointIterator() (*pointIterator, error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, kvstore.ErrClosed
	}
	iter, err := s.db.NewIter(nil)
	if err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	return &pointIterator{store: s, iter: iter}, nil
}

func (i *pointIterator) get(key []byte, remaining int, allowOversized bool) ([]byte, bool, bool, error) {
	if !i.iter.SeekGE(key) {
		return nil, false, false, i.iter.Error()
	}
	if !bytes.Equal(i.iter.Key(), key) {
		return nil, false, false, nil
	}
	value, err := i.iter.ValueAndErr()
	if err != nil {
		return nil, false, false, err
	}
	if len(value) > remaining && !allowOversized {
		return nil, true, true, nil
	}
	return append([]byte(nil), value...), true, false, nil
}

func (i *pointIterator) Close() error {
	if i.closed {
		return nil
	}
	i.closed = true
	err := i.iter.Close()
	i.store.mu.RUnlock()
	return err
}

// Next advances the iterator and reports whether a pair is available.
func (i *iterator) Next() bool {
	if i.closed {
		return false
	}
	if !i.started {
		i.started = true
		return i.iter.Valid()
	}
	return i.iter.Next()
}

// Key returns the key at the current position.
func (i *iterator) Key() []byte {
	if i.closed {
		return nil
	}
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
	if i.closed {
		return nil
	}
	v := i.iter.Value()
	if v == nil {
		return nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp
}

func (i *iterator) Error() error {
	if i.closed {
		return nil
	}
	return i.iter.Error()
}

// Close releases the underlying iterator and its read lock on the store.
func (i *iterator) Close() error {
	if i.closed {
		return nil
	}
	i.closed = true
	err := i.iter.Close()
	i.store.mu.RUnlock()
	return err
}

// Ensure Store implements kvstore.KeyValueStore at compile time.
var _ kvstore.KeyValueStore = (*Store)(nil)
