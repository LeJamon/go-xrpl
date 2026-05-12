package shamap

import (
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

// Regression for issue #413: tx-set sync used AddKnownNodeUnchecked, a
// tree-wide hash-search that silently fails (returning ErrUnexpectedNode
// without diagnostics) whenever the path it would need to follow goes
// through a hash-only stub child. AddKnownNodeByID mirrors rippled's
// SHAMap::addKnownNode: it uses the peer-supplied SHAMapNodeID to drive
// descent, accepts the node when its computed hash matches the parent's
// stored child hash, and returns sentinel errors that let the caller
// distinguish "wrong data" from "need to fetch ancestors first."
func TestAddKnownNodeByID_RippledStyleReconstruct(t *testing.T) {
	source, err := New(TypeTransaction)
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	for branch := byte(0); branch < 4; branch++ {
		for sub := byte(0); sub < 4; sub++ {
			for i := byte(0); i < 4; i++ {
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

// Validates the sentinel-error contract so callers (router.handleTxSetData)
// can react: re-request ancestors on ErrParentNotInTree, drop the peer on
// ErrNodeHashMismatch, log-and-skip on ErrEmptyBranchOnPath.
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
