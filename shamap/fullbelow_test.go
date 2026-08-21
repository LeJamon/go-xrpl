package shamap

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingFamily wraps a Family and counts Fetch calls so a test can prove
// that a full-below walk prunes subtrees instead of re-descending (and
// re-fetching) them.
type countingFamily struct {
	inner   Family
	fetches atomic.Int64
}

type concurrentFetchFamily struct {
	Family
	active atomic.Int64
	peak   atomic.Int64
}

type pendingDurableFamily struct {
	base           Family
	pending        map[[32]byte][]byte
	cache          *FullBelowCache
	durableFetches atomic.Int64
}

func (f *pendingDurableFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if data, ok := f.pending[hash]; ok {
		return append([]byte(nil), data...), nil
	}
	return f.base.Fetch(ctx, hash)
}

func (f *pendingDurableFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.durableFetches.Add(1)
	return f.base.Fetch(ctx, hash)
}

func (f *pendingDurableFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func (f *pendingDurableFamily) FullBelowCache() *FullBelowCache {
	return f.cache
}

func (f *concurrentFetchFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for peak := f.peak.Load(); active > peak && !f.peak.CompareAndSwap(peak, active); peak = f.peak.Load() {
	}
	time.Sleep(time.Millisecond)
	return f.Family.Fetch(ctx, hash)
}

func newCountingFamily() *countingFamily {
	return &countingFamily{inner: newMemoryFamily()}
}

func (c *countingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	c.fetches.Add(1)
	return c.inner.Fetch(ctx, hash)
}

func (c *countingFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return c.inner.StoreBatch(ctx, entries)
}

func (c *countingFamily) FullBelowCache() *FullBelowCache {
	return c.inner.(fullBelowCacheProvider).FullBelowCache()
}

func (c *countingFamily) count() int64 { return c.fetches.Load() }
func (c *countingFamily) reset()       { c.fetches.Store(0) }

func TestWalkFullBelow_HonorsSharedStopBeforeStoreRead(t *testing.T) {
	source := buildRandomState(t, 128)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}

	family := newCountingFamily()
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}
	family.reset()

	gen, cache, done := fullBelowContext(dest)
	defer done()
	full, stopped, err := walkFullBelow(
		context.Background(), dest,
		dest.tree.root,
		NewRootNodeID(),
		rootHash,
		0,
		gen,
		&DefaultSyncFilter{},
		false,
		cache,
		func(MissingNode) bool { return false },
		func() bool { return true },
	)
	if err != nil {
		t.Fatalf("walkFullBelow: %v", err)
	}
	if full || !stopped {
		t.Fatalf("walk result = (full=%t, stopped=%t), want (false, true)", full, stopped)
	}
	if got := family.count(); got != 0 {
		t.Fatalf("cancelled walk performed %d store reads, want 0", got)
	}
}

func TestWalkMapParallel_BoundsFanoutAtRootBranches(t *testing.T) {
	source := buildRandomState(t, 4096)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}

	base := newMemoryFamily()
	if err := base.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	family := &concurrentFetchFamily{Family: base}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	if missing := dest.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("complete backed map reported %d missing nodes", len(missing))
	}
	if peak := family.peak.Load(); peak <= 1 {
		t.Fatalf("peak concurrent store reads = %d, want parallel root branches", peak)
	} else if peak > BranchFactor {
		t.Fatalf("peak concurrent store reads = %d, exceeds root fan-out (%d)", peak, BranchFactor)
	}
}

func TestWalkMapParallel_ReleasesCompleteStoredSubtrees(t *testing.T) {
	source := buildRandomState(t, 4096)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}

	family := newCountingFamily()
	entries := make([]FlushEntry, 0, len(batch.Entries)-1)
	for _, entry := range batch.Entries {
		if entry.Hash != rootHash {
			entries = append(entries, entry)
		}
	}
	if err := family.StoreBatch(context.Background(), entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	if missing := dest.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("complete backed map reported %d missing nodes", len(missing))
	}
	for branch := range BranchFactor {
		child, _, isSet := dest.tree.root.LoadChild(branch)
		if isSet && child != nil {
			t.Fatalf("stored root branch %d remained materialized", branch)
		}
	}

	family.reset()
	if missing := dest.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("cached backed map reported %d missing nodes", len(missing))
	}
	if got := family.count(); got != 1 {
		t.Fatalf("second walk performed %d store reads, want only the unstored root proof", got)
	}
}

