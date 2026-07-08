package state

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMantissaScaleForRules(t *testing.T) {
	t.Parallel()
	// large when no rules, or when either amendment is enabled; small otherwise.
	require.Equal(t, MantissaScaleLarge, MantissaScaleForRules(false, false, false))
	require.Equal(t, MantissaScaleLarge, MantissaScaleForRules(true, true, false))
	require.Equal(t, MantissaScaleLarge, MantissaScaleForRules(true, false, true))
	require.Equal(t, MantissaScaleLarge, MantissaScaleForRules(true, true, true))
	require.Equal(t, MantissaScaleSmall, MantissaScaleForRules(true, false, false))
}

func TestRoundToAsset_Integral(t *testing.T) {
	t.Parallel()
	// A fractional drops value rounds to the nearest whole unit.
	require.Equal(t,
		NewXRPLNumberScaled(8, 0, MantissaScaleLarge, RoundToNearest),
		NewXRPLNumberScaled(75, -1, MantissaScaleLarge, RoundToNearest).RoundToAsset(true)) // 7.5 -> 8 (ties to even)
	require.Equal(t,
		NewXRPLNumberScaled(2, 0, MantissaScaleLarge, RoundToNearest),
		NewXRPLNumberScaled(25, -1, MantissaScaleLarge, RoundToNearest).RoundToAsset(true)) // 2.5 -> 2 (ties to even)

	// A large int64 value is preserved exactly at the large scale.
	maxUnits := NewXRPLNumberScaled(math.MaxInt64, 0, MantissaScaleLarge, RoundToNearest)
	require.Equal(t, int64(math.MaxInt64), maxUnits.RoundToAsset(true).ToInt64WithMode(RoundToNearest))

	// A sub-unit value rounds to zero (would drop a soeDEFAULT field).
	require.True(t, NewXRPLNumberScaled(4, -1, MantissaScaleLarge, RoundToNearest).RoundToAsset(true).IsZero()) // 0.4 -> 0
}

func TestRoundToAsset_IOU(t *testing.T) {
	t.Parallel()
	// An IOU value keeps its magnitude, reduced to 16 significant digits. The
	// result is re-expressed at the Number's (large) scale, so compare by value.
	v := NewXRPLNumberScaled(12345678901234567, -3, MantissaScaleLarge, RoundToNearest) // 17 sig digits
	got := v.RoundToAsset(false)
	want := NewXRPLNumberScaled(1234567890123457, -2, MantissaScaleLarge, RoundToNearest) // 16 sig digits, rounded
	require.True(t, got.Equal(want), "got %s want %s", got, want)
	require.False(t, got.Equal(v), "precision should have been reduced")

	// Zero stays zero.
	require.True(t, NewXRPLNumberScaled(0, 0, MantissaScaleLarge, RoundToNearest).RoundToAsset(false).IsZero())
}

func TestAssociateAssetField_Removal(t *testing.T) {
	t.Parallel()
	// Integral asset, sub-unit value, soeDEFAULT field -> removed.
	_, remove := AssociateAssetField(NewXRPLNumberScaled(4, -1, MantissaScaleLarge, RoundToNearest), true, true)
	require.True(t, remove)

	// Same value but the field is not soeDEFAULT -> kept.
	_, remove = AssociateAssetField(NewXRPLNumberScaled(4, -1, MantissaScaleLarge, RoundToNearest), true, false)
	require.False(t, remove)

	// Non-zero rounded value -> kept even when soeDEFAULT.
	rounded, remove := AssociateAssetField(NewXRPLNumberScaled(15, -1, MantissaScaleLarge, RoundToNearest), true, true)
	require.False(t, remove)
	require.Equal(t, int64(2), rounded.ToInt64WithMode(RoundToNearest)) // 1.5 -> 2
}

func TestNormalizeToRange_IOUBounds(t *testing.T) {
	t.Parallel()
	// Snapping an over-precise value into the IOU mantissa range rounds to
	// nearest and adjusts the exponent.
	n := NewXRPLNumberScaled(99999999999999999, 0, MantissaScaleLarge, RoundToNearest) // 17 nines
	m, e := n.NormalizeToRange(uint64(MinMantissa), uint64(MaxMantissa))
	require.Equal(t, int64(1000000000000000), m)
	require.Equal(t, 2, e)

	// A value already within range is unchanged.
	m, e = NewXRPLNumberScaled(1234567890123456, -6, MantissaScaleLarge, RoundToNearest).NormalizeToRange(uint64(MinMantissa), uint64(MaxMantissa))
	require.Equal(t, int64(1234567890123456), m)
	require.Equal(t, -6, e)
}

// TestToInt64_OverflowPanics mirrors rippled's Number::operator rep() throwing
// on int64 overflow — the error surfaced as a TER by the tx engine's recover.
func TestToInt64_OverflowPanics(t *testing.T) {
	t.Parallel()
	// 10^19 exceeds int64; scaling the mantissa up must panic.
	require.Panics(t, func() {
		NewXRPLNumberScaled(1, 19, MantissaScaleLarge, RoundToNearest).ToInt64WithMode(RoundToNearest)
	})
}
