package shamap

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
)

type countingDurableFamily struct {
	base  *memoryFamily
	mu    sync.Mutex
	reads map[[32]byte]int
}

type excludingHashFilter struct {
	hash [32]byte
}

func (f excludingHashFilter) ShouldFetch(hash [32]byte) bool {
	return hash != f.hash
}

type oneShotMissingFamily struct {
	base        *memoryFamily
	target      [32]byte
	cancelFirst bool
	started     chan struct{}
	mu          sync.Mutex
	failed      bool
}

type cancelOnceBaseFamily struct {
	base    *memoryFamily
	target  [32]byte
	entered chan struct{}
	once    sync.Once
}

func (f *cancelOnceBaseFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.base.Fetch(ctx, hash)
}

func (f *cancelOnceBaseFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	blocked := false
	if hash == f.target {
		f.once.Do(func() {
			blocked = true
			close(f.entered)
		})
	}
	if blocked {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.base.FetchDurable(ctx, hash)
}

func (f *cancelOnceBaseFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func (f *cancelOnceBaseFamily) FullBelowCache() *FullBelowCache {
	return f.base.FullBelowCache()
}

func (f *oneShotMissingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash == f.target {
		return nil, nil
	}
	return f.base.Fetch(ctx, hash)
}

func (f *oneShotMissingFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash == f.target {
		f.mu.Lock()
		if f.failed {
			f.mu.Unlock()
			return nil, nil
		}
		f.failed = true
		cancelFirst := f.cancelFirst
		started := f.started
		f.mu.Unlock()
		if cancelFirst {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, errors.New("one-shot durable fetch failure")
	}
	return f.base.FetchDurable(ctx, hash)
}

func (f *oneShotMissingFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func (f *oneShotMissingFamily) FullBelowCache() *FullBelowCache {
	return f.base.FullBelowCache()
}

func newInterruptedBackedWalk(t *testing.T, cancelFirst bool) (*SHAMap, *oneShotMissingFamily, [32]byte) {
	t.Helper()
	source := buildRandomState(t, 1)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}

	var target [32]byte
	for branch := range BranchFactor {
		_, hash, set := source.tree.root.LoadChild(branch)
		if set {
			target = hash
			break
		}
	}
	if target == ([32]byte{}) {
		t.Fatal("source root has no child")
	}

	base := newMemoryFamily()
	if err := base.StoreBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	family := &oneShotMissingFamily{
		base: base, target: target, cancelFirst: cancelFirst, started: make(chan struct{}),
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	return dest, family, target
}

func TestBackedWalkRetryDoesNotConsumeInvalidDepthBranch(t *testing.T) {
	bottom := newInnerNode()
	bottom.hashes[0] = [32]byte{1}
	bottom.isBranch = 1
	if err := bottom.UpdateHash(); err != nil {
		t.Fatal(err)
	}

	chain := make([]*innerNode, 0, 1+maxDepth)
	chain = append(chain, bottom)
	child := bottom
	for range maxDepth {
		parent := newInnerNode()
		if err := parent.SetChild(0, child); err != nil {
			t.Fatal(err)
		}
		if err := parent.UpdateHash(); err != nil {
			t.Fatal(err)
		}
		chain = append(chain, parent)
		child = parent
	}

	root := chain[len(chain)-1]
	rootHash := root.Hash()
	rootData, err := root.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]FlushEntry, 0, len(chain)-1)
	for _, node := range chain[:len(chain)-1] {
		data, err := node.SerializeWithPrefix()
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, FlushEntry{Hash: node.Hash(), Data: data, MapType: TypeState})
	}

	family := newMemoryFamily()
	if err := family.StoreBatch(t.Context(), entries); err != nil {
		t.Fatal(err)
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	for attempt := range 2 {
		_, err := dest.GetMissingNodesContext(t.Context(), 1, nil)
		if !errors.Is(err, errMaxDepthExceeded) {
			t.Fatalf("attempt %d: got %v, want %v", attempt+1, err, errMaxDepthExceeded)
		}
	}
	gen := family.FullBelowCache().Generation()
	if dest.tree.root.isFullBelow(gen) || family.FullBelowCache().Has(gen, rootHash) {
		t.Fatal("invalid-depth tree was cached as complete")
	}
}

func (f *countingDurableFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.base.Fetch(ctx, hash)
}

func (f *countingDurableFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.mu.Lock()
	f.reads[hash]++
	f.mu.Unlock()
	return f.base.FetchDurable(ctx, hash)
}

func (f *countingDurableFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func (f *countingDurableFamily) FullBelowCache() *FullBelowCache {
	return f.base.FullBelowCache()
}

func (f *countingDurableFamily) count(hash [32]byte) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[hash]
}

func (f *countingDurableFamily) snapshotReads() map[[32]byte]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	reads := make(map[[32]byte]int, len(f.reads))
	for hash, count := range f.reads {
		reads[hash] = count
	}
	return reads
}

func TestBackedWalkVerifiedBaseMatchesStrictFrontierWithFewerReads(t *testing.T) {
	base := buildRandomState(t, 2000)
	pivot, err := base.SnapshotMutable()
	if err != nil {
		t.Fatal(err)
	}
	var changedKey [32]byte
	for i := range changedKey {
		changedKey[i] = 0xff
	}
	if err := pivot.Put(changedKey, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	baseRoot, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	pivotRoot, err := pivot.Hash()
	if err != nil {
		t.Fatal(err)
	}
	pivotRootData, err := pivot.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	baseEntries, err := collectDirtyForTest(base)
	if err != nil {
		t.Fatal(err)
	}
	pivotEntries, err := collectDirtyForTest(pivot)
	if err != nil {
		t.Fatal(err)
	}

	newDestination := func(useBase bool) (*SHAMap, *countingDurableFamily) {
		t.Helper()
		memory := newMemoryFamily()
		if err := memory.StoreBatch(t.Context(), baseEntries); err != nil {
			t.Fatal(err)
		}
		family := &countingDurableFamily{base: memory, reads: make(map[[32]byte]int)}
		dest, err := NewBacked(TypeState, family)
		if err != nil {
			t.Fatal(err)
		}
		if err := dest.AddRootNode(pivotRoot, pivotRootData); err != nil {
			t.Fatal(err)
		}
		if useBase {
			if err := dest.SetVerifiedBaseContext(t.Context(), baseRoot); err != nil {
				t.Fatal(err)
			}
		}
		return dest, family
	}

	strict, strictFamily := newDestination(false)
	strictMissing, err := strict.GetMissingNodesContext(t.Context(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	optimized, optimizedFamily := newDestination(true)
	optimizedMissing, err := optimized.GetMissingNodesContext(t.Context(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	strictFrontier := make(map[[32]byte]NodeID, len(strictMissing))
	for _, missing := range strictMissing {
		strictFrontier[missing.Hash] = missing.NodeID
	}
	if len(optimizedMissing) != len(strictFrontier) {
		t.Fatalf("optimized frontier has %d nodes, strict has %d", len(optimizedMissing), len(strictFrontier))
	}
	for _, missing := range optimizedMissing {
		if nodeID, ok := strictFrontier[missing.Hash]; !ok || nodeID != missing.NodeID {
			t.Fatalf("optimized frontier contains unexpected node %x at %x", missing.Hash[:8], missing.NodeID.Bytes())
		}
	}
	countReads := func(reads map[[32]byte]int) int {
		total := 0
		for _, count := range reads {
			total += count
		}
		return total
	}
	strictReads := countReads(strictFamily.snapshotReads())
	optimizedReads := countReads(optimizedFamily.snapshotReads())
	if optimizedReads >= strictReads {
		t.Fatalf("optimized durable reads = %d, strict = %d", optimizedReads, strictReads)
	}
	stats := optimized.BackedWalkStats()
	if stats.EqualSubtreesSkipped == 0 || stats.NodesDescended == 0 || stats.MissingNodes != uint64(len(optimizedMissing)) {
		t.Fatalf("unexpected backed-walk stats: %+v", stats)
	}
	corruptHash := optimizedMissing[0].Hash
	optimizedFamily.base.mu.Lock()
	optimizedFamily.base.store[corruptHash] = []byte("corrupt")
	optimizedFamily.base.mu.Unlock()
	if _, err := optimized.GetMissingNodesContext(t.Context(), 0, nil); !errors.Is(err, ErrInvalidNodeData) {
		t.Fatalf("corrupt pivot node error = %v", err)
	}
	if fallbacks := optimized.BackedWalkStats().VerifiedBaseFallbacks; fallbacks != 0 {
		t.Fatalf("pivot corruption disabled verified base: fallbacks = %d", fallbacks)
	}
	if err := optimizedFamily.base.StoreBatch(t.Context(), pivotEntries); err != nil {
		t.Fatal(err)
	}
	missing, err := optimized.GetMissingNodesContext(t.Context(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("completed pivot still reports %d missing nodes", len(missing))
	}
	if err := optimized.FinishSyncContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	gotRoot, err := optimized.Hash()
	if err != nil || gotRoot != pivotRoot {
		t.Fatalf("completed pivot root = %x, err = %v", gotRoot[:8], err)
	}
}

func TestBackedWalkVerifiedBaseIdenticalRootSkipsDescendants(t *testing.T) {
	source := buildRandomState(t, 1000)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	memory := newMemoryFamily()
	if err := memory.StoreBatch(t.Context(), entries); err != nil {
		t.Fatal(err)
	}
	family := &countingDurableFamily{base: memory, reads: make(map[[32]byte]int)}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	if err := dest.SetVerifiedBaseContext(t.Context(), rootHash); err != nil {
		t.Fatal(err)
	}
	missing, err := dest.GetMissingNodesContext(t.Context(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("identical root reported %d missing nodes", len(missing))
	}
	stats := dest.BackedWalkStats()
	if stats.EqualSubtreesSkipped != 1 || stats.NodesDescended != 0 || stats.DurableReads != 1 {
		t.Fatalf("unexpected identical-root stats: %+v", stats)
	}
}

func TestBackedWalkVerifiedBaseFailureFallsBackToStrictWalk(t *testing.T) {
	base := buildRandomState(t, 1000)
	pivot, err := base.SnapshotMutable()
	if err != nil {
		t.Fatal(err)
	}
	var changedKey [32]byte
	for i := range changedKey {
		changedKey[i] = 0xee
	}
	if err := pivot.Put(changedKey, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}

	baseRoot, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	pivotRoot, err := pivot.Hash()
	if err != nil {
		t.Fatal(err)
	}
	pivotRootData, err := pivot.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	baseEntries, err := collectDirtyForTest(base)
	if err != nil {
		t.Fatal(err)
	}
	pivotEntries, err := collectDirtyForTest(pivot)
	if err != nil {
		t.Fatal(err)
	}
	branch := selectBranchForPath(changedKey, 0)
	_, damagedHash, set := base.tree.root.LoadChild(branch)
	if !set || damagedHash == ([32]byte{}) {
		t.Fatal("changed branch is absent from verified base")
	}

	for _, test := range []struct {
		name   string
		damage func(*memoryFamily, [32]byte)
	}{
		{
			name: "missing",
			damage: func(family *memoryFamily, hash [32]byte) {
				delete(family.store, hash)
			},
		},
		{
			name: "corrupt",
			damage: func(family *memoryFamily, hash [32]byte) {
				family.store[hash] = []byte("corrupt")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			family := newMemoryFamily()
			if err := family.StoreBatch(t.Context(), append(baseEntries, pivotEntries...)); err != nil {
				t.Fatal(err)
			}
			dest, err := NewBacked(TypeState, family)
			if err != nil {
				t.Fatal(err)
			}
			if err := dest.AddRootNode(pivotRoot, pivotRootData); err != nil {
				t.Fatal(err)
			}
			if err := dest.SetVerifiedBaseContext(t.Context(), baseRoot); err != nil {
				t.Fatal(err)
			}
			family.mu.Lock()
			test.damage(family, damagedHash)
			family.mu.Unlock()

			missing, err := dest.GetMissingNodesContext(t.Context(), 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(missing) != 0 {
				t.Fatalf("strict fallback reported %d missing pivot nodes", len(missing))
			}
			stats := dest.BackedWalkStats()
			if stats.VerifiedBaseFallbacks != 1 {
				t.Fatalf("verified-base fallbacks = %d, want 1", stats.VerifiedBaseFallbacks)
			}
		})
	}
}

func TestBackedWalkVerifiedBaseCancellationResumes(t *testing.T) {
	base := buildRandomState(t, 1000)
	pivot, err := base.SnapshotMutable()
	if err != nil {
		t.Fatal(err)
	}
	var changedKey [32]byte
	for i := range changedKey {
		changedKey[i] = 0xdd
	}
	if err := pivot.Put(changedKey, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	baseRoot, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	pivotRoot, err := pivot.Hash()
	if err != nil {
		t.Fatal(err)
	}
	pivotRootData, err := pivot.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	baseEntries, err := collectDirtyForTest(base)
	if err != nil {
		t.Fatal(err)
	}
	pivotEntries, err := collectDirtyForTest(pivot)
	if err != nil {
		t.Fatal(err)
	}
	branch := selectBranchForPath(changedKey, 0)
	_, target, set := base.tree.root.LoadChild(branch)
	if !set {
		t.Fatal("changed branch is absent from verified base")
	}
	memory := newMemoryFamily()
	if err := memory.StoreBatch(t.Context(), append(baseEntries, pivotEntries...)); err != nil {
		t.Fatal(err)
	}
	family := &cancelOnceBaseFamily{base: memory, target: target, entered: make(chan struct{})}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(pivotRoot, pivotRootData); err != nil {
		t.Fatal(err)
	}
	if err := dest.SetVerifiedBaseContext(t.Context(), baseRoot); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, walkErr := dest.GetMissingNodesContext(ctx, 0, nil)
		done <- walkErr
	}()
	<-family.entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled walk error = %v", err)
	}
	missing, err := dest.GetMissingNodesContext(t.Context(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("resumed walk reported %d missing nodes", len(missing))
	}
	if fallbacks := dest.BackedWalkStats().VerifiedBaseFallbacks; fallbacks != 0 {
		t.Fatalf("cancellation disabled the verified base: fallbacks = %d", fallbacks)
	}
}

func BenchmarkBackedWalkVerifiedBaseSparseChange(b *testing.B) {
	base := buildRandomState(b, 10_000)
	pivot, err := base.SnapshotMutable()
	if err != nil {
		b.Fatal(err)
	}
	var changedKey [32]byte
	for i := range changedKey {
		changedKey[i] = 0xcc
	}
	if err := pivot.Put(changedKey, make([]byte, 12)); err != nil {
		b.Fatal(err)
	}
	baseRoot, err := base.Hash()
	if err != nil {
		b.Fatal(err)
	}
	pivotRoot, err := pivot.Hash()
	if err != nil {
		b.Fatal(err)
	}
	pivotRootData, err := pivot.SerializeRoot()
	if err != nil {
		b.Fatal(err)
	}
	baseEntries, err := collectDirtyForTest(base)
	if err != nil {
		b.Fatal(err)
	}

	for _, benchmark := range []struct {
		name    string
		useBase bool
	}{
		{name: "strict"},
		{name: "verified-base", useBase: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			var reads, skipped uint64
			for range b.N {
				b.StopTimer()
				memory := newMemoryFamily()
				if err := memory.StoreBatch(b.Context(), baseEntries); err != nil {
					b.Fatal(err)
				}
				family := &countingDurableFamily{base: memory, reads: make(map[[32]byte]int)}
				dest, err := NewBacked(TypeState, family)
				if err != nil {
					b.Fatal(err)
				}
				if err := dest.AddRootNode(pivotRoot, pivotRootData); err != nil {
					b.Fatal(err)
				}
				if benchmark.useBase {
					if err := dest.SetVerifiedBaseContext(b.Context(), baseRoot); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				missing, err := dest.GetMissingNodesContext(b.Context(), 0, nil)
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				if len(missing) == 0 {
					b.Fatal("sparse pivot change produced no missing frontier")
				}
				for _, count := range family.snapshotReads() {
					reads += uint64(count)
				}
				skipped += dest.BackedWalkStats().EqualSubtreesSkipped
			}
			b.ReportMetric(float64(reads)/float64(b.N), "durable_reads/op")
			b.ReportMetric(float64(skipped)/float64(b.N), "equal_subtrees/op")
		})
	}
}

func (f *countingDurableFamily) totalReads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, count := range f.reads {
		total += count
	}
	return total
}

func TestBackedWalkResumesWithoutRereadingCompletedNodes(t *testing.T) {
	source := buildRandomState(t, 512)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}

	var withheld []FlushEntry
	stored := make([]FlushEntry, 0, len(batch))
	for _, entry := range batch {
		if len(withheld) < 2 && len(entry.Data) >= 4 && bytes.Equal(entry.Data[:4], protocol.HashPrefixLeafNode().Bytes()) {
			withheld = append(withheld, entry)
			continue
		}
		stored = append(stored, entry)
	}
	if len(withheld) != 2 {
		t.Fatalf("found %d leaf entries to withhold, want 2", len(withheld))
	}

	base := newMemoryFamily()
	if err := base.StoreBatch(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	first := dest.GetMissingNodes(16, nil)
	if len(first) != len(withheld) {
		t.Fatalf("first frontier size = %d, want %d", len(first), len(withheld))
	}
	var completedHash [32]byte
	gen := family.FullBelowCache().Generation()
	for _, entry := range stored {
		if entry.Hash != rootHash && family.count(entry.Hash) > 0 && family.FullBelowCache().Has(gen, entry.Hash) {
			completedHash = entry.Hash
			break
		}
	}
	if completedHash == ([32]byte{}) {
		t.Fatal("first traversal did not read a durable node")
	}
	completedReads := family.count(completedHash)

	var firstEntry FlushEntry
	for _, entry := range withheld {
		if entry.Hash == first[0].Hash {
			firstEntry = entry
			break
		}
	}
	if firstEntry.Hash == ([32]byte{}) {
		t.Fatalf("first missing hash %x was not withheld", first[0].Hash[:8])
	}
	if err := base.StoreBatch(context.Background(), []FlushEntry{firstEntry}); err != nil {
		t.Fatal(err)
	}

	second := dest.GetMissingNodes(1, nil)
	if len(second) != 1 || second[0].Hash == first[0].Hash {
		t.Fatalf("second frontier = %v, want the other withheld node", second)
	}
	if got := family.count(completedHash); got != completedReads {
		t.Fatalf("completed durable node was reread: before=%d after=%d", completedReads, got)
	}

	var secondEntry FlushEntry
	for _, entry := range withheld {
		if entry.Hash == second[0].Hash {
			secondEntry = entry
		}
	}
	if err := base.StoreBatch(context.Background(), []FlushEntry{secondEntry}); err != nil {
		t.Fatal(err)
	}
	if err := dest.FinishSync(); err != nil {
		t.Fatalf("FinishSync: %v", err)
	}
}

func TestBackedWalkResumesAfterTraversalBudget(t *testing.T) {
	source := buildRandomState(t, 512)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}

	base := newMemoryFamily()
	if err := base.StoreBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	_, err = dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 1), 1, nil)
	if !errors.Is(err, ErrTraversalBudget) {
		t.Fatalf("first traversal error = %v, want %v", err, ErrTraversalBudget)
	}
	if got := family.totalReads(); got != 1 {
		t.Fatalf("first traversal durable reads = %d, want 1", got)
	}

	const budget = 32
	for pass := 0; pass < len(batch); pass++ {
		readsBefore := family.totalReads()
		missing, walkErr := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), budget), 1, nil)
		if got := family.totalReads() - readsBefore; got > budget {
			t.Fatalf("pass %d durable reads = %d, exceeds budget %d", pass, got, budget)
		}
		if errors.Is(walkErr, ErrTraversalBudget) {
			continue
		}
		if walkErr != nil {
			t.Fatalf("pass %d: %v", pass, walkErr)
		}
		if len(missing) != 0 {
			t.Fatalf("pass %d returned %d missing nodes from a complete store", pass, len(missing))
		}
		if err := dest.FinishSync(); err != nil {
			t.Fatalf("FinishSync: %v", err)
		}
		return
	}
	t.Fatal("budgeted traversal did not complete")
}

func TestBackedWalkBudgetsFinalRootProof(t *testing.T) {
	source := New(TypeState)
	var key [32]byte
	key[0] = 1
	if err := source.Put(key, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}

	base := newMemoryFamily()
	if err := base.StoreBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	_, err = dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 1), 1, nil)
	if !errors.Is(err, ErrTraversalBudget) {
		t.Fatalf("child traversal error = %v, want %v before root proof", err, ErrTraversalBudget)
	}
	if got := family.totalReads(); got != 1 {
		t.Fatalf("child traversal durable reads = %d, want 1", got)
	}

	missing, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 1), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("complete store reported missing nodes: %v", missing)
	}
	if got := family.totalReads(); got != 2 {
		t.Fatalf("completed traversal durable reads = %d, want 2", got)
	}
}