func TestFinishSync_ReleasesColdDurableTree(t *testing.T) {
	source := New(TypeState)
	for i := range 2048 {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i))
		key := sha256.Sum256(seed[:])
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}

	family := newCountingFamily()
	if err := family.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	if err := dest.FinishSync(); err != nil {
		t.Fatalf("FinishSync: %v", err)
	}
	if got := family.count(); got <= BranchFactor {
		t.Fatalf("cold completeness walk fetched %d nodes, want more than one root fan-out", got)
	}
	for branch := range BranchFactor {
		child, _, isSet := dest.tree.root.LoadChild(branch)
		if isSet && child != nil {
			t.Fatalf("durable root branch %d remained materialized after FinishSync", branch)
		}
	}
}

func TestWalkMapParallel_RetainsIncompleteFrontier(t *testing.T) {
	source := buildRandomState(t, 512)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	wire, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	var withheld WireNode
	var withheldID NodeID
	for _, candidate := range wire {
		nodeID, err := ParseNodeID(candidate.NodeID)
		if err != nil {
			t.Fatalf("ParseNodeID: %v", err)
		}
		if !nodeID.IsRoot() && nodeID.Depth() > withheldID.Depth() {
			withheld = candidate
			withheldID = nodeID
		}
	}
	withheldNode, err := deserializeNodeFromWire(withheld.Data)
	if err != nil {
		t.Fatalf("deserialize withheld node: %v", err)
	}
	withheldHash := withheldNode.Hash()

	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}
	entries := make([]FlushEntry, 0, len(batch.Entries)-1)
	for _, entry := range batch.Entries {
		if entry.Hash != withheldHash {
			entries = append(entries, entry)
		}
	}
	family := newMemoryFamily()
	if err := family.StoreBatch(context.Background(), entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	missing := dest.GetMissingNodes(1, nil)
	if len(missing) != 1 || missing[0].Hash != withheldHash {
		t.Fatalf("missing = %v, want withheld node %x", missing, withheldHash[:8])
	}
	result, err := dest.AddKnownNodeByID(withheldID, withheld.Data)
	if err != nil {
		t.Fatalf("AddKnownNodeByID: %v", err)
	}
	if result != NodeUseful {
		t.Fatalf("AddKnownNodeByID result = %v, want NodeUseful", result)
	}
	if err := dest.FinishSync(); err != nil {
		t.Fatalf("FinishSync: %v", err)
	}
}

// buildRandomState builds a state SHAMap of n random-keyed leaves and
// returns it. Random keys spread the tree across every branch and drive it
// several levels deep, so a walk that fails to prune has real work to skip.
func buildRandomState(t testing.TB, n int) *SHAMap {
	t.Helper()
	sm := New(TypeState)
	for range n {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			t.Fatalf("rand: %v", err)
		}
		if err := sm.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	return sm
}

// syncInto delivers every non-root node of source into dest in WalkWireNodes
// order (parents before children, so AddKnownNodeByID can always hook the
// node), skipping any NodeID for which skip returns true. Returns the count
// delivered.
func syncInto(t testing.TB, dest, source *SHAMap, skip func(id []byte) bool) int {
	t.Helper()
	nodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	delivered := 0
	for _, w := range nodes {
		nid, err := ParseNodeID(w.NodeID)
		if err != nil {
			t.Fatalf("ParseNodeID: %v", err)
		}
		if nid.IsRoot() {
			continue
		}
		if skip != nil && skip(w.NodeID) {
			continue
		}
		if _, err := dest.AddKnownNodeByID(nid, w.Data); err != nil {
			t.Fatalf("AddKnownNodeByID depth=%d: %v", nid.Depth(), err)
		}
		delivered++
	}
	return delivered
}

// wireDataFor returns source's wire bytes for the node at nodeID.
func wireDataFor(t testing.TB, source *SHAMap, nodeID NodeID) []byte {
	t.Helper()
	nodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	want := string(nodeID.Bytes())
	for _, w := range nodes {
		if string(w.NodeID) == want {
			return w.Data
		}
	}
	t.Fatalf("no wire node for id %x", nodeID.Bytes())
	return nil
}

// TestFullBelow_MarksCompleteSubtrees fully syncs a tree and asserts the
// walk marks the whole tree full-below: the root and every inner node.
func TestFullBelow_MarksCompleteSubtrees(t *testing.T) {
	source := buildRandomState(t, 400)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}

	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}
	syncInto(t, dest, source, nil)

	if missing := dest.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("complete tree should have no missing nodes, got %d", len(missing))
	}

	gen := dest.FullBelowCache().Generation()
	if !dest.tree.root.isFullBelow(gen) {
		t.Error("root should be marked full-below after a complete walk")
	}
	// Every resident inner node must be marked.
	var check func(n *innerNode) bool
	check = func(n *innerNode) bool {
		if !n.isFullBelow(gen) {
			return false
		}
		for i := range BranchFactor {
			child, _, isSet := n.LoadChild(i)
			if !isSet || child == nil {
				continue
			}
			if inner, ok := child.(*innerNode); ok {
				if !check(inner) {
					return false
				}
			}
		}
		return true
	}
	if !check(dest.tree.root) {
		t.Error("every inner node in a complete tree should be full-below")
	}
}

