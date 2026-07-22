package shamap

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cancelFetchFamily struct {
	inner   Family
	calls   atomic.Int32
	started chan struct{}
	once    sync.Once
}

func (f *cancelFetchFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if f.calls.Add(1) == 1 {
		return f.inner.Fetch(ctx, hash)
	}
	f.once.Do(func() { close(f.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *cancelFetchFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.inner.StoreBatch(ctx, entries)
}

func newCancelFetchMap(t *testing.T, value byte) (*SHAMap, *cancelFetchFamily, [32]byte) {
	t.Helper()
	mem := newMemoryFamily()
	source, err := NewBacked(TypeState, mem)
	if err != nil {
		t.Fatal(err)
	}
	key := [32]byte{0x10}
	if err := source.Put(key, []byte{value, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}); err != nil {
		t.Fatal(err)
	}
	if err := source.Put([32]byte{0x80}, []byte{value, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}); err != nil {
		t.Fatal(err)
	}
	root, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := flushToFamily(source, mem); err != nil {
		t.Fatal(err)
	}
	blocking := &cancelFetchFamily{inner: mem, started: make(chan struct{})}
	lazy, err := NewFromRootHashContext(context.Background(), TypeState, root, blocking)
	if err != nil {
		t.Fatal(err)
	}
	return lazy, blocking, key
}

func cancelAfterFetchStarts(t *testing.T, started <-chan struct{}, cancel context.CancelFunc, result <-chan error) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("lazy fetch did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("operation error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not return after cancellation")
	}
}

func TestLazyTraversalContextCancellation(t *testing.T) {
	t.Run("missing nodes", func(t *testing.T) {
		lazy, family, _ := newCancelFetchMap(t, 1)
		if err := lazy.StartSync(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := lazy.GetMissingNodesContext(ctx, 256, nil)
			result <- err
		}()
		cancelAfterFetchStarts(t, family.started, cancel, result)
	})

	t.Run("finish sync", func(t *testing.T) {
		lazy, family, _ := newCancelFetchMap(t, 1)
		if err := lazy.StartSync(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- lazy.FinishSyncContext(ctx)
		}()
		cancelAfterFetchStarts(t, family.started, cancel, result)
	})

	t.Run("upper bound", func(t *testing.T) {
		lazy, family, _ := newCancelFetchMap(t, 1)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- lazy.UpperBoundContext(ctx, [32]byte{}).Err()
		}()
		cancelAfterFetchStarts(t, family.started, cancel, result)
	})

	t.Run("compare", func(t *testing.T) {
		lazy, family, _ := newCancelFetchMap(t, 1)
		other, _, _ := newCancelFetchMap(t, 2)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := lazy.CompareContext(ctx, other, 0)
			result <- err
		}()
		cancelAfterFetchStarts(t, family.started, cancel, result)
	})

	t.Run("proof", func(t *testing.T) {
		lazy, family, key := newCancelFetchMap(t, 1)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := lazy.GetProofPathContext(ctx, key)
			result <- err
		}()
		cancelAfterFetchStarts(t, family.started, cancel, result)
	})
}
