package txq

import (
	"testing"
)

// TestToFeeLevel tests fee level calculations matching rippled behavior
func TestToFeeLevel(t *testing.T) {
	tests := []struct {
		name     string
		drops    uint64
		baseFee  uint64
		expected FeeLevel
	}{
		{
			name:     "base fee equals drops - level 256",
			drops:    10,
			baseFee:  10,
			expected: FeeLevel(256), // BaseLevel
		},
		{
			name:     "double the fee - level 512",
			drops:    20,
			baseFee:  10,
			expected: FeeLevel(512),
		},
		{
			name:     "half the fee - level 128",
			drops:    5,
			baseFee:  10,
			expected: FeeLevel(128),
		},
		{
			name:     "10x fee - level 2560",
			drops:    100,
			baseFee:  10,
			expected: FeeLevel(2560),
		},
		{
			name:     "zero drops - level 0",
			drops:    0,
			baseFee:  10,
			expected: FeeLevel(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToFeeLevel(tt.drops, tt.baseFee)
			if result != tt.expected {
				t.Errorf("ToFeeLevel(%d, %d) = %d, want %d",
					tt.drops, tt.baseFee, result, tt.expected)
			}
		})
	}
}

// TestFeeLevel_ToDrops tests converting fee level back to drops
func TestFeeLevel_ToDrops(t *testing.T) {
	tests := []struct {
		name     string
		level    FeeLevel
		baseFee  uint64
		expected uint64
	}{
		{
			name:     "base level - same as base fee",
			level:    FeeLevel(256),
			baseFee:  10,
			expected: 10,
		},
		{
			name:     "double level - double fee",
			level:    FeeLevel(512),
			baseFee:  10,
			expected: 20,
		},
		{
			name:     "half level - half fee",
			level:    FeeLevel(128),
			baseFee:  10,
			expected: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.level.ToDrops(tt.baseFee)
			if result != tt.expected {
				t.Errorf("FeeLevel(%d).ToDrops(%d) = %d, want %d",
					tt.level, tt.baseFee, result, tt.expected)
			}
		})
	}
}

// TestScaleFeeLevel tests the fee escalation formula
// From rippled: fee_level = multiplier * (current^2) / (target^2)
func TestScaleFeeLevel(t *testing.T) {
	tests := []struct {
		name       string
		snapshot   Snapshot
		txInLedger uint32
		expected   FeeLevel
	}{
		{
			name: "under target - base level",
			snapshot: Snapshot{
				TxnsExpected:         10,
				EscalationMultiplier: 128000, // 500 * 256
			},
			txInLedger: 5,
			expected:   FeeLevel(BaseLevel), // 256
		},
		{
			name: "at target - base level",
			snapshot: Snapshot{
				TxnsExpected:         10,
				EscalationMultiplier: 128000,
			},
			txInLedger: 10,
			expected:   FeeLevel(BaseLevel), // 256
		},
		{
			name: "over target - escalated",
			snapshot: Snapshot{
				TxnsExpected:         10,
				EscalationMultiplier: 128000,
			},
			txInLedger: 20,
			// 128000 * 20^2 / 10^2 = 128000 * 400 / 100 = 512000
			expected: FeeLevel(512000),
		},
		{
			name: "slightly over target",
			snapshot: Snapshot{
				TxnsExpected:         10,
				EscalationMultiplier: 128000,
			},
			txInLedger: 11,
			// 128000 * 11^2 / 10^2 = 128000 * 121 / 100 = 154880
			expected: FeeLevel(154880),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ScaleFeeLevel(tt.snapshot, tt.txInLedger)
			if result != tt.expected {
				t.Errorf("ScaleFeeLevel(%+v, %d) = %d, want %d",
					tt.snapshot, tt.txInLedger, result, tt.expected)
			}
		})
	}
}