// TestFullBelow_NeverMarkedWhileDescendantMissing is the invalidation-
// correctness test: with one leaf withheld, no ancestor of the hole may be
// marked full-below, while a complete sibling subtree must be. Filling the
// hole and re-walking then promotes the whole tree to full-below.
func TestFullBelow_NeverMarkedWhileDescendantMissing(t *testing.T) {
	// Keys engineered so branch 0 and branch 1 each hold a multi-leaf
	// subtree; we withhold one leaf under branch 1.
	source := New(TypeState)
	keys := make([][32]byte, 0, 16)
	for hi := byte(0); hi < 2; hi++ { // first nibble 0 and 1
		for lo := byte(0); lo < 8; lo++ {
			var k [32]byte
			k[0] = (hi << 4)
			k[1] = lo << 4
			k[5] = 0x01 // keep every key non-zero without changing the path
			keys = append(keys, k)
			if err := source.Put(k, make([]byte, 12)); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()

	// The withheld leaf lives under first nibble 1.
	withheldKey := keys[len(keys)-1]
	if withheldKey[0]>>4 != 1 {
		t.Fatalf("expected withheld key under branch 1, got nibble %d", withheldKey[0]>>4)
	}

	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	// Identify the wire NodeID of the withheld leaf so we can skip it.
	withheldLeafID := leafNodeIDFor(t, source, withheldKey)
	withheldBytes := string(withheldLeafID.Bytes())
	syncInto(t, dest, source, func(id []byte) bool {
		return string(id) == withheldBytes
	})

	missing := dest.walkMap(0, nil)
	if len(missing) == 0 {
		t.Fatal("expected the withheld leaf to be reported missing")
	}

	gen := dest.FullBelowCache().Generation()
	if dest.tree.root.isFullBelow(gen) {
		t.Error("root must NOT be full-below while a descendant is missing")
	}

	// Branch 0's subtree is complete and must be marked; branch 1's must not.
	b0, _, set0 := dest.tree.root.LoadChild(0)
	if !set0 {
		t.Fatal("branch 0 should be populated")
	}
	if inner0, ok := b0.(*innerNode); ok {
		if !inner0.isFullBelow(gen) {
			t.Error("complete branch-0 subtree should be full-below")
		}
	}
	b1, _, set1 := dest.tree.root.LoadChild(1)
	if !set1 {
		t.Fatal("branch 1 should be populated")
	}
	if inner1, ok := b1.(*innerNode); ok {
		if inner1.isFullBelow(gen) {
			t.Error("branch-1 subtree with a missing leaf must NOT be full-below")
		}
	}

	// Deliver the withheld leaf, re-walk: the whole tree is now complete.
	leafData := wireDataFor(t, source, withheldLeafID)
	if _, err := dest.AddKnownNodeByID(withheldLeafID, leafData); err != nil {
		t.Fatalf("AddKnownNodeByID(withheld): %v", err)
	}
	if missing := dest.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("tree should be complete after filling the hole, got %d missing", len(missing))
	}
	if !dest.tree.root.isFullBelow(gen) {
		t.Error("root should be full-below once the hole is filled")
	}
}

// TestFullBelow_PrunesReleasedSubtree proves the walk is O(new) not
// O(tree): once a backed subtree is proven full-below, a re-walk prunes it
// with zero store fetches even after its child pointers were released;
// defeating the marks (Bump) forces the whole subtree to be re-fetched.
func TestFullBelow_PrunesReleasedSubtree(t *testing.T) {
	fam := newCountingFamily()

	// Build a multi-level tree and flush every node to the family.
	source, err := NewBacked(TypeState, fam)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	for range 2000 {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			t.Fatalf("rand: %v", err)
		}
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if _, err := source.SnapshotImmutable(); err != nil { // flushes dirty nodes to fam
		t.Fatalf("snapshot: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	// A fresh backed view of the same tree: only the root is resident.
	dest, err := NewFromRootHash(TypeState, rootHash, fam)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}

	// Walk 1 (cold): materializes the tree from the store — O(tree) fetches.
	if missing := dest.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("backed complete tree should have no missing nodes, got %d", len(missing))
	}
	coldFetches := fam.count()
	if coldFetches == 0 {
		t.Fatal("cold walk should have fetched the tree from the store")
	}

	// Release the root's children so the whole tree is hash-only again, but
	// keep the root (which stays marked full-below).
	dest.tree.root.ReleaseChildren()

	// Walk 2 (warm marks): the root is proven full-below → prune, zero fetches.
	fam.reset()
	if missing := dest.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("warm walk found %d missing on a complete tree", len(missing))
	}
	if got := fam.count(); got != 0 {
		t.Errorf("warm walk re-fetched %d nodes; expected 0 (subtree pruned)", got)
	}

	// Walk 3 (marks invalidated): with the marks bumped and children
	// released, the walk must re-descend and re-fetch the tree — the
	// pre-full-below O(tree) behaviour we are replacing.
	dest.FullBelowCache().Bump()
	fam.reset()
	if missing := dest.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("post-bump walk found %d missing on a complete tree", len(missing))
	}
	if got := fam.count(); got == 0 {
		t.Error("post-bump walk should re-fetch the released tree (no valid marks)")
	}
}

