package nodestore

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

type syncRecordingStore struct {
	kvstore.KeyValueStore
	mu        sync.Mutex
	syncCalls int
	syncErr   error
}

func (s *syncRecordingStore) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncCalls++
	return s.syncErr
}

func TestDatabaseSyncReachesBackend(t *testing.T) {
	store := &syncRecordingStore{KeyValueStore: memorydb.New()}
	database := testDatabase(t, store, noCacheConfig())
	if err := database.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	store.mu.Lock()
	if store.syncCalls != 1 {
		t.Fatalf("backend Sync called %d times, want 1", store.syncCalls)
	}
	store.syncErr = errors.New("disk on fire")
	store.mu.Unlock()
	if err := database.Sync(context.Background()); !errors.Is(err, store.syncErr) {
		t.Fatalf("Sync error = %v, want backend error", err)
	}
}

type blockingSyncStore struct {
	kvstore.KeyValueStore
	started chan struct{}
	release chan struct{}
}

func (s *blockingSyncStore) Sync() error {
	s.started <- struct{}{}
	<-s.release
	return nil
}

func TestSyncCancellationRetainsBackendExclusion(t *testing.T) {
	store := &blockingSyncStore{
		KeyValueStore: memorydb.New(),
		started:       make(chan struct{}, 2),
		release:       make(chan struct{}, 2),
	}
	database, err := NewKVDatabase(store, noCacheConfig())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- database.Sync(ctx) }()
	<-store.started
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first Sync error = %v, want context.Canceled", err)
	}
	select {
	case <-database.syncGate:
		t.Fatal("canceled Sync released its slot before the backend completed")
	default:
	}
	if database.lifecycleMu.TryLock() {
		database.lifecycleMu.Unlock()
		t.Fatal("canceled Sync stopped pinning the database lifetime")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- database.Sync(context.Background()) }()
	select {
	case <-store.started:
		t.Fatal("second backend Sync overlapped the first")
	default:
	}

	store.release <- struct{}{}
	<-store.started
	store.release <- struct{}{}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForCanceledSync(t *testing.T) {
	store := &blockingSyncStore{
		KeyValueStore: memorydb.New(),
		started:       make(chan struct{}, 1),
		release:       make(chan struct{}, 1),
	}
	database, err := NewKVDatabase(store, noCacheConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	syncDone := make(chan error, 1)
	go func() { syncDone <- database.Sync(ctx) }()
	<-store.started
	cancel()
	if err := <-syncDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync error = %v, want context.Canceled", err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- database.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before backend Sync completed: %v", err)
	default:
	}
	store.release <- struct{}{}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := database.Sync(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Sync after Close = %v, want ErrClosed", err)
	}
}

func TestOperationsAfterCloseReturnErrClosed(t *testing.T) {
	database, err := NewKVDatabase(memorydb.New(), positiveCacheConfig(10))
	if err != nil {
		t.Fatal(err)
	}
	node := testNode(NodeAccount, []byte("cached"), 1)
	if err := database.Store(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Fetch(t.Context(), node.Hash); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	assertClosed := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrClosed) {
			t.Errorf("%s error = %v, want ErrClosed", name, err)
		}
	}
	assertClosed("Store", database.Store(t.Context(), node))
	assertClosed("StoreBatch", database.StoreBatch(t.Context(), []*Node{node}))
	_, err = database.Fetch(t.Context(), node.Hash)
	assertClosed("Fetch", err)
	_, err = database.FetchCached(t.Context(), node.Hash)
	assertClosed("FetchCached", err)
	_, err = database.FetchDataUncached(t.Context(), node.Hash)
	assertClosed("FetchDataUncached", err)
	assertClosed("Sweep", database.Sweep())
	assertClosed("Sync", database.Sync(t.Context()))

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = database.Fetch(canceled, node.Hash)
	assertClosed("Fetch with canceled context", err)
}
