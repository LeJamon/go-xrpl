package shamap

import (
	"errors"
	"testing"
)

func TestSetVerifiedBaseContextReleasesScannerBudget(t *testing.T) {
	source := buildRandomState(t, 128)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	base := newMemoryFamily()
	if err := base.StoreBatch(t.Context(), entries); err != nil {
		t.Fatal(err)
	}
	dest, err := NewBacked(TypeState, &countingDurableFamily{base: base, reads: make(map[[32]byte]int)})
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	budget := NewAcquisitionBudget(1 << 20)
	dest.SetAcquisitionBudget(budget)
	if _, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 1), 1, nil); !errors.Is(err, ErrTraversalBudget) {
		t.Fatalf("bounded walk error = %v, want traversal budget", err)
	}
	if got := budget.Snapshot(); got.Used == 0 {
		t.Fatal("bounded walk did not reserve scanner memory")
	}
	if err := dest.SetVerifiedBaseContext(t.Context(), rootHash); err != nil {
		t.Fatal(err)
	}
	if got := budget.Snapshot(); got.Used != 0 || got.CursorUsed != 0 {
		t.Fatalf("verified-base replacement retained scanner budget: %+v", got)
	}
	if stats := dest.BackedWalkStats(); stats.CursorBytes != 0 || stats.ActiveFrames != 0 {
		t.Fatalf("verified-base replacement retained scanner work: %+v", stats)
	}
}

func TestScanMissingNodesEmptyBackedTreeCompletes(t *testing.T) {
	dest, err := NewBacked(TypeTransaction, newMemoryFamily())
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.StartSync(); err != nil {
		t.Fatal(err)
	}
	result, err := dest.ScanMissingNodesContext(t.Context(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != MissingNodesComplete || len(result.Nodes) != 0 {
		t.Fatalf("empty backed scan = %+v, want complete with no nodes", result)
	}
	if err := dest.FinishSyncContext(t.Context()); err != nil {
		t.Fatalf("empty backed finish = %v", err)
	}
}

func TestScannerMemoryPressureCannotSkipUnstartedLane(t *testing.T) {
	source := New(TypeState)
	if err := source.Put([32]byte{1}, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	family := newMemoryFamily()
	var leaf FlushEntry
	for _, entry := range entries {
		node, err := DeserializeFromPrefix(entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := node.(mapLeaf); ok {
			leaf = entry
			continue
		}
		if err := family.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
			t.Fatal(err)
		}
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	dest.SetAcquisitionBudget(NewAcquisitionBudget(4 * backedScanFrameBytes))
	for attempt := 0; attempt < 2; attempt++ {
		result, err := dest.ScanMissingNodesContext(t.Context(), 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != MissingNodesMemory {
			t.Fatalf("pressure attempt %d = %+v, want memory", attempt+1, result)
		}
	}
	if err := family.StoreBatch(t.Context(), []FlushEntry{leaf}); err != nil {
		t.Fatal(err)
	}
	dest.ReleaseAcquisitionBudget()
	dest.SetAcquisitionBudget(NewAcquisitionBudget(1 << 20))
	if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := dest.FinishSyncContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}
