package nodestore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

type detachedRetentionStore struct {
	retentionRotatingStore
	retire    func() error
	commitErr error
}

func (s *detachedRetentionStore) Rotate(last, minimum uint32) (bool, error) {
	committed, retire, err := s.RotateWithRetirement(last, minimum)
	if retire != nil {
		err = errors.Join(err, retire())
	}
	return committed, err
}

func (s *detachedRetentionStore) RotateWithRetirement(last, minimum uint32) (bool, func() error, error) {
	committed, _ := s.retentionRotatingStore.Rotate(last, minimum)
	return committed, s.retire, s.commitErr
}

func TestRotationRetirementReleasesLiveGuardsBeforeCleanup(t *testing.T) {
	for _, publishedError := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "published-error"}[publishedError], func(t *testing.T) {
			entered := make(chan struct{})
			unblock := make(chan struct{})
			var once sync.Once
			releaseCleanup := func() { once.Do(func() { close(unblock) }) }
			defer releaseCleanup()
			cleanupErr := errors.New("retired cleanup failed")
			store := &detachedRetentionStore{
				retentionRotatingStore: retentionRotatingStore{KeyValueStore: memorydb.New()},
				retire:                 func() error { close(entered); <-unblock; return cleanupErr },
			}
			if publishedError {
				store.commitErr = errors.New("manifest directory sync failed")
			}
			db, err := NewRotatingKVDatabase(store, positiveCacheConfig(8))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				releaseCleanup()
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			node := testNode(NodeAccount, []byte("live during retirement"), 1)
			if err := db.Store(t.Context(), node); err != nil {
				t.Fatal(err)
			}
			generation := db.cacheGeneration.Load()
			var liveMu sync.RWMutex
			finishedPrune := false
			done := make(chan error, 1)
			go func() {
				committed, err := db.RotateGenerationWithRetention(t.Context(), 10, 2,
					func(context.Context) (func(), error) { liveMu.Lock(); return liveMu.Unlock, nil },
					func() func() { return func() { finishedPrune = true } },
				)
				if !committed {
					t.Error("published rotation reported uncommitted")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("retirement not reached")
			}
			if !finishedPrune {
				t.Error("proof invalidation still held during cleanup")
			}
			if !liveMu.TryRLock() {
				t.Error("live ledger guard held during cleanup")
			} else {
				liveMu.RUnlock()
			}
			if !db.pruneMu.TryRLock() {
				t.Error("prune gate held during cleanup")
			} else {
				db.pruneMu.RUnlock()
			}
			if !db.mutationMu.TryRLock() {
				t.Error("mutation gate held during cleanup")
			} else {
				db.mutationMu.RUnlock()
			}
			if db.lifecycleMu.TryLock() {
				db.lifecycleMu.Unlock()
				t.Error("cleanup is not pinned against Close")
			}
			if got := db.cacheGeneration.Load(); got != generation+1 {
				t.Errorf("cache generation=%d, want %d", got, generation+1)
			}
			if _, cached := db.cache.Get(node.Hash); cached {
				t.Error("stale cache survived publication")
			}
			readDone := make(chan error, 1)
			go func() {
				got, err := db.Fetch(t.Context(), node.Hash)
				if got == nil && err == nil {
					err = errors.New("live node disappeared")
				}
				readDone <- err
			}()
			select {
			case err := <-readDone:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("live read blocked by retirement")
			}
			// Another rotation must wait before acquiring any live-state guard,
			// and cancellation of that wait must remain responsive.
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			_, err = db.RotateGenerationWithRetention(ctx, 11, 3,
				func(context.Context) (func(), error) {
					t.Error("second rotation entered live guard during retirement")
					return nil, nil
				}, nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("second rotation error=%v", err)
			}
			releaseCleanup()
			err = <-done
			if !errors.Is(err, cleanupErr) {
				t.Errorf("lost cleanup error: %v", err)
			}
			if publishedError && !errors.Is(err, store.commitErr) {
				t.Errorf("lost publication error: %v", err)
			}
		})
	}
}
