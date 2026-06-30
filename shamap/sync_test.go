package shamap

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestSyncFilter(t *testing.T) {
	// Test DefaultSyncFilter
	t.Run("DefaultSyncFilter", func(t *testing.T) {
		filter := &DefaultSyncFilter{}
		var hash [32]byte
		hash[0] = 1

		if !filter.ShouldFetch(hash) {
			t.Error("DefaultSyncFilter should always return true")
		}
	})
}

func TestMissingNode(t *testing.T) {
	mn := &MissingNode{
		Hash:       [32]byte{1, 2, 3, 4, 5, 6, 7, 8},
		Depth:      5,
		ParentHash: [32]byte{9, 10, 11, 12, 13, 14, 15, 16},
		Branch:     3,
	}

	str := mn.String()
	if str == "" {
		t.Error("MissingNode.String() should return non-empty string")
	}
}

func TestGetMissingNodes(t *testing.T) {
	// Create a complete map
	sMap := New(TypeState)

	// Add some items
	for i := range byte(10) {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	// Complete map should have no missing nodes
	missing := sMap.GetMissingNodes(100, nil)
	if len(missing) != 0 {
		t.Errorf("Complete map should have no missing nodes, got %d", len(missing))
	}
}

func TestStartAndFinishSync(t *testing.T) {
	sMap := New(TypeState)

	// Start sync
	if err := sMap.StartSync(); err != nil {
		t.Fatalf("StartSync failed: %v", err)
	}

	if !sMap.IsSyncing() {
		t.Error("Map should be syncing after StartSync")
	}

	// Finish sync on empty map (which is complete)
	if err := sMap.FinishSync(); err != nil {
		t.Fatalf("FinishSync failed: %v", err)
	}

	if sMap.IsSyncing() {
		t.Error("Map should not be syncing after FinishSync")
	}
}

func TestIsComplete(t *testing.T) {
	sMap := New(TypeState)

	// Empty map is complete
	if !sMap.IsComplete() {
		t.Error("Empty map should be complete")
	}

	// Add items
	var key [32]byte
	key[0] = 1
	if err := sMap.Put(key, make([]byte, 12)); err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	// Map with items should still be complete
	if !sMap.IsComplete() {
		t.Error("Map should be complete after adding items")
	}
}

func TestSyncProgress(t *testing.T) {
	sMap := New(TypeState)

	present, total := sMap.SyncProgress()
	// Empty map should have root
	if total < 0 {
		t.Error("Total should be non-negative")
	}
	if present > total {
		t.Error("Present should not exceed total")
	}

	// Add items
	for i := range byte(5) {
		var key [32]byte
		key[0] = i
		if err := sMap.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}
	}

	present, total = sMap.SyncProgress()
	if present != total {
		t.Errorf("Complete map should have present == total, got %d vs %d", present, total)
	}
}

func TestAddRootNode(t *testing.T) {
	// Create a map and get its serialized root
	sourceMap := New(TypeState)

	var key [32]byte
	key[0] = 1
	if err := sourceMap.Put(key, make([]byte, 12)); err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	rootHash, err := sourceMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get hash: %v", err)
	}

	rootData, err := sourceMap.SerializeRoot()
	if err != nil {
		t.Fatalf("Failed to serialize root: %v", err)
	}

	// Create new map and add root
	destMap := New(TypeState)

	if err := destMap.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode failed: %v", err)
	}

	// Verify root hash matches
	destHash, err := destMap.Hash()
	if err != nil {
		t.Fatalf("Failed to get dest hash: %v", err)
	}

	if destHash != rootHash {
		t.Errorf("Root hash mismatch: expected %x, got %x", rootHash[:8], destHash[:8])
	}
}

func TestAddRootNodeErrors(t *testing.T) {
	sMap := New(TypeState)

	// Empty data should fail
	if err := sMap.AddRootNode([32]byte{}, []byte{}); err == nil {
		t.Error("Empty data should fail")
	}

	// Invalid data should fail
	if err := sMap.AddRootNode([32]byte{}, []byte{1, 2, 3}); err == nil {
		t.Error("Invalid data should fail")
	}
}

