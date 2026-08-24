package shamap

import (
	"context"
	"errors"
	"testing"
)

func TestKnownNodeAcquisitionRejectsLeafAtWrongNodeID(t *testing.T) {
	actualKey := sme_keyFromByte(0x13)
	leaf, err := newAccountStateLeafNode(NewItem(actualKey, sme_data12(1)))
	if err != nil {
		t.Fatalf("newAccountStateLeafNode: %v", err)
	}

	inner := newInnerNode()
	if err := inner.SetChild(3, leaf); err != nil {
		t.Fatalf("inner SetChild: %v", err)
	}
	root := newInnerNode()
	if err := root.SetChild(2, inner); err != nil {
		t.Fatalf("root SetChild: %v", err)
	}

	rootData, err := root.SerializeForWire()
	if err != nil {
		t.Fatalf("root SerializeForWire: %v", err)
	}
	innerWire, err := inner.SerializeForWire()
	if err != nil {
		t.Fatalf("inner SerializeForWire: %v", err)
	}
	innerPrefix, err := inner.SerializeWithPrefix()
	if err != nil {
		t.Fatalf("inner SerializeWithPrefix: %v", err)
	}
	leafWire, err := leaf.SerializeForWire()
	if err != nil {
		t.Fatalf("leaf SerializeForWire: %v", err)
	}
	leafPrefix, err := leaf.SerializeWithPrefix()
	if err != nil {
		t.Fatalf("leaf SerializeWithPrefix: %v", err)
	}

	claimedInnerID, err := createNodeID(1, sme_keyFromByte(0x20))
	if err != nil {
		t.Fatalf("create inner NodeID: %v", err)
	}
	claimedLeafID, err := createNodeID(2, sme_keyFromByte(0x23))
	if err != nil {
		t.Fatalf("create leaf NodeID: %v", err)
	}

	tests := []struct {
		name      string
		innerData []byte
		leafData  []byte
		add       func(*SHAMap, NodeID, []byte) (AddNodeResult, FlushEntry, error)
	}{
		{
			name:      "wire",
			innerData: innerWire,
			leafData:  leafWire,
			add: func(sm *SHAMap, nodeID NodeID, data []byte) (AddNodeResult, FlushEntry, error) {
				return sm.AddKnownNodeByIDWithEntryContext(context.Background(), nodeID, data)
			},
		},
		{
			name:      "prefix",
			innerData: innerPrefix,
			leafData:  leafPrefix,
			add:       (*SHAMap).AddKnownNodeFromPrefixWithEntry,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dest := New(TypeState)
			if err := dest.StartSync(); err != nil {
				t.Fatalf("StartSync: %v", err)
			}
			if err := dest.AddRootNode(root.Hash(), rootData); err != nil {
				t.Fatalf("AddRootNode: %v", err)
			}

			result, entry, err := test.add(dest, claimedInnerID, test.innerData)
			if err != nil || result != NodeUseful || len(entry.Data) == 0 {
				t.Fatalf("attach inner: got (%v, data=%d, %v), want (NodeUseful, non-empty, nil)", result, len(entry.Data), err)
			}

			result, entry, err = test.add(dest, claimedLeafID, test.leafData)
			if result != NodeInvalid || !errors.Is(err, errUnexpectedNode) {
				t.Fatalf("attach mismatched leaf: got (%v, %v), want (NodeInvalid, errUnexpectedNode)", result, err)
			}
			if entry.Hash != ([32]byte{}) || entry.Data != nil || entry.LedgerSeq != 0 || entry.MapType != 0 {
				t.Fatalf("rejected leaf returned FlushEntry: %+v", entry)
			}

			dest.tree.mu.RLock()
			state := dest.tree.state
			loadedInner, _, isSet := dest.tree.root.LoadChild(2)
			dest.tree.mu.RUnlock()
			if state != stateSyncing {
				t.Fatalf("map state = %v, want stateSyncing", state)
			}
			attachedInner, ok := loadedInner.(*innerNode)
			if !isSet || !ok {
				t.Fatalf("branch 2 = (%T, set=%v), want loaded inner", loadedInner, isSet)
			}
			attachedLeaf, childHash, isSet := attachedInner.LoadChild(3)
			if !isSet || attachedLeaf != nil || childHash != leaf.Hash() {
				t.Fatalf("branch 3 = (%T, %x, set=%v), want hash-only leaf %x", attachedLeaf, childHash, isSet, leaf.Hash())
			}
		})
	}
}
