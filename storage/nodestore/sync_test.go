package nodestore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

// syncRecordingStore wraps a real store and records Sync invocations so the
// test can prove Database.Sync reaches the backend instead of silently
// no-opping.
type syncRecordingStore struct {
	kvstore.KeyValueStore
	syncCalls int
	syncErr   error
}

func (s *syncRecordingStore) Sync() error {
	s.syncCalls++
	return s.syncErr
}

func TestDatabaseSyncReachesBackend(t *testing.T) {
	store := &syncRecordingStore{KeyValueStore: memorydb.New()}
	db := nodestore.NewKVDatabase(store, "test", 10, time.Hour)
	defer db.Close()

	if err := db.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if store.syncCalls != 1 {
		t.Fatalf("backend Sync called %d times, want 1", store.syncCalls)
	}

	store.syncErr = errors.New("disk on fire")
	if err := db.Sync(context.Background()); !errors.Is(err, store.syncErr) {
		t.Fatalf("Sync err = %v, want backend error propagated", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := db.Sync(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync with cancelled ctx = %v, want context.Canceled", err)
	}
}
