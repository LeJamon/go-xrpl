package state

// Asset-aware Number rounding, ported from rippled 3.1.0's roundToAsset /
// associateAsset (PRs #6156, #6259). These are the primitives the upcoming
// SingleAssetVault / LendingProtocol transactors call on each sMD_NeedsAsset
// NUMBER field (sfAssetsTotal, sfDebtTotal, ...) before it is written to an SLE
// and serialized: the value is rounded to the precision of the vault's asset,
// and a soeDEFAULT field that rounds to its default is dropped.
//
// Phase-B wiring point. The SLE-level associateAsset(SLE, asset) iterates the
// ten NeedsAsset NUMBER fields on ltVAULT / Loan / LoanBroker entries, calls
// AssociateAssetField on each present value, writes the rounded value back, and
// removes the field when removeIfDefault is true and its template style is
// soeDEFAULT. That SLE iteration (and the per-sfield sMD_NeedsAsset codec flag
// it keys off) lands with the transactors; this file supplies the value-level
// rounding and removal decision so they can be unit-tested independently.

// RoundToAsset rounds the Number to the precision of the asset it represents, as
// rippled's in-place roundToAsset(asset, Number) does — value = STAmount{asset,
// value} read back as a Number — under round-to-nearest.
//
//   - integral assets (native XRP, MPT): the value counts indivisible units
//     (drops / MPT units), so it is rounded to a whole number. Panics on int64
//     overflow, matching rippled's Number::operator rep() throw.
//   - IOU assets: the mantissa is snapped to the 16-significant-digit STAmount
//     range [MinMantissa, MaxMantissa] (rippled cMinValue / cMaxValue).
func (n XRPLNumber) RoundToAsset(integral bool) XRPLNumber {
	if n.IsZero() {
		return n
	}
	if integral {
		v := n.ToInt64WithMode(RoundToNearest)
		return NewXRPLNumberScaled(v, 0, n.scale, RoundToNearest)
	}
	m, e := n.NormalizeToRange(uint64(MinMantissa), uint64(MaxMantissa))
	return NewXRPLNumberScaled(m, e, n.scale, RoundToNearest)
}

// AssociateAssetField applies the associateAsset semantics to one NUMBER field
// value: it rounds the value to the asset's precision and reports whether a
// soeDEFAULT-styled field must be removed because the rounded value equals its
// (zero) default (rippled PR #6259). integral is Asset::integral() for the
// vault's asset (true for native XRP and MPT, false for IOUs); soeDefault is
// whether the field is soeDEFAULT in the entry's template.
func AssociateAssetField(value XRPLNumber, integral, soeDefault bool) (rounded XRPLNumber, remove bool) {
	rounded = value.RoundToAsset(integral)
	return rounded, soeDefault && rounded.IsZero()
}
