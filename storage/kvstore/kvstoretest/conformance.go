// Package kvstoretest provides a shared, backend-agnostic conformance suite
// for the kvstore.KeyValueStore contract. Each concrete backend (memorydb,
// pebble, ...) runs RunConformance with a factory that returns a fresh,
// empty store so the same behavioural guarantees are exercised everywhere.
package kvstoretest

import (
	"bytes"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

// NewStoreFunc returns a fresh, empty store. The factory is responsible for
// registering its own cleanup (e.g. t.Cleanup(store.Close)).
type NewStoreFunc func(t *testing.T) kvstore.KeyValueStore

// RunConformance runs the full KeyValueStore conformance suite against the
// store produced by newStore. A new store is created for every subtest so
// state never leaks between cases.
func RunConformance(t *testing.T, newStore NewStoreFunc) {
	t.Helper()

	cases := []struct {
		name string
		fn   func(t *testing.T, store kvstore.KeyValueStore)
	}{
		{"PutGet", testPutGet},
		{"GetMissing", testGetMissing},
		{"Overwrite", testOverwrite},
		{"EmptyValue", testEmptyValue},
		{"ValueIsolation", testValueIsolation},
		{"Batch", testBatch},
		{"BatchInterleaved", testBatchInterleaved},
		{"BatchInputOwnership", testBatchInputOwnership},
		{"BatchClose", testBatchClose},
		{"BatchReset", testBatchReset},
		{"Sync", testSync},
		{"IteratorFullScan", testIteratorFullScan},
		{"IteratorPrefix", testIteratorPrefix},
		{"IteratorStart", testIteratorStart},
		{"IteratorInputOwnership", testIteratorInputOwnership},
		{"IteratorClose", testIteratorClose},
		{"IteratorEmpty", testIteratorEmpty},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newStore(t))
		})
	}

	// Closed-store behaviour creates and closes its own store, so it is run
	// separately from the cases above.
	t.Run("Closed", func(t *testing.T) {
		testClosed(t, newStore(t))
	})
}

func testPutGet(t *testing.T, store kvstore.KeyValueStore) {
	key, val := []byte("key"), []byte("value")
	if err := store.Put(key, val); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("Get = %q, want %q", got, val)
	}
}

func testGetMissing(t *testing.T, store kvstore.KeyValueStore) {
	if _, err := store.Get([]byte("absent")); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("Get(absent) err = %v, want ErrNotFound", err)
	}
}

func testOverwrite(t *testing.T, store kvstore.KeyValueStore) {
	key := []byte("key")
	if err := store.Put(key, []byte("first")); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := store.Put(key, []byte("second")); err != nil {
		t.Fatalf("Put second: %v", err)
	}
	got, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("second")) {
		t.Fatalf("Get = %q, want %q", got, "second")
	}
}

func testEmptyValue(t *testing.T, store kvstore.KeyValueStore) {
	key := []byte("empty")
	if err := store.Put(key, []byte{}); err != nil {
		t.Fatalf("Put empty: %v", err)
	}
	got, err := store.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Get = %q, want empty", got)
	}
}

// testValueIsolation verifies the documented contract that the store copies
// keys and values: mutating a caller's buffer after Put, or mutating the slice
// returned by Get, must never corrupt stored state.
func testValueIsolation(t *testing.T, store kvstore.KeyValueStore) {
	key := []byte("key")
	val := []byte("value")
	if err := store.Put(key, val); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Mutate caller buffers after Put.
	key[0] = 'X'
	val[0] = 'X'

	got, err := store.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("value")) {
		t.Fatalf("stored value mutated by caller buffer: got %q", got)
	}

	// Mutate the slice returned by Get.
	got[0] = 'Z'
	again, err := store.Get([]byte("key"))
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if !bytes.Equal(again, []byte("value")) {
		t.Fatalf("stored value mutated by Get result: got %q", again)
	}
}

