// Package memorydb implements the kvstore.KeyValueStore interface using an in-memory map.
// Intended for testing and development.
package memorydb

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/LeJamon/goXRPLd/storage/kvstore"
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

// Has returns true if the key exists in the store.
func (m *MemDatabase) Has(key []byte) (bool, error) {
	m.lock.RLock()
	defer m.lock.RUnlock()
	if m.closed {
		return false, kvstore.ErrClosed
	}
	_, ok := m.db[string(key)]
	return ok, nil
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

// Delete removes the value for the given key.
func (m *MemDatabase) Delete(key []byte) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		return kvstore.ErrClosed
	}
	delete(m.db, string(key))
	return nil
}

// NewBatch returns a new batch for accumulating writes.
func (m *MemDatabase) NewBatch() kvstore.Batch {
	return &memBatch{db: m}
}

// NewIterator returns an iterator over key/value pairs.
// prefix filters keys that start with prefix.
// start sets the starting position (relative to prefix).
func (m *MemDatabase) NewIterator(prefix []byte, start []byte) kvstore.Iterator {
	m.lock.RLock()
	defer m.lock.RUnlock()

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

	return &memIterator{pairs: pairs, pos: -1}
}

// Stat returns a string with the count of keys in the store.
func (m *MemDatabase) Stat() (string, error) {
	m.lock.RLock()
	defer m.lock.RUnlock()
	if m.closed {
		return "", kvstore.ErrClosed
	}
	return fmt.Sprintf("memdb: %d keys", len(m.db)), nil
}

// Compact is a no-op for the in-memory store.
func (m *MemDatabase) Compact(start []byte, limit []byte) error {
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

// Len returns the number of entries in the store.
func (m *MemDatabase) Len() int {
	m.lock.RLock()
	defer m.lock.RUnlock()
	return len(m.db)
}

// kv is an internal key-value pair used by the iterator.
type kv struct {
	key []byte
	val []byte
}

// memBatch implements kvstore.Batch for MemDatabase.
type memBatch struct {
	db      *MemDatabase
	writes  []kv
	deletes [][]byte
	size    int
}

func (b *memBatch) Put(key []byte, value []byte) error {
	kCopy := make([]byte, len(key))
	copy(kCopy, key)
	vCopy := make([]byte, len(value))
	copy(vCopy, value)
	b.writes = append(b.writes, kv{key: kCopy, val: vCopy})
	b.size += len(value)
	return nil
}

func (b *memBatch) Delete(key []byte) error {
	kCopy := make([]byte, len(key))
	copy(kCopy, key)
	b.deletes = append(b.deletes, kCopy)
	return nil
}

func (b *memBatch) ValueSize() int {
	return b.size
}

func (b *memBatch) Write() error {
	b.db.lock.Lock()
	defer b.db.lock.Unlock()
	if b.db.closed {
		return kvstore.ErrClosed
	}
	for _, w := range b.writes {
		b.db.db[string(w.key)] = w.val
	}
	for _, d := range b.deletes {
		delete(b.db.db, string(d))
	}
	return nil
}

func (b *memBatch) Reset() {
	b.writes = b.writes[:0]
	b.deletes = b.deletes[:0]
	b.size = 0
}

// memIterator implements kvstore.Iterator for MemDatabase.
type memIterator struct {
	pairs []kv
	pos   int
}

func (it *memIterator) Next() bool {
	it.pos++
	return it.pos < len(it.pairs)
}

func (it *memIterator) Key() []byte {
	if it.pos < 0 || it.pos >= len(it.pairs) {
		return nil
	}
	return it.pairs[it.pos].key
}

func (it *memIterator) Value() []byte {
	if it.pos < 0 || it.pos >= len(it.pairs) {
		return nil
	}
	return it.pairs[it.pos].val
}

func (it *memIterator) Error() error {
	return nil
}

func (it *memIterator) Release() {
	it.pairs = nil
}

// Ensure MemDatabase implements kvstore.KeyValueStore at compile time.
var _ kvstore.KeyValueStore = (*MemDatabase)(nil)