// TestWalkMap_NotGatedOnState verifies that unlike GetMissingNodes, the
// named WalkMap/WalkMapParallel APIs walk the tree regardless of the
// map's state. This matches rippled's SHAMap::walkMap which has no
// state precondition.
func TestWalkMap_NotGatedOnState(t *testing.T) {
	source := New(TypeState)
	for i := range byte(32) {
		var key [32]byte
		key[0] = i
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	// source is StateModifying — a complete tree.
	if got := source.WalkMap(0, nil); len(got) != 0 {
		t.Errorf("WalkMap on complete StateModifying map: want 0 missing, got %d", len(got))
	}
	if got := source.WalkMapParallel(0, nil); len(got) != 0 {
		t.Errorf("WalkMapParallel on complete StateModifying map: want 0 missing, got %d", len(got))
	}

	// GetMissingNodes still requires StateSyncing.
	if got := source.GetMissingNodes(0, nil); got != nil {
		t.Errorf("GetMissingNodes on non-syncing map: want nil, got %v", got)
	}
}

// TestWalkMap_SerialVsParallelAgree builds a partially-synced destination
// map and asserts WalkMap and WalkMapParallel agree on the set of missing
// nodes. The parallel version may reorder results, so comparison is
// set-based.
func TestWalkMap_SerialVsParallelAgree(t *testing.T) {
	source := New(TypeState)
	// Spread keys across every first-nibble branch so the root has all
	// 16 branches populated and the parallel walker actually has work
	// to fan out.
	for branch := range byte(16) {
		for i := range byte(4) {
			var key [32]byte
			key[0] = (branch << 4) | i
			if err := source.Put(key, make([]byte, 12)); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}

	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source.Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}

	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	serial := dest.WalkMap(0, nil)
	parallel := dest.WalkMapParallel(0, nil)

	if len(serial) == 0 {
		t.Fatal("expected missing nodes from a root-only dest, got none")
	}
	if len(serial) != len(parallel) {
		t.Fatalf("serial vs parallel size disagree: serial=%d parallel=%d", len(serial), len(parallel))
	}

	asSet := func(ms []MissingNode) map[[32]byte]int {
		m := make(map[[32]byte]int, len(ms))
		for _, n := range ms {
			m[n.Hash] = n.Branch
		}
		return m
	}
	s, p := asSet(serial), asSet(parallel)
	if len(s) != len(serial) || len(p) != len(parallel) {
		t.Fatalf("duplicate hashes in result: serial uniq=%d/%d parallel uniq=%d/%d",
			len(s), len(serial), len(p), len(parallel))
	}
	for h, branch := range s {
		if pb, ok := p[h]; !ok {
			t.Errorf("hash %x present in serial but missing from parallel", h[:8])
		} else if pb != branch {
			t.Errorf("hash %x: branch mismatch serial=%d parallel=%d", h[:8], branch, pb)
		}
	}
}

// TestWalkMap_MaxMissingHonored verifies both walkers respect the
// maxMissing bound and return exactly that many entries when at least
// that many are available.
func TestWalkMap_MaxMissingHonored(t *testing.T) {
	source := New(TypeState)
	for branch := range byte(16) {
		var key [32]byte
		key[0] = branch << 4
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source.Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}

	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	// 16 leaves on distinct first-nibble branches → 16 missing.
	full := dest.WalkMap(0, nil)
	if len(full) < 4 {
		t.Fatalf("expected at least 4 missing nodes, got %d", len(full))
	}

	bound := 3
	got := dest.WalkMap(bound, nil)
	if len(got) != bound {
		t.Errorf("WalkMap(maxMissing=%d): got %d entries", bound, len(got))
	}

	// The parallel walker's stop flag lives inside the shared-result
	// mutex, so an exact bound holds — workers that hit the lock after
	// stopped is set skip their append entirely.
	gotP := dest.WalkMapParallel(bound, nil)
	if len(gotP) != bound {
		t.Errorf("WalkMapParallel(maxMissing=%d): got %d entries", bound, len(gotP))
	}
}

// TestWalkMap_BackedLazyLoadAfterRelease pins the conformance behavior
// against rippled's descendNoStore-based walker (SHAMap.cpp:351-357): on
// a backed map whose in-memory children have been released after a
// flush, both WalkMap and WalkMapParallel must reach the on-disk data
// via lazy-load and report zero missing nodes — not the false positives
// that a pure in-memory walker would emit.
func TestWalkMap_BackedLazyLoadAfterRelease(t *testing.T) {
	family := newMemoryFamily()
	src, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}

	// 32 keys spread across multiple first-nibble branches so the
	// tree has at least two levels of inner nodes. Start from 1 to
	// avoid an all-zero key (which deserializes back as an invalid
	// account-state leaf).
	for i := byte(1); i <= 32; i++ {
		var key [32]byte
		key[0] = i
		key[31] = i
		if err := src.Put(key, intToBytes(int(i))); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}

	// FlushDirty(true) writes every dirty node to family and then calls
	// ReleaseChildren on each inner — children are nil, hashes remain.
	batch, err := src.FlushDirty(true)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}
	if err := family.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}

	if got := src.WalkMap(0, nil); len(got) != 0 {
		t.Errorf("WalkMap on backed map with released children: want 0 missing, got %d", len(got))
	}
	if got := src.WalkMapParallel(0, nil); len(got) != 0 {
		t.Errorf("WalkMapParallel on backed map with released children: want 0 missing, got %d", len(got))
	}

	// Sanity: every original key still resolves through the lazy-load
	// path (proves the walker didn't just skip silently).
	for i := byte(1); i <= 32; i++ {
		var key [32]byte
		key[0] = i
		key[31] = i
		item, found, err := src.Get(key)
		if err != nil {
			t.Fatalf("Get(%d) after release: %v", i, err)
		}
		if !found {
			t.Fatalf("Get(%d) after release: not found", i)
		}
		want := intToBytes(int(i))
		if !bytes.Equal(item.Data(), want) {
			t.Fatalf("Get(%d) after release: data drift", i)
		}
	}
}

