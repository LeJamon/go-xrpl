package shamap

import (
	"bytes"
	"errors"
	"testing"
)

func TestSme_PutItemWithNodeTypeOnImmutable(t *testing.T) {
	sm := New(TypeTransaction)
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	k := sme_keyFromByte(0x01)
	err := sm.putItemWithNodeType(NewItem(k, sme_data12(1)), NodeTypeTransactionNoMeta)
	if !errors.Is(err, ErrImmutable) {
		t.Errorf("putItemWithNodeType on immutable: want ErrImmutable, got %v", err)
	}
}

func TestSme_PutItemWithNodeTypeNilItem(t *testing.T) {
	sm := New(TypeTransaction)
	if err := sm.putItemWithNodeType(nil, NodeTypeTransactionNoMeta); !errors.Is(err, errNilItem) {
		t.Errorf("putItemWithNodeType(nil): want ErrNilItem, got %v", err)
	}
}

func TestSme_PutWithNodeTypeUpdate(t *testing.T) {
	sm := New(TypeTransaction)
	k := sme_keyFromByte(0x05)
	data1 := sme_data12(0xAA)
	data2 := sme_data12(0xBB)

	if err := sm.PutWithNodeType(k, data1, NodeTypeTransactionNoMeta); err != nil {
		t.Fatalf("PutWithNodeType (insert): %v", err)
	}
	if err := sm.PutWithNodeType(k, data2, NodeTypeTransactionNoMeta); err != nil {
		t.Fatalf("PutWithNodeType (update): %v", err)
	}
	item, ok, err := sm.Get(k)
	if err != nil || !ok {
		t.Fatalf("Get after update: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(item.Data(), data2) {
		t.Error("data not updated")
	}
}

func TestSme_DirtyUpWhileSyncingReturnsInvalidState(t *testing.T) {
	sm := New(TypeState)
	if err := sm.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	stack := newNodeStack()
	_, dirtyErr := sm.dirtyUp(stack, [32]byte{}, newInnerNode())
	if !errors.Is(dirtyErr, errInvalidState) {
		t.Errorf("dirtyUp while syncing: want ErrInvalidState, got %v", dirtyErr)
	}
}

func TestSme_AssignRootWithLeaf(t *testing.T) {
	sm := New(TypeState)
	k := sme_keyFromByte(0x01)
	item := NewItem(k, sme_data12(1))
	leaf, leafErr := newAccountStateLeafNode(item)
	if leafErr != nil {
		t.Fatalf("newAccountStateLeafNode: %v", leafErr)
	}
	if err := sm.assignRoot(leaf, k); err != nil {
		t.Errorf("assignRoot with leaf: %v", err)
	}
	if sm.tree.root == nil {
		t.Error("root must not be nil after assignRoot with leaf")
	}
}

func TestSme_DeleteAbsent(t *testing.T) {
	sm := New(TypeState)
	err := sm.Delete(sme_keyFromByte(0xFF))
	if !errors.Is(err, ErrItemNotFound) {
		t.Errorf("Delete absent: want ErrItemNotFound, got %v", err)
	}
}

func TestSme_DeleteImmutable(t *testing.T) {
	sm := New(TypeState)
	k := sme_keyFromByte(0x10)
	if err := sm.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	if err := sm.Delete(k); !errors.Is(err, ErrImmutable) {
		t.Errorf("Delete on immutable: want ErrImmutable, got %v", err)
	}
}

func TestSme_DeepSplit(t *testing.T) {
	sm := New(TypeState)
	k1 := hexToHash("1234500000000000000000000000000000000000000000000000000000000001")
	k2 := hexToHash("1234510000000000000000000000000000000000000000000000000000000002")

	for i, k := range [][32]byte{k1, k2} {
		if err := sm.Put(k, sme_data12(byte(i+1))); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	for _, k := range [][32]byte{k1, k2} {
		_, ok, err := sm.Get(k)
		if err != nil || !ok {
			t.Errorf("Get after deep split: ok=%v err=%v", ok, err)
		}
	}
}

func TestSme_ConsolidateAfterDeleteSingleSibling(t *testing.T) {
	sm := New(TypeState)
	k1 := hexToHash("f000000000000000000000000000000000000000000000000000000000000001")
	k2 := hexToHash("f100000000000000000000000000000000000000000000000000000000000002")

	if err := sm.Put(k1, sme_data12(1)); err != nil {
		t.Fatalf("Put k1: %v", err)
	}
	if err := sm.Put(k2, sme_data12(2)); err != nil {
		t.Fatalf("Put k2: %v", err)
	}
	if err := sm.Delete(k1); err != nil {
		t.Fatalf("Delete k1: %v", err)
	}

	_, ok, err := sm.Get(k2)
	if err != nil || !ok {
		t.Errorf("Get k2 after consolidation: ok=%v err=%v", ok, err)
	}
	_, ok, err = sm.Get(k1)
	if err != nil || ok {
		t.Errorf("Get k1 after delete: ok=%v err=%v", ok, err)
	}
}

func TestSme_PutItemImmutable(t *testing.T) {
	sm := New(TypeState)
	if err := sm.SetImmutable(); err != nil {
		t.Fatalf("SetImmutable: %v", err)
	}
	k := sme_keyFromByte(0x01)
	if err := sm.putItem(NewItem(k, sme_data12(1))); !errors.Is(err, ErrImmutable) {
		t.Errorf("PutItem on immutable: want ErrImmutable, got %v", err)
	}
}

func TestSme_GetBranchAtDepthBeyondMax(t *testing.T) {
	var k [32]byte
	k[0] = 0xFF
	if got := getBranchAtDepth(k, maxDepth); got != 0 {
		t.Errorf("getBranchAtDepth at MaxDepth = %d, want 0", got)
	}
	if got := getBranchAtDepth(k, maxDepth+10); got != 0 {
		t.Errorf("getBranchAtDepth beyond MaxDepth = %d, want 0", got)
	}
}

func TestSme_PutAndDeleteAll(t *testing.T) {
	sm := New(TypeState)
	keys := make([][32]byte, 32)
	for i := range keys {
		keys[i] = sme_keyFromByte(byte(i + 1))
		if err := sm.Put(keys[i], sme_data12(byte(i+1))); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	for i, k := range keys {
		if err := sm.Delete(k); err != nil {
			t.Fatalf("Delete %d: %v", i, err)
		}
	}
	h, _ := sm.Hash()
	if h != ([32]byte{}) {
		t.Errorf("empty map should have zero hash after all deletes, got %x", h[:8])
	}
}
