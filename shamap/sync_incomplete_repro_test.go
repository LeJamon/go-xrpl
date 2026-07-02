package shamap

import (
	"context"
	"testing"
)

// buildDeepBackedSource builds a state tree with at least one inner node at
// depth >= 3 and flushes every node into family in prefix format. It returns
// the source map, the root hash, and the root's wire bytes.
func buildDeepBackedSource(t *testing.T, family *memoryFamily) (*SHAMap, [32]byte, []byte) {
	t.Helper()
	source, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}

	// Spread keys across every first-nibble branch so the root fans out.
	for b := byte(1); b <= 15; b++ {
		var key [32]byte
		key[0] = b << 4
		key[31] = b
		if err := source.Put(key, intToBytes(int(b))); err != nil {
			t.Fatalf("Put spread %d: %v", b, err)
		}
	}
	// Keys sharing nibbles 0..5, branching at nibble 6: forces a single-child
	// inner chain at depths 1..6, guaranteeing depth>=4 inners.
	for j := byte(0); j < 8; j++ {
		var key [32]byte
		key[0] = 0x10
		key[1] = 0x20
		key[2] = 0x30
		key[3] = j << 4
		key[31] = 0xA0 | j
		if err := source.Put(key, intToBytes(int(0xB0|j))); err != nil {
			t.Fatalf("Put deep %d: %v", j, err)
		}
	}

	batch, err := source.FlushDirty(false)
	if err != nil {
		t.Fatalf("FlushDirty: %v", err)
	}
	if err := family.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}

	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source.Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("SerializeRoot: %v", err)
	}
	return source, rootHash, rootData
}

// pickDeepInner returns the NodeID, hash, and prefix bytes of an inner node at
// depth >= minDepth from source, using the family's stored prefix data.
func pickDeepInner(t *testing.T, source *SHAMap, family *memoryFamily, minDepth int) (NodeID, [32]byte, []byte) {
	t.Helper()
	wire, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	for _, w := range wire {
		if int(w.NodeID[32]) < minDepth {
			continue
		}
		n, err := DeserializeNodeFromWire(w.Data)
		if err != nil {
			continue
		}
		if _, ok := n.(*innerNode); !ok {
			continue
		}
		if err := n.UpdateHash(); err != nil {
			t.Fatalf("UpdateHash: %v", err)
		}
		h := n.Hash()
		prefix, ok := family.store[h]
		if !ok {
			continue
		}
		nid, err := UnmarshalBinary(w.NodeID)
		if err != nil {
			t.Fatalf("UnmarshalBinary: %v", err)
		}
		return nid, h, prefix
	}
	t.Fatalf("no inner node at depth >= %d found", minDepth)
	return NodeID{}, [32]byte{}, nil
}

// familyExcept returns a copy of src with the single hash h omitted, mirroring
// a local node store that holds the shared majority of a fork's state tree but
// is genuinely missing one node that must be fetched from a peer.
func familyExcept(src *memoryFamily, h [32]byte) *memoryFamily {
	out := newMemoryFamily()
	for k, v := range src.store {
		if k == h {
			continue
		}
		out.store[k] = v
	}
	return out
}

// TestIncompleteSyncMap_CompletenessConsistency reproduces issue #1161: an
// acquisition state map built the production way (NewBacked + AddRootNode, no
// StartSync) over a store that is genuinely missing one deep node must report
// itself incomplete consistently. Before the fix IsComplete() short-circuits
// on the stale full flag and returns true while FinishSync() walks and returns
// "still missing", so have_state never latches and the acquisition wedges.
func TestIncompleteSyncMap_CompletenessConsistency(t *testing.T) {
	familyFull := newMemoryFamily()
	source, rootHash, rootData := buildDeepBackedSource(t, familyFull)

	absentID, absentHash, absentPrefix := pickDeepInner(t, source, familyFull, 4)
	t.Logf("absent node depth=%d hash=%x", absentID.Depth(), absentHash[:8])

	familyPartial := familyExcept(familyFull, absentHash)

	dest, err := NewBacked(TypeState, familyPartial)
	if err != nil {
		t.Fatalf("NewBacked dest: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("AddRootNode: %v", err)
	}

	t.Run("IsCompleteAgreesWithFinishSync", func(t *testing.T) {
		isComplete := dest.IsComplete()
		finishErr := dest.FinishSync()
		t.Logf("IsComplete()=%v FinishSync() err=%v", isComplete, finishErr)
		if isComplete != (finishErr == nil) {
			t.Fatalf("PARADOX: IsComplete()=%v but FinishSync()==nil is %v (err=%v)",
				isComplete, finishErr == nil, finishErr)
		}
		if isComplete {
			t.Fatalf("map with one genuinely-absent node must report incomplete")
		}
	})

	t.Run("GetMissingNodesReportsAbsentNode", func(t *testing.T) {
		missing := dest.GetMissingNodes(64, nil)
		if len(missing) == 0 {
			t.Fatalf("GetMissingNodes: expected the absent node, got none")
		}
		found := false
		for _, m := range missing {
			if m.Hash == absentHash {
				found = true
			}
		}
		if !found {
			t.Fatalf("GetMissingNodes did not report the absent node %x", absentHash[:8])
		}
	})

	t.Run("GetMissingNodesIdempotent", func(t *testing.T) {
		// Two consecutive walks over an unchanging tree must agree — the
		// walk's lazy store-loads must not change its own verdict.
		a := len(dest.GetMissingNodes(1, nil))
		b := len(dest.GetMissingNodes(1, nil))
		if a != b {
			t.Fatalf("GetMissingNodes(1) non-idempotent: %d then %d", a, b)
		}
		c := dest.FinishSync()
		d := dest.FinishSync()
		if (c == nil) != (d == nil) {
			t.Fatalf("FinishSync non-idempotent: %v then %v", c, d)
		}
	})

	t.Run("IncrementalFeedConverges", func(t *testing.T) {
		// Deliver the genuinely-absent node as a peer would, then the tree
		// finalizes and all three views agree it is complete.
		res, err := dest.AddKnownNodeFromPrefix(absentID, absentPrefix)
		if err != nil {
			t.Fatalf("AddKnownNodeFromPrefix: %v", err)
		}
		if res != NodeUseful {
			t.Fatalf("feeding absent node: want NodeUseful, got %v", res)
		}
		if err := dest.FinishSync(); err != nil {
			t.Fatalf("FinishSync after feed: %v", err)
		}
		if !dest.IsComplete() {
			t.Fatalf("IsComplete() should be true after finalize")
		}
		if got := len(dest.GetMissingNodes(1, nil)); got != 0 {
			t.Fatalf("GetMissingNodes after finalize: want 0, got %d", got)
		}
		destHash, err := dest.Hash()
		if err != nil {
			t.Fatalf("dest.Hash: %v", err)
		}
		if destHash != rootHash {
			t.Fatalf("reconstructed root hash mismatch: want %x got %x", rootHash[:8], destHash[:8])
		}
	})
}
