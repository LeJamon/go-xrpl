package payment

import (
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
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
	math numberMath
	// m is the slope (zero for CLOB-like constant quality)
	m state.XRPLNumber
	// b is the intercept
	b state.XRPLNumber
	// quality is set when the function is constant (CLOB-like).
	// nil means the function has a non-zero slope (AMM).
	quality *Quality
}

// NewCLOBLikeQualityFunction creates a QualityFunction for CLOB-like offers
// (constant quality). m = 0, b = 1/quality.rate().
// Reference: rippled QualityFunction.cpp QualityFunction(Quality, CLOBLikeTag)
func NewCLOBLikeQualityFunction(q Quality) *QualityFunction {
	return newCLOBLikeQualityFunction(legacyNumberMath(), q)
}

func newCLOBLikeQualityFunction(m numberMath, q Quality) *QualityFunction {
	rate := q.Rate()
	if rate.Signum() <= 0 {
		return nil
	}
	// b = 1 / quality.rate()
	b := m.one().Div(m.fromAmount(rate, state.RoundToNearest))

	return &QualityFunction{
		math:    m,
		m:       m.zero(),
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
	return newAMMQualityFunction(legacyNumberMath(), poolGets, poolPays, tradingFee)
}

func newAMMQualityFunction(m numberMath, poolGets, poolPays tx.Amount, tradingFee uint16) *QualityFunction {
	if poolGets.Signum() <= 0 || poolPays.Signum() <= 0 {
		return nil
	}

	// Convert amounts to Number-like (IOU) for uniform arithmetic
	nPoolGets := m.fromAmount(poolGets, state.RoundToNearest)
	nPoolPays := m.fromAmount(poolPays, state.RoundToNearest)

	// cfee = 1 - tradingFee / 100000
	// Compute as an IOU Amount: (100000 - tradingFee) / 100000
	var cfee state.XRPLNumber
	if tradingFee == 0 {
		cfee = m.one()
	} else {
		feeFrac := m.int(int64(tradingFee)).Div(m.int(AuctionSlotFeeScaleFactor))
		cfee = m.sub(m.one(), feeFrac)
	}

	// m = -cfee / poolGets
	cfeeNeg := cfee.Negate()
	slope := cfeeNeg.Div(nPoolGets)

	// b = poolPays * cfee / poolGets
	b := nPoolPays.Mul(cfee).Div(nPoolGets)

	return &QualityFunction{
		math:    m,
		m:       slope,
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
	bTimesOtherM := qf.b.Mul(other.m)
	qf.m = qf.m.Add(bTimesOtherM)

	// b *= other.b
	qf.b = qf.b.Mul(other.b)

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
func (qf *QualityFunction) OutFromAvgQ(q Quality) *state.XRPLNumber {
	if qf.m.Signum() == 0 || q.Rate().Signum() == 0 {
		return nil
	}

	// rippled wraps the whole expression (1/quality.rate() - b) / m in
	// Number::rounding_mode::upward, so every op — the reciprocal, the
	// subtraction (a catastrophic cancellation) and the final divide — rounds
	// upward in the unified Number space.
	rate := qf.math.fromAmount(q.Rate(), state.RoundUpward)
	invRate := qf.math.one().DivRounded(rate, state.RoundUpward)
	numerator := qf.math.subRounded(invRate, qf.b, state.RoundUpward)
	out := numerator.DivRounded(qf.m, state.RoundUpward)

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
	} else if maxAmt.IsMPT {
		if maxAmt.MPT == 0 {
			return true
		}
		ratio = math.Abs(float64(diff.MPT) / float64(maxAmt.MPT))
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