func TestBackedWalkRetainsExactPositionAcrossCappedPasses(t *testing.T) {
	source := buildRandomState(t, 512)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}

	var withheld []FlushEntry
	stored := make([]FlushEntry, 0, len(batch))
	for _, entry := range batch {
		if len(withheld) < 2 && len(entry.Data) >= 4 && bytes.Equal(entry.Data[:4], protocol.HashPrefixLeafNode().Bytes()) {
			withheld = append(withheld, entry)
			continue
		}
		stored = append(stored, entry)
	}
	if len(withheld) != 2 {
		t.Fatalf("found %d leaf entries to withhold, want 2", len(withheld))
	}

	now := time.Unix(1_000, 0)
	base := newMemoryFamily()
	base.fullBelow = newFullBelowCacheWithClock(1, time.Second, func() time.Time { return now })
	if err := base.StoreBatch(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	first := dest.GetMissingNodes(1, nil)
	if len(first) != 1 {
		t.Fatalf("first frontier size = %d, want 1", len(first))
	}
	before := family.snapshotReads()
	now = now.Add(2 * time.Second)
	base.fullBelow.Sweep()

	second := dest.GetMissingNodes(1, excludingHashFilter{hash: first[0].Hash})
	if len(second) != 1 || second[0].Hash == first[0].Hash {
		t.Fatalf("second frontier = %v, want the other withheld node", second)
	}
	for hash, count := range before {
		if hash == first[0].Hash {
			continue
		}
		if got := family.count(hash); got != count {
			t.Fatalf("durable node %x was reread after capped traversal: before=%d after=%d", hash[:8], count, got)
		}
	}
}

