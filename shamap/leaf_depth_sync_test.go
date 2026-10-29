package shamap

import (
	"errors"
	"testing"
)

// leafDepthChain builds a root plus a single-branch chain of inner nodes whose
// last inner sits at leaf depth, the shape no valid SHAMap can have.
func leafDepthChain(t *testing.T) []*innerNode {
	t.Helper()
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
	return chain
}

// syncingLeafDepthMap starts syncing against the chain's root and attaches
// every inner above leaf depth, leaving only the leaf-depth node to add.
func syncingLeafDepthMap(t *testing.T, chain []*innerNode) *SHAMap {
	t.Helper()
	root := chain[len(chain)-1]
	rootData, err := root.SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}
	sm := New(TypeState)
	if err := sm.StartSync(); err != nil {
		t.Fatal(err)
	}
	if err := sm.AddRootNode(root.Hash(), rootData); err != nil {
		t.Fatal(err)
	}
	for depth := 1; depth < maxDepth; depth++ {
		node := chain[len(chain)-1-depth]
		data, err := node.SerializeForWire()
		if err != nil {
			t.Fatal(err)
		}
		id, err := createNodeID(uint8(depth), [32]byte{})
		if err != nil {
			t.Fatal(err)
		}
		if result, err := sm.AddKnownNodeByID(id, data); result != NodeUseful || err != nil {
			t.Fatalf("depth %d: %v, %v", depth, result, err)
		}
	}
	return sm
}

// Based on rippled SHAMapSync tests for XRPLF/rippled#8084 and #8086.
func TestAddKnownNodeInnerAtLeafDepth(t *testing.T) {
	chain := leafDepthChain(t)
	leafDepthID, err := createNodeID(maxDepth, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	bottomData, err := chain[0].SerializeForWire()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("hash mismatch is judged before depth", func(t *testing.T) {
		sm := syncingLeafDepthMap(t, chain)
		other := newInnerNode()
		other.hashes[1] = [32]byte{2}
		other.isBranch = 1 << 1
		if err := other.UpdateHash(); err != nil {
			t.Fatal(err)
		}
		otherData, err := other.SerializeForWire()
		if err != nil {
			t.Fatal(err)
		}
		result, err := sm.AddKnownNodeByID(leafDepthID, otherData)
		if result != NodeInvalid || !errors.Is(err, errNodeHashMismatch) {
			t.Fatalf("got %v, %v, want a hash mismatch", result, err)
		}
		if sm.tree.state != stateSyncing {
			t.Fatal("an unverified node invalidated the map")
		}
	})

	t.Run("verified inner invalidates the map", func(t *testing.T) {
		sm := syncingLeafDepthMap(t, chain)
		result, err := sm.AddKnownNodeByID(leafDepthID, bottomData)
		if result != NodeInvalid || !errors.Is(err, ErrMapInvalid) {
			t.Fatalf("got %v, %v, want ErrMapInvalid", result, err)
		}
		if sm.tree.state != stateInvalid {
			t.Fatal("map not marked invalid")
		}
		if result, err := sm.AddKnownNodeByID(leafDepthID, bottomData); result != NodeDuplicate || err != nil {
			t.Fatalf("add after invalidation = %v, %v, want duplicate", result, err)
		}
		if missing, err := sm.GetMissingNodesContext(t.Context(), 1, nil); !errors.Is(err, ErrMapInvalid) || len(missing) != 0 {
			t.Fatalf("missing nodes = %v, %v, want ErrMapInvalid", missing, err)
		}
		if result, err := sm.ScanMissingNodesContext(t.Context(), 1, nil); !errors.Is(err, ErrMapInvalid) || len(result.Nodes) != 0 {
			t.Fatalf("scan = %v, %v, want ErrMapInvalid", result, err)
		}
	})
}
