package memorydb_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/storage/kvstore"
	"github.com/LeJamon/goXRPLd/storage/kvstore/kvstoretest"
	"github.com/LeJamon/goXRPLd/storage/kvstore/memorydb"
)

func TestMemDatabaseConformance(t *testing.T) {
	kvstoretest.RunConformance(t, func(t *testing.T) kvstore.KeyValueStore {
		store := memorydb.New()
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}

func TestMemDatabaseLen(t *testing.T) {
	db := memorydb.New()
	defer db.Close()

	if db.Len() != 0 {
		t.Fatalf("empty Len = %d, want 0", db.Len())
	}
	for _, k := range []string{"a", "b", "c"} {
		if err := db.Put([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	if db.Len() != 3 {
		t.Fatalf("Len = %d, want 3", db.Len())
	}
	// Overwriting an existing key must not grow the store.
	if err := db.Put([]byte("a"), []byte("v2")); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	if db.Len() != 3 {
		t.Fatalf("Len after overwrite = %d, want 3", db.Len())
	}
	if err := db.Delete([]byte("a")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if db.Len() != 2 {
		t.Fatalf("Len after delete = %d, want 2", db.Len())
	}
}
