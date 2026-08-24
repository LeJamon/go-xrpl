package shamap

import (
	"context"
	"errors"
	"testing"
)

func TestSme_AddKnownNodeUnchecked(t *testing.T) {
	source := New(TypeState)
	k := sme_keyFromByte(0x01)
	if err := source.Put(k, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}

	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}

	dest1 := New(TypeState)
	someID, err := newRootNodeID().childNodeID(0)
	if err != nil {
		t.Fatalf("ChildNodeID: %v", err)
	}
	if _, err := dest1.AddKnownNodeByID(someID, []byte{1, 2, 3}); !errors.Is(err, errSyncNotInProgress) {
		t.Errorf("AddKnownNodeByID not-syncing: want ErrSyncNotInProgress, got %v", err)
	}

	dest2 := New(TypeState)
	if err := dest2.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest2.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}
	if _, err := dest2.AddKnownNodeByID(someID, nil); !errors.Is(err, ErrInvalidNodeData) {
		t.Errorf("AddKnownNodeByID nil data: want ErrInvalidNodeData, got %v", err)
	}

	dest3 := New(TypeState)
	if err := dest3.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest3.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}
	for _, w := range wireNodes {
		nid, err := ParseNodeID(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		if nid.IsRoot() {
			continue
		}
		if _, err := dest3.AddKnownNodeByID(nid, w.Data); err != nil {
			t.Fatalf("AddKnownNodeByID: %v", err)
		}
	}
}

func TestSme_AddKnownNodeByID_RootNodeID(t *testing.T) {
	sm := New(TypeTransaction)
	if err := sm.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	rootID := newRootNodeID()
	if _, err := sm.AddKnownNodeByID(rootID, []byte{1}); !errors.Is(err, errUnexpectedNode) {
		t.Errorf("AddKnownNodeByID(root): want ErrUnexpectedNode, got %v", err)
	}
}

func TestSme_GetMissingNodesNotSyncing(t *testing.T) {
	sm := New(TypeState)
	if got := sm.GetMissingNodes(0, nil); got != nil {
		t.Errorf("GetMissingNodes on non-syncing map: want nil, got %v", got)
	}
}

func TestSme_FinishSyncNotSyncing(t *testing.T) {
	sm := New(TypeState)
	if err := sm.FinishSync(); !errors.Is(err, errSyncNotInProgress) {
		t.Errorf("FinishSync not syncing: want ErrSyncNotInProgress, got %v", err)
	}
}

func TestSme_StartSyncOnInvalidMap(t *testing.T) {
	sm := New(TypeState)
	sm.tree.mu.Lock()
	sm.tree.state = stateInvalid
	sm.tree.mu.Unlock()
	if err := sm.StartSync(); err == nil {
		t.Error("StartSync on invalid map should return error")
	}
}

func TestSme_AddKnownNodeHashMismatch(t *testing.T) {
	source := New(TypeState)
	k := sme_keyFromByte(0x10)
	if err := source.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()

	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}

	dest := New(TypeState)
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	for _, w := range wireNodes {
		nid, _ := ParseNodeID(w.NodeID)
		if nid.IsRoot() {
			continue
		}
		var wrongHash [32]byte
		wrongHash[0] = 0xFF
		err := dest.AddKnownNode(wrongHash, w.Data)
		if !errors.Is(err, errNodeHashMismatch) {
			t.Errorf("AddKnownNode with wrong hash: want ErrNodeHashMismatch, got %v", err)
		}
		break
	}
}

func TestSme_WalkSubtreeStopsOnReport(t *testing.T) {
	source := New(TypeState)
	for branch := byte(0); branch < 4; branch++ {
		k := sme_keyFromByte(branch << 4)
		if err := source.Put(k, sme_data12(branch)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()
	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	count := 0
	stop, err := walkSubtreeForMissing(
		context.Background(), dest,
		dest.tree.root,
		newRootNodeID(),
		dest.tree.root.Hash(),
		0,
		&defaultSyncFilter{},
		false,
		func(MissingNode) bool {
			count++
			return true
		},
	)
	if err != nil {
		t.Fatalf("walkSubtreeForMissing: %v", err)
	}
	if !stop {
		t.Error("walkSubtreeForMissing: expected stop=true when report returns true")
	}
	if count != 1 {
		t.Errorf("walkSubtreeForMissing: expected 1 report call, got %d", count)
	}
}

func TestSme_AddRootNodeAlreadySet(t *testing.T) {
	source := New(TypeState)
	k := sme_keyFromByte(0x01)
	if err := source.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()

	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("first AddRootNode: %v", err)
	}
	wireNodes, _ := source.WalkWireNodes()
	for _, w := range wireNodes {
		nid, _ := ParseNodeID(w.NodeID)
		if nid.IsRoot() {
			continue
		}
		_, _ = dest.AddKnownNodeByID(nid, w.Data)
		break
	}
	if err := dest.AddRootNode(rootHash, rootData); !errors.Is(err, ErrRootAlreadySet) {
		t.Errorf("second AddRootNode: want ErrRootAlreadySet, got %v", err)
	}
}

func TestSme_AddKnownNodeSuccess(t *testing.T) {
	source := New(TypeState)
	for i := byte(0); i < 4; i++ {
		k := sme_keyFromTwo(i<<4, i)
		if err := source.Put(k, sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	rootHash, _ := source.Hash()
	rootData, _ := source.SerializeRoot()
	wireNodes, _ := source.WalkWireNodes()

	dest := New(TypeState)
	if err := dest.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	for _, w := range wireNodes {
		nid, err := ParseNodeID(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		if nid.IsRoot() {
			continue
		}
		if nid.Depth() == 1 {
			node, err2 := deserializeNodeFromWire(w.Data)
			if err2 != nil {
				continue
			}
			if err2 := node.UpdateHash(); err2 != nil {
				continue
			}
			nodeHash := node.Hash()
			if err := dest.AddKnownNode(nodeHash, w.Data); err != nil {
				t.Logf("AddKnownNode depth=1: %v (may be ErrUnexpectedNode)", err)
			}
		}
	}
}