// TestFullBelow_HashSetSkipsReleasedChildWithoutFetch isolates the backed
// hash set: when a proven-complete child subtree has been released to a
// hash-only stub, a walk of its (re-evaluated) parent prunes it via the
// cache without fetching it from the store. Clearing the set (Bump) then
// forces the same walk to re-fetch, proving the set is what pruned it.
func TestFullBelow_HashSetSkipsReleasedChildWithoutFetch(t *testing.T) {
	fam := newCountingFamily()
	source, err := NewBacked(TypeState, fam)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	for range 1000 {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			t.Fatalf("rand: %v", err)
		}
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if _, err := source.SnapshotImmutable(); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	rootHash, _ := source.Hash()

	dest, err := NewFromRootHash(TypeState, rootHash, fam)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}

	// Materialize and mark the tree; the source flush populated the shared set.
	if missing := dest.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("warm-up walk found %d missing", len(missing))
	}
	if dest.FullBelowCache().Size() == 0 {
		t.Fatal("hash set should be populated after a backed complete walk")
	}

	// Release the root's children to hash-only stubs (their hashes remain in
	// the set) and force the root itself to be re-evaluated, so the walk
	// must reach the child slots and decide via the set.
	dest.tree.root.ReleaseChildren()
	dest.tree.root.mu.Lock()
	dest.tree.root.fullBelowGen = 0
	dest.tree.root.mu.Unlock()

	fam.reset()
	if missing := dest.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("walk found %d missing on a complete tree", len(missing))
	}
	if got := fam.count(); got != 0 {
		t.Errorf("walk fetched %d nodes; released children should be pruned via the hash set", got)
	}

	// Clear the set: the same released children must now be re-fetched.
	dest.FullBelowCache().Bump()
	dest.tree.root.mu.Lock()
	dest.tree.root.fullBelowGen = 0
	dest.tree.root.mu.Unlock()
	fam.reset()
	if missing := dest.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("post-bump walk found %d missing", len(missing))
	}
	if got := fam.count(); got == 0 {
		t.Error("post-bump walk should re-fetch released children (set cleared)")
	}
}

func TestFullBelow_SharedCacheDoesNotPublishUnstoredSubtree(t *testing.T) {
	family := newMemoryFamily()
	source := buildRandomState(t, 100)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootWire, err := source.tree.root.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	source.backing.fullBelow = family.FullBelowCache()
	if missing := source.walkMap(0, nil); len(missing) != 0 {
		t.Fatalf("materialized source missing %d nodes", len(missing))
	}

	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootWire); err != nil {
		t.Fatal(err)
	}
	if missing := dest.GetMissingNodes(0, nil); len(missing) == 0 {
		t.Fatal("shared cache hid an unstored subtree")
	}
}

func TestWalkMapParallel_DoesNotReleaseUnstoredResidentSubtrees(t *testing.T) {
	family := newMemoryFamily()
	source := buildRandomState(t, 256)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootWire, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootWire); err != nil {
		t.Fatal(err)
	}
	syncInto(t, dest, source, nil)

	if missing := dest.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("resident complete map reported %d missing nodes", len(missing))
	}
	resident := 0
	for branch := range BranchFactor {
		child, _, isSet := dest.tree.root.LoadChild(branch)
		if isSet && child != nil {
			resident++
		}
	}
	if resident == 0 {
		t.Fatal("unstored resident root subtrees were released")
	}
	if family.FullBelowCache().Has(family.FullBelowCache().Generation(), rootHash) {
		t.Fatal("unstored resident root was published as recoverable")
	}
}

