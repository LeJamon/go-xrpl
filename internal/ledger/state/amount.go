package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Constants matching rippled's STAmount.h
const (
	// Exponent range for normalized IOU amounts
	MinExponent = -96
	MaxExponent = 80

	// Mantissa range for normalized IOU amounts [10^15, 10^16 - 1]
	// cMinValue / cMaxValue in rippled
	MinMantissa int64 = 1_000_000_000_000_000
	MaxMantissa int64 = 9_999_999_999_999_999

	// cMaxValue in rippled (10^16). Note: MaxMantissa = cMaxValue - 1.
	cMaxValue uint64 = 10_000_000_000_000_000

	// tenTo14: scaling constant matching rippled's STAmount.cpp
	tenTo14 uint64 = 100_000_000_000_000

	// Maximum native XRP in drops that can exist on the network
	MaxNativeDrops uint64 = 100_000_000_000_000_000

	// Drops per XRP
	DropsPerXRP int64 = 1_000_000

	// Zero exponent value used for IOU zero amounts
	zeroExponent = -100
)

// XRPAmount represents XRP in drops (the smallest unit)
// Uses int64 to match rippled's XRPAmount (allows negative for debt calculations)
type XRPAmount struct {
	drops int64
}

// NewXRPAmountFromDrops creates an XRP amount from drops
func NewXRPAmountFromDrops(drops int64) XRPAmount {
	return XRPAmount{drops: drops}
}

// Drops returns the amount in drops
func (x XRPAmount) Drops() int64 {
	return x.drops
}

// IsZero returns true if the amount is zero
func (x XRPAmount) IsZero() bool {
	return x.drops == 0
}

// IsNegative returns true if the amount is negative
func (x XRPAmount) IsNegative() bool {
	return x.drops < 0
}

// Signum returns the sign of the amount (-1, 0, or 1)
func (x XRPAmount) Signum() int {
	if x.drops < 0 {
		return -1
	}
	if x.drops > 0 {
		return 1
	}
	return 0
}

// Negate returns the negated amount
func (x XRPAmount) Negate() XRPAmount {
	return XRPAmount{drops: -x.drops}
}

// Add adds two XRP amounts
func (x XRPAmount) Add(other XRPAmount) XRPAmount {
	return XRPAmount{drops: x.drops + other.drops}
}

// Sub subtracts two XRP amounts
func (x XRPAmount) Sub(other XRPAmount) XRPAmount {
	return XRPAmount{drops: x.drops - other.drops}
}

// String returns the drops as a string
func (x XRPAmount) String() string {
	return strconv.FormatInt(x.drops, 10)
}

// DecimalXRP returns the amount in XRP (not drops)
func (x XRPAmount) DecimalXRP() float64 {
	return float64(x.drops) / float64(DropsPerXRP)
}

// IOUAmountValue represents an issued currency amount using mantissa/exponent
// Matches rippled's IOUAmount representation
type IOUAmountValue struct {
	mantissa int64 // Signed mantissa, allows negative values
	exponent int   // Exponent in range [-96, 80] for non-zero values
}

// NewIOUAmountValue creates a new IOU amount value and normalizes it
func NewIOUAmountValue(mantissa int64, exponent int) IOUAmountValue {
	v := IOUAmountValue{mantissa: mantissa, exponent: exponent}
	v.normalize()
	return v
}

// ZeroIOUValue returns a zero IOU amount value
func ZeroIOUValue() IOUAmountValue {
	return IOUAmountValue{mantissa: 0, exponent: zeroExponent}
}

// normalize adjusts the mantissa and exponent to the proper range
// Matches rippled's IOUAmount::normalize()
// When fixUniversalNumber is enabled, delegates to XRPLNumber for Guard-based rounding.
// Reference: IOUAmount.cpp lines 75-126
func (v *IOUAmountValue) normalize() {
	if v.mantissa == 0 {
		v.mantissa = 0
		v.exponent = zeroExponent
		return
	}

	// When switchover is on, delegate to XRPLNumber (Guard-based precision)
	// Reference: IOUAmount.cpp lines 83-93
	if GetNumberSwitchover() {
		n := NewXRPLNumber(v.mantissa, v.exponent)
		v.mantissa = n.mantissa
		v.exponent = n.exponent
		if v.exponent > MaxExponent {
			panic("IOUAmount overflow")
		}
		if v.exponent < MinExponent {
			v.mantissa = 0
			v.exponent = zeroExponent
		}
		return
	}

	negative := v.mantissa < 0
	if negative {
		v.mantissa = -v.mantissa
	}

	// Scale up if mantissa is too small
	for v.mantissa < MinMantissa && v.exponent > MinExponent {
		v.mantissa *= 10
		v.exponent--
	}

	// Scale down if mantissa is too large
	for v.mantissa > MaxMantissa {
		if v.exponent >= MaxExponent {
			panic("IOUAmount overflow")
		}
		v.mantissa /= 10
		v.exponent++
	}

	// Underflow to zero
	if v.exponent < MinExponent || v.mantissa < MinMantissa {
		v.mantissa = 0
		v.exponent = zeroExponent
		return
	}

	// Overflow check
	if v.exponent > MaxExponent {
		panic("IOUAmount overflow")
	}

	if negative {
		v.mantissa = -v.mantissa
	}
}

// Mantissa returns the mantissa
func (v IOUAmountValue) Mantissa() int64 {
	return v.mantissa
}

// Exponent returns the exponent
func (v IOUAmountValue) Exponent() int {
	return v.exponent
}

// IsZero returns true if the amount is zero
func (v IOUAmountValue) IsZero() bool {
	return v.mantissa == 0
}

// IsNegative returns true if the amount is negative
func (v IOUAmountValue) IsNegative() bool {
	return v.mantissa < 0
}

// Signum returns the sign of the amount (-1, 0, or 1)
func (v IOUAmountValue) Signum() int {
	if v.mantissa < 0 {
		return -1
	}
	if v.mantissa > 0 {
		return 1
	}
	return 0
}

// Negate returns the negated amount
func (v IOUAmountValue) Negate() IOUAmountValue {
	return IOUAmountValue{mantissa: -v.mantissa, exponent: v.exponent}
}

// Float64 returns an approximate float64 representation
func (v IOUAmountValue) Float64() float64 {
	if v.mantissa == 0 {
		return 0
	}
	return float64(v.mantissa) * math.Pow10(v.exponent)
}

// String returns a decimal string representation
func (v IOUAmountValue) String() string {
	if v.mantissa == 0 {
		return "0"
	}

	negative := v.mantissa < 0
	mantissa := v.mantissa
	if negative {
		mantissa = -mantissa
	}

	// Convert mantissa to string
	mantissaStr := strconv.FormatInt(mantissa, 10)
	mantissaLen := len(mantissaStr)

	// Calculate where the decimal point should be
	// The value is mantissa * 10^exponent
	decimalPos := mantissaLen + v.exponent

	var result string
	if decimalPos <= 0 {
		// Need leading zeros: 0.000...digits
		result = "0." + strings.Repeat("0", -decimalPos) + mantissaStr
	} else if decimalPos >= mantissaLen {
		// No decimal point needed, or trailing zeros
		if v.exponent >= 0 {
			result = mantissaStr + strings.Repeat("0", v.exponent)
		} else {
			result = mantissaStr
		}
	} else {
		// Decimal point in the middle
		result = mantissaStr[:decimalPos] + "." + mantissaStr[decimalPos:]
	}

	// Remove trailing zeros after decimal point
	if strings.Contains(result, ".") {
		result = strings.TrimRight(result, "0")
		result = strings.TrimRight(result, ".")
	}

	if negative {
		result = "-" + result
	}

	return result
}

