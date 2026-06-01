package shamap

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/protocol"
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

	// Test CachingSyncFilter
	t.Run("CachingSyncFilter", func(t *testing.T) {
		inner := &DefaultSyncFilter{}
		filter := NewCachingSyncFilter(inner, 100)

		var hash1, hash2 [32]byte
		hash1[0] = 1
		hash2[0] = 2

		// First call should hit inner
		result1 := filter.ShouldFetch(hash1)
		if !result1 {
			t.Error("First call should return true")
		}

		// Second call should hit cache
		result2 := filter.ShouldFetch(hash1)
		if !result2 {
			t.Error("Cached call should return true")
		}

		// Different hash
		result3 := filter.ShouldFetch(hash2)
		if !result3 {
			t.Error("New hash should return true")
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
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	// Add some items
	for i := byte(0); i < 10; i++ {
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

func TestSyncState(t *testing.T) {
	state := NewSyncState()
	if state == nil {
		t.Fatal("NewSyncState should return non-nil")
	}
}

func TestStartAndFinishSync(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

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
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

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
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create SHAMap: %v", err)
	}

	present, total := sMap.SyncProgress()
	// Empty map should have root
	if total < 0 {
		t.Error("Total should be non-negative")
	}
	if present > total {
		t.Error("Present should not exceed total")
	}

	// Add items
	for i := byte(0); i < 5; i++ {
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
	sourceMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create source map: %v", err)
	}

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
	destMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create dest map: %v", err)
	}

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
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create map: %v", err)
	}

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
	source, err := New(TypeState)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := byte(0); i < 32; i++ {
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
	source, err := New(TypeState)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	// Spread keys across every first-nibble branch so the root has all
	// 16 branches populated and the parallel walker actually has work
	// to fan out.
	for branch := byte(0); branch < 16; branch++ {
		for i := byte(0); i < 4; i++ {
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

	dest, err := New(TypeState)
	if err != nil {
		t.Fatalf("New dest: %v", err)
	}
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
	source, err := New(TypeState)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	for branch := byte(0); branch < 16; branch++ {
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

	dest, err := New(TypeState)
	if err != nil {
		t.Fatalf("New dest: %v", err)
	}
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
	source, err := New(TypeState)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	for branch := byte(0); branch < 8; branch++ {
		for i := byte(0); i < 4; i++ {
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

	dest, err := New(TypeState)
	if err != nil {
		t.Fatalf("New dest: %v", err)
	}
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
	source, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	for branch := byte(0); branch < 4; branch++ {
		for sub := byte(0); sub < 4; sub++ {
			for i := byte(0); i < 4; i++ {
				data := []byte{branch, sub, i, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
				// Key the leaf canonically (tx ID = sha512Half(TXN, blob)) as
				// production does: a tx leaf's key is re-derived from its blob on
				// the wire, so its tree position must equal that derived key —
				// AddKnownNodeByID now enforces this.
				key := common.Sha512Half(protocol.HashPrefixTransactionID[:], data)
				if err := source.Put(key, data); err != nil {
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

	dest, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New dest: %v", err)
	}
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
		if err := dest.AddKnownNodeByID(nid, w.Data); err != nil {
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
	source, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	for branch := byte(0); branch < 3; branch++ {
		for sub := byte(0); sub < 3; sub++ {
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

	t.Run("ParentNotInTree", func(t *testing.T) {
		dest, err := New(TypeTransaction)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := dest.StartSync(); err != nil {
			t.Fatalf("StartSync: %v", err)
		}
		if err := dest.AddRootNode(rootHash, rootData); err != nil {
			t.Fatalf("AddRootNode: %v", err)
		}
		nid, _ := UnmarshalBinary(deep.NodeID)
		err = dest.AddKnownNodeByID(nid, deep.Data)
		if !errors.Is(err, ErrParentNotInTree) {
			t.Fatalf("want ErrParentNotInTree, got %v", err)
		}
	})

	t.Run("NodeHashMismatch", func(t *testing.T) {
		dest, err := New(TypeTransaction)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
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
		err = dest.AddKnownNodeByID(nid, d1b.Data)
		if !errors.Is(err, ErrNodeHashMismatch) {
			t.Fatalf("want ErrNodeHashMismatch, got %v", err)
		}
	})

	t.Run("EmptyBranchOnPath", func(t *testing.T) {
		dest, err := New(TypeTransaction)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
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
		nid, err := NewNodeID(1, path)
		if err != nil {
			t.Fatalf("NewNodeID: %v", err)
		}
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
		err = dest.AddKnownNodeByID(nid, anyData)
		if !errors.Is(err, ErrEmptyBranchOnPath) {
			t.Fatalf("want ErrEmptyBranchOnPath, got %v", err)
		}
	})

	t.Run("DuplicateIsNoOp", func(t *testing.T) {
		dest, err := New(TypeTransaction)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
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
		if err := dest.AddKnownNodeByID(nid, d1.Data); err != nil {
			t.Fatalf("first AddKnownNodeByID: %v", err)
		}
		// Second call must be a no-op success (rippled SHAMap::addKnownNode
		// returns SHAMapAddNode::duplicate(); we surface that as nil).
		if err := dest.AddKnownNodeByID(nid, d1.Data); err != nil {
			t.Fatalf("duplicate AddKnownNodeByID: %v", err)
		}
	})
}

// Mirrors rippled SHAMapSync.cpp:597,671-672: a leaf encountered mid-path
// is the canonical content at that slot — return duplicate (nil), not error.
func TestAddKnownNodeByID_LeafMidPathReturnsDuplicate(t *testing.T) {
	source, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	// Single key → root has a single leaf child at the path's first
	// nibble, consolidated at depth 1. Key it canonically (tx ID) so the
	// leaf's position matches its wire-derived key — see AddKnownNodeByID's
	// leaf-position guard.
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	k := common.Sha512Half(protocol.HashPrefixTransactionID[:], data)
	if err := source.Put(k, data); err != nil {
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

	dest, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New dest: %v", err)
	}
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
		if err := dest.AddKnownNodeByID(nid, w.Data); err != nil {
			t.Fatalf("seed AddKnownNodeByID: %v", err)
		}
	}

	// Synthesize a depth-2 NodeID on the same path as the consolidated
	// leaf. The peer's data here is irrelevant — descent must short-
	// circuit on the leaf and return nil.
	deepNID, err := NewNodeID(2, k)
	if err != nil {
		t.Fatalf("NewNodeID: %v", err)
	}
	if err := dest.AddKnownNodeByID(deepNID, []byte{0xFF}); err != nil {
		t.Fatalf("leaf-mid-path: want nil (duplicate), got %v", err)
	}
}

func TestAddKnownNodeErrors(t *testing.T) {
	sMap, err := New(TypeState)
	if err != nil {
		t.Fatalf("Failed to create map: %v", err)
	}

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

// TestAddKnownNodeByID_LeafWrongPosition exercises the leaf-position guard
// added to mirror rippled's SHAMap::addKnownNode (PR #5951): a leaf whose key
// does not derive to the position the descent walked to is rejected even when
// its hash matches the parent's stored child hash. This can only arise from a
// buggy or hostile peer (a correct tree never stores a leaf hash at a branch
// inconsistent with the leaf's key), so the corrupt parent is forged directly.
func TestAddKnownNodeByID_LeafWrongPosition(t *testing.T) {
	// An AccountState leaf whose key's first nibble is 1 — it belongs under
	// the root at branch 1. The key is carried explicitly in the wire form,
	// so we can place the leaf wherever we like to forge the corrupt parent.
	var key [32]byte
	key[0] = 0x10 // nibble0 = 1
	key[1] = 0xAB
	leaf, err := NewAccountStateLeafNode(NewItem(key,
		[]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}))
	if err != nil {
		t.Fatalf("NewAccountStateLeafNode: %v", err)
	}
	leafWire, err := leaf.SerializeForWire()
	if err != nil {
		t.Fatalf("SerializeForWire: %v", err)
	}

	// forgeRoot builds a single-branch root that stores leaf at the given
	// branch, returning the root hash + wire data so a dest map can ingest it
	// as a hash-only stub via AddRootNode.
	forgeRoot := func(branch int) ([32]byte, []byte) {
		root := NewInnerNode()
		if err := root.SetChild(branch, leaf); err != nil {
			t.Fatalf("SetChild: %v", err)
		}
		wire, err := root.SerializeForWire()
		if err != nil {
			t.Fatalf("root SerializeForWire: %v", err)
		}
		return root.Hash(), wire
	}

	newDest := func(rootHash [32]byte, rootData []byte) *SHAMap {
		dest, err := New(TypeState)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := dest.StartSync(); err != nil {
			t.Fatalf("StartSync: %v", err)
		}
		if err := dest.AddRootNode(rootHash, rootData); err != nil {
			t.Fatalf("AddRootNode: %v", err)
		}
		return dest
	}

	t.Run("accepts leaf at correct position", func(t *testing.T) {
		rootHash, rootData := forgeRoot(1) // branch 1 == key's first nibble
		dest := newDest(rootHash, rootData)
		nid, err := CreateNodeID(1, key)
		if err != nil {
			t.Fatalf("CreateNodeID: %v", err)
		}
		if err := dest.AddKnownNodeByID(nid, leafWire); err != nil {
			t.Fatalf("want nil (leaf belongs here), got %v", err)
		}
	})

	t.Run("rejects hash-valid leaf at wrong position", func(t *testing.T) {
		rootHash, rootData := forgeRoot(2) // branch 2 != key's first nibble (1)
		dest := newDest(rootHash, rootData)
		// Request the leaf at branch 2. The forged stub hash there matches the
		// leaf's hash, so the hash check passes — but the leaf's key derives
		// to branch 1, so the position guard must reject it.
		var branch2Key [32]byte
		branch2Key[0] = 0x20 // nibble0 = 2
		nid, err := CreateNodeID(1, branch2Key)
		if err != nil {
			t.Fatalf("CreateNodeID: %v", err)
		}
		if err := dest.AddKnownNodeByID(nid, leafWire); !errors.Is(err, ErrLeafWrongPosition) {
			t.Fatalf("want ErrLeafWrongPosition, got %v", err)
		}
	})
}