func TestBackedWalkCachesShallowProofsAcrossLedgerRoots(t *testing.T) {
	build := func(changed bool) (*SHAMap, [32]byte, []byte, []FlushEntry) {
		sm := New(TypeState)
		for i := range 4096 {
			var key [32]byte
			prefix := uint16(i%1024) * 40503
			binary.BigEndian.PutUint16(key[:2], prefix)
			binary.BigEndian.PutUint16(key[2:4], uint16(i/1024+1))
			data := make([]byte, 12)
			binary.BigEndian.PutUint32(data, uint32(i))
			if changed && i == 0 {
				data[4] = 1
			}
			if err := sm.Put(key, data); err != nil {
				t.Fatal(err)
			}
		}
		hash, err := sm.Hash()
		if err != nil {
			t.Fatal(err)
		}
		root, err := sm.SerializeRoot()
		if err != nil {
			t.Fatal(err)
		}
		batch, err := collectDirtyForTest(sm)
		if err != nil {
			t.Fatal(err)
		}
		return sm, hash, root, batch
	}

	firstSource, firstHash, firstRoot, firstBatch := build(false)
	_, secondHash, secondRoot, secondBatch := build(true)
	base := newMemoryFamily()
	if err := base.StoreBatch(context.Background(), firstBatch); err != nil {
		t.Fatal(err)
	}
	if err := base.StoreBatch(context.Background(), secondBatch); err != nil {
		t.Fatal(err)
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}

	first, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AddRootNode(firstHash, firstRoot); err != nil {
		t.Fatal(err)
	}
	if missing := first.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("first root reported missing nodes: %v", missing)
	}
	firstReads := family.totalReads()
	if firstReads == 0 {
		t.Fatal("first root performed no durable reads")
	}

	var shallowHash, deepHash [32]byte
	var findProofs func(*innerNode, int)
	findProofs = func(node *innerNode, depth int) {
		if shallowHash != ([32]byte{}) && deepHash != ([32]byte{}) {
			return
		}
		for branch := range BranchFactor {
			child, hash, set := node.LoadChild(branch)
			if !set {
				continue
			}
			inner, ok := child.(*innerNode)
			if !ok {
				continue
			}
			childDepth := depth + 1
			if childDepth == 4 && shallowHash == ([32]byte{}) {
				shallowHash = hash
			}
			if childDepth > 4 {
				deepHash = hash
				if shallowHash != ([32]byte{}) {
					return
				}
			}
			findProofs(inner, childDepth)
		}
	}
	findProofs(firstSource.tree.root, 0)
	if shallowHash == ([32]byte{}) || deepHash == ([32]byte{}) {
		t.Fatal("test tree does not contain both shallow and deep inner nodes")
	}
	gen := base.fullBelow.Generation()
	if !base.fullBelow.Has(gen, shallowHash) {
		t.Fatal("depth-four complete subtree was not cached")
	}
	if base.fullBelow.Has(gen, deepHash) {
		t.Fatal("complete subtree deeper than depth four was cached")
	}

	second, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.AddRootNode(secondHash, secondRoot); err != nil {
		t.Fatal(err)
	}
	if missing := second.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("second root reported missing nodes: %v", missing)
	}
	secondReads := family.totalReads() - firstReads
	if secondReads >= firstReads/4 {
		t.Fatalf("shared-root traversal used %d reads after %d cold reads", secondReads, firstReads)
	}
}