// Amount represents either XRP (as drops) or an issued currency amount
// Matches rippled's STAmount which can hold any asset type
type Amount struct {
	// For XRP amounts
	xrp XRPAmount

	// For issued currency amounts
	iou      IOUAmountValue
	Currency string
	Issuer   string

	// Native indicates if this is XRP (true) or issued currency (false)
	Native bool

	// mptRaw stores the raw int64 value for MPT amounts, bypassing IOU normalization
	// which loses precision for large values. The iou field is still set (normalized)
	// for binary codec compatibility. Engine code should use MPTRaw() when available.
	mptRaw *int64
}

// NewXRPAmountFromInt creates an XRP amount from drops as int64
func NewXRPAmountFromInt(drops int64) Amount {
	return Amount{
		xrp:    XRPAmount{drops: drops},
		Native: true,
	}
}

// NewIssuedAmountFromValue creates an issued currency amount from mantissa/exponent
func NewIssuedAmountFromValue(mantissa int64, exponent int, currency, issuer string) Amount {
	iouVal := NewIOUAmountValue(mantissa, exponent)
	result := Amount{
		iou:      iouVal,
		Currency: currency,
		Issuer:   issuer,
		Native:   false,
	}
	return result
}

// NewMPTAmountDirect creates an MPT amount storing the raw int64 value directly.
// Unlike IOU amounts, MPT amounts are whole numbers and should not be normalized.
// The IOU mantissa/exponent is still normalized for binary codec compatibility,
// but the raw int64 is preserved for engine use via MPTRaw().
func NewMPTAmountDirect(value int64, currency, issuer string) Amount {
	iouVal := NewIOUAmountValue(value, 0) // normalized for binary codec
	raw := value
	return Amount{
		iou:      iouVal,
		Currency: currency,
		Issuer:   issuer,
		Native:   false,
		mptRaw:   &raw,
	}
}

// MPTRaw returns the raw int64 value for MPT amounts, if available.
// Returns (value, true) for MPT amounts, (0, false) for other amounts.
func (a Amount) MPTRaw() (int64, bool) {
	if a.mptRaw != nil {
		return *a.mptRaw, true
	}
	return 0, false
}

// NewIssuedAmountFromFloat64 creates an issued currency amount from a float64 value.
// This is a convenience function that converts the float to mantissa/exponent internally.
func NewIssuedAmountFromFloat64(value float64, currency, issuer string) Amount {
	mantissa, exponent := Float64ToMantissaExponent(value)
	return NewIssuedAmountFromValue(mantissa, exponent, currency, issuer)
}

// Float64ToMantissaExponent converts a float64 to mantissa and exponent.
// Returns (mantissa, exponent) where value = mantissa * 10^exponent.
func Float64ToMantissaExponent(value float64) (int64, int) {
	if value == 0 {
		return 0, zeroExponent
	}

	negative := value < 0
	if negative {
		value = -value
	}

	// Find the exponent to normalize value to [1, 10)
	exponent := 0
	if value >= 1 {
		for value >= 10 {
			value /= 10
			exponent++
		}
	} else {
		for value < 1 {
			value *= 10
			exponent--
		}
	}

	// Scale to get ~15 significant digits in mantissa
	// Mantissa should be in range [10^15, 10^16)
	targetMantissa := value * math.Pow10(15)
	mantissa := int64(math.Round(targetMantissa))
	exponent = exponent - 15

	if negative {
		mantissa = -mantissa
	}

	return mantissa, exponent
}

// NewIssuedAmountFromDecimalString creates an Amount from a decimal string value.
// This avoids precision loss that occurs when going through float64.
func NewIssuedAmountFromDecimalString(value, currency, issuer string) Amount {
	iou := parseIOUValueFromString(value)
	return Amount{
		iou:      iou,
		Currency: currency,
		Issuer:   issuer,
		Native:   false,
	}
}

// IsNative returns true if this is an XRP amount
func (a Amount) IsNative() bool {
	// Only check the Native field, which is explicitly set during construction.
	// Do NOT check for empty Currency/Issuer as that would incorrectly classify
	// arithmetic-only Amounts (like Quality rates) as native XRP.
	return a.Native
}

// Drops returns the XRP amount in drops (only valid for native amounts)
func (a Amount) Drops() int64 {
	if !a.IsNative() {
		return 0
	}
	return a.xrp.drops
}

// XRP returns the XRPAmount (only valid for native amounts)
func (a Amount) XRP() XRPAmount {
	return a.xrp
}

// IOU returns the IOUAmountValue (only valid for issued amounts)
func (a Amount) IOU() IOUAmountValue {
	return a.iou
}

// IsZero returns true if the amount is zero
func (a Amount) IsZero() bool {
	if a.IsNative() {
		return a.xrp.IsZero()
	}
	return a.iou.IsZero()
}

// IsNegative returns true if the amount is negative
func (a Amount) IsNegative() bool {
	if a.IsNative() {
		return a.xrp.IsNegative()
	}
	return a.iou.IsNegative()
}

// Signum returns the sign of the amount (-1, 0, or 1)
func (a Amount) Signum() int {
	if a.IsNative() {
		return a.xrp.Signum()
	}
	return a.iou.Signum()
}

// Value returns the value as a string (for JSON serialization)
func (a Amount) Value() string {
	if a.IsNative() {
		return a.xrp.String()
	}
	return a.iou.String()
}

// Float64 returns an approximate float64 representation
func (a Amount) Float64() float64 {
	if a.IsNative() {
		return float64(a.xrp.drops)
	}
	return a.iou.Float64()
}

// Negate returns the negated amount
func (a Amount) Negate() Amount {
	if a.IsNative() {
		return Amount{
			xrp:    a.xrp.Negate(),
			Native: true,
		}
	}
	return Amount{
		iou:      a.iou.Negate(),
		Currency: a.Currency,
		Issuer:   a.Issuer,
		Native:   false,
	}
}

// MarshalJSON implements custom JSON marshaling
func (a Amount) MarshalJSON() ([]byte, error) {
	if a.IsNative() {
		return json.Marshal(a.xrp.String())
	}
	return json.Marshal(map[string]string{
		"value":    a.iou.String(),
		"currency": a.Currency,
		"issuer":   a.Issuer,
	})
}

