package pebble_test

import (
	"bytes"
	"testing"

	"github.com/LeJamon/goXRPLd/storage/kvstore"
	"github.com/LeJamon/goXRPLd/storage/kvstore/kvstoretest"
	"github.com/LeJamon/goXRPLd/storage/kvstore/pebble"
)

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
