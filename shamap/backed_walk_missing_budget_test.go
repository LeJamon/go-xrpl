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
