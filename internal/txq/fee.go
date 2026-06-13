package txq

import (
	"math/bits"
	"slices"
)

// BaseLevel is the reference fee level for a single-signed transaction.
// All fee levels are expressed relative to this value.
// A transaction paying exactly the base fee has a fee level of 256.
const BaseLevel uint64 = 256

// FeeLevel represents a fee level value.
// Fee level = (fee paid / base fee) * BaseLevel
type FeeLevel uint64

// ToFeeLevel converts drops and base fee to a fee level.
// Returns the fee level: (drops * BaseLevel) / baseFee
func ToFeeLevel(drops, baseFee uint64) FeeLevel {
	if baseFee == 0 {
		// rippled's getFeeLevelPaid adds a modifier to both the fee paid and
		// the base fee so a free transaction still has a non-zero fee level
		// (TxQ.cpp:38-64). That modifier is calculateDefaultBaseFee, which is
		// itself 0 exactly when the contextual base fee is 0 (the per-tx base
		// fee is never below the reference fee), so it collapses to 1 — which
		// is what we add to both here. fee=0 → level 256; fee=N → level (N+1)*256.
		drops += 1
		baseFee = 1
	}
	// Use 128-bit arithmetic to avoid overflow
	// fee level = drops * 256 / baseFee
	return FeeLevel(mulDiv(drops, BaseLevel, baseFee))
}

// ToDrops converts a fee level back to drops given a base fee.
// Returns: (level * baseFee) / BaseLevel
func (f FeeLevel) ToDrops(baseFee uint64) uint64 {
	return mulDiv(uint64(f), baseFee, BaseLevel)
}

// mulDiv computes (a * b) / c with overflow protection.
// Returns MaxUint64 on overflow.
func mulDiv(a, b, c uint64) uint64 {
	if c == 0 {
		return ^uint64(0)
	}

	// 128-bit a*b. math/bits.Mul64 compiles to a single MUL on amd64/arm64.
	hi, lo := bits.Mul64(a, b)

	// Avoid bits.Div64's panic on overflow / divide-by-zero.
	if hi >= c {
		return ^uint64(0)
	}

	quo, _ := bits.Div64(hi, lo, c)
	return quo
}

// FeeMetrics tracks and computes fee escalation metrics for the transaction queue.
// It maintains a history of recent ledger transaction counts and computes
// the escalated fee level based on how full the current open ledger is.
type FeeMetrics struct {
	// minimumTxnCount is the minimum value of txnsExpected
	minimumTxnCount uint32

	// targetTxnCount is the number of transactions per ledger that fee
	// escalation "works towards"
	targetTxnCount uint32

	// maximumTxnCount is the optional maximum value of txnsExpected
	// Zero means no maximum
	maximumTxnCount uint32

	// txnsExpected is the number of transactions expected per ledger.
	// One more than this value will be accepted before escalation kicks in.
	txnsExpected uint32

	// recentTxnCounts is a circular buffer of recent transaction counts
	// that exceed targetTxnCount
	recentTxnCounts []uint32
	recentIndex     int
	recentSize      int
	recentCapacity  int

	// escalationMultiplier is based on the median fee of the last closed ledger.
	// Used when fee escalation kicks in.
	escalationMultiplier uint64
}

// NewFeeMetrics creates a new FeeMetrics with the given configuration.
func NewFeeMetrics(cfg Config) *FeeMetrics {
	minTxn := cfg.MinimumTxnInLedger
	if cfg.Standalone {
		minTxn = cfg.MinimumTxnInLedgerStandalone
	}

	targetTxn := max(cfg.TargetTxnInLedger, minTxn)

	maxTxn := cfg.MaximumTxnInLedger
	if maxTxn != 0 && maxTxn < targetTxn {
		maxTxn = targetTxn
	}

	// Ensure recentCapacity is at least 1 to prevent division by zero and index panics.
	// Reference: rippled uses boost::circular_buffer which requires capacity > 0.
	ledgersInQueue := cfg.LedgersInQueue
	if ledgersInQueue == 0 {
		ledgersInQueue = 20 // Match DefaultConfig
	}

	return &FeeMetrics{
		minimumTxnCount:      minTxn,
		targetTxnCount:       targetTxn,
		maximumTxnCount:      maxTxn,
		txnsExpected:         minTxn,
		recentTxnCounts:      make([]uint32, ledgersInQueue),
		recentCapacity:       int(ledgersInQueue),
		escalationMultiplier: cfg.MinimumEscalationMultiplier,
	}
}

// Snapshot holds a point-in-time copy of the fee metrics for calculations.
type Snapshot struct {
	TxnsExpected         uint32
	EscalationMultiplier uint64
}

// GetSnapshot returns the current fee metrics snapshot.
func (fm *FeeMetrics) GetSnapshot() Snapshot {
	return Snapshot{
		TxnsExpected:         fm.txnsExpected,
		EscalationMultiplier: fm.escalationMultiplier,
	}
}

