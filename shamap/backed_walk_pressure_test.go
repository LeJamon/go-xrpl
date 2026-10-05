package shamap

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackedWalkSaturatedFrontierKeepsRequestWidth(t *testing.T) {
	fixture := newIssue2043MemoryFixture(t, 8192)
	budget := NewAcquisitionBudget(8 << 20)
	dest := fixture.newDestination(t, budget)
	defer dest.ReleaseAcquisitionBudget()
	requested := make(requestedHashFilter)
	saturated := false
	for pass := 0; pass < 128; pass++ {
		missing, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 8192), 256, requested)
		for _, node := range missing {
			requested[node.Hash] = struct{}{}
		}
		if errors.Is(err, ErrAcquisitionMemory) {
			saturated = true
			break
		}
		if err != nil && !errors.Is(err, ErrTraversalBudget) {
			t.Fatal(err)
		}
	}
	if !saturated || dest.BackedWalkStats().BlockedItems < 1024 {
		t.Fatalf("frontier did not saturate: %+v", dest.BackedWalkStats())
	}
	clear(requested)
	for peer := 0; peer < 6; peer++ {
		missing, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 8192), 128, requested)
		if err != nil && !errors.Is(err, ErrAcquisitionMemory) && !errors.Is(err, ErrTraversalBudget) {
			t.Fatal(err)
		}
		if len(missing) != 128 {
			t.Fatalf("saturated peer %d received %d node IDs, want 128; err=%v stats=%+v", peer, len(missing), err, dest.BackedWalkStats())
		}
		for _, node := range missing {
			if _, duplicate := requested[node.Hash]; duplicate {
				t.Fatal("duplicate peer assignment")
			}
			requested[node.Hash] = struct{}{}
		}
		if stats := budget.Snapshot(); stats.Used > stats.Limit || stats.CursorPeak > stats.Limit/4 {
			t.Fatalf("over budget: %+v", stats)
		}
	}
}

func TestBackedWalkPendingRotationNeedsNoAllocation(t *testing.T) {
	var queue backedWalkPendingQueue
	var used uint64
	reserve := func(n uint64) bool { used += n; return true }
	release := func(n uint64) {
		if n > used {
			t.Fatal("queue over-release")
		}
		used -= n
	}
	const entries = 1024
	for i := range entries {
		item := backedWalkPending{}
		binary.BigEndian.PutUint64(item.hash[:8], uint64(i))
		if err := queue.append(item, reserve); err != nil {
			t.Fatal(err)
		}
	}
	original := used
	allocations := testing.AllocsPerRun(1000, func() { queue.rotate(release) })
	if allocations != 0 || used != original || queue.len() != entries {
		t.Fatalf("rotation: allocations=%v bytes=%d/%d entries=%d", allocations, used, original, queue.len())
	}
	// AllocsPerRun performs one warmup before the measured rotations.
	for i := range entries {
		item, ok := queue.pop(release)
		want := uint64((i + 1001) % entries)
		if !ok || binary.BigEndian.Uint64(item.hash[:8]) != want {
			t.Fatalf("entry %d out of order", i)
		}
	}
	if used != 0 || queue.capacity() != 0 || queue.spare != nil {
		t.Fatalf("drained queue retains %d bytes", used)
	}
}

func TestBackedWalkMemoryPartialIsNotExhaustion(t *testing.T) {
	fixture := newIssue2043MemoryFixture(t, 128)
	budget := NewAcquisitionBudget(8 << 20)
	dest := fixture.newDestination(t, budget)
	defer dest.ReleaseAcquisitionBudget()
	holder := New(TypeState)
	cursor := &backedWalkCursor{budget: budget}
	held := budget.limit / 8
	if !holder.reservePendingBytes(cursor, held) {
		t.Fatal("reserve pending quota")
	}
	defer holder.releasePendingBytes(cursor, held)
	missing, err := dest.GetMissingNodesContext(t.Context(), 128, nil)
	if !errors.Is(err, ErrAcquisitionMemory) || len(missing) == 0 || len(missing) >= 128 {
		t.Fatalf("partial frontier nodes=%d error=%v", len(missing), err)
	}
	if dest.acquisition.cursor == nil || fixture.family.FullBelowCache().Has(fixture.family.FullBelowCache().Generation(), fixture.rootHash) {
		t.Fatal("partial frontier marked complete")
	}
}

func TestBackedWalkSuppressedRetriesKeepPartialStatus(t *testing.T) {
	source := New(TypeState)
	for i := range 10 {
		require.NoError(t, source.Put([32]byte{0x10 + byte(i)}, make([]byte, 12)))
	}
	root, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	batch, err := collectDirtyForTest(source)
	require.NoError(t, err)
	family := newMemoryFamily()
	for _, entry := range batch {
		node, err := DeserializeFromPrefix(entry.Data)
		require.NoError(t, err)
		if _, inner := node.(InnerNodeReader); inner {
			require.NoError(t, family.StoreBatch(t.Context(), []FlushEntry{entry}))
		}
	}
	dest, err := NewBacked(TypeState, family)
	require.NoError(t, err)
	require.NoError(t, dest.AddRootNode(root, rootData))
	first, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 256), backedWalkRetryBatch+1, nil)
	require.NoError(t, err)
	require.Len(t, first, backedWalkRetryBatch+1)
	partial, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 256), 32, nil)
	require.ErrorIs(t, err, ErrTraversalBudget)
	require.Len(t, partial, 1)
	completeFrontier, err := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), 256), 32, nil)
	require.NoError(t, err, "fully enumerated missing nodes do not imply a limited walk")
	require.Len(t, completeFrontier, 10)
}