func TestBackedWalkProofCacheSurvivesSweeps(t *testing.T) {
	build := func(changed bool) (*SHAMap, [32]byte, []byte, []FlushEntry) {
		sm := New(TypeState)
		for i := range 4096 {
			var key [32]byte
			prefix := uint16(i%1024) * 40503
			binary.BigEndian.PutUint16(key[:2], prefix)
			binary.BigEndian.PutUint16(key[2:4], uint16(i/1024+1))
			data := make([]byte, 12)
			binary.BigEndian.PutUint32(data, uint32(i))
			if changed && i == 0 {
				data[4] = 1
			}
			if err := sm.Put(key, data); err != nil {
				t.Fatal(err)
			}
		}
		hash, err := sm.Hash()
		if err != nil {
			t.Fatal(err)
		}
		root, err := sm.SerializeRoot()
		if err != nil {
			t.Fatal(err)
		}
		batch, err := collectDirtyForTest(sm)
		if err != nil {
			t.Fatal(err)
		}
		return sm, hash, root, batch
	}

	_, firstHash, firstRoot, firstBatch := build(false)
	_, secondHash, secondRoot, secondBatch := build(true)
	now := time.Unix(4_000, 0)
	base := newMemoryFamily()
	base.fullBelow = newFullBelowCacheWithClock(fullBelowCacheTarget, 10*time.Minute, func() time.Time { return now })
	if err := base.StoreBatch(t.Context(), firstBatch); err != nil {
		t.Fatal(err)
	}
	if err := base.StoreBatch(t.Context(), secondBatch); err != nil {
		t.Fatal(err)
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}
	walkReads := func(hash [32]byte, root []byte) int {
		t.Helper()
		before := family.totalReads()
		sm, err := NewBacked(TypeState, family)
		if err != nil {
			t.Fatal(err)
		}
		if err := sm.AddRootNode(hash, root); err != nil {
			t.Fatal(err)
		}
		if missing := sm.GetMissingNodes(1, nil); len(missing) != 0 {
			t.Fatalf("root %x reported missing nodes: %v", hash[:8], missing)
		}
		return family.totalReads() - before
	}

	coldReads := walkReads(firstHash, firstRoot)
	if size := base.fullBelow.Size(); size == 0 || size > len(firstBatch)+1 {
		t.Fatalf("proof cache size = %d, want 1..%d", size, len(firstBatch)+1)
	}

	for range 10 {
		now = now.Add(30 * time.Second)
		base.fullBelow.Sweep()
	}
	warmReads := walkReads(secondHash, secondRoot)
	base.fullBelow.Bump()
	uncachedReads := walkReads(secondHash, secondRoot)
	if warmReads*4 >= uncachedReads {
		t.Fatalf("proof cache used %d durable reads versus %d uncached reads (first cold walk %d)", warmReads, uncachedReads, coldReads)
	}
}

