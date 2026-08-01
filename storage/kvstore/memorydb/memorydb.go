// Package memorydb implements the kvstore.KeyValueStore interface using an in-memory map.
// Intended for testing and development.
package memorydb

import (
	"sort"
	"strings"
	"sync"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// MemDatabase is a thread-safe in-memory implementation of kvstore.KeyValueStore.
type MemDatabase struct {
	db     map[string][]byte
	lock   sync.RWMutex
	closed bool
}

// New creates a new empty in-memory key-value store.
func New() *MemDatabase {
	return &MemDatabase{
		db: make(map[string][]byte),
	}
}

// Get retrieves the value for the given key.
// Returns kvstore.ErrNotFound if the key does not exist.
func (m *MemDatabase) Get(key []byte) ([]byte, error) {
	m.lock.RLock()
	defer m.lock.RUnlock()
	if m.closed {
		return nil, kvstore.ErrClosed
	}
	val, ok := m.db[string(key)]
	if !ok {
		return nil, kvstore.ErrNotFound
	}
	// Return a copy to prevent external mutation
	result := make([]byte, len(val))
	copy(result, val)
	return result, nil
}

// Put stores the value for the given key.
func (m *MemDatabase) Put(key []byte, value []byte) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		return kvstore.ErrClosed
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	m.db[string(key)] = cp
	return nil
}

// NewBatch returns a new batch for accumulating writes.
func (m *MemDatabase) NewBatch() (kvstore.Batch, error) {
	m.lock.RLock()
	defer m.lock.RUnlock()
	if m.closed {
		return nil, kvstore.ErrClosed
	}
	return &memBatch{db: m}, nil
}

// NewIterator returns an iterator over key/value pairs.
// prefix filters keys that start with prefix.
// start sets the starting position (relative to prefix).
func (m *MemDatabase) NewIterator(prefix []byte, start []byte) (kvstore.Iterator, error) {
	m.lock.RLock()
	defer m.lock.RUnlock()
	if m.closed {
		return nil, kvstore.ErrClosed
	}

	// Collect all keys with the given prefix
	var keys []string
	prefixStr := string(prefix)
	for k := range m.db {
		if len(prefix) == 0 || strings.HasPrefix(k, prefixStr) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	// Build snapshot of key/value pairs
	pairs := make([]kv, 0, len(keys))
	var startStr string
	if len(start) > 0 {
		startStr = prefixStr + string(start)
	}

	for _, k := range keys {
		if startStr != "" && k < startStr {
			continue
		}
		val := m.db[k]
		cp := make([]byte, len(val))
		copy(cp, val)
		pairs = append(pairs, kv{key: []byte(k), val: cp})
	}

	return &memIterator{pairs: pairs, pos: -1}, nil
}

// Sync is a no-op: the store has no durable medium.
func (m *MemDatabase) Sync() error {
	m.lock.RLock()
	defer m.lock.RUnlock()
	if m.closed {
		return kvstore.ErrClosed
	}
	return nil
}

// Close marks the store as closed. Further operations will return ErrClosed.
func (m *MemDatabase) Close() error {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.closed = true
	m.db = nil
	return nil
}

// kv is an internal key-value pair used by the iterator.
type kv struct {
	key []byte
	val []byte
}

// memBatch implements kvstore.Batch for MemDatabase. Operations are kept in
// a single ordered list so interleaved Put/Delete of the same key replays in
// insertion order, matching the pebble backend.
type memBatch struct {
	db     *MemDatabase
	ops    []batchOp
	size   int
	closed bool
}

// batchOp is a single queued batch operation: a put when delete is false,
// a deletion otherwise.
type batchOp struct {
	key    []byte
	val    []byte
	delete bool
}

// Put queues a key/value write.
func (b *memBatch) Put(key []byte, value []byte) error {
	if b.closed {
		return kvstore.ErrClosed
	}
	kCopy := make([]byte, len(key))
	copy(kCopy, key)
	vCopy := make([]byte, len(value))
	copy(vCopy, value)
	b.ops = append(b.ops, batchOp{key: kCopy, val: vCopy})
	b.size += len(value)
	return nil
}

// Delete queues deletion of a key.
func (b *memBatch) Delete(key []byte) error {
	if b.closed {
		return kvstore.ErrClosed
	}
	kCopy := make([]byte, len(key))
	copy(kCopy, key)
	b.ops = append(b.ops, batchOp{key: kCopy, delete: true})
	return nil
}

// ValueSize returns an estimate of the queued write size in bytes.
func (b *memBatch) ValueSize() int {
	return b.size
}

func (b *memBatch) Write() error {
	if b.closed {
		return kvstore.ErrClosed
	}
	b.db.lock.Lock()
	defer b.db.lock.Unlock()
	if b.db.closed {
		return kvstore.ErrClosed
	}
	for _, op := range b.ops {
		if op.delete {
			delete(b.db.db, string(op.key))
		} else {
			b.db.db[string(op.key)] = op.val
		}
	}
	return nil
}

// Reset clears the accumulated writes.
func (b *memBatch) Reset() {
	if b.closed {
		return
	}
	// Drop the backing array so a one-shot large batch does not pin
	// memory indefinitely. Subsequent Puts will reallocate as needed.
	b.ops = nil
	b.size = 0
}

func (b *memBatch) Close() error {
	if b.closed {
		return nil
	}
	b.ops = nil
	b.size = 0
	b.closed = true
	return nil
}

// memIterator implements kvstore.Iterator for MemDatabase.
type memIterator struct {
	pairs  []kv
	pos    int
	closed bool
}

// Next advances the iterator and reports whether a pair is available.
func (it *memIterator) Next() bool {
	if it.closed {
		return false
	}
	it.pos++
	return it.pos < len(it.pairs)
}

// Key returns the key at the current position.
func (it *memIterator) Key() []byte {
	if it.pos < 0 || it.pos >= len(it.pairs) {
		return nil
	}
	return it.pairs[it.pos].key
}

// Value returns the value at the current position.
func (it *memIterator) Value() []byte {
	if it.pos < 0 || it.pos >= len(it.pairs) {
		return nil
	}
	return it.pairs[it.pos].val
}

func (it *memIterator) Error() error {
	return nil
}

func (it *memIterator) Close() error {
	if it.closed {
		return nil
	}
	it.pairs = nil
	it.closed = true
	return nil
}

// Ensure MemDatabase implements kvstore.KeyValueStore at compile time.
var _ kvstore.KeyValueStore = (*MemDatabase)(nil)
