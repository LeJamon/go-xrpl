package shamap

import (
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"testing"
)

type requestedHashFilter map[[32]byte]struct{}

func (f requestedHashFilter) ShouldFetch(hash [32]byte) bool {
	_, requested := f[hash]
	return !requested
}

func TestBackedWalkFrontierBoundedAndRetired(t *testing.T) {
	const leafCount = 8192
	source := New(TypeState)
	for i := range leafCount {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(i+1))
		digest := sha512.Sum512(encoded[:])
		var key [32]byte
		copy(key[:], digest[:32])
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
	innerEntries := make([]FlushEntry, 0, len(batch))
	leafEntries := make([]FlushEntry, 0, leafCount)
	for _, entry := range batch {
		node, err := DeserializeFromPrefix(entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		if _, inner := node.(InnerNodeReader); inner {
			innerEntries = append(innerEntries, entry)
		} else {
			leafEntries = append(leafEntries, entry)
		}
	}
	if len(leafEntries) != leafCount {
		t.Fatalf("fixture has %d leaves, want %d", len(leafEntries), leafCount)
	}
	if err := base.StoreBatch(t.Context(), innerEntries); err != nil {
		t.Fatal(err)
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

	budget := NewAcquisitionBudget(2 << 20)
	dest.SetAcquisitionBudget(budget)
	sawMemoryWait := false
	requested := make(requestedHashFilter)
	for i := 0; i < 200; i++ {
		missing, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 10_000), 32, requested)
		for _, node := range missing {
			requested[node.Hash] = struct{}{}
		}
		if errors.Is(err, ErrAcquisitionMemory) {
			sawMemoryWait = true
			break
		}
		if err != nil && !errors.Is(err, ErrTraversalBudget) {
			t.Fatalf("frontier slice %d: %v", i, err)
		}
		stats := dest.BackedWalkStats()
		if stats.BlockedItems >= leafCount {
			t.Fatalf("frontier retained all %d missing leaves", stats.BlockedItems)
		}
		if got := budget.Snapshot(); got.CursorUsed > got.Limit/4 || stats.CursorBytes > got.Limit/4 {
			t.Fatalf("cursor exceeded its partition: stats=%+v budget=%+v", stats, got)
		}
	}
	if !sawMemoryWait {
		t.Fatal("frontier never returned the resumable memory-pressure result")
	}

	if err := base.StoreBatch(t.Context(), leafEntries); err != nil {
		t.Fatal(err)
	}
	dest.ReleaseAcquisitionBudget()
	dest.ReleaseAcquisitionBudget()
	if got := budget.Snapshot(); got.Used != 0 || got.CursorUsed != 0 {
		t.Fatalf("retired cursor retained budget: %+v", got)
	}
	if stats := dest.BackedWalkStats(); stats.CursorBytes != 0 || stats.BlockedItems != 0 || stats.ActiveFrames != 0 {
		t.Fatalf("retired cursor retained work: %+v", stats)
	}

	replacement := NewAcquisitionBudget(8 << 20)
	dest.SetAcquisitionBudget(replacement)
	completed := false
	for i := 0; i < 8; i++ {
		missing, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 50_000), 32, nil)
		if errors.Is(err, ErrTraversalBudget) {
			continue
		}
		if err != nil {
			t.Fatalf("resume slice %d: %v", i, err)
		}
		if len(missing) == 0 {
			completed = true
			break
		}
	}
	if !completed {
		t.Fatal("walk did not complete after missing leaves became available")
	}
	if err := dest.FinishSyncContext(WithTraversalBudget(t.Context(), 50_000)); err != nil {
		t.Fatalf("FinishSyncContext: %v", err)
	}
	dest.ReleaseAcquisitionBudget()
	dest.ReleaseAcquisitionBudget()
	if got := replacement.Snapshot(); got.Used != 0 || got.CursorUsed != 0 {
		t.Fatalf("completed cursor retained budget: %+v", got)
	}
}
