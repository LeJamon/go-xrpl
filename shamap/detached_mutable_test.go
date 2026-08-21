package shamap

import (
	"bytes"
	"testing"
)

func TestDetachedMutableDoesNotFlushAndIsolatesLoadedTree(t *testing.T) {
	family := newMemoryFamily()
	source, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	source.SetLedgerSeq(47)

	first := [32]byte{0x10}
	second := [32]byte{0x20}
	firstData := bytes.Repeat([]byte{1}, 12)
	secondData := bytes.Repeat([]byte{2}, 12)
	if err := source.Put(first, firstData); err != nil {
		t.Fatalf("put first item: %v", err)
	}
	if err := source.Put(second, secondData); err != nil {
		t.Fatalf("put second item: %v", err)
	}

	detached, err := source.DetachedMutable()
	if err != nil {
		t.Fatalf("DetachedMutable: %v", err)
	}
	if got := family.Len(); got != 0 {
		t.Fatalf("DetachedMutable stored %d nodes, want 0", got)
	}
	if detached.tree.mapType != source.tree.mapType ||
		detached.tree.ledgerSeq != source.tree.ledgerSeq {
		t.Fatal("detached map did not preserve tree metadata")
	}
	if detached.backing.access != source.backing.access ||
		detached.backing.fullBelow != source.backing.fullBelow {
		t.Fatal("detached map did not preserve backing access")
	}
	assertLoadedNodesDetached(t, source.tree.root, detached.tree.root)

	sourceData := bytes.Repeat([]byte{3}, 12)
	detachedData := bytes.Repeat([]byte{4}, 12)
	if err := source.Put(first, sourceData); err != nil {
		t.Fatalf("mutate source: %v", err)
	}
	if err := detached.Put(first, detachedData); err != nil {
		t.Fatalf("mutate detached map: %v", err)
	}
	assertItemData(t, source, first, sourceData)
	assertItemData(t, detached, first, detachedData)
	assertItemData(t, source, second, secondData)
	assertItemData(t, detached, second, secondData)
}

func TestDetachedMutablePreservesHashBackedBranches(t *testing.T) {
	family := newMemoryFamily()
	resident, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	key := [32]byte{0x70}
	data := bytes.Repeat([]byte{7}, 12)
	if err := resident.Put(key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	immutable, err := resident.SnapshotImmutable()
	if err != nil {
		t.Fatalf("SnapshotImmutable: %v", err)
	}
	rootHash, err := immutable.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	source, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}
	branch := getBranchAtDepth(key, 0)
	if child, _, _ := source.tree.root.LoadChild(branch); child != nil {
		t.Fatal("new backed source unexpectedly has a loaded child")
	}

	detached, err := source.DetachedMutable()
	if err != nil {
		t.Fatalf("DetachedMutable: %v", err)
	}
	if child, hash, present := detached.tree.root.LoadChild(branch); child != nil || !present || isZeroHash(hash) {
		t.Fatalf("detached branch = (%T, %x, %v), want nil child with stored hash", child, hash, present)
	}
	assertLoadedNodesDetached(t, source.tree.root, detached.tree.root)

	assertItemData(t, detached, key, data)
	if child, _, _ := source.tree.root.LoadChild(branch); child != nil {
		t.Fatal("loading a detached branch attached it to the source")
	}
}

func TestDetachedMutablePersistsResidentTreesToIndependentFamilies(t *testing.T) {
	initialFamily := newMemoryFamily()
	source, err := NewBacked(TypeState, initialFamily)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	key := [32]byte{0x90}
	if err := source.Put(key, bytes.Repeat([]byte{1}, 12)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	detached, err := source.DetachedMutable()
	if err != nil {
		t.Fatalf("DetachedMutable: %v", err)
	}

	sourceFamily := newMemoryFamily()
	detachedFamily := newMemoryFamily()
	source.SetFamily(sourceFamily)
	detached.SetFamily(detachedFamily)
	sourceData := bytes.Repeat([]byte{5}, 12)
	detachedData := bytes.Repeat([]byte{6}, 12)
	if err := source.Put(key, sourceData); err != nil {
		t.Fatalf("mutate source: %v", err)
	}
	if err := detached.Put(key, detachedData); err != nil {
		t.Fatalf("mutate detached map: %v", err)
	}

	if _, err := source.SnapshotImmutable(); err != nil {
		t.Fatalf("persist source: %v", err)
	}
	if _, err := detached.SnapshotImmutable(); err != nil {
		t.Fatalf("persist detached map: %v", err)
	}
	if sourceFamily.Len() == 0 || detachedFamily.Len() == 0 {
		t.Fatalf("stored node counts = source %d, detached %d; want both non-zero", sourceFamily.Len(), detachedFamily.Len())
	}

	sourceHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source Hash: %v", err)
	}
	detachedHash, err := detached.Hash()
	if err != nil {
		t.Fatalf("detached Hash: %v", err)
	}
	storedSource, err := NewFromRootHash(TypeState, sourceHash, sourceFamily)
	if err != nil {
		t.Fatalf("reload source: %v", err)
	}
	storedDetached, err := NewFromRootHash(TypeState, detachedHash, detachedFamily)
	if err != nil {
		t.Fatalf("reload detached map: %v", err)
	}
	assertItemData(t, storedSource, key, sourceData)
	assertItemData(t, storedDetached, key, detachedData)
}

func assertLoadedNodesDetached(t *testing.T, source, detached mapNode) {
	t.Helper()
	if source == detached {
		t.Fatalf("loaded node %T is shared", source)
	}
	if source.Hash() != detached.Hash() || source.IsDirty() != detached.IsDirty() {
		t.Fatalf("loaded node state differs: source %T, detached %T", source, detached)
	}

	switch sourceNode := source.(type) {
	case *innerNode:
		detachedNode, ok := detached.(*innerNode)
		if !ok {
			t.Fatalf("detached node type = %T, want *innerNode", detached)
		}
		for branch := range BranchFactor {
			sourceChild, sourceHash, sourcePresent := sourceNode.LoadChild(branch)
			detachedChild, detachedHash, detachedPresent := detachedNode.LoadChild(branch)
			if sourceHash != detachedHash || sourcePresent != detachedPresent {
				t.Fatalf("branch %d metadata differs", branch)
			}
			if (sourceChild == nil) != (detachedChild == nil) {
				t.Fatalf("branch %d residency differs", branch)
			}
			if sourceChild != nil {
				assertLoadedNodesDetached(t, sourceChild, detachedChild)
			}
		}
	case *leafNode:
		detachedNode, ok := detached.(*leafNode)
		if !ok {
			t.Fatalf("detached node type = %T, want *leafNode", detached)
		}
		if sourceNode.item == detachedNode.item {
			t.Fatal("leaf item is shared")
		}
		if !sourceNode.item.Equal(detachedNode.item) {
			t.Fatal("detached leaf item differs from source")
		}
	default:
		t.Fatalf("unexpected source node type %T", source)
	}
}

func assertItemData(t *testing.T, sm *SHAMap, key [32]byte, want []byte) {
	t.Helper()
	item, found, err := sm.Get(key)
	if err != nil {
		t.Fatalf("Get(%x): %v", key, err)
	}
	if !found {
		t.Fatalf("Get(%x) did not find item", key)
	}
	if got := item.Data(); !bytes.Equal(got, want) {
		t.Fatalf("Get(%x) = %x; want %x", key, got, want)
	}
}
