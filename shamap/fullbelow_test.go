package shamap

import (
	"context"
	"crypto/rand"
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

func newCountingFamily() *countingFamily {
	return &countingFamily{inner: NewMemoryNodeStoreFamily()}
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

	if missing := dest.WalkMap(0, nil); len(missing) != 0 {
		t.Fatalf("complete tree should have no missing nodes, got %d", len(missing))
	}

	gen := dest.FullBelowCache().Generation()
	if !dest.root.isFullBelow(gen) {
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
	if !check(dest.root) {
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

	missing := dest.WalkMap(0, nil)
	if len(missing) == 0 {
		t.Fatal("expected the withheld leaf to be reported missing")
	}

	gen := dest.FullBelowCache().Generation()
	if dest.root.isFullBelow(gen) {
		t.Error("root must NOT be full-below while a descendant is missing")
	}

	// Branch 0's subtree is complete and must be marked; branch 1's must not.
	b0, _, set0 := dest.root.LoadChild(0)
	if !set0 {
		t.Fatal("branch 0 should be populated")
	}
	if inner0, ok := b0.(*innerNode); ok {
		if !inner0.isFullBelow(gen) {
			t.Error("complete branch-0 subtree should be full-below")
		}
	}
	b1, _, set1 := dest.root.LoadChild(1)
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
	if missing := dest.WalkMap(0, nil); len(missing) != 0 {
		t.Fatalf("tree should be complete after filling the hole, got %d missing", len(missing))
	}
	if !dest.root.isFullBelow(gen) {
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
	if missing := dest.WalkMap(0, nil); len(missing) != 0 {
		t.Fatalf("backed complete tree should have no missing nodes, got %d", len(missing))
	}
	coldFetches := fam.count()
	if coldFetches == 0 {
		t.Fatal("cold walk should have fetched the tree from the store")
	}

	// Release the root's children so the whole tree is hash-only again, but
	// keep the root (which stays marked full-below).
	dest.root.ReleaseChildren()

	// Walk 2 (warm marks): the root is proven full-below → prune, zero fetches.
	fam.reset()
	if missing := dest.WalkMap(0, nil); len(missing) != 0 {
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
	if missing := dest.WalkMap(0, nil); len(missing) != 0 {
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
	if missing := dest.WalkMap(0, nil); len(missing) != 0 {
		t.Fatalf("warm-up walk found %d missing", len(missing))
	}
	if dest.FullBelowCache().Size() == 0 {
		t.Fatal("hash set should be populated after a backed complete walk")
	}

	// Release the root's children to hash-only stubs (their hashes remain in
	// the set) and force the root itself to be re-evaluated, so the walk
	// must reach the child slots and decide via the set.
	dest.root.ReleaseChildren()
	dest.root.mu.Lock()
	dest.root.fullBelowGen = 0
	dest.root.mu.Unlock()

	fam.reset()
	if missing := dest.WalkMap(0, nil); len(missing) != 0 {
		t.Fatalf("walk found %d missing on a complete tree", len(missing))
	}
	if got := fam.count(); got != 0 {
		t.Errorf("walk fetched %d nodes; released children should be pruned via the hash set", got)
	}

	// Clear the set: the same released children must now be re-fetched.
	dest.FullBelowCache().Bump()
	dest.root.mu.Lock()
	dest.root.fullBelowGen = 0
	dest.root.mu.Unlock()
	fam.reset()
	if missing := dest.WalkMap(0, nil); len(missing) != 0 {
		t.Fatalf("post-bump walk found %d missing", len(missing))
	}
	if got := fam.count(); got == 0 {
		t.Error("post-bump walk should re-fetch released children (set cleared)")
	}
}

func TestFullBelow_SharedCacheDoesNotPublishUnstoredSubtree(t *testing.T) {
	family := NewMemoryNodeStoreFamily()
	source := buildRandomState(t, 100)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootWire, err := source.root.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	source.fullBelow = family.FullBelowCache()
	if missing := source.WalkMap(0, nil); len(missing) != 0 {
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

func TestStoreDirty_PinsFullBelowGenerationThroughStore(t *testing.T) {
	family := NewMemoryNodeStoreFamily()
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
		t.Fatal("prune invalidated the cache while dirty storage was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-stored; err != nil {
		t.Fatal(err)
	}
	<-pruned
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
	_, id, ok := stack.Top()
	if !ok {
		t.Fatalf("no leaf on stack for key %x", key[:4])
	}
	return id
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
	family := NewMemoryNodeStoreFamily()
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

// TestFullBelowCache_Bounded verifies the two-partition set stays bounded:
// inserting well past a partition's capacity keeps resident size under the
// 2x cap while the most recent inserts remain queryable.
func TestFullBelowCache_Bounded(t *testing.T) {
	c := NewFullBelowCache()
	gen := c.Generation()
	n := fullBelowPartitionMax + fullBelowPartitionMax/2
	var last [32]byte
	for i := range n {
		var h [32]byte
		h[0] = byte(i)
		h[1] = byte(i >> 8)
		h[2] = byte(i >> 16)
		c.Insert(gen, h)
		last = h
	}
	if sz := c.Size(); sz > 2*fullBelowPartitionMax {
		t.Errorf("Size = %d exceeds 2x partition cap %d", sz, 2*fullBelowPartitionMax)
	}
	if !c.Has(gen, last) {
		t.Error("most-recently inserted hash should still be present")
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
			if _, isLeaf := node.(LeafNode); isLeaf {
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
