package payment

import (
	"encoding/binary"
	"math"
	"math/big"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/protocol"
)

// QualityFromKey extracts a Quality from a 32-byte book directory key.
// The quality is stored in the last 8 bytes (24-31) as a big-endian uint64.
// Reference: rippled's getQuality() in Indexes.cpp
func QualityFromKey(key [32]byte) Quality {
	return Quality{Value: binary.BigEndian.Uint64(key[24:])}
}

// DebtDirection indicates whether a step is issuing or redeeming currency
type DebtDirection int

const (
	// DebtDirectionIssues means the step is creating new debt (issuing)
	DebtDirectionIssues DebtDirection = iota
	// DebtDirectionRedeems means the step is reducing existing debt (redeeming)
	DebtDirectionRedeems
)

// Redeems returns true if the direction is redeeming
func Redeems(dir DebtDirection) bool {
	return dir == DebtDirectionRedeems
}

// Issues returns true if the direction is issuing
func Issues(dir DebtDirection) bool {
	return dir == DebtDirectionIssues
}

// StrandDirection indicates the direction of flow through a strand
type StrandDirection int

const (
	// StrandDirectionForward means executing from source to destination
	StrandDirectionForward StrandDirection = iota
	// StrandDirectionReverse means calculating from destination back to source
	StrandDirectionReverse
)

// QualityOne is the identity transfer rate (1e9). Alias for protocol.QualityOne.
const QualityOne = protocol.QualityOne

// EitherAmount holds either an XRP amount (in drops) or an IOU amount
// This allows unified handling in the flow algorithm regardless of currency type
type EitherAmount struct {
	// IsNative is true if this is an XRP amount, false for IOU
	IsNative bool

	// XRP holds the amount in drops (only valid if IsNative is true)
	XRP int64

	// IOU holds the IOU amount (only valid if IsNative is false)
	IOU tx.Amount
}

// NewXRPEitherAmount creates an EitherAmount for XRP
func NewXRPEitherAmount(drops int64) EitherAmount {
	return EitherAmount{
		IsNative: true,
		XRP:      drops,
	}
}

// NewIOUEitherAmount creates an EitherAmount for IOU
func NewIOUEitherAmount(amount tx.Amount) EitherAmount {
	return EitherAmount{
		IsNative: false,
		IOU:      amount,
	}
}

// ZeroXRPEitherAmount creates a zero XRP EitherAmount
func ZeroXRPEitherAmount() EitherAmount {
	return EitherAmount{
		IsNative: true,
		XRP:      0,
	}
}

// ZeroIOUEitherAmount creates a zero IOU EitherAmount with the given currency/issuer
func ZeroIOUEitherAmount(currency, issuer string) EitherAmount {
	return EitherAmount{
		IsNative: false,
		IOU:      tx.NewIssuedAmount(0, -100, currency, issuer),
	}
}

// IsZero returns true if the amount is zero
func (e EitherAmount) IsZero() bool {
	if e.IsNative {
		return e.XRP == 0
	}
	return e.IOU.IsZero()
}

// IsEffectivelyZero returns true if the amount is effectively zero
func (e EitherAmount) IsEffectivelyZero() bool {
	if e.IsNative {
		return e.XRP == 0
	}
	return e.IOU.IsZero()
}

// IsNegative returns true if the amount is negative
func (e EitherAmount) IsNegative() bool {
	if e.IsNative {
		return e.XRP < 0
	}
	return e.IOU.IsNegative()
}

// Add adds two EitherAmounts (must be same type - both XRP or both IOU)
func (e EitherAmount) Add(other EitherAmount) EitherAmount {
	if e.IsNative {
		return NewXRPEitherAmount(e.XRP + other.XRP)
	}
	result, _ := e.IOU.Add(other.IOU)
	return NewIOUEitherAmount(result)
}

// Sub subtracts other from e (must be same type)
func (e EitherAmount) Sub(other EitherAmount) EitherAmount {
	if e.IsNative {
		return NewXRPEitherAmount(e.XRP - other.XRP)
	}
	result, _ := e.IOU.Sub(other.IOU)
	return NewIOUEitherAmount(result)
}

// Compare compares two EitherAmounts
// Returns -1 if e < other, 0 if equal, 1 if e > other
func (e EitherAmount) Compare(other EitherAmount) int {
	if e.IsNative {
		if e.XRP < other.XRP {
			return -1
		}
		if e.XRP > other.XRP {
			return 1
		}
		return 0
	}
	return e.IOU.Compare(other.IOU)
}

// Min returns the minimum of two EitherAmounts
func (e EitherAmount) Min(other EitherAmount) EitherAmount {
	if e.Compare(other) <= 0 {
		return e
	}
	return other
}

// Max returns the maximum of two EitherAmounts
func (e EitherAmount) Max(other EitherAmount) EitherAmount {
	if e.Compare(other) >= 0 {
		return e
	}
	return other
}

