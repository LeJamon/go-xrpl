package txq

import (
	"testing"
)

// TestNew tests TxQ creation with various configurations
func TestNew(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		cfg := DefaultConfig()
		q := New(cfg)

		if q.config.LedgersInQueue != 20 {
			t.Errorf("LedgersInQueue = %d, want 20", q.config.LedgersInQueue)
		}
		if q.config.MaximumTxnPerAccount != 10 {
			t.Errorf("MaximumTxnPerAccount = %d, want 10", q.config.MaximumTxnPerAccount)
		}
		if q.maxSize != cfg.QueueSizeMin {
			t.Errorf("maxSize = %d, want %d", q.maxSize, cfg.QueueSizeMin)
		}
	})

	t.Run("standalone config", func(t *testing.T) {
		cfg := StandaloneConfig()
		q := New(cfg)

		if !q.config.Standalone {
			t.Error("Expected Standalone = true")
		}
	})
}

// TestTxQ_InsertByFee tests the fee-ordered insertion
func TestTxQ_InsertByFee(t *testing.T) {
	cfg := DefaultConfig()
	q := New(cfg)

	// Create test candidates with different fee levels
	c1 := &Candidate{
		TxID:     [32]byte{1},
		Account:  [20]byte{1},
		FeeLevel: FeeLevel(100),
		SeqProxy: NewSeqProxySequence(1),
	}
	c2 := &Candidate{
		TxID:     [32]byte{2},
		Account:  [20]byte{2},
		FeeLevel: FeeLevel(300),
		SeqProxy: NewSeqProxySequence(1),
	}
	c3 := &Candidate{
		TxID:     [32]byte{3},
		Account:  [20]byte{3},
		FeeLevel: FeeLevel(200),
		SeqProxy: NewSeqProxySequence(1),
	}

	// Insert in arbitrary order
	q.insertByFee(c1)
	q.insertByFee(c2)
	q.insertByFee(c3)

	// Should be ordered by fee level (highest first): c2, c3, c1
	if len(q.byFee) != 3 {
		t.Fatalf("byFee length = %d, want 3", len(q.byFee))
	}

	if q.byFee[0].FeeLevel != 300 {
		t.Errorf("byFee[0].FeeLevel = %d, want 300", q.byFee[0].FeeLevel)
	}
	if q.byFee[1].FeeLevel != 200 {
		t.Errorf("byFee[1].FeeLevel = %d, want 200", q.byFee[1].FeeLevel)
	}
	if q.byFee[2].FeeLevel != 100 {
		t.Errorf("byFee[2].FeeLevel = %d, want 100", q.byFee[2].FeeLevel)
	}
}

// TestTxQ_GetMetrics tests metrics retrieval
func TestTxQ_GetMetrics(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinimumTxnInLedgerStandalone = 3
	cfg.Standalone = true
	q := New(cfg)

	metrics := q.GetMetrics(0)

	if metrics.TxCount != 0 {
		t.Errorf("TxCount = %d, want 0", metrics.TxCount)
	}
	if metrics.ReferenceFeeLevel != BaseLevel {
		t.Errorf("ReferenceFeeLevel = %d, want %d", metrics.ReferenceFeeLevel, BaseLevel)
	}
	if metrics.TxQMaxSize == nil || *metrics.TxQMaxSize != cfg.QueueSizeMin {
		t.Errorf("TxQMaxSize = %v, want %d", metrics.TxQMaxSize, cfg.QueueSizeMin)
	}
}

// TestTxQ_IsFull tests queue full detection
func TestTxQ_IsFull(t *testing.T) {
	cfg := DefaultConfig()
	q := New(cfg)
	q.maxSize = 100 // Size for testing percentage math

	// Create test candidates (100 items = 100% full)
	for i := 0; i < 100; i++ {
		c := &Candidate{
			TxID:     [32]byte{byte(i)},
			Account:  [20]byte{byte(i)},
			FeeLevel: FeeLevel(100 + i),
			SeqProxy: NewSeqProxySequence(1),
		}
		q.byFee = append(q.byFee, c)
	}

	if !q.isFull() {
		t.Error("Queue should be full")
	}

	// 95% full check with 90 items (90%)
	// 100 * 95 / 100 = 95, so 90 < 95, should return false
	q.byFee = q.byFee[:90]
	if q.isFullPct(95) {
		t.Error("Queue at 90% should not be 95% full")
	}

	// 80% full check with 90 items (90%)
	// 100 * 80 / 100 = 80, so 90 >= 80, should return true
	if !q.isFullPct(80) {
		t.Error("Queue at 90% should be 80% full")
	}
}