func testBatch(t *testing.T, store kvstore.KeyValueStore) {
	// Pre-existing key to be deleted by the batch.
	if err := store.Put([]byte("del"), []byte("old")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	b, err := store.NewBatch()
	if err != nil {
		t.Fatalf("NewBatch: %v", err)
	}
	defer b.Close()
	if err := b.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("batch Put a: %v", err)
	}
	if err := b.Put([]byte("b"), []byte("22")); err != nil {
		t.Fatalf("batch Put b: %v", err)
	}
	if err := b.Delete([]byte("del")); err != nil {
		t.Fatalf("batch Delete: %v", err)
	}

	if got := b.ValueSize(); got != 3 {
		t.Fatalf("ValueSize = %d, want 3", got)
	}

	// Nothing is visible until Write.
	if _, err := store.Get([]byte("a")); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("batched key visible before Write: %v", err)
	}

	if err := b.Write(); err != nil {
		t.Fatalf("batch Write: %v", err)
	}

	got, err := store.Get([]byte("a"))
	if err != nil || !bytes.Equal(got, []byte("1")) {
		t.Fatalf("after Write Get(a) = %q, %v; want \"1\"", got, err)
	}
	got, err = store.Get([]byte("b"))
	if err != nil || !bytes.Equal(got, []byte("22")) {
		t.Fatalf("after Write Get(b) = %q, %v; want \"22\"", got, err)
	}
	if _, err := store.Get([]byte("del")); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("batched delete not applied after Write: %v", err)
	}
}

// testBatchInterleaved verifies that a batch replays interleaved Put/Delete
// operations on the same key in insertion order: the last operation wins.
func testBatchInterleaved(t *testing.T, store kvstore.KeyValueStore) {
	b, err := store.NewBatch()
	if err != nil {
		t.Fatalf("NewBatch: %v", err)
	}
	defer b.Close()
	// Delete then Put: the key must be present after Write.
	if err := b.Put([]byte("k1"), []byte("old")); err != nil {
		t.Fatalf("batch Put k1: %v", err)
	}
	if err := b.Delete([]byte("k1")); err != nil {
		t.Fatalf("batch Delete k1: %v", err)
	}
	if err := b.Put([]byte("k1"), []byte("new")); err != nil {
		t.Fatalf("batch re-Put k1: %v", err)
	}
	// Put then Delete: the key must be absent after Write.
	if err := b.Put([]byte("k2"), []byte("v")); err != nil {
		t.Fatalf("batch Put k2: %v", err)
	}
	if err := b.Delete([]byte("k2")); err != nil {
		t.Fatalf("batch Delete k2: %v", err)
	}
	if err := b.Write(); err != nil {
		t.Fatalf("batch Write: %v", err)
	}

	got, err := store.Get([]byte("k1"))
	if err != nil || !bytes.Equal(got, []byte("new")) {
		t.Fatalf("Get(k1) = %q, %v; want \"new\" (delete-then-put must keep the key)", got, err)
	}
	if _, err := store.Get([]byte("k2")); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("k2 present after put-then-delete in the same batch: %v", err)
	}
}

func testBatchInputOwnership(t *testing.T, store kvstore.KeyValueStore) {
	if err := store.Put([]byte("delete"), []byte("old")); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	b, err := store.NewBatch()
	if err != nil {
		t.Fatalf("NewBatch: %v", err)
	}
	defer b.Close()

	key := []byte("key")
	value := []byte("value")
	deleteKey := []byte("delete")
	if err := b.Put(key, value); err != nil {
		t.Fatalf("batch Put: %v", err)
	}
	if err := b.Delete(deleteKey); err != nil {
		t.Fatalf("batch Delete: %v", err)
	}
	key[0], value[0], deleteKey[0] = 'X', 'X', 'X'

	if err := b.Write(); err != nil {
		t.Fatalf("batch Write: %v", err)
	}
	got, err := store.Get([]byte("key"))
	if err != nil || !bytes.Equal(got, []byte("value")) {
		t.Fatalf("Get(key) = %q, %v; want value", got, err)
	}
	if _, err := store.Get([]byte("delete")); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("Get(delete) = %v, want ErrNotFound", err)
	}
}

func testBatchClose(t *testing.T, store kvstore.KeyValueStore) {
	b, err := store.NewBatch()
	if err != nil {
		t.Fatalf("NewBatch: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := b.Put([]byte("k"), []byte("v")); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Put after Close = %v, want ErrClosed", err)
	}
	if err := b.Delete([]byte("k")); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Delete after Close = %v, want ErrClosed", err)
	}
	if err := b.Write(); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Write after Close = %v, want ErrClosed", err)
	}
}

// testSync verifies that Sync succeeds on an open store and that previously
// written data is still readable afterwards.
func testSync(t *testing.T, store kvstore.KeyValueStore) {
	if err := store.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got, err := store.Get([]byte("k"))
	if err != nil || !bytes.Equal(got, []byte("v")) {
		t.Fatalf("Get after Sync = %q, %v; want \"v\"", got, err)
	}
}

