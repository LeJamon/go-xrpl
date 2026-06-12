package pebble_test

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/kvstoretest"
	"github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
)

// The ledger-persist fsync path depends on the store exposing a real Sync;
// keep this explicit so it can never regress to a silently-missed type assert.
var _ interface{ Sync() error } = (*pebble.Store)(nil)

func TestStoreConformance(t *testing.T) {
	kvstoretest.RunConformance(t, func(t *testing.T) kvstore.KeyValueStore {
		store, err := pebble.New(t.TempDir(), 0, 0, false)
		if err != nil {
			t.Fatalf("open pebble: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}

// TestStorePersistence verifies the production backend actually persists data
// to disk across a close/reopen cycle.
func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()

	store, err := pebble.New(dir, 0, 0, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Put([]byte("durable"), []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := pebble.New(dir, 0, 0, false)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.Get([]byte("durable"))
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !bytes.Equal(got, []byte("value")) {
		t.Fatalf("Get after reopen = %q, want %q", got, "value")
	}
}

// TestStoreSyncDurability verifies Sync succeeds after non-durable writes and
// that the data is durable across a close/reopen cycle.
func TestStoreSyncDurability(t *testing.T) {
	dir := t.TempDir()

	store, err := pebble.New(dir, 0, 0, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b := store.NewBatch()
	if err := b.Put([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("batch Put: %v", err)
	}
	if err := b.Write(); err != nil {
		t.Fatalf("batch Write: %v", err)
	}
	if err := store.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := pebble.New(dir, 0, 0, false)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	for _, kv := range [][2]string{{"k", "v"}, {"k2", "v2"}} {
		got, err := reopened.Get([]byte(kv[0]))
		if err != nil || !bytes.Equal(got, []byte(kv[1])) {
			t.Fatalf("Get(%q) after reopen = %q, %v; want %q", kv[0], got, err, kv[1])
		}
	}
}

// TestStoreReadonly verifies Sync and Close behave on a read-only store:
// Sync is a no-op and Close must release the handle even though Flush is
// not possible.
func TestStoreReadonly(t *testing.T) {
	dir := t.TempDir()

	rw, err := pebble.New(dir, 0, 0, false)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	if err := rw.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("Close rw: %v", err)
	}

	ro, err := pebble.New(dir, 0, 0, true)
	if err != nil {
		t.Fatalf("open readonly: %v", err)
	}
	got, err := ro.Get([]byte("k"))
	if err != nil || !bytes.Equal(got, []byte("v")) {
		t.Fatalf("Get = %q, %v; want \"v\"", got, err)
	}
	if err := ro.Sync(); err != nil {
		t.Fatalf("Sync on readonly: %v", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("Close on readonly: %v", err)
	}

	// The handle must actually be released: a subsequent open of the same
	// directory would fail on pebble's file lock if Close leaked it.
	again, err := pebble.New(dir, 0, 0, false)
	if err != nil {
		t.Fatalf("reopen after readonly close: %v", err)
	}
	_ = again.Close()
}