func TestBackedWalkRequeuesSubtreeAfterFetchError(t *testing.T) {
	dest, _, target := newInterruptedBackedWalk(t, false)

	if missing, err := dest.GetMissingNodesContext(context.Background(), 1, nil); err == nil {
		t.Fatalf("first walk error = nil, missing = %v", missing)
	}
	missing, err := dest.GetMissingNodesContext(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("second walk: %v", err)
	}
	if len(missing) != 1 || missing[0].Hash != target {
		t.Fatalf("second walk missing = %v, want target %x", missing, target[:8])
	}
	if err := dest.FinishSync(); err == nil {
		t.Fatal("FinishSync succeeded after the failed subtree became missing")
	}
}

func TestBackedWalkRequeuesSubtreeAfterCancellation(t *testing.T) {
	dest, family, target := newInterruptedBackedWalk(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	walkDone := make(chan error, 1)
	go func() {
		_, err := dest.GetMissingNodesContext(ctx, 1, nil)
		walkDone <- err
	}()
	<-family.started
	cancel()
	if err := <-walkDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled walk error = %v, want context.Canceled", err)
	}

	missing, err := dest.GetMissingNodesContext(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("second walk: %v", err)
	}
	if len(missing) != 1 || missing[0].Hash != target {
		t.Fatalf("second walk missing = %v, want target %x", missing, target[:8])
	}
	if err := dest.FinishSync(); err == nil {
		t.Fatal("FinishSync succeeded after the canceled subtree became missing")
	}
}

