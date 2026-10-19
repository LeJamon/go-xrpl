package shamap

import (
	"bytes"
	"context"
	"errors"
	"slices"
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
	context.Context
	cancel   context.CancelFunc
	cache    *CleanResidency
	baseline uint64
}

func (c cancelAfterCacheHitContext) Err() error {
	if c.cache.Stats().Hits > c.baseline {
		c.cancel()
	}
	return c.Context.Err()
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
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := cancelAfterCacheHitContext{Context: parent, cancel: cancel, cache: cache, baseline: baselineHits}
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
	wantNodes, err := sm.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	wantRoot := wantNodes[0].Data
	gotRoot, err := sm.SerializeRootContext(context.Background())
	if err != nil {
		t.Fatalf("SerializeRootContext: %v", err)
	}
	if !bytes.Equal(gotRoot, wantRoot) {
		t.Fatalf("SerializeRootContext changed wire bytes")
	}
	gotNodes, err := sm.GetNodeFatByPathContext(context.Background(), [32]byte{}, 0, 2, true)
	if err != nil {
		t.Fatalf("GetNodeFatByPathContext: %v", err)
	}
	if len(gotNodes) != len(wantNodes) {
		t.Fatalf("wire node count changed: got=%d want=%d", len(gotNodes), len(wantNodes))
	}
	if !bytes.Equal(gotNodes[0].Data, wantRoot) {
		t.Fatal("wire traversal did not emit the root first")
	}
	// The full walker and fat-node serving emit siblings in different orders.
	byID := func(a, b WireNode) int { return bytes.Compare(a.NodeID, b.NodeID) }
	slices.SortFunc(gotNodes, byID)
	slices.SortFunc(wantNodes, byID)
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

type cancelOnSerializeNode struct {
	mapNode
	cancel  context.CancelFunc
	onClean context.CancelFunc
	calls   int
}

func (n *cancelOnSerializeNode) SerializeWithPrefix() ([]byte, error) {
	n.calls++
	data, err := n.mapNode.SerializeWithPrefix()
	if n.cancel != nil {
		n.cancel()
	}
	return data, err
}

func (n *cancelOnSerializeNode) SetDirty(dirty bool) {
	n.mapNode.SetDirty(dirty)
	if !dirty && n.onClean != nil {
		n.onClean()
	}
}

func TestSnapshotContextStopsBetweenDirtyNodes(t *testing.T) {
	for _, boundary := range []string{"serialization", "cleanup"} {
		t.Run(boundary, func(t *testing.T) {
			family := &snapshotStoreFamily{
				memoryFamily: newMemoryFamily(),
				started:      make(chan struct{}),
				release:      make(chan struct{}),
			}
			close(family.release)
			sm, err := NewBacked(TypeState, family)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var leaves [2]*cancelOnSerializeNode
			for i := range leaves {
				leaf, err := newAccountStateLeafNode(NewItem([32]byte{byte(i) << 4}, bytes.Repeat([]byte{byte(i)}, 12)))
				if err != nil {
					t.Fatal(err)
				}
				leaves[i] = &cancelOnSerializeNode{mapNode: leaf}
				if err := sm.tree.root.SetChild(i, leaves[i]); err != nil {
					t.Fatal(err)
				}
			}
			wantSecond, wantStores := 0, int32(0)
			if boundary == "serialization" {
				leaves[0].cancel = cancel
			} else {
				leaves[0].onClean = cancel
				wantSecond, wantStores = 1, 1
			}
			snapshot, err := sm.SnapshotImmutableContext(ctx)
			if snapshot != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("snapshot = %v, error = %v; want cancellation without a snapshot", snapshot, err)
			}
			if leaves[0].calls != 1 || leaves[1].calls != wantSecond || family.calls.Load() != wantStores {
				t.Fatalf("work continued after cancellation: serialized=%d,%d stored=%d", leaves[0].calls, leaves[1].calls, family.calls.Load())
			}
			if !sm.tree.root.IsDirty() || !leaves[1].IsDirty() {
				t.Fatal("canceled preparation cleared remaining dirty state")
			}
			if leaves[0].IsDirty() != (boundary == "serialization") {
				t.Fatal("first leaf dirty state does not match the persistence boundary")
			}
			leaves[0].cancel, leaves[0].onClean = nil, nil
			if _, err := sm.SnapshotImmutableContext(t.Context()); err != nil {
				t.Fatalf("snapshot retry: %v", err)
			}
			if family.calls.Load() != wantStores+1 || leaves[1].calls != wantSecond+1 || sm.tree.root.IsDirty() {
				t.Fatal("snapshot retry did not persist the remaining dirty tree")
			}
		})
	}
}
