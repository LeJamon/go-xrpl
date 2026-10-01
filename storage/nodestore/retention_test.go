package nodestore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
)

type retentionRotatingStore struct {
	kvstore.KeyValueStore
	mu            sync.Mutex
	rotateHook    func()
	rotateCalls   int
	lastRotated   uint32
	minimumOnline uint32
}

func (s *retentionRotatingStore) CanRotateWithoutRefresh() (bool, error) {
	return true, nil
}

func (s *retentionRotatingStore) Promote(key []byte) ([]byte, error) {
	return s.Get(key)
}

func (s *retentionRotatingStore) Rotate(lastRotated, minimumOnline uint32) (bool, error) {
	if s.rotateHook != nil {
		s.rotateHook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotateCalls++
	s.lastRotated = lastRotated
	s.minimumOnline = minimumOnline
	return true, nil
}

func (s *retentionRotatingStore) RotationState() (lastRotated, minimumOnline uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRotated, s.minimumOnline
}

func TestDeleteBeforeWithRetentionRejectsBeforePruneOrMutation(t *testing.T) {
	database := testDatabase(t, memorydb.New(), positiveCacheConfig(8))
	node := testNode(NodeAccount, []byte("retained"), 1)
	if err := database.Store(t.Context(), node); err != nil {
		t.Fatal(err)
	}

	guardErr := errors.New("stale live roots")
	guardCalled := false
	released := false
	pruneCalled := false
	deleted, err := database.DeleteBeforeWithRetention(
		t.Context(), 2, 1,
		func(context.Context) (func(), error) {
			guardCalled = true
			if database.mutationMu.TryRLock() {
				database.mutationMu.RUnlock()
				t.Fatal("retention guard ran outside the mutation gate")
			}
			return func() { released = true }, guardErr
		},
		func() func() {
			pruneCalled = true
			return func() {}
		},
	)
	if deleted != 0 || !errors.Is(err, guardErr) {
		t.Fatalf("deleted=%d err=%v, want guard rejection", deleted, err)
	}
	if !guardCalled || !released {
		t.Fatalf("guard called=%t released=%t, want both true", guardCalled, released)
	}
	if pruneCalled {
		t.Fatal("guard rejection entered prune invalidation")
	}
	if got, fetchErr := database.Fetch(t.Context(), node.Hash); fetchErr != nil || got == nil {
		t.Fatalf("node after rejected retention = %+v, err=%v", got, fetchErr)
	}
	if _, cached := database.cache.Get(node.Hash); !cached {
		t.Fatal("guard rejection invalidated the positive cache")
	}
}

func TestDeleteBeforeWithRetentionReleasesAfterDestructiveSection(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	node := testNode(NodeAccount, []byte("retained"), 1)
	if err := database.Store(t.Context(), node); err != nil {
		t.Fatal(err)
	}

	var events []string
	appendEvent := func(event string) { events = append(events, event) }
	deleted, err := database.DeleteBeforeWithRetention(
		t.Context(), 2, 1,
		func(context.Context) (func(), error) {
			if database.mutationMu.TryRLock() {
				database.mutationMu.RUnlock()
				t.Fatal("retention guard ran outside the mutation gate")
			}
			appendEvent("guard")
			return func() { appendEvent("release") }, nil
		},
		func() func() {
			appendEvent("begin-prune")
			return func() { appendEvent("finish-prune") }
		},
	)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v, want one successful deletion", deleted, err)
	}
	want := []string{"guard", "begin-prune", "finish-prune", "release"}
	if len(events) != len(want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events=%v, want %v", events, want)
		}
	}
}

