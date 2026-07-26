package shamap

import "testing"

func TestSme_FlushDirtyNilRoot(t *testing.T) {
	sm := New(TypeState)
	sm.tree.mu.Lock()
	sm.tree.root = nil
	sm.tree.mu.Unlock()
	batch, err := sm.FlushDirty()
	if err != nil {
		t.Fatalf("FlushDirty with nil root: %v", err)
	}
	if len(batch.Entries) != 0 {
		t.Errorf("FlushDirty with nil root: expected 0 entries, got %d", len(batch.Entries))
	}
}

func TestSme_NewBackedNilFamily(t *testing.T) {
	if _, err := NewBacked(TypeState, nil); err == nil {
		t.Error("NewBacked(nil family) should return error")
	}
}

func TestSme_NewFromRootHashNilFamily(t *testing.T) {
	if _, err := NewFromRootHash(TypeState, [32]byte{}, nil); err == nil {
		t.Error("NewFromRootHash(nil family) should return error")
	}
}

func TestSme_NewFromRootHashMissingRoot(t *testing.T) {
	family := newMemoryFamily()
	var h [32]byte
	h[0] = 0xDE
	_, err := NewFromRootHash(TypeState, h, family)
	if err == nil {
		t.Error("NewFromRootHash with missing root should return error")
	}
}

func TestSme_SetFamilyToNilMakesUnbacked(t *testing.T) {
	family := newMemoryFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	sm.SetFamily(nil)
	if sm.IsBacked() {
		t.Error("map should be unbacked after SetFamily(nil)")
	}
}
