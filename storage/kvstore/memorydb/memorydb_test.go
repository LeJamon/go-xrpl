package memorydb_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/kvstoretest"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

func TestMemDatabaseConformance(t *testing.T) {
	kvstoretest.RunConformance(t, func(t *testing.T) kvstore.KeyValueStore {
		store := memorydb.New()
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}
