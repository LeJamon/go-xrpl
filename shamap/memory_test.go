package shamap

import (
	"context"
	"testing"
)

func TestRetainedMemoryBytesCountsResidentStructureAndPayloadCapacity(t *testing.T) {
	shared := newRetainedMemoryTestMap(t, [][32]byte{{0x12}, {0x13}}, 128)
	divergent := newRetainedMemoryTestMap(t, [][32]byte{{0x12}, {0x22}}, 128)

	sharedBytes, err := shared.RetainedMemoryBytes()
	if err != nil {
		t.Fatalf("shared RetainedMemoryBytes: %v", err)
	}
	divergentBytes, err := divergent.RetainedMemoryBytes()
	if err != nil {
		t.Fatalf("divergent RetainedMemoryBytes: %v", err)
	}
	if sharedBytes <= divergentBytes {
		t.Fatalf("shared-prefix tree bytes = %d, divergent tree bytes = %d; shared inner node was not retained", sharedBytes, divergentBytes)
	}

	const expandedCapacity = 512
	expanded := newRetainedMemoryTestMap(t, [][32]byte{{0x12}, {0x13}}, expandedCapacity)
	expandedBytes, err := expanded.RetainedMemoryBytes()
	if err != nil {
		t.Fatalf("expanded RetainedMemoryBytes: %v", err)
	}
	wantIncrease := uint64(2 * 2 * (expandedCapacity - 128))
	if got := expandedBytes - sharedBytes; got != wantIncrease {
		t.Fatalf("payload-capacity increase = %d, want %d", got, wantIncrease)
	}
}

func TestRetainedMemoryBytesBackedMapDoesNotFetchHashOnlyBranches(t *testing.T) {
	source := New(TypeState)
	for _, key := range [][32]byte{{0x12}, {0x13}} {
		if err := source.Put(key, []byte("resident payload")); err != nil {
			t.Fatalf("Put(%x): %v", key[:1], err)
		}
	}

	family := newCountingFamily()
	if err := source.StoreDirty(func(entries []FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	}); err != nil {
		t.Fatalf("StoreDirty: %v", err)
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	backed, err := NewFromRootHash(TypeState, rootHash, family)
	if err != nil {
		t.Fatalf("NewFromRootHash: %v", err)
	}
	family.reset()

	if _, err := backed.RetainedMemoryBytes(); err != nil {
		t.Fatalf("RetainedMemoryBytes: %v", err)
	}
	if got := family.count(); got != 0 {
		t.Fatalf("RetainedMemoryBytes fetched %d hash-only branches", got)
	}
}

func TestRetainedMemoryBytesRejectsMalformedTree(t *testing.T) {
	sm := New(TypeState)
	sm.tree.mu.Lock()
	sm.tree.root = nil
	sm.tree.mu.Unlock()

	if _, err := sm.RetainedMemoryBytes(); err == nil {
		t.Fatal("RetainedMemoryBytes accepted a map without a root")
	}
}

func newRetainedMemoryTestMap(t *testing.T, keys [][32]byte, payloadCapacity int) *SHAMap {
	t.Helper()
	sm := New(TypeState)
	for _, key := range keys {
		data := make([]byte, 32, payloadCapacity)
		for i := range data {
			data[i] = byte(i + 1)
		}
		if err := sm.putItem(&Item{key: key, data: data}); err != nil {
			t.Fatalf("putItem(%x): %v", key[:1], err)
		}
	}
	return sm
}