func TestWalkMapParallel_DoesNotReleasePendingNodesBeforeDurability(t *testing.T) {
	source := buildRandomState(t, 256)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootWire, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	base := newMemoryFamily()
	family := &pendingDurableFamily{
		base:    base,
		pending: make(map[[32]byte][]byte, len(batch.Entries)),
		cache:   base.FullBelowCache(),
	}
	for _, entry := range batch.Entries {
		family.pending[entry.Hash] = entry.Data
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootWire); err != nil {
		t.Fatal(err)
	}
	if missing := dest.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("pending complete map reported %d missing nodes", len(missing))
	}
	resident := 0
	for branch := range BranchFactor {
		child, _, isSet := dest.tree.root.LoadChild(branch)
		if isSet && child != nil {
			resident++
		}
	}
	if resident != 0 {
		t.Fatalf("structural traversal materialized %d pending root subtrees", resident)
	}
	if family.cache.Has(family.cache.Generation(), rootHash) {
		t.Fatal("pending root was published as durable")
	}

	if err := base.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatal(err)
	}
	family.durableFetches.Store(0)
	if missing := dest.GetMissingNodes(1, nil); len(missing) != 0 {
		t.Fatalf("durable complete map reported %d missing nodes", len(missing))
	}
	if reads := family.durableFetches.Load(); reads != 0 {
		t.Fatalf("logically complete frontier poll performed %d durable reads", reads)
	}
	if family.cache.Has(family.cache.Generation(), rootHash) {
		t.Fatal("frontier poll published the root before persistence acknowledgement")
	}
	if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !family.cache.Has(family.cache.Generation(), rootHash) {
		t.Fatal("persistence acknowledgement did not publish the complete root")
	}
}

