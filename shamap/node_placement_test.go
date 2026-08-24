package shamap

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
)

type nodePlacementTestFamily struct {
	*memoryFamily

	mu             sync.Mutex
	discoveryReads int
	placementReads int
}

func newNodePlacementTestFamily() *nodePlacementTestFamily {
	return &nodePlacementTestFamily{memoryFamily: newMemoryFamily()}
}

func (f *nodePlacementTestFamily) Fetch(ctx context.Context, _ [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.discoveryReads++
	f.mu.Unlock()
	return nil, nil
}

func (f *nodePlacementTestFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.Fetch(ctx, hash)
}

func (f *nodePlacementTestFamily) FetchForNodePlacement(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.placementReads++
	f.mu.Unlock()
	f.memoryFamily.mu.RLock()
	defer f.memoryFamily.mu.RUnlock()
	return bytes.Clone(f.memoryFamily.store[hash]), nil
}

func TestAddKnownNodeByIDHydratesRequiredAncestorForPlacement(t *testing.T) {
	source := New(TypeState)
	for branch := range byte(4) {
		for sub := range byte(4) {
			var key [32]byte
			key[0] = branch<<4 | sub
			key[1] = 0x80
			if err := source.Put(key, []byte{branch, sub, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	wire, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}

	var target WireNode
	var targetID NodeID
	for i := range wire {
		nodeID, parseErr := ParseNodeID(wire[i].NodeID)
		if parseErr == nil && nodeID.Depth() >= 2 {
			target = wire[i]
			targetID = nodeID
			break
		}
	}
	if targetID.Depth() < 2 {
		t.Fatal("test tree did not produce a deep node")
	}

	family := newNodePlacementTestFamily()
	for i := range wire {
		if bytes.Equal(wire[i].NodeID, target.NodeID) {
			continue
		}
		entry, entryErr := FlushEntryFromWire(wire[i].Data, 1, TypeState)
		if entryErr != nil {
			t.Fatalf("FlushEntryFromWire: %v", entryErr)
		}
		if err := family.memoryFamily.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
			t.Fatalf("StoreBatch: %v", err)
		}
	}

	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	result, entry, err := dest.AddKnownNodeByIDWithEntryContext(t.Context(), targetID, target.Data)
	if err != nil {
		t.Fatalf("AddKnownNodeByIDWithEntryContext: %v", err)
	}
	if result != NodeUseful {
		t.Fatalf("result = %v, want NodeUseful", result)
	}
	wantEntry, err := FlushEntryFromWire(target.Data, 1, TypeState)
	if err != nil {
		t.Fatalf("target FlushEntryFromWire: %v", err)
	}
	if entry.Hash != wantEntry.Hash {
		t.Fatalf("entry hash = %x, want %x", entry.Hash, wantEntry.Hash)
	}
	family.mu.Lock()
	defer family.mu.Unlock()
	if family.discoveryReads != 0 {
		t.Fatalf("discovery reads = %d, want 0", family.discoveryReads)
	}
	if family.placementReads == 0 || family.placementReads >= int(targetID.Depth()) {
		t.Fatalf("placement reads = %d, want 1..%d", family.placementReads, targetID.Depth()-1)
	}
}

func TestAddKnownNodeByIDPlacementHonorsContextCancellation(t *testing.T) {
	source := New(TypeState)
	var first, second [32]byte
	first[0], second[0] = 0x10, 0x11
	if err := source.Put(first, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := source.Put(second, []byte{2, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}); err != nil {
		t.Fatalf("Put second: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	wire, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}

	var target WireNode
	var targetID NodeID
	for i := range wire {
		nodeID, parseErr := ParseNodeID(wire[i].NodeID)
		if parseErr == nil && nodeID.Depth() >= 2 {
			target = wire[i]
			targetID = nodeID
			break
		}
	}
	if targetID.Depth() < 2 {
		t.Fatal("test tree did not produce a deep node")
	}

	family := newNodePlacementTestFamily()
	for i := range wire {
		entry, entryErr := FlushEntryFromWire(wire[i].Data, 1, TypeState)
		if entryErr != nil {
			t.Fatalf("FlushEntryFromWire: %v", entryErr)
		}
		if err := family.memoryFamily.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
			t.Fatalf("StoreBatch: %v", err)
		}
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, _, err := dest.AddKnownNodeByIDWithEntryContext(ctx, targetID, target.Data)
	if result != NodeInvalid || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled placement = (%v, %v), want (NodeInvalid, context.Canceled)", result, err)
	}
	result, _, err = dest.AddKnownNodeByIDWithEntryContext(t.Context(), targetID, target.Data)
	if err != nil || result != NodeUseful {
		t.Fatalf("retry placement = (%v, %v), want (NodeUseful, nil)", result, err)
	}
}