// DivideFloat divides this amount by another, returning a float64 ratio
func (e EitherAmount) DivideFloat(other EitherAmount) float64 {
	var eVal, otherVal float64
	if e.IsNative {
		eVal = float64(e.XRP)
	} else {
		eVal = e.IOU.Float64()
	}
	if other.IsNative {
		otherVal = float64(other.XRP)
	} else {
		otherVal = other.IOU.Float64()
	}
	if otherVal == 0 {
		return 0
	}
	return eVal / otherVal
}

// MultiplyFloat multiplies this amount by a float64 factor
func (e EitherAmount) MultiplyFloat(factor float64) EitherAmount {
	if e.IsNative {
		return NewXRPEitherAmount(int64(float64(e.XRP) * factor))
	}
	val := e.IOU.Float64()
	newVal := val * factor
	return NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(newVal, e.IOU.Currency, e.IOU.Issuer))
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

// pow10 returns 10^n for small n values
func pow10(n int) float64 {
	if n == 0 {
		return 1
	}
	if n > 0 {
		result := 1.0
		for i := 0; i < n; i++ {
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

// CanonicalizeDrops converts an IOU-style mantissa/exponent to XRP drops,
// matching rippled's canonicalizeRound (non-strict) for native amounts.
// Uses loop count (not actual remainder) to decide rounding: adds 10 when
// only 1 division loop occurred, 9 when 2+ loops.
// Reference: rippled STAmount.cpp canonicalizeRound lines 1432-1464
func CanonicalizeDrops(mantissa int64, exponent int) int64 {
	if mantissa == 0 {
		return 0
	}
	value := mantissa
	if value < 0 {
		value = -value
	}

	// Scale up if exponent > 0
	for exponent > 0 {
		value *= 10
		exponent--
	}

	// Scale down if exponent < 0
	if exponent < 0 {
		loops := 0
		for exponent < -1 {
			value /= 10
			exponent++
			loops++
		}
		// Non-strict: add 10 when loops < 2, add 9 when loops >= 2
		// Reference: rippled "value += (loops >= 2) ? 9 : 10;"
		var adder int64 = 10
		if loops >= 2 {
			adder = 9
		}
		value = (value + adder) / 10
	}

	if mantissa < 0 {
		return -value
	}
	return value
}

// canonicalizeDropsFloor converts an IOU-style mantissa/exponent to XRP drops
// using plain floor (truncation toward zero).
// This matches rippled's STAmount::canonicalize() for native amounts when
// canonicalizeRoundStrict is NOT called (i.e., when roundUp=false for positive values).
// Reference: rippled STAmount.cpp canonicalize() lines 914-918:
//
//	while (mOffset < 0) { mValue /= 10; ++mOffset; }
func canonicalizeDropsFloor(mantissa int64, exponent int) int64 {
	if mantissa == 0 || exponent <= -20 {
		return 0
	}
	value := mantissa
	if value < 0 {
		value = -value
	}
	for exponent > 0 {
		value *= 10
		exponent--
	}
	for exponent < 0 {
		value /= 10
		exponent++
	}
	if mantissa < 0 {
		return -value
	}
	return value
}

// canonicalizeDropsRound converts an IOU-style mantissa/exponent to XRP drops
// using round-to-nearest with ties going to even (banker's rounding).
// This matches rippled's Number::operator rep() which is used by
// XRPAmount{Number} constructor (e.g., in limitOut for XRP output).
// Reference: rippled Number.cpp operator rep() lines 480-512
func canonicalizeDropsRound(mantissa int64, exponent int) int64 {
	if mantissa == 0 || exponent <= -20 {
		return 0
	}
	value := mantissa
	negative := false
	if value < 0 {
		negative = true
		value = -value
	}
	for exponent > 0 {
		value *= 10
		exponent--
	}
	// Track remainder digits for rounding
	var lastDigit int64
	var hasRemainder bool
	for exponent < 0 {
		d := value % 10
		if exponent == -1 {
			// This is the digit we'll round on
			lastDigit = d
		} else if d != 0 {
			hasRemainder = true
		}
		value /= 10
		exponent++
	}
	// Round to nearest, even on tie
	// lastDigit > 5: round up
	// lastDigit == 5 && hasRemainder: round up (more than 0.5)
	// lastDigit == 5 && !hasRemainder: round to even (banker's rounding)
	// lastDigit < 5: round down (already done by truncation)
	if lastDigit > 5 || (lastDigit == 5 && hasRemainder) || (lastDigit == 5 && !hasRemainder && (value%2) == 1) {
		value++
	}
	if negative {
		return -value
	}
	return value
}

// CanonicalizeDropsStrict converts an IOU-style mantissa/exponent to XRP drops,
// matching rippled's canonicalizeRoundStrict for native amounts.
// Reference: rippled STAmount.cpp canonicalizeRoundStrict lines 1471-1497
func CanonicalizeDropsStrict(mantissa int64, exponent int, roundUp bool) int64 {
	if mantissa == 0 {
		return 0
	}
	value := mantissa
	if value < 0 {
		value = -value
	}

	// Scale up if exponent > 0
	for exponent > 0 {
		value *= 10
		exponent--
	}

	// Scale down if exponent < 0
	// Track whether any bits were lost during intermediate divisions
	if exponent < 0 {
		hadRemainder := false
		for exponent < -1 {
			newValue := value / 10
			if value != newValue*10 {
				hadRemainder = true
			}
			value = newValue
			exponent++
		}
		// Final division with proper rounding
		// When roundUp=true and there was a remainder, add 10 to force round-up
		// Otherwise add 9 (rounds to nearest, up on 5)
		var adder int64 = 9
		if hadRemainder && roundUp {
			adder = 10
		}
		value = (value + adder) / 10
	}

	if mantissa < 0 {
		return -value
	}
	return value
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

	// Extract mantissa and exponent from each quality
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

// Issue represents a currency/issuer pair
type Issue struct {
	Currency string
	Issuer   [20]byte
}

// IsXRP returns true if this issue represents XRP
func (i Issue) IsXRP() bool {
	return i.Currency == "XRP" || i.Currency == ""
}

// Book represents an order book (input/output issue pair)
type Book struct {
	In       Issue
	Out      Issue
	DomainID *[32]byte // nil for open market, non-nil for permissioned domain book
}

// Strand is a sequence of Steps forming a complete payment path
type Strand []Step

// Step is a single unit of payment flow in a strand.
// Steps transform amounts from one currency/account to another.
type Step interface {
	Rev(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, out EitherAmount) (EitherAmount, EitherAmount)
	Fwd(sb *PaymentSandbox, afView *PaymentSandbox, ofrsToRm map[[32]byte]bool, in EitherAmount) (EitherAmount, EitherAmount)
	CachedIn() *EitherAmount
	CachedOut() *EitherAmount
	DebtDirection(sb *PaymentSandbox, dir StrandDirection) DebtDirection
	QualityUpperBound(v *PaymentSandbox, prevStepDir DebtDirection) (*Quality, DebtDirection)
	// GetQualityFunc returns the QualityFunction for this step.
	// Used in one-path optimization where the quality function is non-constant
	// (has AMM) and there is a limitQuality. The QualityFunction allows
	// calculation of required path output given requested limitQuality.
	// The default implementation creates a CLOB-like QF from QualityUpperBound.
	// Reference: rippled Steps.h Step::getQualityFunc()
	GetQualityFunc(v *PaymentSandbox, prevStepDir DebtDirection) (*QualityFunction, DebtDirection)
	IsZero(amt EitherAmount) bool
	EqualIn(a, b EitherAmount) bool
	EqualOut(a, b EitherAmount) bool
	Inactive() bool
	OffersUsed() uint32
	DirectStepAccts() *[2][20]byte
	BookStepBook() *Book
	LineQualityIn(v *PaymentSandbox) uint32
	ValidFwd(sb *PaymentSandbox, afView *PaymentSandbox, in EitherAmount) (bool, EitherAmount)
}

// StrandResult captures the outcome of executing a single strand
type StrandResult struct {
	Success    bool
	In         EitherAmount
	Out        EitherAmount
	Sandbox    *PaymentSandbox
	OffsToRm   map[[32]byte]bool
	OffersUsed uint32
	Inactive   bool
}

// FlowResult captures the overall result of payment flow execution
type FlowResult struct {
	In              EitherAmount
	Out             EitherAmount
	Sandbox         *PaymentSandbox
	RemovableOffers map[[32]byte]bool
	Result          tx.Result
}

// ToEitherAmount converts a tx.Amount to EitherAmount
func ToEitherAmount(amt tx.Amount) EitherAmount {
	if amt.IsNative() {
		return NewXRPEitherAmount(amt.Drops())
	}
	return NewIOUEitherAmount(amt)
}

// FromEitherAmount converts EitherAmount back to tx.Amount
func FromEitherAmount(e EitherAmount) tx.Amount {
	if e.IsNative {
		return tx.NewXRPAmount(e.XRP)
	}
	return e.IOU
}

// GetIssue extracts the Issue from a tx.Amount
func GetIssue(amt tx.Amount) Issue {
	if amt.IsNative() {
		return Issue{Currency: "XRP"}
	}

	var issuerBytes [20]byte
	if issuerID, err := state.DecodeAccountID(amt.Issuer); err == nil {
		issuerBytes = issuerID
	}

	return Issue{
		Currency: amt.Currency,
		Issuer:   issuerBytes,
	}
}

// MulRatio multiplies an amount by a ratio (num/den)
func MulRatio(amt EitherAmount, num, den uint32, roundUp bool) EitherAmount {
	if den == 0 {
		return amt
	}

	if amt.IsNative {
		xrpAmt := tx.NewXRPAmount(amt.XRP)
		result := xrpAmt.MulRatio(num, den, roundUp)
		return NewXRPEitherAmount(result.Drops())
	}

	return NewIOUEitherAmount(amt.IOU.MulRatio(num, den, roundUp))
}

// DivRatio divides an amount by a ratio (num/den) = amt * den / num
func DivRatio(amt EitherAmount, num, den uint32, roundUp bool) EitherAmount {
	if num == 0 {
		return amt
	}
	return MulRatio(amt, den, num, roundUp)
}