// UnmarshalJSON implements custom JSON unmarshaling
func (a *Amount) UnmarshalJSON(data []byte) error {
	// Try as string first (XRP drops)
	var strVal string
	if err := json.Unmarshal(data, &strVal); err == nil {
		drops, err := strconv.ParseInt(strVal, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid XRP drops value: %w", err)
		}
		a.xrp = XRPAmount{drops: drops}
		a.Native = true
		return nil
	}

	// Try as object (issued currency)
	var objVal struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
		Issuer   string `json:"issuer"`
	}
	if err := json.Unmarshal(data, &objVal); err != nil {
		return err
	}

	a.iou = parseIOUValueFromString(objVal.Value)
	a.Currency = objVal.Currency
	a.Issuer = objVal.Issuer
	a.Native = false
	return nil
}

// parseIOUValueFromString parses a decimal string into IOUAmountValue (for JSON unmarshaling)
func parseIOUValueFromString(value string) IOUAmountValue {
	if value == "" || value == "0" {
		return ZeroIOUValue()
	}

	negative := false
	if strings.HasPrefix(value, "-") {
		negative = true
		value = value[1:]
	}

	// Split on decimal point
	parts := strings.Split(value, ".")
	intPart := parts[0]
	fracPart := ""
	if len(parts) > 1 {
		fracPart = parts[1]
	}

	// Remove leading zeros from int part
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}

	// Combine digits
	digits := intPart + fracPart

	// Remove trailing zeros (we'll account for them in exponent)
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		return ZeroIOUValue()
	}

	// Parse mantissa
	mantissa, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return ZeroIOUValue()
	}

	// Calculate exponent
	originalDigits := intPart + fracPart
	trailingZeros := len(originalDigits) - len(digits)
	exponent := -len(fracPart) + trailingZeros

	if negative {
		mantissa = -mantissa
	}

	return NewIOUAmountValue(mantissa, exponent)
}

// flattenAmount converts an Amount to its JSON-compatible representation
func flattenAmount(a Amount) any {
	if a.IsNative() {
		return a.xrp.String()
	}
	return map[string]string{
		"value":    a.iou.String(),
		"currency": a.Currency,
		"issuer":   a.Issuer,
	}
}

// Add adds two amounts (must be same type)
func (a Amount) Add(b Amount) (Amount, error) {
	if a.IsNative() != b.IsNative() {
		return Amount{}, errors.New("cannot add XRP and IOU amounts")
	}
	if a.IsNative() {
		return Amount{
			xrp:    a.xrp.Add(b.xrp),
			Native: true,
		}, nil
	}
	result := addIOUValues(a.iou, b.iou)
	return Amount{
		iou:      result,
		Currency: a.Currency,
		Issuer:   a.Issuer,
		Native:   false,
	}, nil
}

// Sub subtracts two amounts (must be same type)
func (a Amount) Sub(b Amount) (Amount, error) {
	return a.Add(b.Negate())
}

// addIOUValues adds two IOU values with proper exponent handling.
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Add() for Guard-based precision.
// Reference: IOUAmount::operator+= in IOUAmount.cpp lines 137-181
func addIOUValues(a, b IOUAmountValue) IOUAmountValue {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}

	// When switchover is on, delegate to XRPLNumber (Guard-based precision)
	// Reference: IOUAmount.cpp lines 149-153
	if GetNumberSwitchover() {
		na := NewXRPLNumber(a.mantissa, a.exponent)
		nb := NewXRPLNumber(b.mantissa, b.exponent)
		result := na.Add(nb)
		r := result.ToIOUAmountValue()
		return r
	}

	// Legacy path (without fixUniversalNumber)
	// Align exponents
	aExp := a.exponent
	bExp := b.exponent
	aMant := a.mantissa
	bMant := b.mantissa

	// Align to the larger exponent
	for aExp < bExp {
		aMant /= 10
		aExp++
	}
	for bExp < aExp {
		bMant /= 10
		bExp++
	}

	result := aMant + bMant

	// Handle near-zero results
	if result >= -10 && result <= 10 {
		return ZeroIOUValue()
	}

	r := NewIOUAmountValue(result, aExp)
	return r
}

// Compare compares two amounts
// Returns -1 if a < b, 0 if a == b, 1 if a > b
func (a Amount) Compare(b Amount) int {
	if a.IsNative() && b.IsNative() {
		if a.xrp.drops < b.xrp.drops {
			return -1
		}
		if a.xrp.drops > b.xrp.drops {
			return 1
		}
		return 0
	}
	if !a.IsNative() && !b.IsNative() {
		return compareIOUValues(a.iou, b.iou)
	}
	// Mixed types - XRP comes first
	if a.IsNative() {
		return -1
	}
	return 1
}

// compareIOUValues compares two IOU values using mantissa/exponent without float64 conversion.
func compareIOUValues(a, b IOUAmountValue) int {
	// Handle signs first
	aSign := a.Signum()
	bSign := b.Signum()
	if aSign < bSign {
		return -1
	}
	if aSign > bSign {
		return 1
	}
	if aSign == 0 && bSign == 0 {
		return 0
	}

	// Same sign - compare magnitudes
	// For positive values: larger exponent = larger value (if mantissas are normalized)
	if a.exponent > b.exponent {
		if aSign > 0 {
			return 1
		}
		return -1
	}
	if a.exponent < b.exponent {
		if aSign > 0 {
			return -1
		}
		return 1
	}

	// Same exponent - compare mantissas
	if a.mantissa < b.mantissa {
		return -1
	}
	if a.mantissa > b.mantissa {
		return 1
	}
	return 0
}

