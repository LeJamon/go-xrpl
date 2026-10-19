package shamap

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type wireFetchFamily struct {
	*memoryFamily
	fetches       atomic.Int32
	started       chan struct{}
	release       chan struct{}
	once          sync.Once
	block         bool
	honorContext  bool
	ignoreContext bool
}

func (f *wireFetchFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.fetches.Add(1)
	if f.block {
		f.once.Do(func() { close(f.started) })
		if f.honorContext {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-f.release:
			}
		} else {
			<-f.release
		}
	}
	if f.ignoreContext {
		ctx = context.Background()
	}
	return f.memoryFamily.Fetch(ctx, hash)
}

func (f *wireFetchFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.memoryFamily.StoreBatch(ctx, entries)
}

type snapshotStoreFamily struct {
	*memoryFamily
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (f *snapshotStoreFamily) StoreBatch(_ context.Context, entries []FlushEntry) error {
	f.calls.Add(1)
	f.once.Do(func() { close(f.started) })
	<-f.release
	return f.memoryFamily.StoreBatch(context.Background(), entries)
}

func newWireContextMap(t *testing.T) (*SHAMap, *memoryFamily) {
	t.Helper()
	base := newMemoryFamily()
	source := New(TypeState)
	for i := byte(0); i < 32; i++ {
		key := [32]byte{i << 3, i, 1}
		if err := source.Put(key, bytes.Repeat([]byte{i}, 12)); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}
	root, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if err := flushToFamily(source, base); err != nil {
		t.Fatalf("flushToFamily: %v", err)
	}
	lazy, err := NewFromRootHash(TypeState, root, base)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}
	return lazy, base
}

func TestWireContextPreCanceledDoesNoWork(t *testing.T) {
	sm, base := newWireContextMap(t)
	family := &wireFetchFamily{memoryFamily: base}
	sm.SetFamily(family)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if nodes, err := sm.GetNodeFatByPathContext(ctx, [32]byte{}, 0, 2, true); !errors.Is(err, context.Canceled) || nodes != nil {
		t.Fatalf("GetNodeFatByPathContext = nodes=%v err=%v, want cancellation and no result", nodes, err)
	}
	if got := family.fetches.Load(); got != 0 {
		t.Fatalf("pre-canceled traversal performed %d backing reads", got)
	}
	if data, err := sm.SerializeRootContext(ctx); !errors.Is(err, context.Canceled) || data != nil {
		t.Fatalf("SerializeRootContext = data=%v err=%v, want cancellation and no result", data, err)
	}
}

func TestWireContextColdFetchCancellationDiscardsResult(t *testing.T) {
	sm, base := newWireContextMap(t)
	family := &wireFetchFamily{
		memoryFamily: base,
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		block:        true,
		honorContext: true,
	}
	sm.SetFamily(family)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := sm.GetNodeFatByPathContext(ctx, [32]byte{}, 0, 2, true)
		result <- err
	}()

	select {
	case <-family.started:
	case <-time.After(2 * time.Second):
		t.Fatal("cold wire fetch did not start")
	}
	cancel()
	close(family.release)

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("GetNodeFatByPathContext error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GetNodeFatByPathContext did not return after cancellation")
	}
	if got := family.fetches.Load(); got != 1 {
		t.Fatalf("canceled cold traversal performed %d backing reads, want 1", got)
	}
}

func TestWireContextNonCancellableFetchStopsBeforeNextRead(t *testing.T) {
	sm, base := newWireContextMap(t)
	family := &wireFetchFamily{
		memoryFamily:  base,
		started:       make(chan struct{}),
		release:       make(chan struct{}),
		block:         true,
		ignoreContext: true,
	}
	sm.SetFamily(family)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := sm.GetNodeFatByPathContext(ctx, [32]byte{}, 0, 2, true)
		result <- err
	}()

	select {
	case <-family.started:
	case <-time.After(2 * time.Second):
		t.Fatal("non-cancellable wire fetch did not start")
	}
	cancel()
	close(family.release)

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("GetNodeFatByPathContext error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GetNodeFatByPathContext did not return after non-cancellable fetch")
	}
	if got := family.fetches.Load(); got != 1 {
		t.Fatalf("canceled traversal performed %d backing reads after fetch returned, want 1", got)
	}
}

type cancelAfterCacheHitContext struct {
	cache    *CleanResidency
	baseline uint64
}

func (c cancelAfterCacheHitContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c cancelAfterCacheHitContext) Done() <-chan struct{}       { return nil }
func (c cancelAfterCacheHitContext) Value(any) any               { return nil }
func (c cancelAfterCacheHitContext) Err() error {
	if c.cache.Stats().Hits > c.baseline {
		return context.Canceled
	}
	return nil
}

