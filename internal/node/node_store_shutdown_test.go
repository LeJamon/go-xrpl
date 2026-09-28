package node

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

type nodeShutdownBlockingStore struct {
	kvstore.KeyValueStore

	syncStarted   chan struct{}
	syncRelease   chan struct{}
	closeStarted  chan struct{}
	closeFinished chan struct{}

	releaseOnce sync.Once
}

func newNodeShutdownBlockingStore() *nodeShutdownBlockingStore {
	return &nodeShutdownBlockingStore{
		KeyValueStore: memorydb.New(),
		syncStarted:   make(chan struct{}),
		syncRelease:   make(chan struct{}),
		closeStarted:  make(chan struct{}),
		closeFinished: make(chan struct{}),
	}
}

func (s *nodeShutdownBlockingStore) Sync() error {
	close(s.syncStarted)
	<-s.syncRelease
	return nil
}

func (s *nodeShutdownBlockingStore) Close() error {
	close(s.closeStarted)
	err := s.KeyValueStore.Close()
	close(s.closeFinished)
	return err
}

func (s *nodeShutdownBlockingStore) releaseSync() {
	s.releaseOnce.Do(func() { close(s.syncRelease) })
}

func TestNodeRuntimeShutdownDrainsCanceledNodeStoreSync(t *testing.T) {
	store := newNodeShutdownBlockingStore()
	database, err := nodestore.NewKVDatabase(store, nodestore.DatabaseConfig{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	syncDone := make(chan error, 1)
	go func() { syncDone <- database.Sync(ctx) }()

	t.Cleanup(func() {
		cancel()
		store.releaseSync()
		if err := database.Close(); err != nil {
			t.Errorf("close node store during cleanup: %v", err)
		}
	})

	select {
	case <-store.syncStarted:
	case <-time.After(time.Second):
		t.Fatal("backend Sync did not start")
	}
	cancel()
	select {
	case err := <-syncDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("node-store Sync error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled node-store Sync did not return promptly")
	}

	shutdownDone := make(chan error, 1)
	runtime := &nodeRuntime{
		nodeStore: database,
		serverLog: xrpllog.Discard(),
	}
	go func() { shutdownDone <- runtime.shutdownWithin(100 * time.Millisecond) }()
	select {
	case err := <-shutdownDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("node shutdown error = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("node shutdown exceeded its bounded timeout")
	}

	select {
	case <-store.closeStarted:
		t.Fatal("node store closed while backend Sync was still blocked")
	default:
	}

	store.releaseSync()
	select {
	case <-store.closeFinished:
	case <-time.After(time.Second):
		t.Fatal("node store Close did not drain after backend Sync finished")
	}
}
