package payment

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
)

// AuctionSlotFeeScaleFactor is the denominator for trading fee calculations.
// tradingFee is in basis points out of 100,000.
// Reference: rippled AMMCore.h AUCTION_SLOT_FEE_SCALE_FACTOR = 100000
const AuctionSlotFeeScaleFactor = 100000

// QualityFunction represents the average quality of a path as a function of output:
//
//	q(out) = m * out + b
//
// For AMM (single-path):
//
//	m = -cfee / poolGets,  b = poolPays * cfee / poolGets
//	where cfee = feeMult(tradingFee) = 1 - tradingFee/100000
//
// For CLOB (or CLOB-like, including multi-path AMM):
//
//	m = 0,  b = 1 / quality.rate()
//
// The function is used to limit required output amount when quality limit
// is provided in one-path optimization.
//
// Reference: rippled QualityFunction.h and QualityFunction.cpp
type QualityFunction struct {
	// m is the slope (zero for CLOB-like constant quality)
	m tx.Amount
	// b is the intercept
	b tx.Amount
	// quality is set when the function is constant (CLOB-like).
	// nil means the function has a non-zero slope (AMM).
	quality *Quality
}

// numberOne returns 1.0 as an IOU Amount for arithmetic.
func numberOne() tx.Amount {
	return state.NewIssuedAmountFromValue(1e15, -15, "", "")
}

// numberZero returns 0 as an IOU Amount.
func numberZero() tx.Amount {
	return state.NewIssuedAmountFromValue(0, -100, "", "")
}

// NewCLOBLikeQualityFunction creates a QualityFunction for CLOB-like offers
// (constant quality). m = 0, b = 1/quality.rate().
// Reference: rippled QualityFunction.cpp QualityFunction(Quality, CLOBLikeTag)
func NewCLOBLikeQualityFunction(q Quality) *QualityFunction {
	rate := q.Rate()
	if rate.Signum() <= 0 {
		return nil
	}
	// b = 1 / quality.rate()
	one := numberOne()
	b := one.Div(rate, false)

	return &QualityFunction{
		m:       numberZero(),
		b:       b,
		quality: &q,
	}
}

// NewAMMQualityFunction creates a QualityFunction for AMM (single-path).
// Uses the AMM formula:
//
//	cfee = 1 - tradingFee / 100000
//	m = -cfee / poolGets
//	b = poolPays * cfee / poolGets
//
// where poolGets is the pool's input balance (amounts.in) and
// poolPays is the pool's output balance (amounts.out).
//
// Reference: rippled QualityFunction.h AMMTag constructor
func NewAMMQualityFunction(poolGets, poolPays tx.Amount, tradingFee uint16) *QualityFunction {
	if poolGets.Signum() <= 0 || poolPays.Signum() <= 0 {
		return nil
	}

	// Convert amounts to Number-like (IOU) for uniform arithmetic
	nPoolGets := toNumber(poolGets)
	nPoolPays := toNumber(poolPays)

	// cfee = 1 - tradingFee / 100000
	// Compute as an IOU Amount: (100000 - tradingFee) / 100000
	one := numberOne()
	var cfee tx.Amount
	if tradingFee == 0 {
		cfee = one
	} else {
		feeNum := state.NewIssuedAmountFromValue(int64(tradingFee), 0, "", "")
		scaleFactor := state.NewIssuedAmountFromValue(AuctionSlotFeeScaleFactor, 0, "", "")
		feeFrac := feeNum.Div(scaleFactor, false) // tradingFee / 100000
		cfee, _ = one.Sub(feeFrac)                // 1 - tradingFee/100000
	}

	// m = -cfee / poolGets
	cfeeNeg := cfee.Negate()
	m := cfeeNeg.Div(nPoolGets, false)

	// b = poolPays * cfee / poolGets
	b := nPoolPays.Mul(cfee, false)
	b = b.Div(nPoolGets, false)

	return &QualityFunction{
		m:       m,
		b:       b,
		quality: nil,
	}
}

// Combine composes this QualityFunction with another (the next step's QF).
// The combined function represents the chained quality across steps.
//
//	new_m = m + b * other.m
//	new_b = b * other.b
//	if new_m != 0, quality = nil
//
// Reference: rippled QualityFunction.cpp combine()
func (qf *QualityFunction) Combine(other QualityFunction) {
	// m += b * other.m
	bTimesOtherM := qf.b.Mul(other.m, false)
	qf.m, _ = qf.m.Add(bTimesOtherM)

	// b *= other.b
	qf.b = qf.b.Mul(other.b, false)

	// If m != 0, this is no longer a constant quality function
	if qf.m.Signum() != 0 {
		qf.quality = nil
	}
}

// IsConst returns true if the quality function is constant (CLOB-like).
// Reference: rippled QualityFunction.h isConst()
func (qf *QualityFunction) IsConst() bool {
	return qf.quality != nil
}

// OutFromAvgQ finds the output that produces the requested average quality.
//
//	out = (1/quality.rate() - b) / m
//
// Returns nil if the function is constant (m == 0) or if the result is non-positive.
// Reference: rippled QualityFunction.cpp outFromAvgQ()
func (qf *QualityFunction) OutFromAvgQ(q Quality) *tx.Amount {
	if qf.m.Signum() == 0 || q.Rate().Signum() == 0 {
		return nil
	}

	// Compute 1 / quality.rate() with round-up mode
	// Reference: rippled uses saveNumberRoundMode(Number::rounding_mode::upward)
	one := numberOne()
	rate := q.Rate()
	invRate := one.Div(rate, true) // round up

	// out = (invRate - b) / m
	numerator, _ := invRate.Sub(qf.b)
	out := numerator.Div(qf.m, true) // round up

	if out.Signum() <= 0 {
		return nil
	}

	return &out
}

// withinRelativeDistanceAmounts checks if two EitherAmounts are within
// a relative distance threshold: |a - b| / max(a, b) < dist.
// Reference: rippled AMMHelpers.h withinRelativeDistance() for amounts
func withinRelativeDistanceAmounts(a, b EitherAmount, dist float64) bool {
	if a.Compare(b) == 0 {
		return true
	}

	// Determine min and max
	minAmt, maxAmt := a, b
	if a.Compare(b) > 0 {
		minAmt, maxAmt = b, a
	}

	// Compute (max - min) / max
	diff := maxAmt.Sub(minAmt)

	var ratio float64
	if maxAmt.IsNative {
		if maxAmt.XRP == 0 {
			return false
		}
		ratio = float64(diff.XRP) / float64(maxAmt.XRP)
	} else {
		maxF := maxAmt.IOU.Float64()
		if maxF == 0 {
			return false
		}
		diffF := diff.IOU.Float64()
		ratio = diffF / maxF
	}

	return ratio < dist
}
