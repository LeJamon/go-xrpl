package shamap

import (
	"encoding/hex"
	"sort"
	"testing"
)

// TestAddKnownNodeByID_OutOfOrderConverges feeds a multi-level tree's wire
// nodes deepest-first — the worst case for ancestor gaps — and asserts that
// off-frontier nodes return NodeReRequest (never an error) and that the map
// still converges to the source root. This is the regression for issue #1143:
// fat-reply / off-frontier nodes must follow rippled's "attach-or-re-request"
// instead of flooding the reject log with ErrParentNotInTree.
func TestAddKnownNodeByID_OutOfOrderConverges(t *testing.T) {
	source := New(TypeTransaction)

	// Three keys sharing the first two bytes force a deep (depth>=3) chain;
	// the spread keys give the tree breadth so gaps appear at several levels.
	put := func(k [32]byte) {
		if err := source.Put(k, []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	for i := range 3 {
		var k [32]byte
		k[0], k[1], k[2] = 0xAA, 0xAA, byte(i)
		put(k)
	}
	for i := range 240 {
		var k [32]byte
		k[0], k[1], k[2] = byte(i), byte(i*7+1), byte(i*13+2)
		put(k)
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

	type item struct {
		id   NodeID
		data []byte
		key  string
	}
	var list []item
	maxDepth := 0
	for _, w := range wireNodes {
		nid, err := UnmarshalBinary(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		if nid.IsRoot() {
			continue
		}
		if int(nid.Depth()) > maxDepth {
			maxDepth = int(nid.Depth())
		}
		list = append(list, item{id: nid, data: w.Data, key: hex.EncodeToString(w.NodeID)})
	}
	if maxDepth < 3 {
		t.Fatalf("test setup too shallow: maxDepth=%d, want >=3", maxDepth)
	}
	// Deepest-first: every node arrives before its parent — the worst case.
	sort.SliceStable(list, func(i, j int) bool { return list[i].id.Depth() > list[j].id.Depth() })

	dest := New(TypeTransaction)
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	placed := make(map[string]bool, len(list))
	sawReRequest := false
	for round := 0; len(placed) < len(list); round++ {
		if round > maxDepth+1 {
			t.Fatalf("did not converge: %d/%d placed after %d rounds", len(placed), len(list), round)
		}
		progressed := 0
		for _, it := range list {
			if placed[it.key] {
				continue
			}
			res, err := dest.AddKnownNodeByID(it.id, it.data)
			if err != nil {
				t.Fatalf("out-of-order delivery must never error, got %v at depth=%d", err, it.id.Depth())
			}
			switch res {
			case NodeUseful:
				placed[it.key] = true
				progressed++
			case NodeDuplicate:
				placed[it.key] = true
			case NodeReRequest:
				sawReRequest = true
			default:
				t.Fatalf("unexpected NodeInvalid at depth=%d", it.id.Depth())
			}
		}
		if progressed == 0 {
			t.Fatalf("stalled: %d/%d placed, no progress this round", len(placed), len(list))
		}
	}

	if !sawReRequest {
		t.Fatal("deepest-first delivery never exercised NodeReRequest")
	}

	if err := dest.FinishSync(); err != nil {
		t.Fatalf("FinishSync: %v", err)
	}
	destHash, err := dest.Hash()
	if err != nil {
		t.Fatalf("dest hash: %v", err)
	}
	if destHash != rootHash {
		t.Errorf("reconstructed root mismatch: want %x got %x", rootHash[:8], destHash[:8])
	}
}