// MulRatio multiplies this amount by num/den with optional rounding up.
// Uses big.Int arithmetic to avoid overflow on large mantissa * num products.
// Includes roomToGrow precision enhancement matching rippled's IOUAmount mulRatio.
// Reference: IOUAmount.cpp mulRatio() lines 189-323
func (a Amount) MulRatio(num, den uint32, roundUp bool) Amount {
	if a.IsNative() {
		// Use big.Int to avoid int64 overflow for large XRP amounts.
		// E.g. 150000000000 drops * 1000000000 overflows int64.
		// Reference: rippled uses uint128_t for XRP mulRatio.
		bigDrops := new(big.Int).SetInt64(a.Drops())
		bigNum := new(big.Int).SetInt64(int64(num))
		bigDen := new(big.Int).SetInt64(int64(den))
		product := new(big.Int).Mul(bigDrops, bigNum)
		result := new(big.Int).Div(product, bigDen)
		if roundUp {
			rem := new(big.Int).Mod(product, bigDen)
			if rem.Sign() != 0 {
				result.Add(result, big.NewInt(1))
			}
		}
		return NewXRPAmountFromInt(result.Int64())
	}

	if den == 0 || a.IsZero() {
		return a
	}

	// For IOU: multiply mantissa by num/den
	mantissa := a.iou.Mantissa()
	negative := mantissa < 0
	if negative {
		mantissa = -mantissa
	}

	bigMant := new(big.Int).SetInt64(mantissa)
	bigNum := new(big.Int).SetUint64(uint64(num))
	bigDen := new(big.Int).SetUint64(uint64(den))

	// mul = mantissa * num (32-bit * 64-bit → fits in 128 bits)
	mul := new(big.Int).Mul(bigMant, bigNum)

	low := new(big.Int).Div(mul, bigDen)
	rem := new(big.Int).Sub(mul, new(big.Int).Mul(low, bigDen))

	exponent := a.iou.Exponent()

	// roomToGrow: scale up to capture fractional digits from rem/den
	// Reference: IOUAmount.cpp lines 254-272
	if rem.Sign() != 0 {
		roomToGrow := mulRatioFL64 - log10Ceil(low)
		if roomToGrow > 0 {
			exponent -= roomToGrow
			scale := pow10Big(roomToGrow)
			low.Mul(low, scale)
			rem.Mul(rem, scale)
		}
		addRem := new(big.Int).Div(rem, bigDen)
		low.Add(low, addRem)
		rem.Sub(rem, new(big.Int).Mul(addRem, bigDen))
	}

	// mustShrink: scale down if low exceeds int64 range
	// Reference: IOUAmount.cpp lines 278-287
	hasRem := rem.Sign() != 0
	mustShrink := log10Ceil(low) - mulRatioFL64
	if mustShrink > 0 {
		sav := new(big.Int).Set(low)
		exponent += mustShrink
		scale := pow10Big(mustShrink)
		low.Div(low, scale)
		if !hasRem {
			hasRem = new(big.Int).Sub(sav, new(big.Int).Mul(low, scale)).Sign() != 0
		}
	}

	resultMant := low.Int64()

	// Normalize FIRST, then apply rounding, matching rippled's IOUAmount.cpp lines 289-319:
	//   std::int64_t mantissa = low.convert_to<std::int64_t>();
	//   if (neg) mantissa *= -1;
	//   IOUAmount result(mantissa, exponent);  // constructor normalizes
	//   if (hasRem) {
	//       if (roundUp && !neg)  return IOUAmount(result.mantissa() + 1, result.exponent());
	//       if (!roundUp && neg)  return IOUAmount(result.mantissa() - 1, result.exponent());
	//   }
	//   return result;
	if negative {
		resultMant = -resultMant
	}

	result := NewIssuedAmountFromValue(resultMant, exponent, a.Currency, a.Issuer)

	// Apply rounding AFTER normalization. Two cases round away from zero:
	//   roundUp && !neg: +1 to positive mantissa (round up)
	//   !roundUp && neg: -1 to negative mantissa (round more negative = away from zero)
	if hasRem {
		iou := result.IOU()
		if roundUp && !negative {
			if result.IsZero() {
				return NewIssuedAmountFromValue(MinMantissa, MinExponent, a.Currency, a.Issuer)
			}
			return NewIssuedAmountFromValue(iou.mantissa+1, iou.exponent, a.Currency, a.Issuer)
		}
		if !roundUp && negative {
			if result.IsZero() {
				return NewIssuedAmountFromValue(-MinMantissa, MinExponent, a.Currency, a.Issuer)
			}
			return NewIssuedAmountFromValue(iou.mantissa-1, iou.exponent, a.Currency, a.Issuer)
		}
	} else {
	}

	return result
}

// mulRatioFL64 is floor(log10(math.MaxInt64)) = 18
// Reference: IOUAmount.cpp line 239-241
const mulRatioFL64 = 18

// log10Ceil returns ceil(log10(v)) for a big.Int.
// Returns -1 for v == 0, 0 for v == 1.
// Reference: IOUAmount.cpp lines 231-237
func log10Ceil(v *big.Int) int {
	if v.Sign() <= 0 {
		return -1
	}
	// Find smallest power of 10 >= v
	p := big.NewInt(1)
	idx := 0
	for p.Cmp(v) < 0 {
		p.Mul(p, big.NewInt(10))
		idx++
	}
	return idx
}

// pow10Big returns 10^n as a big.Int.
func pow10Big(n int) *big.Int {
	result := big.NewInt(1)
	ten := big.NewInt(10)
	for i := 0; i < n; i++ {
		result.Mul(result, ten)
	}
	return result
}

// Mul multiplies this Amount by another Amount.
// Reference: rippled's mulRound() in STAmount.cpp
// For IOU * IOU: result = (m1 * m2) * 10^(e1 + e2)
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Mul() for Guard-based rounding.
func (a Amount) Mul(other Amount, roundUp bool) Amount {
	if a.IsZero() || other.IsZero() {
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		return NewIssuedAmountFromValue(0, -100, a.Currency, a.Issuer)
	}

	// Handle XRP * XRP case
	if a.IsNative() && other.IsNative() {
		result := a.Drops() * other.Drops()
		return NewXRPAmountFromInt(result)
	}

	// For IOU multiplication, use precise big.Int arithmetic
	// result = (a.mantissa * other.mantissa) * 10^(a.exp + other.exp)
	m1 := a.Mantissa()
	e1 := a.Exponent()
	m2 := other.Mantissa()
	e2 := other.Exponent()

	// When switchover is on, delegate to XRPLNumber for Guard-based rounding
	if GetNumberSwitchover() && !a.IsNative() {
		negative := (m1 < 0) != (m2 < 0)
		if m1 < 0 {
			m1 = -m1
		}
		if m2 < 0 {
			m2 = -m2
		}
		na := NewXRPLNumber(m1, e1)
		nb := NewXRPLNumber(m2, e2)
		result := na.Mul(nb)
		iou := result.ToIOUAmountValue()
		rm := iou.mantissa
		if negative {
			rm = -rm
		}
		return NewIssuedAmountFromValue(rm, iou.exponent, a.Currency, a.Issuer)
	}

	// Handle sign
	negative := (m1 < 0) != (m2 < 0)
	if m1 < 0 {
		m1 = -m1
	}
	if m2 < 0 {
		m2 = -m2
	}

	// Pre-normalize native (XRP) inputs to IOU range [cMinValue, cMaxValue)
	// Reference: rippled multiply() lines 1382-1398
	if a.IsNative() {
		for m1 < MinMantissa {
			m1 *= 10
			e1--
		}
	}
	if other.IsNative() {
		for m2 < MinMantissa {
			m2 *= 10
			e2--
		}
	}

	// Multiply mantissas (each in [10^15, 10^16) range, product in [10^30, 10^32) range)
	// Then divide by 10^14 to bring result to [10^16, 10^18) range.
	// Reference: rippled multiply() line 1406, mulRound() line 1590
	bigM1 := new(big.Int).SetUint64(uint64(m1))
	bigM2 := new(big.Int).SetUint64(uint64(m2))
	bigProduct := new(big.Int).Mul(bigM1, bigM2)
	bigTenTo14 := new(big.Int).SetUint64(tenTo14)

	if roundUp {
		// Match rippled's mulRound(): muldiv_round(v1, v2, tenTo14, tenTo14-1)
		// For positive result with roundUp=true: add tenTo14-1 before dividing (ceiling division)
		// Reference: rippled mulRound() lines 1590-1612
		rounding := new(big.Int).SetUint64(tenTo14 - 1)
		bigProduct.Add(bigProduct, rounding)
	}

	bigResult := new(big.Int).Div(bigProduct, bigTenTo14)

	if !roundUp {
		// Match rippled's multiply(): muldiv(v1, v2, tenTo14) + 7
		// Reference: rippled multiply() line 1406
		bigResult.Add(bigResult, big.NewInt(7))
	}

	resultExp := e1 + e2 + 14

	if roundUp {
		// Apply canonicalizeRound for mulRound()
		// Reference: rippled canonicalizeRound() lines 1431-1464
		bigCMaxValue := new(big.Int).SetUint64(cMaxValue)
		tenCMaxValue := new(big.Int).Mul(big.NewInt(10), bigCMaxValue)
		ten := big.NewInt(10)

		if bigResult.Cmp(bigCMaxValue) > 0 {
			for bigResult.Cmp(tenCMaxValue) > 0 {
				bigResult.Div(bigResult, ten)
				resultExp++
			}
			bigResult.Add(bigResult, big.NewInt(9))
			bigResult.Div(bigResult, ten)
			resultExp++
		}
	}

	// Normalize the result to mantissa in [cMinValue, cMaxValue)
	bigMinMantissa := new(big.Int).SetInt64(MinMantissa)
	bigMaxMantissa := new(big.Int).SetUint64(cMaxValue)
	ten := big.NewInt(10)

	for bigResult.Cmp(bigMaxMantissa) >= 0 {
		bigResult.Div(bigResult, ten)
		resultExp++
	}
	for bigResult.Cmp(bigMinMantissa) < 0 && bigResult.Sign() != 0 {
		bigResult.Mul(bigResult, ten)
		resultExp--
	}

	resultMant := bigResult.Int64()
	if negative {
		resultMant = -resultMant
	}

	if a.IsNative() {
		// Result is XRP - convert from mantissa/exponent to drops
		for resultExp > 0 {
			resultMant *= 10
			resultExp--
		}
		for resultExp < 0 {
			resultMant /= 10
			resultExp++
		}
		return NewXRPAmountFromInt(resultMant)
	}

	return NewIssuedAmountFromValue(resultMant, resultExp, a.Currency, a.Issuer)
}