func TestWireContextCacheHitCancellationDiscardsResult(t *testing.T) {
	base := newMemoryFamily()
	cache := NewCleanResidency(1<<20, nil)
	t.Cleanup(cache.Close)
	family := &cleanResidencyFamily{memoryFamily: base, cache: cache}
	source := New(TypeState)
	for i := byte(0); i < 32; i++ {
		key := [32]byte{i << 3, i, 2}
		if err := source.Put(key, bytes.Repeat([]byte{i}, 12)); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}
	root, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if err := flushToFamily(source, base); err != nil {
		t.Fatalf("flushToFamily: %v", err)
	}
	sm, err := NewFromRootHash(TypeState, root, family)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}
	if _, err := sm.GetNodeFatByPath([32]byte{}, 0, 2, true); err != nil {
		t.Fatalf("warm GetNodeFatByPath: %v", err)
	}
	baselineHits := cache.Stats().Hits
	ctx := cancelAfterCacheHitContext{cache: cache, baseline: baselineHits}
	nodes, err := sm.GetNodeFatByPathContext(ctx, [32]byte{}, 0, 2, true)
	if !errors.Is(err, context.Canceled) || nodes != nil {
		t.Fatalf("cache-hit traversal = nodes=%v err=%v, want cancellation and no result", nodes, err)
	}
	if got := cache.Stats().Hits; got <= baselineHits {
		t.Fatalf("canceled traversal did not perform a cache hit: before=%d after=%d", baselineHits, got)
	}
}

func TestWireContextPreservesWireAndHash(t *testing.T) {
	sm := New(TypeState)
	for i := byte(0); i < 8; i++ {
		key := [32]byte{i << 4, i, 3}
		if err := sm.Put(key, bytes.Repeat([]byte{i}, 12)); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}
	wantHash, err := sm.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	wantRoot, err := sm.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	gotRoot, err := sm.SerializeRootContext(context.Background())
	if err != nil {
		t.Fatalf("SerializeRootContext: %v", err)
	}
	if !bytes.Equal(gotRoot, wantRoot) {
		t.Fatalf("SerializeRootContext changed wire bytes")
	}
	wantNodes, err := sm.GetNodeFatByPath([32]byte{}, 0, 2, true)
	if err != nil {
		t.Fatalf("GetNodeFatByPath: %v", err)
	}
	gotNodes, err := sm.GetNodeFatByPathContext(context.Background(), [32]byte{}, 0, 2, true)
	if err != nil {
		t.Fatalf("GetNodeFatByPathContext: %v", err)
	}
	if len(gotNodes) != len(wantNodes) {
		t.Fatalf("wire node count changed: got=%d want=%d", len(gotNodes), len(wantNodes))
	}
	for i := range wantNodes {
		if !bytes.Equal(gotNodes[i].NodeID, wantNodes[i].NodeID) || !bytes.Equal(gotNodes[i].Data, wantNodes[i].Data) {
			t.Fatalf("wire node %d changed", i)
		}
	}
	gotHash, err := sm.Hash()
	if err != nil {
		t.Fatalf("Hash after wire traversal: %v", err)
	}
	if gotHash != wantHash {
		t.Fatalf("wire traversal changed hash: got=%x want=%x", gotHash, wantHash)
	}
}

func TestSnapshotContextPreCanceledSkipsPreparation(t *testing.T) {
	family := &snapshotStoreFamily{
		memoryFamily: newMemoryFamily(),
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := sm.Put([32]byte{0x11}, bytes.Repeat([]byte{1}, 12)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := sm.SnapshotImmutableContext(ctx)
	if snapshot != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("SnapshotImmutableContext = snapshot=%v err=%v, want cancellation and no snapshot", snapshot, err)
	}
	if got := family.calls.Load(); got != 0 {
		t.Fatalf("pre-canceled snapshot invoked StoreBatch %d times", got)
	}
	if !sm.tree.root.IsDirty() {
		t.Fatal("pre-canceled snapshot cleared dirty state")
	}
}

func TestSnapshotContextNonCancellableStoreStopsAfterReturn(t *testing.T) {
	family := &snapshotStoreFamily{
		memoryFamily: newMemoryFamily(),
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := sm.Put([32]byte{0x22}, bytes.Repeat([]byte{2}, 12)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := sm.SnapshotImmutableContext(ctx)
		result <- err
	}()
	select {
	case <-family.started:
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot StoreBatch did not start")
	}
	cancel()
	close(family.release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SnapshotImmutableContext error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SnapshotImmutableContext did not return after StoreBatch")
	}
	if got := family.calls.Load(); got != 1 {
		t.Fatalf("canceled snapshot invoked StoreBatch %d times, want 1", got)
	}
	if !sm.tree.root.IsDirty() {
		t.Fatal("canceled snapshot cleared dirty state after StoreBatch returned")
	}
}