func TestRotateGenerationWithRetentionRejectsBeforeSwapOrInvalidation(t *testing.T) {
	store := &retentionRotatingStore{KeyValueStore: memorydb.New()}
	database, err := NewRotatingKVDatabase(store, positiveCacheConfig(8))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	node := testNode(NodeAccount, []byte("live"), 1)
	if err := database.Store(t.Context(), node); err != nil {
		t.Fatal(err)
	}
	cacheGeneration := database.cacheGeneration.Load()
	guardErr := errors.New("root set changed")
	released := false
	pruneCalled := false
	committed, err := database.RotateGenerationWithRetention(
		t.Context(), 10, 2,
		func(context.Context) (func(), error) {
			if database.mutationMu.TryRLock() {
				database.mutationMu.RUnlock()
				t.Fatal("retention guard ran outside the mutation gate")
			}
			return func() { released = true }, guardErr
		},
		func() func() {
			pruneCalled = true
			return func() {}
		},
	)
	if committed || !errors.Is(err, guardErr) {
		t.Fatalf("committed=%t err=%v, want guard rejection", committed, err)
	}
	if !released || pruneCalled {
		t.Fatalf("released=%t pruneCalled=%t, want true/false", released, pruneCalled)
	}
	if got := store.rotateCalls; got != 0 {
		t.Fatalf("backend rotations=%d, want 0", got)
	}
	if got := database.cacheGeneration.Load(); got != cacheGeneration {
		t.Fatalf("cache generation=%d, want unchanged %d", got, cacheGeneration)
	}
	if _, cached := database.cache.Get(node.Hash); !cached {
		t.Fatal("guard rejection cleared the positive cache")
	}
}

func TestRotateGenerationWithRetentionReleasesAfterCommit(t *testing.T) {
	events := make([]string, 0, 8)
	store := &retentionRotatingStore{
		KeyValueStore: memorydb.New(),
		rotateHook:    func() { events = append(events, "rotate") },
	}
	database, err := NewRotatingKVDatabase(store, noCacheConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	committed, err := database.RotateGenerationWithRetention(
		t.Context(), 10, 2,
		func(context.Context) (func(), error) {
			if database.mutationMu.TryRLock() {
				database.mutationMu.RUnlock()
				t.Fatal("retention guard ran outside the mutation gate")
			}
			events = append(events, "guard")
			return func() { events = append(events, "release") }, nil
		},
		func() func() {
			events = append(events, "begin-prune")
			return func() { events = append(events, "finish-prune") }
		},
	)
	if err != nil || !committed {
		t.Fatalf("committed=%t err=%v, want committed rotation", committed, err)
	}
	want := []string{"guard", "begin-prune", "rotate", "finish-prune", "release"}
	if len(events) != len(want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events=%v, want %v", events, want)
		}
	}
}

func TestMutationAdmissionHonorsCancellation(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	database.mutationMu.Lock()
	defer database.mutationMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := database.DeleteBeforeWithRetention(ctx, 2, 1, nil, nil)
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DeleteBeforeWithRetention error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled mutation admission remained blocked")
	}
}

func TestRetentionGuardCanUseDatabaseWhileLifecycleWriterWaits(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	node := testNode(NodeAccount, []byte("guard-read"), 1)
	if err := database.Store(t.Context(), node); err != nil {
		t.Fatal(err)
	}

	writerStarted := make(chan struct{})
	writerAcquired := make(chan struct{})
	guardErr := errors.New("lifecycle writer did not acquire")
	deleted, err := database.DeleteBeforeWithRetention(
		t.Context(), 2, 1,
		func(ctx context.Context) (func(), error) {
			go func() {
				close(writerStarted)
				database.lifecycleMu.Lock()
				close(writerAcquired)
				database.lifecycleMu.Unlock()
			}()
			<-writerStarted
			select {
			case <-writerAcquired:
			case <-time.After(time.Second):
				return nil, guardErr
			}
			if _, fetchErr := database.Fetch(ctx, node.Hash); fetchErr != nil {
				return nil, fetchErr
			}
			return nil, nil
		},
		nil,
	)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v, want one successful deletion", deleted, err)
	}
}

func TestRetentionCancellationAfterGuardReleasesWithoutMutation(t *testing.T) {
	database := testDatabase(t, memorydb.New(), noCacheConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	released := false
	pruneCalled := false
	deleted, err := database.DeleteBeforeWithRetention(
		ctx, 1, 1,
		func(context.Context) (func(), error) {
			cancel()
			return func() { released = true }, nil
		},
		func() func() {
			pruneCalled = true
			return func() {}
		},
	)
	if deleted != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("deleted=%d err=%v, want cancellation before mutation", deleted, err)
	}
	if !released || pruneCalled {
		t.Fatalf("released=%t pruneCalled=%t, want true/false", released, pruneCalled)
	}
}
