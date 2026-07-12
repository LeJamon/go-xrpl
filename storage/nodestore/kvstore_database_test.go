package nodestore

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

type staleMissStore struct {
	kvstore.KeyValueStore
	target Hash256
	onMiss func()
	once   sync.Once
}

func (s *staleMissStore) Get(key []byte) ([]byte, error) {
	stale := false
	if bytes.Equal(key, s.target[:]) {
		s.once.Do(func() {
			stale = true
			s.onMiss()
		})
	}
	if stale {
		return nil, kvstore.ErrNotFound
	}
	return s.KeyValueStore.Get(key)
}

func TestFetchDoesNotCacheMissAcrossStore(t *testing.T) {
	tests := []struct {
		name  string
		store func(context.Context, *KVDatabaseImpl, *Node) error
	}{
		{
			name: "Store",
			store: func(ctx context.Context, db *KVDatabaseImpl, node *Node) error {
				return db.Store(ctx, node)
			},
		},
		{
			name: "StoreBatch",
			store: func(ctx context.Context, db *KVDatabaseImpl, node *Node) error {
				return db.StoreBatch(ctx, []*Node{node})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			node := NewNode(NodeTransaction, Blob("stored during stale miss"))
			backend := &staleMissStore{
				KeyValueStore: memorydb.New(),
				target:        node.Hash,
			}
			db := NewKVDatabaseWithConfig(backend, "test", &DatabaseConfig{
				NegativeCacheTTL:     time.Hour,
				NegativeCacheMaxSize: 10,
			})
			t.Cleanup(func() { _ = db.Close() })

			var storeErr error
			backend.onMiss = func() {
				storeErr = tt.store(ctx, db, node)
			}

			got, err := db.Fetch(ctx, node.Hash)
			if err != nil {
				t.Fatalf("Fetch stale miss: %v", err)
			}
			if storeErr != nil {
				t.Fatalf("write during Fetch: %v", storeErr)
			}
			if got != nil {
				t.Fatalf("Fetch stale miss = %v, want nil", got)
			}
			if db.negativeCache.IsMissing(node.Hash) {
				t.Fatal("stale miss remained in negative cache after write")
			}

			got, err = db.Fetch(ctx, node.Hash)
			if err != nil {
				t.Fatalf("Fetch stored node: %v", err)
			}
			if got == nil || got.Hash != node.Hash || !bytes.Equal(got.Data, node.Data) {
				t.Fatalf("Fetch stored node = %#v, want %#v", got, node)
			}
		})
	}
}
