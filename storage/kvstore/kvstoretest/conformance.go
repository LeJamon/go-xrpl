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
		{"Has", testHas},
		{"Overwrite", testOverwrite},
		{"Delete", testDelete},
		{"DeleteMissing", testDeleteMissing},
		{"EmptyValue", testEmptyValue},
		{"ValueIsolation", testValueIsolation},
		{"Batch", testBatch},
		{"BatchReset", testBatchReset},
		{"IteratorFullScan", testIteratorFullScan},
		{"IteratorPrefix", testIteratorPrefix},
		{"IteratorStart", testIteratorStart},
		{"IteratorEmpty", testIteratorEmpty},
		{"Stat", testStat},
		{"Compact", testCompact},
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

func testHas(t *testing.T, store kvstore.KeyValueStore) {
	key := []byte("key")
	has, err := store.Has(key)
	if err != nil {
		t.Fatalf("Has(absent): %v", err)
	}
	if has {
		t.Fatal("Has(absent) = true, want false")
	}
	if err := store.Put(key, []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	has, err = store.Has(key)
	if err != nil {
		t.Fatalf("Has(present): %v", err)
	}
	if !has {
		t.Fatal("Has(present) = false, want true")
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

func testDelete(t *testing.T, store kvstore.KeyValueStore) {
	key := []byte("key")
	if err := store.Put(key, []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(key); !errors.Is(err, kvstore.ErrNotFound) {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
	has, err := store.Has(key)
	if err != nil {
		t.Fatalf("Has after Delete: %v", err)
	}
	if has {
		t.Fatal("Has after Delete = true, want false")
	}
}

func testDeleteMissing(t *testing.T, store kvstore.KeyValueStore) {
	if err := store.Delete([]byte("absent")); err != nil {
		t.Fatalf("Delete(absent) = %v, want nil", err)
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
	has, err := store.Has(key)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !has {
		t.Fatal("Has(empty value) = false, want true")
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

	b := store.NewBatch()
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
	if has, _ := store.Has([]byte("a")); has {
		t.Fatal("batched key visible before Write")
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
	if has, _ := store.Has([]byte("del")); has {
		t.Fatal("batched delete not applied after Write")
	}
}

func testBatchReset(t *testing.T, store kvstore.KeyValueStore) {
	b := store.NewBatch()
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
	if has, _ := store.Has([]byte("a")); has {
		t.Fatal("Reset did not discard accumulated writes")
	}
}

func testIteratorFullScan(t *testing.T, store kvstore.KeyValueStore) {
	// Insert out of order; iteration must return ascending key order.
	insert(t, store, map[string]string{"c": "3", "a": "1", "b": "2"})

	it := store.NewIterator(nil, nil)
	defer it.Release()

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

	it := store.NewIterator([]byte("a:"), nil)
	defer it.Release()

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
	it := store.NewIterator([]byte("p"), []byte("3"))
	defer it.Release()

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

func testIteratorEmpty(t *testing.T, store kvstore.KeyValueStore) {
	it := store.NewIterator(nil, nil)
	defer it.Release()
	if it.Next() {
		t.Fatalf("Next on empty store = true, want false (key %q)", it.Key())
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
}

func testStat(t *testing.T, store kvstore.KeyValueStore) {
	s, err := store.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if s == "" {
		t.Fatal("Stat returned empty string")
	}
}

func testCompact(t *testing.T, store kvstore.KeyValueStore) {
	insert(t, store, map[string]string{"a": "1", "b": "2"})
	if err := store.Compact([]byte{0x00}, []byte{0xff}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// Data must survive compaction.
	got, err := store.Get([]byte("a"))
	if err != nil || !bytes.Equal(got, []byte("1")) {
		t.Fatalf("after Compact Get(a) = %q, %v; want \"1\"", got, err)
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
	if err := store.Delete([]byte("k")); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Delete on closed err = %v, want ErrClosed", err)
	}
	if _, err := store.Has([]byte("k")); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Has on closed err = %v, want ErrClosed", err)
	}
	if _, err := store.Stat(); !errors.Is(err, kvstore.ErrClosed) {
		t.Fatalf("Stat on closed err = %v, want ErrClosed", err)
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
