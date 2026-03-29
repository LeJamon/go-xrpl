package txq

import (
	"sort"
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
		// Reference: rippled Transactor.cpp toFeeLevel()
		// When baseFee is 0: fee=0 → level 0, fee>0 → level max.
		if drops == 0 {
			return 0
		}
		return FeeLevel(^uint64(0))
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

	// Use 128-bit arithmetic via two 64-bit values
	// high, low = a * b
	hi, lo := mul64(a, b)

	// If high bits are significant relative to c, we'll overflow
	if hi >= c {
		return ^uint64(0)
	}

	// Divide 128-bit value by c
	return div128(hi, lo, c)
}

// mul64 multiplies two uint64 values and returns a 128-bit result as (high, low).
func mul64(a, b uint64) (hi, lo uint64) {
	const mask32 = (1 << 32) - 1
	a0 := a & mask32
	a1 := a >> 32
	b0 := b & mask32
	b1 := b >> 32

	p0 := a0 * b0
	p1 := a0 * b1
	p2 := a1 * b0
	p3 := a1 * b1

	mid := p1 + (p0 >> 32) + (p2 & mask32)
	hi = p3 + (p1 >> 32) + (p2 >> 32) + (mid >> 32)
	lo = (p0 & mask32) | (mid << 32)
	return
}

// div128 divides a 128-bit value (hi, lo) by a 64-bit divisor.
func div128(hi, lo, divisor uint64) uint64 {
	if hi == 0 {
		return lo / divisor
	}

	// Simple long division for the case where hi < divisor
	// This works because we already checked hi < divisor in mulDiv
	quotient := uint64(0)
	remainder := hi

	for i := 63; i >= 0; i-- {
		remainder = (remainder << 1) | ((lo >> i) & 1)
		if remainder >= divisor {
			remainder -= divisor
			quotient |= 1 << i
		}
	}

	return quotient
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

	targetTxn := cfg.TargetTxnInLedger
	if targetTxn < minTxn {
		targetTxn = minTxn
	}

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
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})

	if timeLeap {
		// Ledgers are taking too long to process, so clamp down on limits
		cutPct := uint64(100 - cfg.SlowConsensusDecreasePercent)
		upperLimit := mulDiv(uint64(fm.txnsExpected), cutPct, 100)
		if upperLimit < uint64(fm.minimumTxnCount) {
			upperLimit = uint64(fm.minimumTxnCount)
		}

		newExpected := mulDiv(uint64(size), cutPct, 100)
		if newExpected < uint64(fm.minimumTxnCount) {
			newExpected = uint64(fm.minimumTxnCount)
		}
		if newExpected > upperLimit {
			newExpected = upperLimit
		}
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
		// Compute median: for odd count, middle element;
		// for even count, average of two middle elements
		var median uint64
		if size%2 == 1 {
			median = uint64(sorted[size/2])
		} else {
			median = (uint64(sorted[size/2]) + uint64(sorted[(size-1)/2]) + 1) / 2
		}

		if median < cfg.MinimumEscalationMultiplier {
			median = cfg.MinimumEscalationMultiplier
		}
		fm.escalationMultiplier = median
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
	current := uint64(txInLedger)
	target := uint64(snapshot.TxnsExpected)

	numerator := snapshot.EscalationMultiplier * current * current
	denominator := target * target

	return FeeLevel(numerator / denominator)
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