// Div divides this Amount by another Amount.
// Reference: rippled's divRound() in STAmount.cpp
// For IOU / IOU: result = (m1 / m2) * 10^(e1 - e2)
// When fixUniversalNumber is enabled, delegates to XRPLNumber.Div() for Guard-based rounding.
func (a Amount) Div(other Amount, roundUp bool) Amount {
	if other.IsZero() {
		// Division by zero - return zero
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		return NewIssuedAmountFromValue(0, -100, a.Currency, a.Issuer)
	}

	if a.IsZero() {
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		return NewIssuedAmountFromValue(0, -100, a.Currency, a.Issuer)
	}

	// Handle XRP / XRP case
	if a.IsNative() && other.IsNative() {
		result := a.Drops() / other.Drops()
		if roundUp && a.Drops()%other.Drops() != 0 {
			result++
		}
		return NewXRPAmountFromInt(result)
	}

	// For IOU division, use precise big.Int arithmetic
	// Reference: rippled STAmount.cpp divide() and divRound()
	m1 := a.Mantissa()
	e1 := a.Exponent()
	m2 := other.Mantissa()
	e2 := other.Exponent()

	// rippled's divide() NEVER uses Number/switchover - it always uses
	// muldiv(numVal, tenTo17, denVal) + 5 regardless of getSTNumberSwitchover().
	// Reference: rippled STAmount.cpp divide() lines 1293-1336

	// Handle sign
	negative := (m1 < 0) != (m2 < 0)
	if m1 < 0 {
		m1 = -m1
	}
	if m2 < 0 {
		m2 = -m2
	}

	// Pre-normalize native (XRP) inputs to IOU range [cMinValue, cMaxValue)
	// Reference: rippled divide() lines 1307-1324
	if a.IsNative() {
		for m1 < MinMantissa {
			m1 *= 10
			e1--
		}
	}
	if other.IsNative() {
		for m2 < MinMantissa {
			m2 *= 10
			e2--
		}
	}

	bigM1 := new(big.Int).SetUint64(uint64(m1))
	bigM2 := new(big.Int).SetUint64(uint64(m2))

	// Scale numerator by 10^17 for precision (matching rippled's tenTo17)
	// Reference: rippled divide() line 1333, divRound() line 1712
	bigM1.Mul(bigM1, new(big.Int).Set(tenTo17))

	if roundUp {
		// Match rippled's divRound(): muldiv_round(numVal, tenTo17, denVal, denVal-1)
		// When rounding away from zero, add denVal-1 before dividing.
		// Reference: rippled divRound() lines 1712-1713
		rounding := new(big.Int).SetUint64(uint64(m2) - 1)
		bigM1.Add(bigM1, rounding)
	}

	bigResult := new(big.Int).Div(bigM1, bigM2)

	if !roundUp {
		// Match rippled's divide(): result = muldiv(numVal, tenTo17, denVal) + 5
		// Reference: rippled divide() line 1333
		bigResult.Add(bigResult, big.NewInt(5))
	}

	resultExp := e1 - e2 - 17 // -17 because we scaled by 10^17

	if roundUp {
		// Apply canonicalizeRound for divRound()
		// Reference: rippled canonicalizeRound() lines 1431-1464
		bigCMaxValue := new(big.Int).SetUint64(cMaxValue)
		tenCMaxValue := new(big.Int).Mul(big.NewInt(10), bigCMaxValue)
		ten := big.NewInt(10)

		if bigResult.Cmp(bigCMaxValue) > 0 {
			for bigResult.Cmp(tenCMaxValue) > 0 {
				bigResult.Div(bigResult, ten)
				resultExp++
			}
			bigResult.Add(bigResult, big.NewInt(9))
			bigResult.Div(bigResult, ten)
			resultExp++
		}
	}

	// Normalize the result to mantissa in [cMinValue, cMaxValue)
	bigMinMantissa := new(big.Int).SetInt64(MinMantissa)
	bigMaxMantissa := new(big.Int).SetUint64(cMaxValue)
	ten := big.NewInt(10)

	for bigResult.Cmp(bigMaxMantissa) >= 0 {
		bigResult.Div(bigResult, ten)
		resultExp++
	}
	for bigResult.Cmp(bigMinMantissa) < 0 && bigResult.Sign() != 0 {
		bigResult.Mul(bigResult, ten)
		resultExp--
	}

	resultMant := bigResult.Int64()
	if negative {
		resultMant = -resultMant
	}

	if a.IsNative() {
		// Result is XRP - convert from mantissa/exponent to drops
		for resultExp > 0 {
			resultMant *= 10
			resultExp--
		}
		for resultExp < 0 {
			resultMant /= 10
			resultExp++
		}
		return NewXRPAmountFromInt(resultMant)
	}

	return NewIssuedAmountFromValue(resultMant, resultExp, a.Currency, a.Issuer)
}