func TestBackedWalkDoesNotPublishDurableRootAbovePendingDescendant(t *testing.T) {
	source := buildRandomState(t, 256)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}

	ancestorForLeaf := func(entry FlushEntry) ([32]byte, int, bool) {
		node, err := deserializeFromPrefix(entry.Data)
		if err != nil {
			return [32]byte{}, 0, false
		}
		leaf, ok := node.(mapLeaf)
		if !ok {
			return [32]byte{}, 0, false
		}
		key := leaf.Item().Key()
		current := source.tree.root
		var candidate [32]byte
		candidateDepth := 0
		for depth := 1; depth <= maxDepth; depth++ {
			child, hash, set := current.LoadChild(selectBranchForPath(key, depth-1))
			if !set {
				break
			}
			inner, ok := child.(*innerNode)
			if !ok {
				break
			}
			if depth >= 2 {
				candidate = hash
				candidateDepth = depth
			}
			current = inner
		}
		return candidate, candidateDepth, candidate != ([32]byte{})
	}

	var pending FlushEntry
	var pendingAncestor [32]byte
	pendingAncestorDepth := 0
	durable := make([]FlushEntry, 0, len(batch)-1)
	for _, entry := range batch {
		if pending.Hash == ([32]byte{}) && len(entry.Data) >= 4 && bytes.Equal(entry.Data[:4], protocol.HashPrefixLeafNode().Bytes()) {
			if ancestor, depth, ok := ancestorForLeaf(entry); ok {
				pending = entry
				pendingAncestor = ancestor
				pendingAncestorDepth = depth
				continue
			}
		}
		durable = append(durable, entry)
	}
	if pending.Hash == ([32]byte{}) {
		t.Fatal("no leaf available for pending descendant")
	}

	base := newMemoryFamily()
	if err := base.StoreBatch(context.Background(), durable); err != nil {
		t.Fatal(err)
	}
	family := &pendingDurableFamily{
		base:    base,
		pending: map[[32]byte][]byte{pending.Hash: pending.Data},
		cache:   base.FullBelowCache(),
	}

	first, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	if missing := first.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("pending descendant reported missing: %v", missing)
	}
	proofs := 0
	for i := range BranchFactor {
		proofs += first.acquisition.cursor.lanes[i].proofs.count()
	}
	if proofs > BranchFactor {
		t.Fatalf("pending subtree retained %d proofs, want at most one maximal proof per lane", proofs)
	}
	gen := family.cache.Generation()
	if family.cache.Has(gen, rootHash) {
		t.Fatal("durable root above pending descendant was published as full below")
	}
	if family.cache.Has(gen, pendingAncestor) {
		t.Fatalf("depth-%d ancestor above pending descendant was published", pendingAncestorDepth)
	}

	delete(family.pending, pending.Hash)
	retry, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := retry.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	missing := retry.GetMissingNodes(1, nil)
	if len(missing) != 1 || missing[0].Hash != pending.Hash {
		t.Fatalf("retry frontier = %v, want failed pending hash %x", missing, pending.Hash[:8])
	}
	if family.cache.Has(gen, rootHash) {
		t.Fatal("failed pending descendant poisoned the shared root cache")
	}

	if err := base.StoreBatch(context.Background(), []FlushEntry{pending}); err != nil {
		t.Fatal(err)
	}
	if missing := retry.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("durable retry reported missing nodes: %v", missing)
	}
	if family.cache.Has(gen, rootHash) {
		t.Fatal("resolved frontier published the shared root before persistence acknowledgement")
	}
	if err := retry.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !family.cache.Has(gen, rootHash) {
		t.Fatal("persistence acknowledgement did not publish the shared root")
	}
	if retry.acquisition.cursor != nil {
		t.Fatal("persistence acknowledgement retained the completed walk cursor")
	}
	for branch := range BranchFactor {
		child, _, set := retry.tree.root.LoadChild(branch)
		if set && child != nil {
			t.Fatalf("persistence acknowledgement retained root child %d", branch)
		}
	}
}