// Update updates fee metrics based on the closed ledger and returns
// the number of transactions in that ledger.
func (fm *FeeMetrics) Update(feeLevels []FeeLevel, timeLeap bool, cfg Config) uint32 {
	size := uint32(len(feeLevels))

	// Sort fee levels to compute median
	sorted := make([]FeeLevel, len(feeLevels))
	copy(sorted, feeLevels)
	slices.Sort(sorted)

	if timeLeap {
		// Ledgers are taking too long to process, so clamp down on limits
		cutPct := uint64(100 - cfg.SlowConsensusDecreasePercent)
		upperLimit := max(mulDiv(uint64(fm.txnsExpected), cutPct, 100), uint64(fm.minimumTxnCount))

		newExpected := min(max(mulDiv(uint64(size), cutPct, 100), uint64(fm.minimumTxnCount)), upperLimit)
		fm.txnsExpected = uint32(newExpected)

		// Clear recent history
		fm.recentSize = 0
		fm.recentIndex = 0
	} else if size > fm.txnsExpected || size > fm.targetTxnCount {
		// Add to recent counts with increase percentage
		increased := mulDiv(uint64(size), 100+uint64(cfg.NormalConsensusIncreasePercent), 100)
		fm.addRecentCount(uint32(increased))

		// Find max in recent counts
		maxRecent := fm.maxRecentCount()

		var next uint32
		if maxRecent >= fm.txnsExpected {
			// Grow quickly
			next = maxRecent
		} else {
			// Shrink slowly: 90% of the way from max to current
			next = (fm.txnsExpected*9 + maxRecent) / 10
		}

		// Don't exceed maximum if set
		if fm.maximumTxnCount != 0 && next > fm.maximumTxnCount {
			next = fm.maximumTxnCount
		}
		fm.txnsExpected = next
	}

	// Update escalation multiplier based on median fee level
	if size == 0 {
		fm.escalationMultiplier = cfg.MinimumEscalationMultiplier
	} else {
		// Median fee level. The single expression matches rippled (TxQ.cpp:160-162):
		// for odd sizes size/2 == (size-1)/2, so it reduces to the middle
		// element; for even sizes it averages the two middle elements.
		median := (uint64(sorted[size/2]) + uint64(sorted[(size-1)/2]) + 1) / 2
		fm.escalationMultiplier = max(median, cfg.MinimumEscalationMultiplier)
	}

	return size
}

// addRecentCount adds a count to the circular buffer.
func (fm *FeeMetrics) addRecentCount(count uint32) {
	if fm.recentCapacity == 0 {
		return
	}
	fm.recentTxnCounts[fm.recentIndex] = count
	fm.recentIndex = (fm.recentIndex + 1) % fm.recentCapacity
	if fm.recentSize < fm.recentCapacity {
		fm.recentSize++
	}
}

// maxRecentCount returns the maximum value in the recent counts buffer.
func (fm *FeeMetrics) maxRecentCount() uint32 {
	if fm.recentSize == 0 {
		return 0
	}

	max := uint32(0)
	for i := 0; i < fm.recentSize; i++ {
		if fm.recentTxnCounts[i] > max {
			max = fm.recentTxnCounts[i]
		}
	}
	return max
}

// ScaleFeeLevel computes the fee level a transaction must pay to bypass
// the queue and get into the open ledger directly.
func ScaleFeeLevel(snapshot Snapshot, txInLedger uint32) FeeLevel {
	// If we haven't exceeded the expected count, use base level
	if txInLedger <= snapshot.TxnsExpected {
		return FeeLevel(BaseLevel)
	}

	// Compute escalated fee level:
	// fee_level = multiplier * (current^2) / (target^2)
	// Uses mulDiv for overflow-safe 128-bit intermediate arithmetic,
	// matching rippled's scaleFeeLevel which saturates to max on overflow.
	current := uint64(txInLedger)
	target := uint64(snapshot.TxnsExpected)

	return FeeLevel(mulDiv(snapshot.EscalationMultiplier, current*current, target*target))
}

// EscalatedSeriesFeeLevel computes the total fee level required for a series
// of transactions to clear the queue. This is used when a transaction wants
// to "rescue" earlier queued transactions by paying enough to cover all of them.
func EscalatedSeriesFeeLevel(snapshot Snapshot, txInLedger, extraCount, seriesSize uint32) (FeeLevel, bool) {
	current := uint64(txInLedger) + uint64(extraCount)
	last := current + uint64(seriesSize) - 1
	target := uint64(snapshot.TxnsExpected)

	// Sum of squares formula: sum(n=current->last) n^2
	// = sum(1->last) n^2 - sum(1->current-1) n^2
	// = last*(last+1)*(2*last+1)/6 - (current-1)*current*(2*current-1)/6
	sumLast, ok1 := sumOfSquares(last)
	sumCurrent, ok2 := sumOfSquares(current - 1)
	if !ok1 || !ok2 {
		return FeeLevel(^uint64(0)), false
	}

	// total = multiplier * (sumLast - sumCurrent) / (target^2)
	diff := sumLast - sumCurrent
	result := mulDiv(snapshot.EscalationMultiplier, diff, target*target)

	return FeeLevel(result), true
}

// sumOfSquares computes sum(n=1->x) n^2 = x*(x+1)*(2x+1)/6
// Returns false if overflow would occur.
func sumOfSquares(x uint64) (uint64, bool) {
	// If x is anywhere close to 2^21, it will overflow
	if x >= (1 << 21) {
		return ^uint64(0), false
	}
	return (x * (x + 1) * (2*x + 1)) / 6, true
}
