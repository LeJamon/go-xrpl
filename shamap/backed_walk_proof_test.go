package shamap

import (
	"errors"
	"testing"
)

func TestBackedWalkProofOuterCapacityIsBudgeted(t *testing.T) {
	budget := NewAcquisitionBudget(1 << 20)
	var proofs backedWalkProofs
	reserve := func(bytes uint64) bool { return budget.tryReserveCursor(bytes) }
	release := func(bytes uint64) { budget.releaseCursor(bytes) }

	for i := 0; i < backedWalkProofChunkSize*5; i++ {
		if err := proofs.addWithBudget([32]byte{byte(i)}, i, reserve, release); err != nil {
			t.Fatalf("add proof %d: %v", i, err)
		}
	}
	outerBytes := uint64(cap(proofs.chunks)) * backedWalkProofSliceBytes
	dataBytes := uint64(len(proofs.chunks)) * backedWalkProofChunkSize * backedWalkProofBytes
	if got := budget.Snapshot(); got.Used != outerBytes+dataBytes || got.CursorUsed != got.Used {
		t.Fatalf("proof reservation = %+v, want %d", got, outerBytes+dataBytes)
	}

	proofs.truncateWithRelease(1, release)
	if got := budget.Snapshot(); got.Used != outerBytes+backedWalkProofChunkSize*backedWalkProofBytes {
		t.Fatalf("truncated proof reservation = %+v, want %d", got, outerBytes+backedWalkProofChunkSize*backedWalkProofBytes)
	}
	proofs.clearWithRelease(release)
	if got := budget.Snapshot(); got.Used != 0 || got.CursorUsed != 0 {
		t.Fatalf("cleared proof reservation = %+v", got)
	}
}

func TestSetVerifiedBaseContextReleasesCursorBudget(t *testing.T) {
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
		t.Fatal("bounded walk did not reserve cursor memory")
	}
	if err := dest.SetVerifiedBaseContext(t.Context(), rootHash); err != nil {
		t.Fatal(err)
	}
	if got := budget.Snapshot(); got.Used != 0 || got.CursorUsed != 0 {
		t.Fatalf("verified-base replacement retained cursor budget: %+v", got)
	}
	if stats := dest.BackedWalkStats(); stats.CursorBytes != 0 || stats.ActiveFrames != 0 || stats.BlockedItems != 0 {
		t.Fatalf("verified-base replacement retained cursor work: %+v", stats)
	}
}

func TestBackedWalkPressureCannotSkipUnstartedLane(t *testing.T) {
	source := New(TypeState)
	key := [32]byte{1}
	if err := source.Put(key, make([]byte, 12)); err != nil {
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
	if len(leaf.Data) == 0 {
		t.Fatal("fixture has no leaf")
	}
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
	// Leave exactly one frame-sized cursor slot. The retry headroom is
	// admitted, while the first discovery frame must wait for capacity.
	budget := NewAcquisitionBudget(4 * backedWalkFrameBytes)
	dest.SetAcquisitionBudget(budget)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := dest.GetMissingNodesContext(t.Context(), 1, nil); !errors.Is(err, ErrAcquisitionMemory) {
			t.Fatalf("pressure attempt %d = %v, want acquisition memory", attempt+1, err)
		}
	}
	if err := dest.FinishSyncContext(t.Context()); !errors.Is(err, ErrAcquisitionMemory) {
		t.Fatalf("pressure finish = %v, want acquisition memory", err)
	}

	if err := family.StoreBatch(t.Context(), []FlushEntry{leaf}); err != nil {
		t.Fatal(err)
	}
	dest.ReleaseAcquisitionBudget()
	dest.SetAcquisitionBudget(NewAcquisitionBudget(1 << 20))
	if err := dest.FinishSyncContext(t.Context()); err != nil {
		t.Fatalf("finish after pressure recovery: %v", err)
	}
}