func TestBackedWalkAcknowledgementIgnoresStaleGeneration(t *testing.T) {
	source := buildRandomState(t, 64)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}

	var pending FlushEntry
	durable := make([]FlushEntry, 0, len(batch)-1)
	for _, entry := range batch {
		if pending.Hash == ([32]byte{}) && len(entry.Data) >= 4 && bytes.Equal(entry.Data[:4], protocol.HashPrefixLeafNode().Bytes()) {
			pending = entry
			continue
		}
		durable = append(durable, entry)
	}
	if pending.Hash == ([32]byte{}) {
		t.Fatal("no leaf available for pending descendant")
	}

	base := newMemoryFamily()
	if err := base.StoreBatch(t.Context(), durable); err != nil {
		t.Fatal(err)
	}
	family := &pendingDurableFamily{
		base:    base,
		pending: map[[32]byte][]byte{pending.Hash: pending.Data},
		cache:   base.FullBelowCache(),
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	if missing := dest.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("pending descendant reported missing: %v", missing)
	}

	family.cache.Bump()
	delete(family.pending, pending.Hash)
	if err := base.StoreBatch(t.Context(), []FlushEntry{pending}); err != nil {
		t.Fatal(err)
	}
	if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	gen := family.cache.Generation()
	if family.cache.Has(gen, rootHash) {
		t.Fatal("stale traversal generation published the shared root")
	}
}