// Regression for issue #395: GetMissingNodes must populate
// MissingNode.NodeID with each missing node's path-based identifier so
// the caller can request that exact subtree from a peer.
func TestGetMissingNodes_PathNodeIDs(t *testing.T) {
	source := New(TypeState)
	for branch := range byte(8) {
		for i := range byte(4) {
			var key [32]byte
			key[0] = (branch << 4) | i
			if err := source.Put(key, make([]byte, 12)); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}

	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}

	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	missing := dest.GetMissingNodes(64, nil)
	if len(missing) == 0 {
		t.Fatal("expected missing nodes after syncing only root, got none")
	}

	seen := make(map[[33]byte]struct{}, len(missing))
	for _, m := range missing {
		if m.NodeID.IsRoot() {
			t.Errorf("missing node at depth %d should not have root NodeID", m.Depth)
		}
		if int(m.NodeID.Depth()) != m.Depth {
			t.Errorf("NodeID depth %d != MissingNode.Depth %d", m.NodeID.Depth(), m.Depth)
		}
		var k [33]byte
		copy(k[:], m.NodeID.Bytes())
		if _, dup := seen[k]; dup {
			t.Errorf("duplicate NodeID for missing node at depth %d", m.Depth)
		}
		seen[k] = struct{}{}
		if err := m.NodeID.Validate(); err != nil {
			t.Errorf("missing NodeID is malformed: %v", err)
		}
	}
}