func testBatchReset(t *testing.T, store kvstore.KeyValueStore) {
	b, err := store.NewBatch()
	if err != nil {
		t.Fatalf("NewBatch: %v", err)
	}
	defer b.Close()
	if err := b.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("batch Put: %v", err)
	}
	b.Reset()
	if got := b.ValueSize(); got != 0 {
		t.Fatalf("ValueSize after Reset = %d, want 0", got)
	}
	if err := b.Write(); err != nil {
		t.Fatalf("Write after Reset: %v", err)
	}
	if _, err := store.Get([]byte("a")); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("Reset did not discard accumulated writes: %v", err)
	}
}

func testIteratorFullScan(t *testing.T, store kvstore.KeyValueStore) {
	// Insert out of order; iteration must return ascending key order.
	insert(t, store, map[string]string{"c": "3", "a": "1", "b": "2"})

	it, err := store.NewIterator(nil, nil)
	if err != nil {
		t.Fatalf("NewIterator: %v", err)
	}
	defer it.Close()

	var keys, vals []string
	for it.Next() {
		keys = append(keys, string(it.Key()))
		vals = append(vals, string(it.Value()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if want := []string{"a", "b", "c"}; !equalStrings(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if want := []string{"1", "2", "3"}; !equalStrings(vals, want) {
		t.Fatalf("vals = %v, want %v", vals, want)
	}
}

func testIteratorPrefix(t *testing.T, store kvstore.KeyValueStore) {
	insert(t, store, map[string]string{
		"a:1": "x", "a:2": "y", "b:1": "z", "c": "w",
	})

	it, err := store.NewIterator([]byte("a:"), nil)
	if err != nil {
		t.Fatalf("NewIterator: %v", err)
	}
	defer it.Close()

	var keys []string
	for it.Next() {
		keys = append(keys, string(it.Key()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if want := []string{"a:1", "a:2"}; !equalStrings(keys, want) {
		t.Fatalf("prefix scan keys = %v, want %v", keys, want)
	}
}

func testIteratorStart(t *testing.T, store kvstore.KeyValueStore) {
	insert(t, store, map[string]string{
		"p1": "1", "p2": "2", "p3": "3", "p4": "4", "p5": "5",
	})

	// start is relative to the prefix: prefix "p" + start "3" => seek "p3".
	it, err := store.NewIterator([]byte("p"), []byte("3"))
	if err != nil {
		t.Fatalf("NewIterator: %v", err)
	}
	defer it.Close()

	var keys []string
	for it.Next() {
		keys = append(keys, string(it.Key()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if want := []string{"p3", "p4", "p5"}; !equalStrings(keys, want) {
		t.Fatalf("start scan keys = %v, want %v", keys, want)
	}
}

func testIteratorInputOwnership(t *testing.T, store kvstore.KeyValueStore) {
	insert(t, store, map[string]string{
		"a1": "1", "a2": "2", "b1": "3",
	})
	prefix := []byte("a")
	start := []byte("1")
	it, err := store.NewIterator(prefix, start)
	if err != nil {
		t.Fatalf("NewIterator: %v", err)
	}
	defer it.Close()
	prefix[0], start[0] = 'b', '9'

	var keys []string
	for it.Next() {
		keys = append(keys, string(it.Key()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if want := []string{"a1", "a2"}; !equalStrings(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func testIteratorClose(t *testing.T, store kvstore.KeyValueStore) {
	it, err := store.NewIterator(nil, nil)
	if err != nil {
		t.Fatalf("NewIterator: %v", err)
	}
	if err := it.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := it.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if it.Next() {
		t.Fatal("Next after Close = true, want false")
	}
}

func testIteratorEmpty(t *testing.T, store kvstore.KeyValueStore) {
	it, err := store.NewIterator(nil, nil)
	if err != nil {
		t.Fatalf("NewIterator: %v", err)
	}
	defer it.Close()
	if it.Next() {
		t.Fatalf("Next on empty store = true, want false (key %q)", it.Key())
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
}

func testClosed(t *testing.T, store kvstore.KeyValueStore) {
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := store.Get([]byte("k")); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Get on closed err = %v, want ErrClosed", err)
	}
	if err := store.Put([]byte("k"), []byte("v")); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Put on closed err = %v, want ErrClosed", err)
	}
	if err := store.Sync(); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Sync on closed err = %v, want ErrClosed", err)
	}
	if _, err := store.NewIterator(nil, nil); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("NewIterator on closed store = %v, want ErrClosed", err)
	}
	if _, err := store.NewBatch(); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("NewBatch on closed store = %v, want ErrClosed", err)
	}
}

func insert(t *testing.T, store kvstore.KeyValueStore, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := store.Put([]byte(k), []byte(v)); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
