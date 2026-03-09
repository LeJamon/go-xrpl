package txq

import (
	"testing"
)

// TestSeqProxy_Less tests SeqProxy ordering
func TestSeqProxy_Less(t *testing.T) {
	tests := []struct {
		name     string
		a        SeqProxy
		b        SeqProxy
		expected bool
	}{
		{
			name:     "sequence < sequence",
			a:        NewSeqProxySequence(1),
			b:        NewSeqProxySequence(2),
			expected: true,
		},
		{
			name:     "sequence > sequence",
			a:        NewSeqProxySequence(2),
			b:        NewSeqProxySequence(1),
			expected: false,
		},
		{
			name:     "sequence = sequence",
			a:        NewSeqProxySequence(1),
			b:        NewSeqProxySequence(1),
			expected: false,
		},
		{
			name:     "ticket < ticket",
			a:        NewSeqProxyTicket(1),
			b:        NewSeqProxyTicket(2),
			expected: true,
		},
		{
			name:     "sequence < ticket (sequences come first)",
			a:        NewSeqProxySequence(100),
			b:        NewSeqProxyTicket(1),
			expected: true,
		},
		{
			name:     "ticket > sequence",
			a:        NewSeqProxyTicket(1),
			b:        NewSeqProxySequence(100),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.a.Less(tt.b)
			if result != tt.expected {
				t.Errorf("%v.Less(%v) = %v, want %v", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

// TestAccountQueue_Add tests adding candidates to an account queue
func TestAccountQueue_Add(t *testing.T) {
	account := [20]byte{1, 2, 3}
	aq := NewAccountQueue(account)

	c1 := &Candidate{
		TxID:     [32]byte{1},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(1),
	}

	c2 := &Candidate{
		TxID:     [32]byte{2},
		Account:  account,
		FeeLevel: FeeLevel(512),
		SeqProxy: NewSeqProxySequence(2),
	}

	aq.Add(c1)
	if aq.Count() != 1 {
		t.Errorf("Count after first add = %d, want 1", aq.Count())
	}

	aq.Add(c2)
	if aq.Count() != 2 {
		t.Errorf("Count after second add = %d, want 2", aq.Count())
	}
}

// TestAccountQueue_Remove tests removing candidates from an account queue
func TestAccountQueue_Remove(t *testing.T) {
	account := [20]byte{1, 2, 3}
	aq := NewAccountQueue(account)

	c1 := &Candidate{
		TxID:     [32]byte{1},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(1),
	}

	aq.Add(c1)
	if aq.Count() != 1 {
		t.Fatalf("Count after add = %d, want 1", aq.Count())
	}

	removed := aq.Remove(NewSeqProxySequence(1))
	if !removed {
		t.Error("Remove should return true")
	}
	if aq.Count() != 0 {
		t.Errorf("Count after remove = %d, want 0", aq.Count())
	}

	// Try to remove non-existent
	removed = aq.Remove(NewSeqProxySequence(999))
	if removed {
		t.Error("Remove should return false for non-existent")
	}
}

// TestAccountQueue_Empty tests the Empty method
func TestAccountQueue_Empty(t *testing.T) {
	account := [20]byte{1, 2, 3}
	aq := NewAccountQueue(account)

	if !aq.Empty() {
		t.Error("New queue should be empty")
	}

	c := &Candidate{
		TxID:     [32]byte{1},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(1),
	}
	aq.Add(c)

	if aq.Empty() {
		t.Error("Queue with one item should not be empty")
	}
}

// TestAccountQueue_GetPrevTx tests finding the previous transaction
func TestAccountQueue_GetPrevTx(t *testing.T) {
	account := [20]byte{1, 2, 3}
	aq := NewAccountQueue(account)

	c1 := &Candidate{
		TxID:     [32]byte{1},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(1),
	}
	c2 := &Candidate{
		TxID:     [32]byte{2},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(2),
	}
	c3 := &Candidate{
		TxID:     [32]byte{3},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(3),
	}

	aq.Add(c1)
	aq.Add(c2)
	aq.Add(c3)

	// Get prev for seq 3 should be seq 2
	prev := aq.GetPrevTx(NewSeqProxySequence(3))
	if prev == nil {
		t.Fatal("GetPrevTx(3) should not be nil")
	}
	if prev.SeqProxy.Value != 2 {
		t.Errorf("GetPrevTx(3) = seq %d, want 2", prev.SeqProxy.Value)
	}

	// Get prev for seq 1 should be nil
	prev = aq.GetPrevTx(NewSeqProxySequence(1))
	if prev != nil {
		t.Error("GetPrevTx(1) should be nil")
	}
}

// TestAccountQueue_GetFirstSeqTx tests finding the first sequence-based tx
func TestAccountQueue_GetFirstSeqTx(t *testing.T) {
	account := [20]byte{1, 2, 3}
	aq := NewAccountQueue(account)

	// Add ticket first
	ticket := &Candidate{
		TxID:     [32]byte{1},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxyTicket(10),
	}
	aq.Add(ticket)

	// Should be nil since no sequence-based tx
	first := aq.GetFirstSeqTx()
	if first != nil {
		t.Error("GetFirstSeqTx should be nil with only tickets")
	}

	// Add sequences
	c1 := &Candidate{
		TxID:     [32]byte{2},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(5),
	}
	c2 := &Candidate{
		TxID:     [32]byte{3},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(3),
	}
	aq.Add(c1)
	aq.Add(c2)

	first = aq.GetFirstSeqTx()
	if first == nil {
		t.Fatal("GetFirstSeqTx should not be nil")
	}
	if first.SeqProxy.Value != 3 {
		t.Errorf("GetFirstSeqTx = seq %d, want 3", first.SeqProxy.Value)
	}
}

// TestAccountQueue_GetSortedCandidates tests getting sorted candidates
func TestAccountQueue_GetSortedCandidates(t *testing.T) {
	account := [20]byte{1, 2, 3}
	aq := NewAccountQueue(account)

	// Add in random order
	c3 := &Candidate{
		TxID:     [32]byte{3},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(3),
	}
	c1 := &Candidate{
		TxID:     [32]byte{1},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(1),
	}
	c2 := &Candidate{
		TxID:     [32]byte{2},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(2),
	}

	aq.Add(c3)
	aq.Add(c1)
	aq.Add(c2)

	sorted := aq.GetSortedCandidates()
	if len(sorted) != 3 {
		t.Fatalf("GetSortedCandidates length = %d, want 3", len(sorted))
	}

	// Should be sorted by SeqProxy
	for i := 0; i < len(sorted)-1; i++ {
		if !sorted[i].SeqProxy.Less(sorted[i+1].SeqProxy) {
			t.Errorf("Candidates not sorted: %v should be less than %v",
				sorted[i].SeqProxy, sorted[i+1].SeqProxy)
		}
	}
}

// TestCandidate_RetriesRemaining tests retry tracking
func TestCandidate_RetriesRemaining(t *testing.T) {
	account := [20]byte{1, 2, 3}
	c := NewCandidate(
		nil,
		[32]byte{1},
		account,
		FeeLevel(256),
		NewSeqProxySequence(1),
		0,
		0,
		TxConsequences{},
	)

	if c.RetriesRemaining != RetriesAllowed {
		t.Errorf("Initial RetriesRemaining = %d, want %d",
			c.RetriesRemaining, RetriesAllowed)
	}

	// Decrement retries
	c.RetriesRemaining--
	if c.RetriesRemaining != RetriesAllowed-1 {
		t.Errorf("RetriesRemaining after decrement = %d, want %d",
			c.RetriesRemaining, RetriesAllowed-1)
	}
}