// Regression for issue #413: full SHAMap reconstruct via AddKnownNodeByID.
func TestAddKnownNodeByID_RippledStyleReconstruct(t *testing.T) {
	source := New(TypeTransaction)
	for branch := range byte(4) {
		for sub := range byte(4) {
			for i := range byte(4) {
				var key [32]byte
				key[0] = (branch << 4) | sub
				key[1] = i << 4
				if err := source.Put(key, []byte{branch, sub, i, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66}); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
		}
	}

	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	if len(wireNodes) < 3 {
		t.Fatalf("test setup gave only %d wire nodes; need a multi-level tree", len(wireNodes))
	}

	dest := New(TypeTransaction)
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	for i, w := range wireNodes {
		nid, err := UnmarshalBinary(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary[%d]: %v", i, err)
		}
		if nid.IsRoot() {
			continue
		}
		if _, err := dest.AddKnownNodeByID(nid, w.Data); err != nil {
			t.Fatalf("AddKnownNodeByID[%d] depth=%d: %v", i, nid.Depth(), err)
		}
	}

	if err := dest.FinishSync(); err != nil {
		t.Fatalf("FinishSync: %v", err)
	}

	destHash, err := dest.Hash()
	if err != nil {
		t.Fatalf("dest hash: %v", err)
	}
	if destHash != rootHash {
		t.Errorf("reconstructed hash mismatch: want %x got %x", rootHash[:8], destHash[:8])
	}
}

func TestAddKnownNodeByID_SentinelErrors(t *testing.T) {
	source := New(TypeTransaction)
	for branch := range byte(3) {
		for sub := range byte(3) {
			var key [32]byte
			key[0] = (branch << 4) | sub
			if err := source.Put(key, []byte{branch, sub, 0xCA, 0xFE, 0xBA, 0xBE, 0xDE, 0xAD, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66}); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}

	// Find a depth>=2 wire node — its parent stub will not be loaded yet
	// on the dest map after only AddRootNode.
	var deep *WireNode
	for i := range wireNodes {
		nid, err := UnmarshalBinary(wireNodes[i].NodeID)
		if err != nil || nid.Depth() < 2 {
			continue
		}
		deep = &wireNodes[i]
		break
	}
	if deep == nil {
		t.Fatal("test setup did not produce a depth>=2 node")
	}

	t.Run("AncestorGapReRequests", func(t *testing.T) {
		dest := New(TypeTransaction)
		if err := dest.StartSync(); err != nil {
			t.Fatalf("StartSync: %v", err)
		}
		if err := dest.AddRootNode(rootHash, rootData); err != nil {
			t.Fatalf("AddRootNode: %v", err)
		}
		// The deep node's parent is still a hash-only stub. rippled
		// re-requests rather than rejecting (descend()→nullptr, iNodeID !=
		// node → useful()), so this must be NodeReRequest, not an error.
		nid, _ := UnmarshalBinary(deep.NodeID)
		res, err := dest.AddKnownNodeByID(nid, deep.Data)
		if err != nil {
			t.Fatalf("ancestor gap: want nil error, got %v", err)
		}
		if res != NodeReRequest {
			t.Fatalf("ancestor gap: want NodeReRequest, got %v", res)
		}
	})

	t.Run("NodeHashMismatch", func(t *testing.T) {
		dest := New(TypeTransaction)
		if err := dest.StartSync(); err != nil {
			t.Fatalf("StartSync: %v", err)
		}
		if err := dest.AddRootNode(rootHash, rootData); err != nil {
			t.Fatalf("AddRootNode: %v", err)
		}
		// Use a depth-1 node's NodeID with another depth-1 node's data:
		// hash will be the unrelated sibling's hash, not what the parent
		// has stored for that branch.
		var d1a, d1b *WireNode
		for i := range wireNodes {
			nid, _ := UnmarshalBinary(wireNodes[i].NodeID)
			if nid.Depth() != 1 {
				continue
			}
			if d1a == nil {
				d1a = &wireNodes[i]
			} else {
				d1b = &wireNodes[i]
				break
			}
		}
		if d1a == nil || d1b == nil {
			t.Skip("need at least two depth-1 wire nodes")
		}
		nid, _ := UnmarshalBinary(d1a.NodeID)
		_, err = dest.AddKnownNodeByID(nid, d1b.Data)
		if !errors.Is(err, ErrNodeHashMismatch) {
			t.Fatalf("want ErrNodeHashMismatch, got %v", err)
		}
	})

	t.Run("EmptyBranchOnPath", func(t *testing.T) {
		dest := New(TypeTransaction)
		if err := dest.StartSync(); err != nil {
			t.Fatalf("StartSync: %v", err)
		}
		if err := dest.AddRootNode(rootHash, rootData); err != nil {
			t.Fatalf("AddRootNode: %v", err)
		}
		// Build a NodeID at depth=1 whose first nibble points into an
		// empty branch on root (we used only branches 0..2 above, so
		// nibble 0xF is guaranteed empty).
		var path [32]byte
		path[0] = 0xF0
		nid := NodeID{depth: 1, id: path}
		// Borrow any non-root wire data; descent fails before the data
		// is parsed.
		var anyData []byte
		for i := range wireNodes {
			rid, _ := UnmarshalBinary(wireNodes[i].NodeID)
			if !rid.IsRoot() {
				anyData = wireNodes[i].Data
				break
			}
		}
		_, err = dest.AddKnownNodeByID(nid, anyData)
		if !errors.Is(err, ErrEmptyBranchOnPath) {
			t.Fatalf("want ErrEmptyBranchOnPath, got %v", err)
		}
	})

	t.Run("DuplicateIsNoOp", func(t *testing.T) {
		dest := New(TypeTransaction)
		if err := dest.StartSync(); err != nil {
			t.Fatalf("StartSync: %v", err)
		}
		if err := dest.AddRootNode(rootHash, rootData); err != nil {
			t.Fatalf("AddRootNode: %v", err)
		}
		var d1 *WireNode
		for i := range wireNodes {
			nid, _ := UnmarshalBinary(wireNodes[i].NodeID)
			if nid.Depth() == 1 {
				d1 = &wireNodes[i]
				break
			}
		}
		if d1 == nil {
			t.Skip("no depth-1 node available")
		}
		nid, _ := UnmarshalBinary(d1.NodeID)
		res, err := dest.AddKnownNodeByID(nid, d1.Data)
		if err != nil {
			t.Fatalf("first AddKnownNodeByID: %v", err)
		}
		if res != NodeUseful {
			t.Fatalf("first AddKnownNodeByID: want NodeUseful, got %v", res)
		}
		// Second call must be a no-op success (rippled SHAMap::addKnownNode
		// returns SHAMapAddNode::duplicate()).
		res, err = dest.AddKnownNodeByID(nid, d1.Data)
		if err != nil {
			t.Fatalf("duplicate AddKnownNodeByID: %v", err)
		}
		if res != NodeDuplicate {
			t.Fatalf("duplicate AddKnownNodeByID: want NodeDuplicate, got %v", res)
		}
	})
}

// Mirrors rippled SHAMapSync.cpp:597,671-672: a leaf encountered mid-path
// is the canonical content at that slot — return duplicate (nil), not error.
func TestAddKnownNodeByID_LeafMidPathReturnsDuplicate(t *testing.T) {
	source := New(TypeTransaction)
	// Single key → root has a single leaf child at the path's first
	// nibble, consolidated at depth 1.
	var k [32]byte
	k[0] = 0x12
	k[1] = 0x34
	if err := source.Put(k, []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}

	dest := New(TypeTransaction)
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}
	for _, w := range wireNodes {
		nid, err := UnmarshalBinary(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		if nid.IsRoot() {
			continue
		}
		if _, err := dest.AddKnownNodeByID(nid, w.Data); err != nil {
			t.Fatalf("seed AddKnownNodeByID: %v", err)
		}
	}

	// Synthesize a depth-2 NodeID on the same path as the consolidated
	// leaf. The peer's data here is irrelevant — descent must short-
	// circuit on the leaf and return nil.
	deepNID := NodeID{depth: 2, id: k}
	res, err := dest.AddKnownNodeByID(deepNID, []byte{0xFF})
	if err != nil {
		t.Fatalf("leaf-mid-path: want nil (duplicate), got %v", err)
	}
	if res != NodeDuplicate {
		t.Fatalf("leaf-mid-path: want NodeDuplicate, got %v", res)
	}
}

// Direct exercise of the public AddKnownNodeFromPrefix API: reconstructs a
// map from fetch-pack ([HashPrefix][body]) blobs keyed by the NodeIDs that
// GetMissingNodes reports, then pins the duplicate / poison / root /
// empty-data / not-syncing outcomes.
func TestAddKnownNodeFromPrefix_Direct(t *testing.T) {
	source := New(TypeTransaction)
	for branch := range byte(3) {
		for sub := range byte(3) {
			var key [32]byte
			key[0] = (branch << 4) | sub
			key[1] = 0x99
			if err := source.Put(key, []byte{branch, sub, 0xCA, 0xFE, 0xBA, 0xBE, 0xDE, 0xAD, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66}); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	packNodes, err := source.WalkFetchPackNodes(1 << 10)
	if err != nil {
		t.Fatalf("WalkFetchPackNodes: %v", err)
	}
	byHash := make(map[[32]byte][]byte, len(packNodes))
	for _, n := range packNodes {
		byHash[n.Hash] = n.Data
	}

	dest := New(TypeTransaction)

	someID, err := NewRootNodeID().ChildNodeID(0)
	if err != nil {
		t.Fatalf("ChildNodeID: %v", err)
	}
	if _, err := dest.AddKnownNodeFromPrefix(someID, []byte{1}); !errors.Is(err, ErrSyncNotInProgress) {
		t.Errorf("not-syncing: want ErrSyncNotInProgress, got %v", err)
	}

	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	if _, err := dest.AddKnownNodeFromPrefix(NewRootNodeID(), rootData); !errors.Is(err, ErrUnexpectedNode) {
		t.Errorf("root nodeID: want ErrUnexpectedNode, got %v", err)
	}
	if _, err := dest.AddKnownNodeFromPrefix(someID, nil); !errors.Is(err, ErrInvalidNodeData) {
		t.Errorf("empty data: want ErrInvalidNodeData, got %v", err)
	}

	poisonTested := false
	for {
		missing := dest.GetMissingNodes(64, nil)
		if len(missing) == 0 {
			break
		}
		// Poison: another missing node's blob at this NodeID must be
		// rejected by the parent-hash check before it can attach.
		if !poisonTested && len(missing) >= 2 && missing[0].Hash != missing[1].Hash {
			poisonTested = true
			if _, err := dest.AddKnownNodeFromPrefix(missing[0].NodeID, byHash[missing[1].Hash]); !errors.Is(err, ErrNodeHashMismatch) {
				t.Fatalf("poison: want ErrNodeHashMismatch, got %v", err)
			}
		}
		for i := range missing {
			data, ok := byHash[missing[i].Hash]
			if !ok {
				t.Fatalf("no fetch-pack blob for missing hash %x", missing[i].Hash[:8])
			}
			res, err := dest.AddKnownNodeFromPrefix(missing[i].NodeID, data)
			if err != nil {
				t.Fatalf("AddKnownNodeFromPrefix depth=%d: %v", missing[i].NodeID.Depth(), err)
			}
			if res != NodeUseful {
				t.Fatalf("fresh attach depth=%d: want NodeUseful, got %v", missing[i].NodeID.Depth(), res)
			}
			res, err = dest.AddKnownNodeFromPrefix(missing[i].NodeID, data)
			if err != nil || res != NodeDuplicate {
				t.Fatalf("duplicate: want (NodeDuplicate, nil), got (%v, %v)", res, err)
			}
		}
	}
	if !poisonTested {
		t.Error("poison case never exercised — tree shape too small")
	}

	if err := dest.FinishSync(); err != nil {
		t.Fatalf("FinishSync: %v", err)
	}
	destHash, err := dest.Hash()
	if err != nil {
		t.Fatalf("dest hash: %v", err)
	}
	if destHash != rootHash {
		t.Errorf("reconstructed hash mismatch: want %x got %x", rootHash[:8], destHash[:8])
	}
}

func TestAddKnownNodeErrors(t *testing.T) {
	sMap := New(TypeState)

	// Should fail when not syncing
	if err := sMap.AddKnownNode([32]byte{}, []byte{1, 2, 3}); err == nil {
		t.Error("AddKnownNode should fail when not syncing")
	}

	// Start sync
	if err := sMap.StartSync(); err != nil {
		t.Fatalf("StartSync failed: %v", err)
	}

	// Empty data should fail
	if err := sMap.AddKnownNode([32]byte{}, []byte{}); err == nil {
		t.Error("Empty data should fail")
	}
}
