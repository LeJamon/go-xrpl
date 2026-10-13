package shamap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type concurrentAcquisitionFamily struct {
	Family
	entered chan struct{}
	release chan struct{}
}

func (f *concurrentAcquisitionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.entered <- struct{}{}
	select {
	case <-f.release:
		return f.Family.Fetch(ctx, hash)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestConcurrentAcquisitionLoadChargesOnlyInstalledNode(t *testing.T) {
	source := New(TypeState)
	require.NoError(t, source.Put([32]byte{0x12}, make([]byte, 128)))
	hash, err := source.Hash()
	require.NoError(t, err)
	root, err := source.SerializeRoot()
	require.NoError(t, err)
	entries, err := collectDirtyForTest(source)
	require.NoError(t, err)
	base := newMemoryFamily()
	require.NoError(t, base.StoreBatch(t.Context(), entries))
	const readers = 16
	family := &concurrentAcquisitionFamily{Family: base, entered: make(chan struct{}, readers), release: make(chan struct{})}
	dest, err := NewBacked(TypeState, family)
	require.NoError(t, err)
	require.NoError(t, dest.AddRootNode(hash, root))
	type result struct {
		node mapNode
		err  error
	}
	results := make(chan result, readers)
	for range readers {
		go func() {
			dest.tree.mu.RLock()
			node, err := dest.descendCtx(t.Context(), dest.tree.root, 1)
			dest.tree.mu.RUnlock()
			results <- result{node, err}
		}()
	}
	ready := false
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for i := 0; i < readers; i++ {
		select {
		case <-family.entered:
			ready = i == readers-1
		case <-timer.C:
			i = readers
		}
	}
	close(family.release)
	var installed mapNode
	for range readers {
		got := <-results
		require.NoError(t, got.err)
		if installed == nil {
			installed = got.node
		}
		require.Same(t, installed, got.node)
	}
	require.True(t, ready, "all readers must fetch before any install")
	require.Equal(t, uint64(1), dest.BackedWalkStats().RetainedNodes)
	require.Equal(t, acquisitionNodeBytes(installed), dest.acquisition.budget.Snapshot().LoadedUsed)
	require.NoError(t, dest.AcknowledgePersistedAndReleaseContext(t.Context()))
	require.Zero(t, dest.acquisition.budget.Snapshot().Used)
}

func TestBackedMutableForkHasIndependentBoundedWalkBudget(t *testing.T) {
	source := New(TypeState)
	require.NoError(t, source.Put([32]byte{0x12}, make([]byte, 16)))
	hash, err := source.Hash()
	require.NoError(t, err)
	root, err := source.SerializeRoot()
	require.NoError(t, err)
	backed, err := NewBacked(TypeState, newMemoryFamily())
	require.NoError(t, err)
	_, err = backed.AddRootNodeWithEntry(hash, root)
	require.NoError(t, err)
	copied, err := backed.MutableFork()
	require.NoError(t, err)
	require.NoError(t, copied.StartSync())
	missing, err := copied.GetMissingNodesContext(t.Context(), 1, nil)
	require.NoError(t, err)
	require.Len(t, missing, 1)
	budget := copied.acquisition.budget
	require.Positive(t, budget.Snapshot().Limit)
	require.Positive(t, budget.Snapshot().CursorUsed)
	require.Zero(t, backed.acquisition.budget.Snapshot().Used)
	copied.ReleaseAcquisitionBudget()
	require.Zero(t, budget.Snapshot().Used)
}

func TestAcquisitionPersistsReplyWithoutRetainingOversizedNode(t *testing.T) {
	source := New(TypeState)
	key := [32]byte{0x12, 0x34}
	require.NoError(t, source.Put(key, make([]byte, 2048)))
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	require.Len(t, wire, 2)
	id, err := ParseNodeID(wire[1].NodeID)
	require.NoError(t, err)
	family := newMemoryFamily()
	dest, err := NewBacked(TypeState, family)
	require.NoError(t, err)
	budget := NewAcquisitionBudget(1024)
	dest.SetAcquisitionBudget(budget)
	rootEntry, err := dest.AddRootNodeWithEntry(rootHash, rootData)
	require.NoError(t, err)
	require.NoError(t, family.StoreBatch(t.Context(), []FlushEntry{rootEntry}))

	result, entry, err := dest.AddKnownNodeByIDWithEntryContext(t.Context(), id, wire[1].Data)
	require.NoError(t, err)
	require.Equal(t, NodeUseful, result)
	require.Nil(t, attachedNode(dest.tree.root, id))
	require.True(t, dest.BackedWalkStats().RetentionPressure)
	require.Zero(t, budget.Snapshot().Used)
	require.NoError(t, family.StoreBatch(t.Context(), []FlushEntry{entry}))
	require.NoError(t, dest.AcknowledgePersistedAndReleaseContext(t.Context()))
	dest.SetAcquisitionBudget(NewAcquisitionBudget(0))
	require.NoError(t, dest.FinishSyncContext(t.Context()))
	got, err := dest.Hash()
	require.NoError(t, err)
	require.Equal(t, rootHash, got)
}

func TestAcquisitionPressureStillChecksHashesAndPreservesDirtyData(t *testing.T) {
	source := New(TypeState)
	key := [32]byte{0x12, 0x34}
	require.NoError(t, source.Put(key, make([]byte, 2048)))
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	id, err := ParseNodeID(wire[1].NodeID)
	require.NoError(t, err)
	dest, err := NewBacked(TypeState, newMemoryFamily())
	require.NoError(t, err)
	dest.SetAcquisitionBudget(NewAcquisitionBudget(1024))
	require.NoError(t, dest.AddRootNode(rootHash, rootData))
	_, err = dest.AddKnownNodeByID(id, wire[1].Data)
	require.ErrorIs(t, err, ErrAcquisitionMemory)
	require.Nil(t, attachedNode(dest.tree.root, id))
	bad := append([]byte(nil), wire[1].Data...)
	bad[0] ^= 0xFF
	_, _, err = dest.AddKnownNodeByIDWithEntryContext(t.Context(), id, bad)
	require.ErrorIs(t, err, errNodeHashMismatch)
	require.Nil(t, attachedNode(dest.tree.root, id))
}

func TestPressureCheckpointPreservesIncompleteCursor(t *testing.T) {
	source := New(TypeState)
	for i := 0; i < 512; i++ {
		var key [32]byte
		key[0], key[1], key[31] = byte(i>>4), byte(i<<4), 1
		require.NoError(t, source.Put(key, make([]byte, 16)))
	}
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	entries, err := collectDirtyForTest(source)
	require.NoError(t, err)
	family := newMemoryFamily()
	var leaves []FlushEntry
	for _, entry := range entries {
		node, decodeErr := deserializeFromPrefix(entry.Data)
		require.NoError(t, decodeErr)
		if _, ok := node.(mapLeaf); ok {
			leaves = append(leaves, entry)
		} else {
			require.NoError(t, family.StoreBatch(t.Context(), []FlushEntry{entry}))
		}
	}
	dest, err := NewBacked(TypeState, family)
	require.NoError(t, err)
	budget := NewAcquisitionBudget(16 << 20)
	dest.SetAcquisitionBudget(budget)
	_, err = dest.AddRootNodeWithEntry(rootHash, rootData)
	require.NoError(t, err)
	missing, err := dest.GetMissingNodesContext(t.Context(), 8, nil)
	require.NoError(t, err)
	require.NotEmpty(t, missing)
	var offered FlushEntry
	for _, leaf := range leaves {
		if leaf.Hash == missing[0].Hash {
			offered = leaf
			break
		}
	}
	node, err := deserializeFromPrefix(offered.Data)
	require.NoError(t, err)
	data, err := node.SerializeForWire()
	require.NoError(t, err)
	result, entry, err := dest.AddKnownNodeByIDWithEntryContext(t.Context(), missing[0].NodeID, data)
	require.NoError(t, err)
	require.Equal(t, NodeUseful, result)
	require.NoError(t, family.StoreBatch(t.Context(), []FlushEntry{entry}))
	require.Positive(t, dest.BackedWalkStats().RetainedBytes)
	scan := dest.acquisition.scan
	require.NotNil(t, scan)
	held := budget.Snapshot().Limit - budget.Snapshot().Used
	require.True(t, budget.TryReserve(held))
	require.NoError(t, dest.AcknowledgePersistedContext(t.Context()))
	budget.Release(held)
	require.Same(t, scan, dest.acquisition.scan)
	require.Zero(t, dest.BackedWalkStats().RetainedBytes)
	for branch := range BranchFactor {
		child, _, _ := dest.tree.root.LoadChild(branch)
		require.Nil(t, child)
	}
	require.False(t, family.FullBelowCache().Has(family.FullBelowCache().Generation(), rootHash))
	require.NoError(t, family.StoreBatch(t.Context(), leaves))
	for pass := 0; ; pass++ {
		require.Less(t, pass, 1000)
		err = dest.FinishSyncContext(WithTraversalBudget(t.Context(), 64))
		if err == nil {
			break
		}
		if errors.Is(err, ErrAcquisitionMemory) {
			require.NoError(t, dest.AcknowledgePersistedAndReleaseContext(t.Context()))
		} else if strings.Contains(err.Error(), "sync incomplete: unresolved paths remain") {
			// A capped scan may finish its first pass after the previously
			// missing descendants became durable. The next pass rechecks that
			// frontier from the root.
			continue
		} else {
			require.ErrorIs(t, err, ErrTraversalBudget)
		}
	}
	got, err := dest.Hash()
	require.NoError(t, err)
	require.Equal(t, rootHash, got)
	require.NoError(t, dest.AcknowledgePersistedAndReleaseContext(t.Context()))
	dest.ReleaseAcquisitionBudget()
	require.Zero(t, budget.Snapshot().Used)
}

func TestDurableCompletionReleasesCursorBeforeNextRead(t *testing.T) {
	source := New(TypeState)
	require.NoError(t, source.Put([32]byte{0x12}, make([]byte, 128)))
	hash, err := source.Hash()
	require.NoError(t, err)
	root, err := source.SerializeRoot()
	require.NoError(t, err)
	entries, err := collectDirtyForTest(source)
	require.NoError(t, err)
	family := newMemoryFamily()
	require.NoError(t, family.StoreBatch(t.Context(), entries))
	dest, err := NewBacked(TypeState, family)
	require.NoError(t, err)
	require.NoError(t, dest.AddRootNode(hash, root))
	dest.tree.mu.RLock()
	_, err = dest.descendCtx(t.Context(), dest.tree.root, 1)
	dest.tree.mu.RUnlock()
	require.NoError(t, err)
	require.NoError(t, dest.AcknowledgePersistedContext(t.Context()))
	budget := dest.acquisition.budget
	require.Positive(t, budget.Snapshot().LoadedUsed)
	missing, err := dest.GetMissingNodesContext(t.Context(), 0, nil)
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Nil(t, dest.acquisition.scan)
	require.Zero(t, budget.Snapshot().CursorUsed)
	require.NoError(t, dest.AcknowledgePersistedContext(t.Context()))
	require.Zero(t, budget.Snapshot().Used)
}
