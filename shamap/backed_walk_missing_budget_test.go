package shamap

import (
	"errors"
	"testing"
)

func TestBackedWalkBudgetReturnsPartialMissingFrontier(t *testing.T) {
	source := New(TypeState)
	for _, key := range [][32]byte{{0x01}, {0x02}, {0x03}} {
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	base := newMemoryFamily()
	for _, entry := range batch {
		node, err := DeserializeFromPrefix(entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		if _, inner := node.(InnerNodeReader); inner {
			if err := base.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
				t.Fatal(err)
			}
		}
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	for range len(batch) + 1 {
		missing, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 1), 32, nil)
		if errors.Is(err, ErrTraversalBudget) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(missing) == 0 {
			t.Fatal("missing leaves were not reported")
		}
		if reads := family.totalReads(); reads > len(batch) {
			t.Fatalf("walk repeated missing reads before reporting the frontier: %d", reads)
		}
		return
	}
	t.Fatal("missing-node retries starved unvisited siblings across traversal slices")
}

func TestBackedWalkBudgetDefersUnavailableMissingBeforeSibling(t *testing.T) {
	source := New(TypeState)
	keys := [][32]byte{{0x10}, {0x11}, {0x12}}
	for _, key := range keys {
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	base := newMemoryFamily()
	for _, entry := range batch {
		node, err := DeserializeFromPrefix(entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		if _, inner := node.(InnerNodeReader); inner {
			if err := base.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
				t.Fatal(err)
			}
		}
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	var first []MissingNode
	for range len(batch) + 1 {
		first, err = dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 1), 1, nil)
		if errors.Is(err, ErrTraversalBudget) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if len(first) != 1 {
		t.Fatalf("first frontier = %v, want one missing sibling", first)
	}

	second, err := dest.GetMissingNodesContext(
		WithTraversalBudget(t.Context(), 1), 1, excludingHashFilter{hash: first[0].Hash})
	if err != nil {
		t.Fatalf("second frontier: %v", err)
	}
	if len(second) != 1 || second[0].Hash == first[0].Hash {
		t.Fatalf("second frontier = %v, want a different undiscovered sibling", second)
	}
}

func TestBackedWalkDeferredMissingNodeCannotFinishSync(t *testing.T) {
	source := New(TypeState)
	missingKey := [32]byte{0x10}
	for _, key := range [][32]byte{missingKey, {0x11}} {
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	base := newMemoryFamily()
	var absent FlushEntry
	for _, entry := range batch {
		node, err := DeserializeFromPrefix(entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		if leaf, ok := node.(mapLeaf); ok && leaf.Item().Key() == missingKey {
			absent = entry
			continue
		}
		if err := base.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
			t.Fatal(err)
		}
	}
	if len(absent.Data) == 0 {
		t.Fatal("fixture has no missing leaf")
	}
	family := &countingDurableFamily{base: base, reads: make(map[[32]byte]int)}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	if err := dest.StartSync(); err != nil {
		t.Fatal(err)
	}
	missing, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 2), 1, nil)
	if err != nil || len(missing) != 1 || missing[0].Hash != absent.Hash {
		t.Fatalf("initial missing frontier = %v, %v; want missing leaf", missing, err)
	}
	if err := dest.FinishSyncContext(WithTraversalBudget(t.Context(), 32)); !errors.Is(err, ErrTraversalBudget) {
		t.Fatalf("finish with a deferred missing leaf = %v; want another traversal slice", err)
	}
	if dest.tree.state != stateSyncing {
		t.Fatal("incomplete tree left synchronization")
	}
	missing, err = dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 32), 1, nil)
	if err != nil || len(missing) != 1 || missing[0].Hash != absent.Hash {
		t.Fatalf("resumed missing frontier = %v, %v; want deferred leaf", missing, err)
	}
	if err := base.StoreBatch(t.Context(), []FlushEntry{absent}); err != nil {
		t.Fatal(err)
	}
	if err := dest.FinishSyncContext(WithTraversalBudget(t.Context(), 32)); err != nil {
		t.Fatalf("finish after storing missing leaf: %v", err)
	}
}