// MulRoundStrict multiplies two Amounts using rippled's mulRoundStrict algorithm.
// This differs from Amount.Mul() in that it uses:
// 1. muldiv_round(v1, v2, 10^14, rounding) instead of big.Int product + normalize
// 2. canonicalizeRoundStrict for overflow handling
// 3. Number::towards_zero rounding mode during normalization
// Reference: STAmount.cpp mulRoundImpl with canonicalizeRoundStrict + NumberRoundModeGuard
func MulRoundStrict(v1, v2 Amount, currency, issuer string, roundUp bool) Amount {
	if v1.IsZero() || v2.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}

	value1 := v1.Mantissa()
	offset1 := v1.Exponent()
	value2 := v2.Mantissa()
	offset2 := v2.Exponent()

	// Normalize native/MPT values to IOU range [10^15, 10^16)
	if v1.IsNative() {
		if value1 < 0 {
			value1 = -value1
		}
		for value1 < MinMantissa {
			value1 *= 10
			offset1--
		}
	}
	if v2.IsNative() {
		if value2 < 0 {
			value2 = -value2
		}
		for value2 < MinMantissa {
			value2 *= 10
			offset2--
		}
	}

	resultNegative := v1.IsNegative() != v2.IsNegative()

	// Make mantissas positive for multiplication
	if value1 < 0 {
		value1 = -value1
	}
	if value2 < 0 {
		value2 = -value2
	}

	// muldiv_round: (value1 * value2 + rounding) / 10^14
	// rounding = (resultNegative != roundUp) ? 10^14 - 1 : 0
	tenTo14 := new(big.Int).SetUint64(100_000_000_000_000)  // 10^14
	tenTo14m1 := new(big.Int).SetUint64(99_999_999_999_999) // 10^14 - 1
	product := new(big.Int).Mul(big.NewInt(value1), big.NewInt(value2))
	if resultNegative != roundUp {
		product.Add(product, tenTo14m1)
	}
	product.Div(product, tenTo14)

	amount := product.Uint64()
	offset := offset1 + offset2 + 14

	// canonicalizeRoundStrict: only when resultNegative != roundUp
	if resultNegative != roundUp {
		if amount > uint64(MaxMantissa) {
			for amount > 10*uint64(MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	}

	// Create the result with Number in towards_zero mode
	// This affects how normalization rounds during STAmount construction
	guard := NewNumberRoundModeGuard(RoundTowardsZero)
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := NewIssuedAmountFromValue(mantissa, offset, currency, issuer)
	guard.Release()

	// If roundUp and positive and result is zero, return minimum value
	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}

	return result
}

// MulRound multiplies two Amounts using rippled's mulRound (non-strict) algorithm.
// This is the legacy version with "slop" that uses canonicalizeRound instead of
// canonicalizeRoundStrict. The key difference: canonicalizeRound adds 9 or 10
// based on loop count (not actual remainder), and uses DontAffectNumberRoundMode
// (no-op) instead of NumberRoundModeGuard(towards_zero).
// Reference: STAmount.cpp mulRoundImpl with canonicalizeRound + DontAffectNumberRoundMode
func MulRound(v1, v2 Amount, currency, issuer string, roundUp bool) Amount {
	if v1.IsZero() || v2.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}

	value1 := v1.Mantissa()
	offset1 := v1.Exponent()
	value2 := v2.Mantissa()
	offset2 := v2.Exponent()

	// Normalize native/MPT values to IOU range [10^15, 10^16)
	if v1.IsNative() {
		if value1 < 0 {
			value1 = -value1
		}
		for value1 < MinMantissa {
			value1 *= 10
			offset1--
		}
	}
	if v2.IsNative() {
		if value2 < 0 {
			value2 = -value2
		}
		for value2 < MinMantissa {
			value2 *= 10
			offset2--
		}
	}

	resultNegative := v1.IsNegative() != v2.IsNegative()

	// Make mantissas positive for multiplication
	if value1 < 0 {
		value1 = -value1
	}
	if value2 < 0 {
		value2 = -value2
	}

	// muldiv_round: (value1 * value2 + rounding) / 10^14
	tenTo14 := new(big.Int).SetUint64(100_000_000_000_000)
	tenTo14m1 := new(big.Int).SetUint64(99_999_999_999_999)
	product := new(big.Int).Mul(big.NewInt(value1), big.NewInt(value2))
	if resultNegative != roundUp {
		product.Add(product, tenTo14m1)
	}
	product.Div(product, tenTo14)

	amount := product.Uint64()
	offset := offset1 + offset2 + 14

	// canonicalizeRound (non-strict): uses loop count, NOT actual remainder.
	// Reference: rippled STAmount.cpp canonicalizeRound lines 1432-1464
	if resultNegative != roundUp {
		if amount > uint64(MaxMantissa) {
			for amount > 10*uint64(MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	}

	// DontAffectNumberRoundMode: NO guard (no-op), unlike strict which uses towards_zero
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := NewIssuedAmountFromValue(mantissa, offset, currency, issuer)

	// If roundUp and positive and result is zero, return minimum value
	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}

	return result
}

