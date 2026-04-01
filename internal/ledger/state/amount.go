package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

// Amount represents either XRP (as drops), an issued currency amount, or an MPT amount.
// Matches rippled's STAmount which can hold any asset type (XRP, IOU, or MPT).
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

	// mptIssuanceID stores the 24-byte MPT issuance ID as an uppercase hex string.
	// When set, this Amount represents an MPT amount rather than an IOU.
	// This is populated from the binary codec's "mpt_issuance_id" field during
	// deserialization and used by the engine to detect MPT payments/clawbacks.
	mptIssuanceID string
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

// IsMPT returns true if this Amount holds an MPT issuance (has mpt_issuance_id).
// Matches rippled's STAmount::holds<MPTIssue>() check.
func (a Amount) IsMPT() bool {
	return a.mptIssuanceID != ""
}

// MPTIssuanceID returns the MPT issuance ID hex string, or empty if not an MPT amount.
func (a Amount) MPTIssuanceID() string {
	return a.mptIssuanceID
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
	if a.IsMPT() {
		return json.Marshal(map[string]string{
			"value":           a.iou.String(),
			"mpt_issuance_id": a.mptIssuanceID,
		})
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

	// Try as object (issued currency or MPT amount)
	var objVal struct {
		Value         string `json:"value"`
		Currency      string `json:"currency"`
		Issuer        string `json:"issuer"`
		MPTIssuanceID string `json:"mpt_issuance_id"`
	}
	if err := json.Unmarshal(data, &objVal); err != nil {
		return err
	}

	// MPT amounts have mpt_issuance_id instead of currency/issuer
	if objVal.MPTIssuanceID != "" {
		// Parse value as integer for MPT (whole number amounts)
		raw, err := strconv.ParseInt(objVal.Value, 10, 64)
		if err != nil {
			// Fallback to IOU parsing if not a plain integer
			a.iou = parseIOUValueFromString(objVal.Value)
		} else {
			a.iou = NewIOUAmountValue(raw, 0) // normalized for binary codec
			a.mptRaw = &raw
		}
		a.mptIssuanceID = strings.ToUpper(objVal.MPTIssuanceID)
		a.Native = false
		return nil
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
	if a.IsMPT() {
		return map[string]string{
			"value":           a.iou.String(),
			"mpt_issuance_id": a.mptIssuanceID,
		}
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
