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
	base  *NodeStoreFamily
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
	base        *NodeStoreFamily
	target      [32]byte
	cancelFirst bool
	started     chan struct{}
	mu          sync.Mutex
	failed      bool
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
	batch, err := source.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}

	var target [32]byte
	for branch := range BranchFactor {
		_, hash, set := source.root.LoadChild(branch)
		if set {
			target = hash
			break
		}
	}
	if target == ([32]byte{}) {
		t.Fatal("source root has no child")
	}

	base := NewMemoryNodeStoreFamily()
	if err := base.StoreBatch(context.Background(), batch.Entries); err != nil {
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

	chain := make([]*innerNode, 0, 1+MaxDepth)
	chain = append(chain, bottom)
	child := bottom
	for range MaxDepth {
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

	family := NewMemoryNodeStoreFamily()
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
		if !errors.Is(err, ErrMaxDepthExceeded) {
			t.Fatalf("attempt %d: got %v, want %v", attempt+1, err, ErrMaxDepthExceeded)
		}
	}
	gen := family.FullBelowCache().Generation()
	if dest.root.isFullBelow(gen) || family.FullBelowCache().Has(gen, rootHash) {
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
	batch, err := source.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}

	var withheld []FlushEntry
	stored := make([]FlushEntry, 0, len(batch.Entries))
	for _, entry := range batch.Entries {
		if len(withheld) < 2 && len(entry.Data) >= 4 && bytes.Equal(entry.Data[:4], protocol.HashPrefixLeafNode().Bytes()) {
			withheld = append(withheld, entry)
			continue
		}
		stored = append(stored, entry)
	}
	if len(withheld) != 2 {
		t.Fatalf("found %d leaf entries to withhold, want 2", len(withheld))
	}

	base := NewMemoryNodeStoreFamily()
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
	batch, err := source.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}

	var withheld []FlushEntry
	stored := make([]FlushEntry, 0, len(batch.Entries))
	for _, entry := range batch.Entries {
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
	base := NewMemoryNodeStoreFamily()
	base.fullBelow = newFullBelowCacheWithClock(1, time.Second, time.Hour, func() time.Time { return now })
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
	build := func(changed bool) (*SHAMap, [32]byte, []byte, *NodeBatch) {
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
		batch, err := sm.FlushDirty()
		if err != nil {
			t.Fatal(err)
		}
		return sm, hash, root, batch
	}

	firstSource, firstHash, firstRoot, firstBatch := build(false)
	_, secondHash, secondRoot, secondBatch := build(true)
	base := NewMemoryNodeStoreFamily()
	if err := base.StoreBatch(context.Background(), firstBatch.Entries); err != nil {
		t.Fatal(err)
	}
	if err := base.StoreBatch(context.Background(), secondBatch.Entries); err != nil {
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

	var deepHash [32]byte
	var findDeep func(*innerNode, int)
	findDeep = func(node *innerNode, depth int) {
		if deepHash != ([32]byte{}) {
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
			if depth+1 > fullBelowCacheMaxDepth {
				deepHash = hash
				return
			}
			findDeep(inner, depth+1)
		}
	}
	findDeep(firstSource.root, 0)
	if deepHash == ([32]byte{}) {
		t.Fatal("test tree has no inner node below the retained cache depth")
	}
	gen := base.fullBelow.Generation()
	if base.fullBelow.Has(gen, deepHash) {
		t.Fatal("deep proof displaced capacity reserved for shallow subtrees")
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
	batch, err := source.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}

	var pending FlushEntry
	durable := make([]FlushEntry, 0, len(batch.Entries)-1)
	for _, entry := range batch.Entries {
		if pending.Hash == ([32]byte{}) && len(entry.Data) >= 4 && bytes.Equal(entry.Data[:4], protocol.HashPrefixLeafNode().Bytes()) {
			pending = entry
			continue
		}
		durable = append(durable, entry)
	}
	if pending.Hash == ([32]byte{}) {
		t.Fatal("no leaf available for pending descendant")
	}

	base := NewMemoryNodeStoreFamily()
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
	gen := family.cache.Generation()
	if family.cache.Has(gen, rootHash) {
		t.Fatal("durable root above pending descendant was published as full below")
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
	if !family.cache.Has(gen, rootHash) {
		t.Fatal("fully durable retry did not publish the shared root")
	}
}

func TestBackedWalkReprovesPendingSubtreeAfterBlockedRetry(t *testing.T) {
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
	batch, err := source.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}

	var pending, missing FlushEntry
	durable := make([]FlushEntry, 0, len(batch.Entries)-2)
	for _, entry := range batch.Entries {
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

	base := NewMemoryNodeStoreFamily()
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
	if err := base.StoreBatch(context.Background(), []FlushEntry{missing}); err != nil {
		t.Fatal(err)
	}
	if frontier := dest.GetMissingNodes(1, nil); len(frontier) != 0 {
		t.Fatalf("resolved blocked descendant still missing: %v", frontier)
	}
	gen := family.cache.Generation()
	if family.cache.Has(gen, rootHash) {
		t.Fatal("blocked retry published root above a non-durable pending subtree")
	}

	delete(family.pending, pending.Hash)
	if err := base.StoreBatch(context.Background(), []FlushEntry{pending}); err != nil {
		t.Fatal(err)
	}
	if frontier := dest.GetMissingNodes(1, nil); len(frontier) != 0 {
		t.Fatalf("fully durable tree reported missing: %v", frontier)
	}
	if !family.cache.Has(gen, rootHash) {
		t.Fatal("fully durable root was not published after proof")
	}
}

func TestDecodeTraversalNodeValidatesCanonicalPrefixData(t *testing.T) {
	source := buildRandomState(t, 32)
	batch, err := source.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range batch.Entries {
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

	entry := batch.Entries[0]
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
	benchmarkDecodedNode   Node
)

func BenchmarkTraversalDecode(b *testing.B) {
	source := buildRandomState(b, 128)
	batch, err := source.FlushDirty()
	if err != nil {
		b.Fatal(err)
	}
	var inner FlushEntry
	for _, entry := range batch.Entries {
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