// TestTxQ_XorHash tests the XOR hash function for tie-breaking
func TestTxQ_XorHash(t *testing.T) {
	a := [32]byte{0xFF, 0x00, 0xAA}
	b := [32]byte{0x0F, 0xF0, 0x55}
	expected := [32]byte{0xF0, 0xF0, 0xFF}

	result := xorHash(a, b)
	if result != expected {
		t.Errorf("xorHash result mismatch: got %x, want %x", result[:3], expected[:3])
	}
}

// TestTxQ_CompareHashes tests lexicographic hash comparison
func TestTxQ_CompareHashes(t *testing.T) {
	tests := []struct {
		name     string
		a        [32]byte
		b        [32]byte
		expected int
	}{
		{
			name:     "equal",
			a:        [32]byte{1, 2, 3},
			b:        [32]byte{1, 2, 3},
			expected: 0,
		},
		{
			name:     "a < b",
			a:        [32]byte{1, 2, 3},
			b:        [32]byte{1, 2, 4},
			expected: -1,
		},
		{
			name:     "a > b",
			a:        [32]byte{1, 3, 3},
			b:        [32]byte{1, 2, 4},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := compareHashes(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("compareHashes() = %d, want %d", result, tt.expected)
			}
		})
	}
}

// TestTxQ_Size tests the Size method
func TestTxQ_Size(t *testing.T) {
	cfg := DefaultConfig()
	q := New(cfg)

	if q.Size() != 0 {
		t.Errorf("Initial size = %d, want 0", q.Size())
	}

	// Add a candidate directly
	c := &Candidate{
		TxID:     [32]byte{1},
		Account:  [20]byte{1},
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(1),
	}
	q.byFee = append(q.byFee, c)

	if q.Size() != 1 {
		t.Errorf("Size after add = %d, want 1", q.Size())
	}
}

// TestTxQ_GetRequiredFeeLevel tests fee level calculation
func TestTxQ_GetRequiredFeeLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinimumTxnInLedgerStandalone = 5
	cfg.Standalone = true
	q := New(cfg)

	// Under threshold - should return base level
	level := q.GetRequiredFeeLevel(3)
	if level != FeeLevel(BaseLevel) {
		t.Errorf("GetRequiredFeeLevel(3) = %d, want %d", level, BaseLevel)
	}
}

// TestTxQ_Clear tests clearing the queue
func TestTxQ_Clear(t *testing.T) {
	cfg := DefaultConfig()
	q := New(cfg)

	// Add some candidates
	account := [20]byte{1}
	aq := NewAccountQueue(account)
	c := &Candidate{
		TxID:     [32]byte{1},
		Account:  account,
		FeeLevel: FeeLevel(256),
		SeqProxy: NewSeqProxySequence(1),
	}
	aq.Add(c)
	q.byAccount[account] = aq
	q.byFee = append(q.byFee, c)

	if q.Size() != 1 {
		t.Fatalf("Size before clear = %d, want 1", q.Size())
	}

	q.Clear()

	if q.Size() != 0 {
		t.Errorf("Size after clear = %d, want 0", q.Size())
	}
	if len(q.byAccount) != 0 {
		t.Errorf("byAccount length after clear = %d, want 0", len(q.byAccount))
	}
}

// TestTxQ_SetMaxSize tests setting max size
func TestTxQ_SetMaxSize(t *testing.T) {
	cfg := DefaultConfig()
	q := New(cfg)

	q.SetMaxSize(100)
	q.mu.Lock()
	maxSize := q.maxSize
	q.mu.Unlock()

	if maxSize != 100 {
		t.Errorf("maxSize = %d, want 100", maxSize)
	}
}
