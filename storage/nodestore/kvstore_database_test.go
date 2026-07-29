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

type staleReadStore struct {
	kvstore.KeyValueStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingPutCache struct {
	*nodeCache
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingPutCache) Put(node *Node) {
	c.once.Do(func() {
		close(c.started)
		<-c.release
	})
	c.nodeCache.Put(node)
}

func (s *blockingGetStore) Get(key []byte) ([]byte, error) {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return s.KeyValueStore.Get(key)
}

func (s *staleReadStore) Get(key []byte) ([]byte, error) {
	data, err := s.KeyValueStore.Get(key)
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	return data, err
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

func TestStatsTrackPositiveCacheActivity(t *testing.T) {
	ctx := context.Background()
	store := memorydb.New()
	db := testDatabase(t, store, positiveCacheConfig(16))
	missing := testHash([]byte("missing"))
	if node, err := db.Fetch(ctx, missing); err != nil || node != nil {
		t.Fatalf("Fetch missing = (%v, %v), want (nil, nil)", node, err)
	}

	node := testNode(NodeAccount, []byte("cached"), 1)
	encoded := encodeNodeData(node)
	if err := store.Put(node.Hash[:], encoded); err != nil {
		releaseEncodeBuf(encoded)
		t.Fatal(err)
	}
	releaseEncodeBuf(encoded)
	for range 2 {
		if got, err := db.Fetch(ctx, node.Hash); err != nil || got == nil {
			t.Fatalf("Fetch stored = (%v, %v), want node", got, err)
		}
	}

	stats := db.Stats()
	if stats.Reads != 3 ||
		stats.FetchHits != 2 ||
		stats.CacheHits != 1 ||
		stats.CacheMisses != 2 ||
		stats.CacheSize != 1 {
		t.Fatalf("unexpected statistics: %+v", stats)
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

func TestStoreDoesNotAllowStaleFetchToOverwriteCache(t *testing.T) {
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
			backend := memorydb.New()
			store := &staleReadStore{
				KeyValueStore: backend,
				started:       make(chan struct{}),
				release:       make(chan struct{}),
			}
			database := testDatabase(t, store, positiveCacheConfig(16))
			hash := testHash([]byte("mutable-node"))
			oldNode := &Node{
				Type:      NodeLedger,
				Hash:      hash,
				Data:      []byte("old"),
				LedgerSeq: 1,
			}
			newNode := &Node{
				Type:      NodeLedger,
				Hash:      hash,
				Data:      []byte("new"),
				LedgerSeq: 2,
			}
			encoded := encodeNodeData(oldNode)
			if err := backend.Put(hash[:], encoded); err != nil {
				t.Fatal(err)
			}
			releaseEncodeBuf(encoded)

			done := make(chan error, 1)
			go func() {
				_, err := database.Fetch(context.Background(), hash)
				done <- err
			}()
			<-store.started
			if err := test.store(context.Background(), database, newNode); err != nil {
				close(store.release)
				t.Fatalf("%s: %v", test.name, err)
			}
			close(store.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			cached, found := database.cache.Get(hash)
			if !found {
				t.Fatal("stored node was not published to the cache")
			}
			if !bytes.Equal(cached.Data, newNode.Data) || cached.LedgerSeq != newNode.LedgerSeq {
				t.Fatalf("cache contains stale node: data=%q ledger=%d", cached.Data, cached.LedgerSeq)
			}
		})
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

func TestStoresPublishCacheInBackendOrder(t *testing.T) {
	tests := []struct {
		name        string
		firstStore  func(context.Context, *KVDatabase, *Node) error
		secondStore func(context.Context, *KVDatabase, *Node) error
	}{
		{
			name: "Store then StoreBatch",
			firstStore: func(ctx context.Context, database *KVDatabase, node *Node) error {
				return database.Store(ctx, node)
			},
			secondStore: func(ctx context.Context, database *KVDatabase, node *Node) error {
				return database.StoreBatch(ctx, []*Node{node})
			},
		},
		{
			name: "StoreBatch then Store",
			firstStore: func(ctx context.Context, database *KVDatabase, node *Node) error {
				return database.StoreBatch(ctx, []*Node{node})
			},
			secondStore: func(ctx context.Context, database *KVDatabase, node *Node) error {
				return database.Store(ctx, node)
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
			hash := testHash([]byte("restamped-node"))
			oldNode := &Node{
				Type:      NodeLedger,
				Hash:      hash,
				Data:      []byte("old"),
				LedgerSeq: 1,
			}
			newNode := &Node{
				Type:      NodeLedger,
				Hash:      hash,
				Data:      []byte("new"),
				LedgerSeq: 2,
			}

			firstDone := make(chan error, 1)
			go func() {
				firstDone <- test.firstStore(context.Background(), database, oldNode)
			}()
			<-blocking.started

			secondStarted := make(chan struct{})
			secondDone := make(chan error, 1)
			go func() {
				close(secondStarted)
				secondDone <- test.secondStore(context.Background(), database, newNode)
			}()
			<-secondStarted
			if database.writeMu.TryLock() {
				database.writeMu.Unlock()
				t.Fatal("write lock was available before cache publication completed")
			}

			close(blocking.release)
			if err := <-firstDone; err != nil {
				t.Fatalf("first store: %v", err)
			}
			if err := <-secondDone; err != nil {
				t.Fatalf("second store: %v", err)
			}
			cached, found := blocking.Get(hash)
			if !found {
				t.Fatal("stored node was not published to the cache")
			}
			if !bytes.Equal(cached.Data, newNode.Data) || cached.LedgerSeq != newNode.LedgerSeq {
				t.Fatalf("cache is out of backend order: data=%q ledger=%d", cached.Data, cached.LedgerSeq)
			}
		})
	}
}
