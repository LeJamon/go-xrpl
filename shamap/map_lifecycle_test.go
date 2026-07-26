package shamap

import "testing"

func TestSme_TypeString(t *testing.T) {
	cases := []struct {
		typ  Type
		want string
	}{
		{TypeTransaction, "transaction"},
		{TypeState, "state"},
		{Type(99), "unknown(99)"},
	}
	for _, c := range cases {
		if got := c.typ.String(); got != c.want {
			t.Errorf("Type(%d).String() = %q, want %q", c.typ, got, c.want)
		}
	}
}

func TestSme_TypeAccessorAndInitialState(t *testing.T) {
	sm := New(TypeTransaction)
	if sm.Type() != TypeTransaction {
		t.Errorf("Type() = %v, want TypeTransaction", sm.Type())
	}
	if sm.tree.state != stateModifying {
		t.Errorf("initial state = %v, want modifying", sm.tree.state)
	}
	var zero SHAMap
	if zero.tree.state != stateModifying {
		t.Errorf("zero-value state = %v, want modifying", zero.tree.state)
	}
}

func TestSme_SetLedgerSeq(t *testing.T) {
	sm := New(TypeState)
	sm.SetLedgerSeq(42)
	sm.tree.mu.RLock()
	seq := sm.tree.ledgerSeq
	sm.tree.mu.RUnlock()
	if seq != 42 {
		t.Errorf("ledgerSeq = %d, want 42", seq)
	}
}

func TestSme_SetImmutableOnInvalidReturnsError(t *testing.T) {
	sm := New(TypeState)
	sm.tree.mu.Lock()
	sm.tree.state = stateInvalid
	sm.tree.mu.Unlock()
	if err := sm.SetImmutable(); err == nil {
		t.Error("SetImmutable on invalid map should return error")
	}
}

func TestSme_HashOnInvalidReturnsError(t *testing.T) {
	sm := New(TypeState)
	sm.tree.mu.Lock()
	sm.tree.state = stateInvalid
	sm.tree.mu.Unlock()
	if _, err := sm.Hash(); err == nil {
		t.Error("Hash on invalid map should return error")
	}
}

func TestSme_IsBackedFalse(t *testing.T) {
	sm := New(TypeState)
	if sm.IsBacked() {
		t.Error("unbacked map should return false for IsBacked()")
	}
}