// DivRound divides two Amounts using rippled's divRound (non-strict) algorithm.
// This is the legacy version with "slop" that uses canonicalizeRound.
// Reference: STAmount.cpp divRoundImpl with canonicalizeRound + DontAffectNumberRoundMode
func DivRound(num, den Amount, currency, issuer string, roundUp bool) Amount {
	if den.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}
	if num.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}

	numVal := num.Mantissa()
	numOff := num.Exponent()
	denVal := den.Mantissa()
	denOff := den.Exponent()

	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < MinMantissa {
			numVal *= 10
			numOff--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < MinMantissa {
			denVal *= 10
			denOff--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// divmod with rounding: (numVal * 10^17 + rounding) / denVal
	tenTo17 := new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil)
	numerator := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	if resultNegative != roundUp {
		// Round up: add (denVal - 1) before division
		numerator.Add(numerator, new(big.Int).Sub(big.NewInt(denVal), big.NewInt(1)))
	}
	quotient := new(big.Int).Div(numerator, big.NewInt(denVal))
	amount := quotient.Uint64()
	offset := numOff - denOff - 17

	// canonicalizeRound (non-strict): same as MulRound
	if resultNegative != roundUp {
		if amount > uint64(MaxMantissa) {
			for amount > 10*uint64(MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	}

	// DontAffectNumberRoundMode: NO guard
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := NewIssuedAmountFromValue(mantissa, offset, currency, issuer)

	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}

	return result
}

// DivRoundNative divides two Amounts and returns the result as XRP drops (int64),
// using native canonicalization matching rippled's canonicalizeRound(native=true).
// When the output asset is native (XRP), rippled's divRoundImpl calls
// canonicalizeRound with native=true, which uses a different rounding path
// than the IOU overflow case. This function matches that native path exactly.
// Reference: STAmount.cpp divRoundImpl + canonicalizeRound(native=true) lines 1434-1451
func DivRoundNative(num, den Amount, roundUp bool) int64 {
	if den.IsZero() || num.IsZero() {
		return 0
	}

	numVal := num.Mantissa()
	numOff := num.Exponent()
	denVal := den.Mantissa()
	denOff := den.Exponent()

	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < MinMantissa {
			numVal *= 10
			numOff--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < MinMantissa {
			denVal *= 10
			denOff--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// divmod with rounding: (numVal * 10^17 + rounding) / denVal
	tenTo17 := new(big.Int).SetUint64(100_000_000_000_000_000)
	numerator := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	if resultNegative != roundUp {
		numerator.Add(numerator, new(big.Int).Sub(big.NewInt(denVal), big.NewInt(1)))
	}
	quotient := new(big.Int).Div(numerator, big.NewInt(denVal))
	amount := quotient.Uint64()
	offset := numOff - denOff - 17

	// canonicalizeRound(native=true): use native rounding path.
	// Reference: rippled STAmount.cpp canonicalizeRound lines 1434-1451
	if resultNegative != roundUp {
		if offset < 0 {
			loops := 0
			for offset < -1 {
				amount /= 10
				offset++
				loops++
			}
			var adder uint64 = 10
			if loops >= 2 {
				adder = 9
			}
			amount = (amount + adder) / 10
		}
	} else {
		// When resultNegative == roundUp (i.e., no rounding needed),
		// still need to convert to drops (offset → 0).
		for offset < 0 {
			amount /= 10
			offset++
		}
		for offset > 0 {
			amount *= 10
			offset--
		}
	}

	if roundUp && !resultNegative && amount == 0 {
		return 1
	}

	result := int64(amount)
	if resultNegative {
		result = -result
	}
	return result
}

// MulRoundNative multiplies two Amounts and returns the result as XRP drops (int64),
// using native canonicalization matching rippled's canonicalizeRound(native=true).
// When the output asset is native (XRP), rippled's mulRoundImpl calls
// canonicalizeRound with native=true, which uses a different rounding path
// than the IOU overflow case. This function matches that native path exactly.
// Reference: STAmount.cpp mulRoundImpl + canonicalizeRound(native=true) lines 1434-1451
func MulRoundNative(v1, v2 Amount, roundUp bool) int64 {
	if v1.IsZero() || v2.IsZero() {
		return 0
	}

	value1 := v1.Mantissa()
	offset1 := v1.Exponent()
	value2 := v2.Mantissa()
	offset2 := v2.Exponent()

	if v1.IsNative() {
		if value1 < 0 {
			value1 = -value1
		}
		for value1 < MinMantissa {
			value1 *= 10
			offset1--
		}
	}
	if v2.IsNative() {
		if value2 < 0 {
			value2 = -value2
		}
		for value2 < MinMantissa {
			value2 *= 10
			offset2--
		}
	}

	resultNegative := v1.IsNegative() != v2.IsNegative()

	if value1 < 0 {
		value1 = -value1
	}
	if value2 < 0 {
		value2 = -value2
	}

	// muldiv_round: (value1 * value2 + rounding) / 10^14
	tenTo14 := new(big.Int).SetUint64(100_000_000_000_000)
	tenTo14m1 := new(big.Int).SetUint64(99_999_999_999_999)
	product := new(big.Int).Mul(big.NewInt(value1), big.NewInt(value2))
	if resultNegative != roundUp {
		product.Add(product, tenTo14m1)
	}
	product.Div(product, tenTo14)

	amount := product.Uint64()
	offset := offset1 + offset2 + 14

	// canonicalizeRound(native=true): use native rounding path.
	// Reference: rippled STAmount.cpp canonicalizeRound lines 1434-1451
	if resultNegative != roundUp {
		if offset < 0 {
			loops := 0
			for offset < -1 {
				amount /= 10
				offset++
				loops++
			}
			var adder uint64 = 10
			if loops >= 2 {
				adder = 9
			}
			amount = (amount + adder) / 10
		}
	} else {
		// When resultNegative == roundUp, no special rounding needed.
		// Still convert to drops (offset → 0).
		for offset < 0 {
			amount /= 10
			offset++
		}
		for offset > 0 {
			amount *= 10
			offset--
		}
	}

	if roundUp && !resultNegative && amount == 0 {
		return 1
	}

	result := int64(amount)
	if resultNegative {
		result = -result
	}
	return result
}

// DivRoundStrict divides two Amounts using rippled's divRoundStrict algorithm.
// Reference: STAmount.cpp divRoundImpl with NumberRoundModeGuard
func DivRoundStrict(num, den Amount, currency, issuer string, roundUp bool) Amount {
	if den.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}
	if num.IsZero() {
		return NewIssuedAmountFromValue(0, -100, currency, issuer)
	}

	numVal := num.Mantissa()
	numOffset := num.Exponent()
	denVal := den.Mantissa()
	denOffset := den.Exponent()

	// Normalize native values to IOU range
	if num.IsNative() {
		if numVal < 0 {
			numVal = -numVal
		}
		for numVal < MinMantissa {
			numVal *= 10
			numOffset--
		}
	}
	if den.IsNative() {
		if denVal < 0 {
			denVal = -denVal
		}
		for denVal < MinMantissa {
			denVal *= 10
			denOffset--
		}
	}

	resultNegative := num.IsNegative() != den.IsNegative()

	if numVal < 0 {
		numVal = -numVal
	}
	if denVal < 0 {
		denVal = -denVal
	}

	// muldiv_round: (numVal * 10^17 + rounding) / denVal
	// rounding = (resultNegative != roundUp) ? denVal - 1 : 0
	tenTo17 := new(big.Int).SetUint64(100_000_000_000_000_000) // 10^17
	bigNum := new(big.Int).Mul(big.NewInt(numVal), tenTo17)
	bigDen := new(big.Int).SetInt64(denVal)
	if resultNegative != roundUp {
		bigNum.Add(bigNum, new(big.Int).Sub(bigDen, big.NewInt(1)))
	}
	bigResult := new(big.Int).Div(bigNum, bigDen)

	amount := bigResult.Uint64()
	offset := numOffset - denOffset - 17

	// canonicalizeRound (used in divRoundImpl)
	if resultNegative != roundUp {
		if amount > uint64(MaxMantissa) {
			for amount > 10*uint64(MaxMantissa) {
				amount /= 10
				offset++
			}
			amount += 9
			amount /= 10
			offset++
		}
	}

	// Create result with appropriate Number rounding mode
	// divRoundStrict uses upward if (roundUp ^ resultNegative), else downward
	var mode RoundingMode
	if roundUp != resultNegative {
		mode = RoundUpward
	} else {
		mode = RoundDownward
	}
	guard := NewNumberRoundModeGuard(mode)
	mantissa := int64(amount)
	if resultNegative {
		mantissa = -mantissa
	}
	result := NewIssuedAmountFromValue(mantissa, offset, currency, issuer)
	guard.Release()

	// If roundUp and positive and result is zero, return minimum value
	if roundUp && !resultNegative && result.IsZero() {
		return NewIssuedAmountFromValue(MinMantissa, MinExponent, currency, issuer)
	}

	return result
}

// Mantissa returns the mantissa of the Amount (for IOU) or drops (for XRP).
func (a Amount) Mantissa() int64 {
	if a.IsNative() {
		return a.Drops()
	}
	return a.iou.Mantissa()
}

// Exponent returns the exponent of the Amount (for IOU) or 0 (for XRP).
func (a Amount) Exponent() int {
	if a.IsNative() {
		return 0
	}
	return a.iou.Exponent()
}

// Sqrt returns the square root of this Amount.
// Reference: rippled's root2() function in Number.cpp
// Uses Newton-Raphson iteration for precision.
// Panics if the amount is negative.
// When fixUniversalNumber is enabled, delegates to XRPLNumber.root2().
func (a Amount) Sqrt() Amount {
	// Handle special cases
	if a.IsZero() {
		if a.IsNative() {
			return NewXRPAmountFromInt(0)
		}
		return NewIssuedAmountFromValue(0, zeroExponent, a.Currency, a.Issuer)
	}
	if a.IsNegative() {
		panic("cannot take square root of negative amount")
	}

	// For XRP (native), compute sqrt of drops
	if a.IsNative() {
		drops := a.Drops()
		// Use integer square root for XRP
		result := intSqrt(uint64(drops))
		return NewXRPAmountFromInt(int64(result))
	}

	// When switchover is on, delegate to XRPLNumber.root2()
	if GetNumberSwitchover() {
		n := NewXRPLNumber(a.iou.Mantissa(), a.iou.Exponent())
		result := n.root2()
		iou := result.ToIOUAmountValue()
		return NewIssuedAmountFromValue(iou.mantissa, iou.exponent, a.Currency, a.Issuer)
	}

	// For IOU amounts, use Newton-Raphson iteration
	// Scale f into a range where exponent is even
	mantissa := a.iou.Mantissa()
	exponent := a.iou.Exponent()

	// Adjust exponent to be even (required for sqrt)
	e := exponent + 16 // shift to positive range
	if e%2 != 0 {
		e++
	}
	// Create scaled value: f = mantissa * 10^(exponent - e)
	scaledExp := exponent - e

	// Convert to a working Number-like representation
	// We need to do Newton-Raphson: r = (r + f/r) / 2
	// Start with initial guess using quadratic approximation
	// Coefficients from rippled: a0=18, a1=144, a2=-60, D=105
	// r = ((a2*f + a1)*f + a0) / D

	// For simplicity, use a good initial guess based on the mantissa
	// sqrt(m * 10^e) = sqrt(m) * 10^(e/2)
	// Initial guess: sqrt of mantissa scaled appropriately
	fVal := IOUAmountValue{mantissa: mantissa, exponent: scaledExp}

	// Initial guess using the quadratic fit from rippled
	// r = ((a2*f + a1)*f + a0) / D where a0=18, a1=144, a2=-60, D=105
	// Simplified: start with a reasonable approximation
	rVal := initialSqrtGuess(fVal)

	// Newton-Raphson iteration: r = (r + f/r) / 2
	// Continue until convergence (r stops changing)
	var rm1, rm2 IOUAmountValue
	for i := 0; i < 100; i++ { // max iterations for safety
		rm2 = rm1
		rm1 = rVal

		// r = (r + f/r) / 2
		fDivR := divIOUValues(fVal, rVal)
		rPlusFDivR := addIOUValues(rVal, fDivR)
		rVal = divIOUValuesByInt(rPlusFDivR, 2)

		// Check for convergence
		if rVal.mantissa == rm1.mantissa && rVal.exponent == rm1.exponent {
			break
		}
		if rVal.mantissa == rm2.mantissa && rVal.exponent == rm2.exponent {
			break
		}
	}

	// Reverse the scaling: multiply by 10^(e/2)
	resultExp := rVal.exponent + e/2
	result := NewIOUAmountValue(rVal.mantissa, resultExp)

	return NewIssuedAmountFromValue(result.Mantissa(), result.Exponent(), a.Currency, a.Issuer)
}

// intSqrt computes integer square root using Newton's method
func intSqrt(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return x
}

// initialSqrtGuess returns an initial guess for Newton-Raphson sqrt iteration
func initialSqrtGuess(f IOUAmountValue) IOUAmountValue {
	if f.mantissa == 0 {
		return ZeroIOUValue()
	}
	// Use the quadratic fit from rippled: r = ((a2*f + a1)*f + a0) / D
	// where a0=18, a1=144, a2=-60, D=105
	// This gives a good initial approximation in the range [0, 1]

	// Simplified approach: sqrt(m * 10^e) ≈ sqrt(m) * 10^(e/2)
	// For normalized mantissa in [10^15, 10^16), sqrt is in [~3.16*10^7, 10^8)
	mantissa := f.mantissa
	if mantissa < 0 {
		mantissa = -mantissa
	}

	// Approximate sqrt of mantissa
	sqrtMant := intSqrt(uint64(mantissa))

	// Adjust exponent: if original exp is e, result exp is e/2
	// But mantissa sqrt reduces magnitude by half in log scale
	// sqrt(m * 10^e) = sqrt(m) * 10^(e/2)
	// If m is ~10^15, sqrt(m) is ~3*10^7, so we need to normalize
	resultExp := f.exponent / 2

	// Normalize the result
	return NewIOUAmountValue(int64(sqrtMant), resultExp)
}

// divIOUValues divides two IOU values
func divIOUValues(a, b IOUAmountValue) IOUAmountValue {
	if b.mantissa == 0 {
		return ZeroIOUValue()
	}
	if a.mantissa == 0 {
		return ZeroIOUValue()
	}

	// a / b = (a.mantissa / b.mantissa) * 10^(a.exponent - b.exponent)
	// Scale up a.mantissa to preserve precision
	aMant := a.mantissa
	bMant := b.mantissa

	negative := (aMant < 0) != (bMant < 0)
	if aMant < 0 {
		aMant = -aMant
	}
	if bMant < 0 {
		bMant = -bMant
	}

	// Scale aMant by 10^16 for precision
	// Use big.Int to avoid overflow
	bigA := new(big.Int).SetInt64(aMant)
	bigB := new(big.Int).SetInt64(bMant)
	scale := new(big.Int).SetInt64(1e16)
	bigA.Mul(bigA, scale)

	bigResult := new(big.Int).Div(bigA, bigB)
	resultMant := bigResult.Int64()
	resultExp := a.exponent - b.exponent - 16

	if negative {
		resultMant = -resultMant
	}

	return NewIOUAmountValue(resultMant, resultExp)
}

// divIOUValuesByInt divides an IOU value by an integer
func divIOUValuesByInt(a IOUAmountValue, d int64) IOUAmountValue {
	if d == 0 || a.mantissa == 0 {
		return ZeroIOUValue()
	}

	mantissa := a.mantissa
	negative := (mantissa < 0) != (d < 0)
	if mantissa < 0 {
		mantissa = -mantissa
	}
	if d < 0 {
		d = -d
	}

	// Simple integer division for small divisors
	// For divisors like 2, we can just divide directly
	resultMant := mantissa / d
	remainder := mantissa % d
	resultExp := a.exponent

	// Handle case where result is too small (below normalized range)
	// by scaling up and adjusting exponent
	if resultMant < MinMantissa && resultMant > 0 {
		// Multiply mantissa by 10 and check remainder contribution
		for resultMant < MinMantissa && resultExp > MinExponent {
			resultMant = resultMant*10 + (remainder*10)/d
			remainder = (remainder * 10) % d
			resultExp--
		}
	}

	if negative {
		resultMant = -resultMant
	}

	return NewIOUAmountValue(resultMant, resultExp)
}
