package payment

import (
	"encoding/binary"
	"math"
	"math/big"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// QualityOne is the identity transfer rate (1e9). Alias for protocol.QualityOne.
const QualityOne = protocol.QualityOne

// QualityFromKey extracts a Quality from a 32-byte book directory key.
// The quality is stored in the last 8 bytes (24-31) as a big-endian uint64.
// Reference: rippled's getQuality() in Indexes.cpp
func QualityFromKey(key [32]byte) Quality {
	return Quality{Value: binary.BigEndian.Uint64(key[24:])}
}

// Quality represents an exchange rate as output/input ratio.
// Internally stored as a uint64 where higher values represent lower quality
// (worse exchange rate for the taker).
//
// Quality is computed as: in/out, so lower quality means better deal
// (less input required for the same output).
type Quality struct {
	// Value is the encoded quality (same representation as STAmount)
	Value uint64
}

// qualityOne is the identity quality (1:1 exchange rate), equivalent to
// rippled's Quality{STAmount{uRateOne}} where uRateOne is the rate 1.0
// representation. Encoded as mantissa 1e15 with exponent -15 (i.e. the
// normalized STAmount form of the value 1.0). This is the value
// qualityFromFloat64(1.0) produces; defined once so the hot paths share a
// single constant instead of re-encoding it.
var qualityOne = Quality{Value: (uint64(-15+100) << 56) | uint64(1_000_000_000_000_000)}

// QualityFromAmounts creates a Quality from input and output amounts.
// Quality = in / out, encoded using STAmount-like floating point representation.
// Reference: rippled's getRate(offerOut, offerIn) in STAmount.cpp calls divide(offerIn, offerOut, noIssue()).
// Despite the parameter order (out, in), it returns in / out.
// Lower quality value means you pay less per unit received (better for taker).
func QualityFromAmounts(in, out EitherAmount) Quality {
	if out.IsZero() || in.IsZero() {
		return Quality{Value: 0}
	}

	// Convert both amounts to IOU-style for precise integer division.
	// Reference: rippled's getRate() calls divide() which normalizes XRP amounts
	// to [10^15, 10^16) mantissa range before performing the division.
	var inAmt, outAmt tx.Amount
	if in.IsNative {
		inAmt = state.NewIssuedAmountFromValue(in.XRP, 0, "", "")
	} else {
		inAmt = in.IOU
	}
	if out.IsNative {
		outAmt = state.NewIssuedAmountFromValue(out.XRP, 0, "", "")
	} else {
		outAmt = out.IOU
	}

	if outAmt.IsZero() {
		return Quality{Value: 0}
	}

	// Quality = in / out using precise STAmount division
	// Reference: rippled's getRate() → divide(offerIn, offerOut, noIssue())
	result := inAmt.Div(outAmt, false)

	mantissa := result.Mantissa()
	exponent := result.Exponent()

	if mantissa <= 0 {
		return Quality{Value: 0}
	}

	// Clamp exponent to valid range [-100, 155]
	if exponent < -100 {
		return Quality{Value: 0}
	}
	if exponent > 155 {
		return Quality{Value: ^uint64(0)}
	}

	storedExponent := uint64(exponent + 100)
	storedMantissa := uint64(mantissa)

	q := Quality{Value: (storedExponent << 56) | (storedMantissa & 0x00FFFFFFFFFFFFFF)}
	return q
}

// Compare compares two qualities
// Returns -1 if q < other (better), 0 if equal, 1 if q > other (worse)
func (q Quality) Compare(other Quality) int {
	if q.Value < other.Value {
		return -1
	}
	if q.Value > other.Value {
		return 1
	}
	return 0
}

// BetterThan returns true if q is better quality than other
// Lower value = better quality (less input for same output)
func (q Quality) BetterThan(other Quality) bool {
	return q.Value < other.Value
}

// WorseThan returns true if q is worse quality than other
func (q Quality) WorseThan(other Quality) bool {
	return q.Value > other.Value
}

// RelativeDistance computes the relative distance between two qualities.
// Returns |a-b|/min(a,b) using the encoded mantissa and exponent.
// Reference: rippled Quality.h relativeDistance()
func RelativeDistance(q1, q2 Quality) float64 {
	if q1.Value == q2.Value {
		return 0
	}

	minV, maxV := q1.Value, q2.Value
	if minV > maxV {
		minV, maxV = maxV, minV
	}

	extractMantissa := func(rate uint64) uint64 {
		return rate & ^(uint64(255) << 56)
	}
	extractExponent := func(rate uint64) int {
		return int(rate>>56) - 100
	}

	minVMantissa := extractMantissa(minV)
	maxVMantissa := extractMantissa(maxV)
	expDiff := extractExponent(maxV) - extractExponent(minV)

	minVD := float64(minVMantissa)
	var maxVD float64
	if expDiff != 0 {
		maxVD = float64(maxVMantissa) * math.Pow(10, float64(expDiff))
	} else {
		maxVD = float64(maxVMantissa)
	}

	return (maxVD - minVD) / minVD
}

// QualityFromMantissaExp creates a Quality representing the value mantissa * 10^exponent.
// This mirrors rippled's TheoreticalQuality_test.cpp toQuality() helper which creates
// a Quality from STAmount(noIssue(), mantissa, exponent) / STAmount(noIssue(), 1).
// Reference: rippled TheoreticalQuality_test.cpp lines 501-509
func QualityFromMantissaExp(mantissa uint64, exponent int) Quality {
	one := NewIOUEitherAmount(state.NewIssuedAmountFromValue(1, 0, "", ""))
	v := NewIOUEitherAmount(state.NewIssuedAmountFromValue(int64(mantissa), exponent, "", ""))
	return QualityFromAmounts(v, one)
}

// Increment returns a Quality that is slightly better (lower value).
// This is used for passive offers where we only want to cross against
// offers with STRICTLY better quality.
// Reference: rippled CreateOffer.cpp line 364: ++threshold (which does --m_value).
// In rippled's Quality encoding, lower m_value = better quality. So ++threshold
// makes the threshold better, and the check "offer >= threshold" then only passes
// for offers that are strictly better than the original passive-offer quality.
// Our encoding matches rippled: lower Value = better quality, so Increment() decrements.
func (q Quality) Increment() Quality {
	if q.Value == 0 {
		return q // Already at min, can't decrement
	}
	return Quality{Value: q.Value - 1}
}

// Float64 decodes the quality value to a float64 ratio (in/out).
// The quality is stored in STAmount format: top 8 bits = exponent+100, lower 56 bits = mantissa.
// Reference: rippled's amountFromQuality() in STAmount.cpp
func (q Quality) Float64() float64 {
	if q.Value == 0 {
		return 0
	}
	mantissa := q.Value & 0x00FFFFFFFFFFFFFF
	exponent := int((q.Value >> 56)) - 100

	// The encoding already normalized mantissa to [10^15, 10^16) and adjusted
	// exponent accordingly, so we just decode directly: value = mantissa * 10^exponent
	return float64(mantissa) * pow10(exponent)
}

// Rate returns the quality rate as an Amount for precise arithmetic.
// This is equivalent to rippled's quality.rate() which returns an STAmount.
// Reference: rippled's amountFromQuality() in STAmount.cpp
func (q Quality) Rate() tx.Amount {
	if q.Value == 0 {
		return tx.NewIssuedAmount(0, -100, "", "")
	}
	mantissa := int64(q.Value & 0x00FFFFFFFFFFFFFF)
	exponent := int((q.Value >> 56)) - 100
	result := tx.NewIssuedAmount(mantissa, exponent, "", "")
	return result
}

// CeilOut limits the output amount and recalculates input using mulRound (non-strict).
// This is the legacy version with "slop" used when fixReducedOffersV1 is NOT enabled.
// Uses mulRound with hardcoded roundUp=true (matching rippled's ceil_out behavior).
// Reference: rippled Quality.cpp ceil_out (non-strict) — uses mulRound, always roundUp=true
func (q Quality) CeilOut(amtIn, amtOut EitherAmount, limit EitherAmount) (EitherAmount, EitherAmount) {
	if amtOut.Compare(limit) <= 0 {
		return amtIn, amtOut
	}

	qRate := q.Rate()

	var limitAmt tx.Amount
	if limit.IsNative {
		limitAmt = state.NewIssuedAmountFromValue(limit.XRP, 0, "", "")
	} else {
		limitAmt = limit.IOU
	}

	var inCurrency, inIssuer string
	if amtIn.IsNative {
		inCurrency = ""
		inIssuer = ""
	} else {
		inCurrency = amtIn.IOU.Currency
		inIssuer = amtIn.IOU.Issuer
	}

	var resultInEither EitherAmount
	if amtIn.IsNative {
		// Native output: use MulRoundNative which applies canonicalizeRound(native=true)
		// directly, matching rippled's mulRoundImpl when the output asset is XRP.
		// The non-native MulRound path applies IOU canonicalization first, which
		// uses different rounding than the native path and causes off-by-one errors.
		// Reference: rippled STAmount.cpp mulRoundImpl + canonicalizeRound(native=true)
		resultInEither = NewXRPEitherAmount(state.MulRoundNative(limitAmt, qRate, true))
	} else {
		// Non-strict: mulRound with roundUp=true (always)
		resultIn := state.MulRound(limitAmt, qRate, inCurrency, inIssuer, true)
		resultInEither = NewIOUEitherAmount(tx.NewIssuedAmount(
			resultIn.Mantissa(), resultIn.Exponent(), inCurrency, inIssuer))
	}

	// Clamp: result.in must not exceed amount.in
	if resultInEither.Compare(amtIn) > 0 {
		resultInEither = amtIn
	}

	return resultInEither, limit
}

// CeilOutStrict limits the output amount and recalculates input using mulRoundStrict.
// If amount.out > limit, compute result.in = mulRoundStrict(limit, quality.rate(), ...)
// and clamp result.in to amount.in.
// Reference: rippled Quality.cpp ceil_out_impl with mulRoundStrict (lines 115-155)
func (q Quality) CeilOutStrict(amtIn, amtOut EitherAmount, limit EitherAmount, roundUp bool) (EitherAmount, EitherAmount) {
	if amtOut.Compare(limit) <= 0 {
		return amtIn, amtOut
	}

	// result.in = mulRoundStrict(limit, quality.rate(), amtIn.asset, roundUp)
	qRate := q.Rate()

	var limitAmt tx.Amount
	if limit.IsNative {
		limitAmt = state.NewIssuedAmountFromValue(limit.XRP, 0, "", "")
	} else {
		limitAmt = limit.IOU
	}

	var inCurrency, inIssuer string
	if amtIn.IsNative {
		inCurrency = ""
		inIssuer = ""
	} else {
		inCurrency = amtIn.IOU.Currency
		inIssuer = amtIn.IOU.Issuer
	}

	resultIn := state.MulRoundStrict(limitAmt, qRate, inCurrency, inIssuer, roundUp)

	var resultInEither EitherAmount
	if amtIn.IsNative {
		var drops int64
		if roundUp {
			// roundUp=true: rippled calls canonicalizeRoundStrict before STAmount construction.
			// Reference: rippled mulRoundImpl - CanonicalizeFunc called when resultNegative != roundUp
			drops = CanonicalizeDropsStrict(resultIn.Mantissa(), resultIn.Exponent(), roundUp)
		} else {
			// roundUp=false (positive values): rippled does NOT call canonicalizeRoundStrict.
			// STAmount::canonicalize() for native applies plain floor (truncation):
			//   while (mOffset < 0) { mValue /= 10; ++mOffset; }
			// Reference: rippled STAmount.cpp canonicalize() lines 914-918
			drops = canonicalizeDropsFloor(resultIn.Mantissa(), resultIn.Exponent())
		}
		resultInEither = NewXRPEitherAmount(drops)
	} else {
		resultInEither = NewIOUEitherAmount(tx.NewIssuedAmount(
			resultIn.Mantissa(), resultIn.Exponent(), inCurrency, inIssuer))
	}

	// Clamp: result.in must not exceed amount.in
	if resultInEither.Compare(amtIn) > 0 {
		resultInEither = amtIn
	}

	return resultInEither, limit
}

// CeilIn limits the input amount and recalculates output using divRound (non-strict).
// Equivalent to rippled's ceil_in which uses divRound with hardcoded roundUp=true.
// Used when fixReducedOffersV2 is NOT enabled.
// Reference: rippled Quality.cpp ceil_in (lines 100-104) uses divRound (always rounds up)
func (q Quality) CeilIn(amtIn, amtOut EitherAmount, limit EitherAmount) (EitherAmount, EitherAmount) {
	if amtIn.Compare(limit) <= 0 {
		return amtIn, amtOut
	}

	qRate := q.Rate()

	var limitAmt tx.Amount
	if limit.IsNative {
		limitAmt = state.NewIssuedAmountFromValue(limit.XRP, 0, "", "")
	} else {
		limitAmt = limit.IOU
	}

	var outCurrency, outIssuer string
	if amtOut.IsNative {
		outCurrency = ""
		outIssuer = ""
	} else {
		outCurrency = amtOut.IOU.Currency
		outIssuer = amtOut.IOU.Issuer
	}

	var resultOutEither EitherAmount
	if amtOut.IsNative {
		// Native output: use DivRoundNative which applies canonicalizeRound(native=true)
		// directly, matching rippled's divRoundImpl when the output asset is XRP.
		// The non-native DivRound path applies IOU canonicalization first, which
		// uses different rounding than the native path and causes off-by-one errors.
		// Reference: rippled STAmount.cpp divRoundImpl + canonicalizeRound(native=true)
		resultOutEither = NewXRPEitherAmount(state.DivRoundNative(limitAmt, qRate, true))
	} else {
		// Non-strict: divRound with roundUp=true (matching rippled's ceil_in which uses divRound)
		resultOut := state.DivRound(limitAmt, qRate, outCurrency, outIssuer, true)
		resultOutEither = NewIOUEitherAmount(tx.NewIssuedAmount(
			resultOut.Mantissa(), resultOut.Exponent(), outCurrency, outIssuer))
	}

	// Clamp: result.out must not exceed amount.out
	if resultOutEither.Compare(amtOut) > 0 {
		resultOutEither = amtOut
	}

	return limit, resultOutEither
}

// CeilInStrict limits the input amount and recalculates output using divRoundStrict.
// If amount.in > limit, compute result.out = divRoundStrict(limit, quality.rate(), ...)
// and clamp result.out to amount.out.
// Reference: rippled Quality.cpp ceil_in_impl with divRoundStrict (lines 75-113)
func (q Quality) CeilInStrict(amtIn, amtOut EitherAmount, limit EitherAmount, roundUp bool) (EitherAmount, EitherAmount) {
	if amtIn.Compare(limit) <= 0 {
		return amtIn, amtOut
	}

	qRate := q.Rate()

	var limitAmt tx.Amount
	if limit.IsNative {
		limitAmt = state.NewIssuedAmountFromValue(limit.XRP, 0, "", "")
	} else {
		limitAmt = limit.IOU
	}

	var outCurrency, outIssuer string
	if amtOut.IsNative {
		outCurrency = ""
		outIssuer = ""
	} else {
		outCurrency = amtOut.IOU.Currency
		outIssuer = amtOut.IOU.Issuer
	}

	resultOut := state.DivRoundStrict(limitAmt, qRate, outCurrency, outIssuer, roundUp)

	var resultOutEither EitherAmount
	if amtOut.IsNative {
		var drops int64
		if roundUp {
			// roundUp=true: rippled calls canonicalizeRound before STAmount construction.
			// Reference: rippled divRoundImpl - canonicalizeRound called when resultNegative != roundUp
			drops = CanonicalizeDrops(resultOut.Mantissa(), resultOut.Exponent())
		} else {
			// roundUp=false (positive values): rippled does NOT call canonicalizeRound.
			// STAmount::canonicalize() for native applies plain floor (truncation):
			//   while (mOffset < 0) { mValue /= 10; ++mOffset; }
			// Reference: rippled STAmount.cpp canonicalize() lines 914-918
			drops = canonicalizeDropsFloor(resultOut.Mantissa(), resultOut.Exponent())
		}
		resultOutEither = NewXRPEitherAmount(drops)
	} else {
		resultOutEither = NewIOUEitherAmount(tx.NewIssuedAmount(
			resultOut.Mantissa(), resultOut.Exponent(), outCurrency, outIssuer))
	}

	// Clamp: result.out must not exceed amount.out
	if resultOutEither.Compare(amtOut) > 0 {
		resultOutEither = amtOut
	}

	return limit, resultOutEither
}

// Compose multiplies two qualities together using exact STAmount arithmetic.
// This matches rippled's composed_quality() in Quality.cpp which uses mulRound().
//
// Algorithm:
//  1. Extract mantissa/exponent from each quality (STAmount-like encoding)
//  2. Multiply mantissas, divide by 10^14 with round-up (mulRound with roundUp=true)
//  3. Canonicalize result mantissa to [10^15, 10^16-1] with round-up
//  4. Encode back to quality format
//
// Reference: rippled Quality.cpp composed_quality() lines 157-180
func (q Quality) Compose(other Quality) Quality {
	if q.Value == 0 || other.Value == 0 {
		return Quality{Value: 0}
	}

	m1 := int64(q.Value & 0x00FFFFFFFFFFFFFF)
	e1 := int((q.Value >> 56)) - 100
	m2 := int64(other.Value & 0x00FFFFFFFFFFFFFF)
	e2 := int((other.Value >> 56)) - 100

	if m1 == 0 || m2 == 0 {
		return Quality{Value: 0}
	}

	// mulRound(lhs_rate, rhs_rate, asset, roundUp=true) for positive values:
	// amount = (m1 * m2 + 10^14 - 1) / 10^14
	// Reference: rippled STAmount.cpp mulRoundImpl lines 1599-1610
	bigM1 := new(big.Int).SetInt64(m1)
	bigM2 := new(big.Int).SetInt64(m2)
	product := new(big.Int).Mul(bigM1, bigM2)

	tenTo14 := new(big.Int).SetInt64(100000000000000)  // 10^14
	tenTo14m1 := new(big.Int).SetInt64(99999999999999) // 10^14 - 1
	product.Add(product, tenTo14m1)                    // round up
	product.Div(product, tenTo14)

	offset := e1 + e2 + 14

	// canonicalizeRound with roundUp=true
	// Reference: rippled STAmount.cpp canonicalizeRound
	minMantissa := new(big.Int).SetInt64(1000000000000000) // 10^15
	maxMantissa := new(big.Int).SetInt64(9999999999999999) // 10^16 - 1
	ten := big.NewInt(10)
	nine := big.NewInt(9)

	// Scale up if too small
	for product.Cmp(minMantissa) < 0 && product.Sign() > 0 {
		product.Mul(product, ten)
		offset--
	}
	// Scale down if too large, rounding up: (amount + 9) / 10
	for product.Cmp(maxMantissa) > 0 {
		product.Add(product, nine)
		product.Div(product, ten)
		offset++
	}

	storedExponent := uint64(offset + 100)
	storedMantissa := product.Uint64()

	return Quality{Value: (storedExponent << 56) | storedMantissa}
}

// qualityFromFloat64 encodes a float64 rate back to Quality format
func qualityFromFloat64(rate float64) Quality {
	if rate <= 0 {
		return Quality{Value: 0}
	}

	// Normalize mantissa to [10^15, 10^16)
	exponent := 0
	mantissa := rate

	minMantissa := 1e15
	maxMantissa := 1e16

	if mantissa != 0 {
		for mantissa < minMantissa {
			mantissa *= 10
			exponent--
		}
		for mantissa >= maxMantissa {
			mantissa /= 10
			exponent++
		}
	}

	// Clamp exponent
	if exponent < -100 {
		return Quality{Value: 0}
	}
	if exponent > 155 {
		return Quality{Value: ^uint64(0)}
	}

	storedExponent := uint64(exponent + 100)
	storedMantissa := uint64(mantissa)

	return Quality{Value: (storedExponent << 56) | (storedMantissa & 0x00FFFFFFFFFFFFFF)}
}

// pow10 returns 10^n for small n values
func pow10(n int) float64 {
	if n == 0 {
		return 1
	}
	if n > 0 {
		result := 1.0
		for range n {
			result *= 10
		}
		return result
	}
	// n < 0
	result := 1.0
	for i := 0; i > n; i-- {
		result /= 10
	}
	return result
}