func TestBackedWalkDefersPendingProofUntilAcknowledged(t *testing.T) {
	source := New(TypeState)
	for i := range 64 {
		var key [32]byte
		key[0] = 0xA0 | byte(i>>4)
		key[1] = byte(i)
		data := make([]byte, 12)
		data[0] = byte(i)
		if err := source.Put(key, data); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}

	var pending, missing FlushEntry
	durable := make([]FlushEntry, 0, len(batch)-2)
	for _, entry := range batch {
		if len(entry.Data) >= 4 && bytes.Equal(entry.Data[:4], protocol.HashPrefixLeafNode().Bytes()) {
			if pending.Hash == ([32]byte{}) {
				pending = entry
				continue
			}
			if missing.Hash == ([32]byte{}) {
				missing = entry
				continue
			}
		}
		durable = append(durable, entry)
	}
	if pending.Hash == ([32]byte{}) || missing.Hash == ([32]byte{}) {
		t.Fatal("tree did not provide two leaf descendants")
	}

	base := newMemoryFamily()
	if err := base.StoreBatch(context.Background(), durable); err != nil {
		t.Fatal(err)
	}
	family := &pendingDurableFamily{
		base:    base,
		pending: map[[32]byte][]byte{pending.Hash: pending.Data},
		cache:   base.FullBelowCache(),
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	frontier := dest.GetMissingNodes(1, nil)
	if len(frontier) != 1 || frontier[0].Hash != missing.Hash {
		t.Fatalf("frontier = %v, want missing descendant %x", frontier, missing.Hash[:8])
	}
	delete(family.pending, pending.Hash)
	if err := base.StoreBatch(context.Background(), []FlushEntry{pending}); err != nil {
		t.Fatal(err)
	}
	if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	gen := family.cache.Generation()
	if family.cache.Has(gen, rootHash) {
		t.Fatal("incomplete persistence checkpoint published the shared root")
	}
	if dest.acquisition.cursor == nil {
		t.Fatal("incomplete persistence checkpoint discarded the live frontier")
	}
	for i := range BranchFactor {
		if count := dest.acquisition.cursor.lanes[i].proofs.count(); count != 0 {
			t.Fatalf("lane %d retained %d acknowledged proofs", i, count)
		}
	}

	if err := base.StoreBatch(context.Background(), []FlushEntry{missing}); err != nil {
		t.Fatal(err)
	}
	if frontier := dest.GetMissingNodes(1, nil); len(frontier) != 0 {
		t.Fatalf("resolved blocked descendant still missing: %v", frontier)
	}
	if family.cache.Has(gen, rootHash) {
		t.Fatal("blocked retry published root above a non-durable pending subtree")
	}

	family.durableFetches.Store(0)
	if frontier := dest.GetMissingNodes(1, nil); len(frontier) != 0 {
		t.Fatalf("fully durable tree reported missing: %v", frontier)
	}
	if reads := family.durableFetches.Load(); reads != 0 {
		t.Fatalf("logically complete frontier poll performed %d durable reads", reads)
	}
	if family.cache.Has(gen, rootHash) {
		t.Fatal("frontier poll published the shared root before persistence acknowledgement")
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := dest.AcknowledgePersistedContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled persistence acknowledgement error = %v", err)
	}
	if family.cache.Has(gen, rootHash) {
		t.Fatal("canceled persistence acknowledgement published the shared root")
	}

	if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !family.cache.Has(gen, rootHash) {
		t.Fatal("persistence acknowledgement did not publish the shared root")
	}
}

func TestBackedWalkCompletionRetainsMaximalCheckpointProof(t *testing.T) {
	for _, stored := range []bool{false, true} {
		name := "pending"
		if stored {
			name = "stored"
		}
		t.Run(name, func(t *testing.T) {
			sm := New(TypeState)
			cache := newFullBelowCacheWithClock(8, time.Hour, time.Now)
			gen := cache.Generation()
			lane := backedWalkLane{passDurable: true}
			lane.proofs.add([32]byte{0x11}, 3)
			lane.proofs.add([32]byte{0x12}, 4)
			hash := [32]byte{0x21}
			lane.stack = []backedWalkFrame{{
				item:       backedWalkItem{hash: hash, depth: 2},
				full:       true,
				stored:     stored,
				topLevel:   true,
				proofStart: 0,
			}}

			sm.finishBackedWalkFrame(&lane, cache, gen, true, stored, false)

			if got := lane.proofs.count(); got != 1 {
				t.Fatalf("proof count = %d, want one maximal proof", got)
			}
			if got := lane.proofs.chunks[0][0]; got.hash != hash || got.depth != 2 {
				t.Fatalf("proof = (%x, %d), want completed root (%x, 2)", got.hash[:8], got.depth, hash[:8])
			}
		})
	}
}

func TestPublishAcknowledgedFullBelowPreservesProofDepthAdmission(t *testing.T) {
	sm := New(TypeState)
	rootHash, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	sm.backing.fullBelow = newFullBelowCacheWithClock(64, time.Hour, time.Now)
	gen := sm.backing.fullBelow.Generation()
	sm.acquisition.cursor = &backedWalkCursor{generation: gen, rootHash: rootHash}

	shallow := [32]byte{0x31}
	deep := [32]byte{0x32}
	lane := &sm.acquisition.cursor.lanes[0]
	lane.proofs.add(shallow, FullBelowCacheMaxDepth)
	lane.proofs.add(deep, FullBelowCacheMaxDepth+1)

	complete, release := sm.publishAcknowledgedFullBelow()
	if complete {
		t.Fatal("incomplete cursor reported complete")
	}
	if !sm.backing.fullBelow.Has(gen, shallow) {
		t.Fatal("shallow acknowledged proof was not admitted")
	}
	if sm.backing.fullBelow.Has(gen, deep) {
		t.Fatal("deep acknowledged proof was admitted")
	}
	if _, ok := release[shallow]; !ok {
		t.Fatal("shallow acknowledged proof was not releasable")
	}
	if _, ok := release[deep]; !ok {
		t.Fatal("deep acknowledged proof was not releasable")
	}
	if got := lane.proofs.count(); got != 0 {
		t.Fatalf("published proof count = %d, want 0", got)
	}
}

func TestAcknowledgePersistedReleasesProofsEvictedDuringPublication(t *testing.T) {
	sm := New(TypeState)
	for branch := range BranchFactor {
		var key [32]byte
		key[0] = byte(branch << 4)
		data := make([]byte, 12)
		data[0] = byte(branch)
		if err := sm.Put(key, data); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	sm.backing.fullBelow = newFullBelowCacheWithClock(1, time.Hour, time.Now)
	gen := sm.backing.fullBelow.Generation()
	sm.acquisition.cursor = &backedWalkCursor{generation: gen, rootHash: rootHash}

	proofs := 0
	for branch := range BranchFactor {
		child, hash, set := sm.tree.root.LoadChild(branch)
		if !set || child == nil {
			continue
		}
		sm.acquisition.cursor.lanes[branch].proofs.add(hash, 1)
		proofs++
	}
	if proofs < 2 {
		t.Fatalf("tree produced %d attached proof roots, want at least two", proofs)
	}

	if err := sm.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := sm.backing.fullBelow.Size(); got != 1 {
		t.Fatalf("FullBelow size = %d, want capacity 1 after publication churn", got)
	}
	for branch := range BranchFactor {
		child, _, set := sm.tree.root.LoadChild(branch)
		if set && child != nil {
			t.Fatalf("acknowledgement retained durably complete child %d", branch)
		}
	}
}

func TestDecodeTraversalNodeValidatesCanonicalPrefixData(t *testing.T) {
	source := buildRandomState(t, 32)
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range batch {
		view, err := decodeTraversalNode(entry.Data, entry.Hash)
		if err != nil {
			t.Fatalf("decode %x: %v", entry.Hash[:8], err)
		}
		node, err := deserializeFromPrefix(entry.Data)
		if err != nil {
			t.Fatalf("full decode %x: %v", entry.Hash[:8], err)
		}
		_, inner := node.(*innerNode)
		if view.inner != inner {
			t.Fatalf("node %x inner=%v, want %v", entry.Hash[:8], view.inner, inner)
		}
	}

	entry := batch[0]
	corrupt := append([]byte(nil), entry.Data...)
	corrupt[len(corrupt)-1] ^= 0xff
	if _, err := decodeTraversalNode(corrupt, entry.Hash); err == nil {
		t.Fatal("corrupt node passed structural hash validation")
	}
	if _, err := decodeTraversalNode([]byte{1, 2, 3}, entry.Hash); err == nil {
		t.Fatal("short node passed structural validation")
	}
}

var (
	benchmarkTraversalView traversalNode
	benchmarkDecodedNode   mapNode
)

func BenchmarkTraversalDecode(b *testing.B) {
	source := buildRandomState(b, 128)
	batch, err := collectDirtyForTest(source)
	if err != nil {
		b.Fatal(err)
	}
	var inner FlushEntry
	for _, entry := range batch {
		if len(entry.Data) >= 4 && bytes.Equal(entry.Data[:4], protocol.HashPrefixInnerNode().Bytes()) {
			inner = entry
			break
		}
	}
	if inner.Hash == ([32]byte{}) {
		b.Fatal("no inner node found")
	}

	b.Run("Structural", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkTraversalView, err = decodeTraversalNode(inner.Data, inner.Hash)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Materialized", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			benchmarkDecodedNode, err = deserializeFromPrefix(inner.Data)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
