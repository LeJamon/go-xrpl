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

type blockingGetStore struct {
	kvstore.KeyValueStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingPutCache struct {
	*nodeCache
	started chan struct{}
	release chan struct{}
}

func (c *blockingPutCache) Put(node *Node) {
	close(c.started)
	<-c.release
	c.nodeCache.Put(node)
}

func (s *blockingGetStore) Get(key []byte) ([]byte, error) {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return s.KeyValueStore.Get(key)
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
		store func(context.Context, *KVDatabase, *Node) error
	}{
		{
			name: "Store",
			store: func(ctx context.Context, db *KVDatabase, node *Node) error {
				return db.Store(ctx, node)
			},
		},
		{
			name: "StoreBatch",
			store: func(ctx context.Context, db *KVDatabase, node *Node) error {
				return db.StoreBatch(ctx, []*Node{node})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			node := testNode(NodeTransaction, []byte("stored during stale miss"), 0)
			backend := &staleMissStore{
				KeyValueStore: memorydb.New(),
				target:        node.Hash,
			}
			db, err := NewKVDatabase(backend, DatabaseConfig{
				NegativeCache: CacheConfig{
					Enabled:    true,
					MaxEntries: 10,
					TTL:        time.Hour,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
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

func TestStoreBatchWithoutNodesDoesNotAdvanceGeneration(t *testing.T) {
	tests := []struct {
		name  string
		nodes []*Node
	}{
		{name: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := NewKVDatabase(memorydb.New(), DatabaseConfig{
				NegativeCache: CacheConfig{
					Enabled:    true,
					MaxEntries: 10,
					TTL:        time.Hour,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })

			if err := db.StoreBatch(context.Background(), tt.nodes); err != nil {
				t.Fatalf("StoreBatch: %v", err)
			}
			if got := db.storeGeneration.Load(); got != 0 {
				t.Fatalf("store generation = %d, want 0", got)
			}
		})
	}
}

func TestFetchCrossingCacheGenerationDoesNotRepopulate(t *testing.T) {
	ctx := context.Background()
	store := &blockingGetStore{
		KeyValueStore: memorydb.New(),
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	db := testDatabase(t, store, positiveCacheConfig(16))
	node := &Node{
		Type: NodeAccount,
		Hash: testHash([]byte("cache-generation")),
		Data: []byte("cache-generation"),
	}
	encoded := encodeNodeData(node)
	if err := store.Put(node.Hash[:], encoded); err != nil {
		t.Fatal(err)
	}
	releaseEncodeBuf(encoded)

	done := make(chan error, 1)
	go func() {
		_, err := db.Fetch(ctx, node.Hash)
		done <- err
	}()
	<-store.started
	db.cacheGeneration.Add(1)
	db.cache.Clear()
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, found := db.cache.Get(node.Hash); found {
		t.Fatal("fetch crossing a cache generation repopulated a retired entry")
	}
}

func TestStorePublishesCacheUnderPruneLock(t *testing.T) {
	tests := []struct {
		name  string
		store func(context.Context, *KVDatabase, *Node) error
	}{
		{
			name: "Store",
			store: func(ctx context.Context, database *KVDatabase, node *Node) error {
				return database.Store(ctx, node)
			},
		},
		{
			name: "StoreBatch",
			store: func(ctx context.Context, database *KVDatabase, node *Node) error {
				return database.StoreBatch(ctx, []*Node{node})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := testDatabase(t, memorydb.New(), positiveCacheConfig(16))
			blocking := &blockingPutCache{
				nodeCache: database.cache.(*nodeCache),
				started:   make(chan struct{}),
				release:   make(chan struct{}),
			}
			database.cache = blocking
			node := testNode(NodeAccount, []byte(test.name), 1)
			done := make(chan error, 1)
			go func() {
				done <- test.store(context.Background(), database, node)
			}()
			<-blocking.started

			if database.pruneMu.TryLock() {
				database.pruneMu.Unlock()
				t.Fatal("prune lock was available before cache publication completed")
			}
			close(blocking.release)
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", test.name, err)
			}
			if !database.pruneMu.TryLock() {
				t.Fatal("store retained prune lock after cache publication")
			}
			database.pruneMu.Unlock()
			if _, found := blocking.Get(node.Hash); !found {
				t.Fatal("stored node was not published to the cache")
			}
		})
	}
}
