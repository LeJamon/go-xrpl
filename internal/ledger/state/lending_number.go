package state

// Number primitives the XLS-66 lending amortization math builds on, ported from
// rippled 3.1.0. These extend the XRPLNumber foundation (#1263) with the two
// operations LendingHelpers needs beyond +/-/*//: integer exponentiation
// (power) and asset-precision rounding to an explicit decimal scale
// (roundToAsset(asset, value, scale, mode)).

// Power returns n raised to exp using exponentiation by squaring, matching
// rippled basics/Number.cpp power(Number, unsigned): log2(exp) multiplications
// under the default (round-to-nearest) mode. exp==0 yields one in n's scale.
func (n XRPLNumber) Power(exp uint32) XRPLNumber {
	if exp == 0 {
		return n.oneVal()
	}
	if exp == 1 {
		return n
	}
	r := n.Power(exp / 2)
	r = r.Mul(r)
	if exp%2 != 0 {
		r = r.Mul(n)
	}
	return r
}

// RoundToAssetScale rounds n to the precision of the asset it denominates,
// mirroring rippled STAmount.h roundToAsset(asset, value, scale, rounding):
//
//   - integral assets (native XRP, MPT): the value counts indivisible units, so
//     it is rounded to a whole number under mode (scale is ignored).
//   - IOU assets: the value is first snapped to STAmount's 16-significant-digit
//     mantissa under mode, then rounded to the decimal exponent `scale` via the
//     reference-value trick (roundToScale). A scale at or below the value's
//     exponent is a no-op.
//
// The result carries n's mantissa scale (large in a lending/SAV context).
func (n XRPLNumber) RoundToAssetScale(integral bool, scale int, mode RoundingMode) XRPLNumber {
	if n.IsZero() {
		return n
	}
	if integral {
		v := n.ToInt64WithMode(mode)
		return NewXRPLNumberScaled(v, 0, n.scale, RoundToNearest)
	}
	iou := newIOUAmountValueRounded(n.Mantissa(), n.Exponent(), mode)
	iou = roundIOUToScale(iou, scale, mode)
	if iou.IsZero() {
		return n.zero()
	}
	return NewXRPLNumberScaled(iou.Mantissa(), iou.Exponent(), n.scale, RoundToNearest)
}

// AssetExponent returns the decimal exponent of STAmount{asset, n} under mode,
// used to derive a loan's scale (rippled computeLoanProperties reads
// amount.exponent()). Integral assets and zero have exponent 0; an IOU reports
// the exponent of its 16-significant-digit normalization.
func (n XRPLNumber) AssetExponent(integral bool, mode RoundingMode) int {
	if n.IsZero() || integral {
		return 0
	}
	return newIOUAmountValueRounded(n.Mantissa(), n.Exponent(), mode).Exponent()
}

// roundIOUToScale rounds a 16-digit IOU value to the coarser decimal exponent
// `scale`, mirroring rippled STAmount.cpp roundToScale. When the value's
// exponent already meets or exceeds scale there is nothing to drop. Otherwise it
// adds a reference value (mantissa cMinValue at exponent scale) so IOU addition
// truncates the sub-scale digits under mode, then subtracts the reference back.
func roundIOUToScale(v IOUAmountValue, scale int, mode RoundingMode) IOUAmountValue {
	if v.IsZero() || v.Exponent() >= scale {
		return v
	}
	refMant := int64(MinMantissa)
	if v.IsNegative() {
		refMant = -refMant
	}
	ref := IOUAmountValue{mantissa: refMant, exponent: scale}
	negRef := IOUAmountValue{mantissa: -refMant, exponent: scale}
	sum := addIOUValuesRounded(v, ref, mode)
	return addIOUValuesRounded(sum, negRef, mode)
}