// TestFeeMetrics_Update tests fee metrics update after ledger close
func TestFeeMetrics_Update(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinimumTxnInLedger = 3 // Match rippled test: minimum_txn_in_ledger_standalone = 3
	cfg.Standalone = true

	fm := NewFeeMetrics(cfg)

	// Initial state
	snapshot := fm.GetSnapshot()
	if snapshot.TxnsExpected != cfg.MinimumTxnInLedgerStandalone {
		t.Errorf("Initial TxnsExpected = %d, want %d",
			snapshot.TxnsExpected, cfg.MinimumTxnInLedgerStandalone)
	}

	// Simulate ledger with 5 transactions
	feeLevels := []FeeLevel{256, 256, 300, 400, 500}
	txCount := fm.Update(feeLevels, false, cfg)

	if txCount != 5 {
		t.Errorf("Update returned txCount = %d, want 5", txCount)
	}

	// Median of [256, 256, 300, 400, 500] = 300
	snapshot = fm.GetSnapshot()
	if snapshot.EscalationMultiplier < 300 {
		// Should be at least the median, but minimum is 128000
		if snapshot.EscalationMultiplier != cfg.MinimumEscalationMultiplier {
			t.Errorf("EscalationMultiplier = %d, want >= 300 or minimum %d",
				snapshot.EscalationMultiplier, cfg.MinimumEscalationMultiplier)
		}
	}
}

// TestFeeMetrics_TimeLeap tests fee metrics update during slow consensus
func TestFeeMetrics_TimeLeap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinimumTxnInLedger = 3
	cfg.MinimumTxnInLedgerStandalone = 3 // Set low for testing
	cfg.Standalone = true
	cfg.SlowConsensusDecreasePercent = 50

	fm := NewFeeMetrics(cfg)

	// First, increase txnsExpected by processing large ledgers
	feeLevels := make([]FeeLevel, 100)
	for i := range feeLevels {
		feeLevels[i] = FeeLevel(256)
	}
	fm.Update(feeLevels, false, cfg)

	snapshot := fm.GetSnapshot()
	txnsBeforeTimeLeap := snapshot.TxnsExpected

	// Now simulate a time leap (slow consensus)
	smallLedger := make([]FeeLevel, 10)
	for i := range smallLedger {
		smallLedger[i] = FeeLevel(256)
	}
	fm.Update(smallLedger, true, cfg)

	snapshot = fm.GetSnapshot()
	// With 50% decrease, new expected should be less
	if snapshot.TxnsExpected >= txnsBeforeTimeLeap {
		t.Errorf("After time leap, TxnsExpected = %d, should be less than %d",
			snapshot.TxnsExpected, txnsBeforeTimeLeap)
	}
}

// TestMulDiv tests the overflow-safe multiplication and division
func TestMulDiv(t *testing.T) {
	tests := []struct {
		name     string
		a, b, c  uint64
		expected uint64
	}{
		{
			name:     "simple case",
			a:        10,
			b:        256,
			c:        10,
			expected: 256,
		},
		{
			name:     "large numbers",
			a:        1000000,
			b:        256,
			c:        10,
			expected: 25600000,
		},
		{
			name:     "division by 1",
			a:        100,
			b:        200,
			c:        1,
			expected: 20000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mulDiv(tt.a, tt.b, tt.c)
			if result != tt.expected {
				t.Errorf("mulDiv(%d, %d, %d) = %d, want %d",
					tt.a, tt.b, tt.c, result, tt.expected)
			}
		})
	}
}

// TestSumOfSquares tests the sum of squares calculation for fee averaging
func TestSumOfSquares(t *testing.T) {
	tests := []struct {
		name     string
		x        uint64
		expected uint64
		ok       bool
	}{
		{
			name:     "x=1",
			x:        1,
			expected: 1,
			ok:       true,
		},
		{
			name:     "x=2",
			x:        2,
			expected: 5, // 1 + 4
			ok:       true,
		},
		{
			name:     "x=3",
			x:        3,
			expected: 14, // 1 + 4 + 9
			ok:       true,
		},
		{
			name:     "x=10",
			x:        10,
			expected: 385, // 1 + 4 + 9 + 16 + 25 + 36 + 49 + 64 + 81 + 100
			ok:       true,
		},
		{
			name:     "overflow case",
			x:        1 << 21, // 2097152
			expected: ^uint64(0),
			ok:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := sumOfSquares(tt.x)
			if ok != tt.ok {
				t.Errorf("sumOfSquares(%d) ok = %v, want %v", tt.x, ok, tt.ok)
			}
			if result != tt.expected {
				t.Errorf("sumOfSquares(%d) = %d, want %d", tt.x, result, tt.expected)
			}
		})
	}
}