func TestStoreDirty_DoesNotPublishPartialSubtree(t *testing.T) {
	family := newMemoryFamily()
	source := buildRandomState(t, 256)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootWire, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := source.WalkWireNodes()
	if err != nil {
		t.Fatal(err)
	}

	partial, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := partial.AddRootNode(rootHash, rootWire); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range wire {
		nodeID, err := ParseNodeID(candidate.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		if nodeID.IsRoot() {
			continue
		}
		result, err := partial.AddKnownNodeByID(nodeID, candidate.Data)
		if err != nil {
			t.Fatal(err)
		}
		if result == NodeUseful {
			break
		}
	}
	if err := partial.StoreDirty(func(entries []FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	}); err != nil {
		t.Fatal(err)
	}

	gen := family.FullBelowCache().Generation()
	if family.FullBelowCache().Has(gen, rootHash) {
		t.Fatal("StoreDirty published a partial root as full-below")
	}
	probe, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.AddRootNode(rootHash, rootWire); err != nil {
		t.Fatal(err)
	}
	if missing := probe.GetMissingNodes(1, nil); len(missing) == 0 {
		t.Fatal("partial StoreDirty cache state hid missing descendants")
	}
}

func TestStoreDirty_DoesNotHoldFullBelowLease(t *testing.T) {
	family := newMemoryFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.Put([32]byte{1}, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	stored := make(chan error, 1)
	go func() {
		stored <- sm.StoreDirty(func([]FlushEntry) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	pruned := make(chan struct{})
	go func() {
		unlock := family.BeginPrune()
		unlock()
		close(pruned)
	}()
	select {
	case <-pruned:
	case <-time.After(time.Second):
		t.Fatal("prune waited for StoreDirty even though it publishes no cache entries")
	}
	close(release)
	if err := <-stored; err != nil {
		t.Fatal(err)
	}
	if size := family.FullBelowCache().Size(); size != 0 {
		t.Fatalf("cache size after prune = %d, want 0", size)
	}
}

// leafNodeIDFor returns the wire NodeID of the leaf holding key in sm.
func leafNodeIDFor(t testing.TB, sm *SHAMap, key [32]byte) NodeID {
	t.Helper()
	stack := newNodeStack()
	if _, err := sm.walkToKey(context.Background(), key, stack, true); err != nil {
		t.Fatalf("walkToKey: %v", err)
	}
	if stack.IsEmpty() {
		t.Fatalf("no leaf on stack for key %x", key[:4])
	}
	return stack.entries[len(stack.entries)-1].nodeID
}

// TestFullBelowCache_GenerationAndBump covers the cache's generation
// semantics: it starts at 1, Bump advances it and drops recorded hashes,
// and a per-node mark only counts at the live generation.
func TestFullBelowCache_GenerationAndBump(t *testing.T) {
	c := NewFullBelowCache()
	if c.Generation() != 1 {
		t.Fatalf("fresh cache generation = %d, want 1", c.Generation())
	}

	var h [32]byte
	h[0] = 0xAB
	gen := c.Generation()
	c.Insert(gen, h)
	if !c.Has(gen, h) {
		t.Error("Has should report an inserted hash")
	}
	if c.Size() != 1 {
		t.Errorf("Size = %d, want 1", c.Size())
	}

	// A node marked at the current generation is full-below; after Bump it
	// is not, and the hash is gone.
	n := newInnerNode()
	n.setFullBelowGen(c.Generation())
	if !n.isFullBelow(c.Generation()) {
		t.Error("node should be full-below at its marked generation")
	}
	c.Bump()
	if c.Generation() != 2 {
		t.Errorf("generation after Bump = %d, want 2", c.Generation())
	}
	if n.isFullBelow(c.Generation()) {
		t.Error("node mark must not survive a generation bump")
	}
	if c.Has(c.Generation(), h) {
		t.Error("Bump should drop recorded hashes")
	}
	if c.Size() != 0 {
		t.Errorf("Size after Bump = %d, want 0", c.Size())
	}
}

func TestFullBelowCacheBeginMutationInvalidatesAndBlocksWalks(t *testing.T) {
	cache := NewFullBelowCache()
	generation := cache.Generation()
	hash := [32]byte{0xA5}
	cache.Insert(generation, hash)

	endMutation := cache.BeginMutation()
	if got := cache.Generation(); got == generation {
		t.Fatalf("generation remained %d during backing mutation", got)
	}
	if got := cache.Size(); got != 0 {
		t.Fatalf("cache size during backing mutation = %d, want 0", got)
	}

	walkStarted := make(chan struct{})
	walkDone := make(chan struct{})
	go func() {
		close(walkStarted)
		_, endWalk := cache.Begin()
		endWalk()
		close(walkDone)
	}()
	<-walkStarted
	select {
	case <-walkDone:
		t.Fatal("walk started before backing mutation completed")
	default:
	}

	endMutation()
	<-walkDone
}

func TestFullBelowCache_RejectsStaleGenerationInsert(t *testing.T) {
	c := NewFullBelowCache()
	stale := c.Generation()
	c.Bump()
	h := [32]byte{0x7A}
	c.Insert(stale, h)
	if c.Has(c.Generation(), h) {
		t.Fatal("stale generation insert survived reset")
	}
}

func TestFullBelowCache_SharedByFamily(t *testing.T) {
	family := newMemoryFamily()
	first, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewBacked(TypeTransaction, family)
	if err != nil {
		t.Fatal(err)
	}
	if first.FullBelowCache() != second.FullBelowCache() {
		t.Fatal("maps backed by one family must share completeness marks")
	}
}

// TestFullBelowCache_ZeroGenNeverMatches guards the core invariant: a fresh
// or deserialized node (generation 0) is never mistaken for full-below.
func TestFullBelowCache_ZeroGenNeverMatches(t *testing.T) {
	c := NewFullBelowCache()
	n := newInnerNode() // fullBelowGen defaults to 0
	if n.isFullBelow(c.Generation()) {
		t.Error("an unmarked node (gen 0) must never read as full-below")
	}
	// Even after many bumps, gen never returns to 0, so the invariant holds.
	for range 5 {
		c.Bump()
	}
	if n.isFullBelow(c.Generation()) {
		t.Error("unmarked node still must not be full-below after bumps")
	}
	if c.Generation() == 0 {
		t.Error("generation must never be 0")
	}
}

func TestFullBelowCache_DeepProofStreamDoesNotEvictShallowProof(t *testing.T) {
	const target = 64
	c := newFullBelowCacheWithClock(target, time.Hour, time.Now)
	gen := c.Generation()
	shallow := [32]byte{0, 0xff}
	cacheFullBelow(c, gen, shallow, FullBelowCacheMaxDepth)

	var lastDeep [32]byte
	for i := range 10_000 {
		var hash [32]byte
		binary.BigEndian.PutUint64(hash[8:], uint64(i+1))
		cacheFullBelow(c, gen, hash, FullBelowCacheMaxDepth+1)
		lastDeep = hash
	}

	if got := c.Size(); got != 1 {
		t.Fatalf("cache size = %d, want only the shallow proof", got)
	}
	if !c.Has(gen, shallow) {
		t.Fatal("deep proof stream evicted the shallow reusable proof")
	}
	if c.Has(gen, lastDeep) {
		t.Fatal("deep proof was admitted to the reusable cache")
	}
	stats := c.Stats()
	if stats.Inserts != 1 || stats.Evictions != 0 {
		t.Fatalf("unexpected admission stats: %+v", stats)
	}
}

func TestFullBelowCache_StrictTargetBoundsAcquisitionBurst(t *testing.T) {
	const target = 64
	now := time.Unix(1_000, 0)
	c := newFullBelowCacheWithClock(target, 10*time.Minute, func() time.Time { return now })
	gen := c.Generation()
	n := target + target/2
	var first [32]byte
	var last [32]byte
	for i := range n {
		var h [32]byte
		h[0] = byte(i)
		h[1] = byte(i >> 8)
		h[2] = byte(i >> 16)
		c.Insert(gen, h)
		if i == 0 {
			first = h
		}
		last = h
	}
	if sz := c.Size(); sz != target {
		t.Fatalf("Size = %d, want hard target %d", sz, target)
	}
	if c.Has(gen, first) {
		t.Fatal("least-recently-used entry survived capacity pressure")
	}

	now = now.Add(6 * time.Minute)
	if !c.Has(gen, last) {
		t.Fatal("entry missing before sweep")
	}
	now = now.Add(5 * time.Minute)
	c.Sweep()
	if sz := c.Size(); sz != 1 {
		t.Fatalf("Size after age sweep = %d, want refreshed entry only", sz)
	}
	stats := c.Stats()
	if stats.Evictions != uint64(n-target)+uint64(target-1) {
		t.Fatalf("Evictions = %d, want %d", stats.Evictions, n-target+target-1)
	}
}

func TestFullBelowCache_ConcurrentPressureStaysBounded(t *testing.T) {
	const target = 512
	c := newFullBelowCacheWithClock(target, time.Hour, time.Now)
	gen := c.Generation()
	var exceeded atomic.Bool
	var wg sync.WaitGroup
	for worker := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var key [8]byte
			for i := range 20_000 {
				binary.BigEndian.PutUint32(key[:4], uint32(worker))
				binary.BigEndian.PutUint32(key[4:], uint32(i))
				hash := sha256.Sum256(key[:])
				c.Insert(gen, hash)
				if i%257 == 0 {
					c.Has(gen, hash)
					if c.Size() > target {
						exceeded.Store(true)
					}
				}
			}
		}()
	}
	for range 8 {
		c.Sweep()
	}
	wg.Wait()
	if exceeded.Load() || c.Size() > target {
		t.Fatalf("cache exceeded hard target: size=%d target=%d", c.Size(), target)
	}
}

func TestFullBelowCache_SkewedShardRemainsBounded(t *testing.T) {
	const target = 64
	c := newFullBelowCacheWithClock(target, time.Hour, time.Now)
	gen := c.Generation()
	for i := range 10_000 {
		var hash [32]byte
		binary.BigEndian.PutUint64(hash[8:], uint64(i))
		c.Insert(gen, hash)
	}
	if got := c.Size(); got > target/fullBelowCacheShards {
		t.Fatalf("skewed shard size = %d, want at most %d", got, target/fullBelowCacheShards)
	}
}

func TestFullBelowCache_HitRefreshesAge(t *testing.T) {
	const target = 64
	now := time.Unix(2_000, 0)
	c := newFullBelowCacheWithClock(target, 10*time.Minute, func() time.Time { return now })
	gen := c.Generation()
	hash := func(n byte) [32]byte { return [32]byte{0, n} }

	for i := byte(1); i <= 3; i++ {
		c.Insert(gen, hash(i))
	}
	hot := hash(1)
	cold := hash(2)
	if !c.Has(gen, hot) {
		t.Fatal("hot entry missing before capacity rotation")
	}
	for i := byte(4); i <= 5; i++ {
		c.Insert(gen, hash(i))
	}
	c.Sweep()
	if c.Has(gen, cold) {
		t.Fatal("old untouched entry survived capacity pressure")
	}
	if !c.Has(gen, hot) {
		t.Fatal("recently touched entry did not survive sweep")
	}
}

func TestFullBelowCache_InsertRefreshesAge(t *testing.T) {
	now := time.Unix(3_000, 0)
	c := newFullBelowCacheWithClock(64, 10*time.Minute, func() time.Time { return now })
	gen := c.Generation()
	first := [32]byte{0, 1}
	second := [32]byte{0, 2}
	c.Insert(gen, first)
	c.Insert(gen, second)
	c.Insert(gen, [32]byte{0, 3})
	now = now.Add(8 * time.Minute)
	c.Insert(gen, first)
	for i := byte(4); i <= 5; i++ {
		c.Insert(gen, [32]byte{0, i})
	}
	c.Sweep()

	if !c.Has(gen, first) {
		t.Fatal("reinserted entry was not refreshed")
	}
	if c.Has(gen, second) {
		t.Fatal("older entry survived capacity pressure")
	}
	stats := c.Stats()
	if stats.TargetSize != 64 || stats.Evictions == 0 || stats.Sweeps != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

// buildStateWireNodes builds a random state tree of n leaves and returns its
// root hash, root wire bytes, and every non-root node in deliverable order.
func buildStateWireNodes(t testing.TB, n int) (rootHash [32]byte, rootData []byte, ordered []WireNode) {
	t.Helper()
	source := buildRandomState(t, n)
	rh, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rd, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	nodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	return rh, rd, nodes
}

// benchmarkIncrementalSync replays an inbound acquisition: it delivers the
// tree in fixed-size batches and, after each batch, runs the missing-node
// walk the router runs per reply. bumpEachWalk defeats the full-below cache
// (the pre-fix O(tree)-per-walk behaviour) so the two variants bracket the
// quadratic->linear improvement.
func benchmarkIncrementalSync(b *testing.B, leaves, batch int, bumpEachWalk bool) {
	rootHash, rootData, nodes := buildStateWireNodes(b, leaves)

	b.ResetTimer()
	for range b.N {
		dest := New(TypeState)
		if err := dest.AddRootNode(rootHash, rootData); err != nil {
			b.Fatalf("AddRootNode: %v", err)
		}
		pending := make([]WireNode, 0, batch)
		flush := func() {
			for _, w := range pending {
				nid, err := ParseNodeID(w.NodeID)
				if err != nil || nid.IsRoot() {
					continue
				}
				_, _ = dest.AddKnownNodeByID(nid, w.Data)
			}
			pending = pending[:0]
			if bumpEachWalk {
				dest.FullBelowCache().Bump()
			}
			// The per-reply missing-node walk (rippled trigger()).
			_ = dest.GetMissingNodes(256, nil)
		}
		for _, w := range nodes {
			pending = append(pending, w)
			if len(pending) >= batch {
				flush()
			}
		}
		flush()
	}
}

func BenchmarkIncrementalSync_WithFullBelow(b *testing.B) {
	benchmarkIncrementalSync(b, 20000, 256, false)
}

func BenchmarkIncrementalSync_WithoutFullBelow(b *testing.B) {
	benchmarkIncrementalSync(b, 20000, 256, true)
}

// benchmarkMissingWalk isolates the per-reply missing-node walk on a large
// tree that is complete except for a few deep leaves — the audit's
// quadratic scenario, where late in a sync the frontier is deep and every
// walk without a full-below cache re-scans the whole fetched tree to reach
// it. bumpEachWalk defeats the cache to reproduce that O(tree)-per-walk cost.
func benchmarkMissingWalk(b *testing.B, leaves int, bumpEachWalk bool) {
	source := buildRandomState(b, leaves)
	rootHash, err := source.Hash()
	if err != nil {
		b.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		b.Fatalf("SerializeRoot: %v", err)
	}
	nodes, err := source.WalkWireNodes()
	if err != nil {
		b.Fatalf("WalkWireNodes: %v", err)
	}

	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		b.Fatalf("AddRootNode: %v", err)
	}
	// Deliver everything but the last few deep leaves, leaving a sparse,
	// deep frontier over an otherwise-complete tree.
	const withhold = 4
	leafSeen := 0
	for i := len(nodes) - 1; i >= 0 && leafSeen < withhold; i-- {
		if node, derr := deserializeNodeFromWire(nodes[i].Data); derr == nil {
			if _, isLeaf := node.(mapLeaf); isLeaf {
				nodes[i], nodes[len(nodes)-1-leafSeen] = nodes[len(nodes)-1-leafSeen], nodes[i]
				leafSeen++
			}
		}
	}
	deliver := nodes[:len(nodes)-withhold]
	for _, w := range deliver {
		nid, err := ParseNodeID(w.NodeID)
		if err != nil || nid.IsRoot() {
			continue
		}
		_, _ = dest.AddKnownNodeByID(nid, w.Data)
	}

	b.ResetTimer()
	for range b.N {
		if bumpEachWalk {
			dest.FullBelowCache().Bump()
		}
		_ = dest.GetMissingNodes(256, nil)
	}
}

func BenchmarkMissingWalk_WithFullBelow(b *testing.B) {
	benchmarkMissingWalk(b, 90000, false)
}

func BenchmarkMissingWalk_WithoutFullBelow(b *testing.B) {
	benchmarkMissingWalk(b, 90000, true)
}
