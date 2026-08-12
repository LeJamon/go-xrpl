package state

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests port rippled 3.1.0's Number_test.cpp coverage for the two mantissa
// scales (small / large), exercising to_string, int64 conversion, rounding and
// the range limits in both.

// newNumberInternal builds a Number from its internal (sign, uint64 mantissa,
// exponent) triple and normalizes under mode — rippled's Number{..., normalized}.
func newNumberInternal(negative bool, mantissa uint64, exponent int, scale MantissaScale, mode RoundingMode) XRPLNumber {
	n := XRPLNumber{negative: negative, mantissa: mantissa, exponent: exponent, scale: scale}
	n.normalize(mode)
	return n
}

func TestNewXRPLNumberFromUint(t *testing.T) {
	tests := []struct {
		mantissa uint64
		exponent int
		want     string
	}{
		{mantissa: math.MaxInt64, want: "9223372036854776e3"},
		{mantissa: uint64(math.MaxInt64) + 1, want: "9223372036854776e3"},
		{mantissa: math.MaxUint64, want: "1844674407370955e4"},
		{mantissa: math.MaxUint64, exponent: -255, want: "1844674407370955e-251"},
	}

	for _, test := range tests {
		number := NewXRPLNumberFromUint(test.mantissa, test.exponent)
		require.Equal(t, test.want, number.String())
		require.Positive(t, number.Signum())
	}
}

// numberMin / numberMax / numberLowest mirror Number::min/max/lowest for a scale.
func numberMin(scale MantissaScale) XRPLNumber {
	minM, _, _ := scale.params()
	return XRPLNumber{mantissa: minM, exponent: xrplNumMinExponent, scale: scale}
}

func numberMax(scale MantissaScale) XRPLNumber {
	_, maxM, _ := scale.params()
	if maxM > xrplNumMaxRep {
		maxM = xrplNumMaxRep
	}
	return XRPLNumber{mantissa: maxM, exponent: xrplNumMaxExponent, scale: scale}
}

func numberLowest(scale MantissaScale) XRPLNumber {
	n := numberMax(scale)
	n.negative = true
	return n
}

func TestXRPLNumber_ToString_CommonBothScales(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mant int64
		exp  int
		want string
	}{
		{-2, 0, "-2"},
		{0, 0, "0"},
		{2, 0, "2"},
		{25, -3, "0.025"},
		{-25, -3, "-0.025"},
		{25, 1, "250"},
		{-25, 1, "-250"},
		{2, 20, "2e20"},
		{-2, -20, "-2e-20"},
		// Threshold edges: decimal just inside, scientific just outside.
		{2, -10, "0.0000000002"},
		{2, -11, "2e-11"},
		{-2, 10, "-20000000000"},
		{-2, 11, "-2e11"},
	}
	for _, scale := range []MantissaScale{MantissaScaleSmall, MantissaScaleLarge} {
		for _, tc := range cases {
			got := NewXRPLNumberScaled(tc.mant, tc.exp, scale, RoundToNearest).String()
			require.Equalf(t, tc.want, got, "scale=%d Number(%d,%d)", scale, tc.mant, tc.exp)
		}
	}
}

func TestXRPLNumber_ToString_SmallScale(t *testing.T) {
	t.Parallel()
	s := MantissaScaleSmall

	require.Equal(t, "1e-32753", numberMin(s).String())
	require.Equal(t, "9999999999999999e32768", numberMax(s).String())
	require.Equal(t, "-9999999999999999e32768", numberLowest(s).String())

	const maxMantissa = uint64(9999999999999999)
	require.Equal(t, "9999999999999999",
		newNumberInternal(false, maxMantissa*1000+999, -3, s, RoundTowardsZero).String())
	require.Equal(t, "-9999999999999999",
		newNumberInternal(true, maxMantissa*1000+999, -3, s, RoundTowardsZero).String())

	require.Equal(t, "9223372036854775",
		NewXRPLNumberScaled(math.MaxInt64, -3, s, RoundTowardsZero).String())
	require.Equal(t, "-9223372036854775",
		NewXRPLNumberScaled(math.MaxInt64, -3, s, RoundTowardsZero).Negate().String())

	require.Equal(t, "-9223372036854775e3",
		NewXRPLNumberScaled(math.MinInt64, 0, s, RoundTowardsZero).String())
	require.Equal(t, "9223372036854775e3",
		NewXRPLNumberScaled(math.MinInt64, 0, s, RoundTowardsZero).Negate().String())
}

func TestXRPLNumber_ToString_LargeScale(t *testing.T) {
	t.Parallel()
	l := MantissaScaleLarge

	require.Equal(t, "1e-32750", numberMin(l).String())
	require.Equal(t, "9223372036854775807e32768", numberMax(l).String())
	require.Equal(t, "-9223372036854775807e32768", numberLowest(l).String())

	const maxMantissa = uint64(9999999999999999999)
	require.Equal(t, "9999999999999999990",
		newNumberInternal(false, maxMantissa, 0, l, RoundTowardsZero).String())
	require.Equal(t, "-9999999999999999990",
		newNumberInternal(true, maxMantissa, 0, l, RoundTowardsZero).String())

	require.Equal(t, "9223372036854775807",
		NewXRPLNumberScaled(math.MaxInt64, 0, l, RoundTowardsZero).String())
	require.Equal(t, "-9223372036854775807",
		NewXRPLNumberScaled(math.MaxInt64, 0, l, RoundTowardsZero).Negate().String())

	require.Equal(t, "-9223372036854775807",
		NewXRPLNumberScaled(math.MinInt64, 0, l, RoundTowardsZero).String())
	require.Equal(t, "9223372036854775807",
		NewXRPLNumberScaled(math.MinInt64, 0, l, RoundTowardsZero).Negate().String())

	maxPlus1 := NewXRPLNumberScaled(math.MaxInt64, 0, l, RoundToNearest).Add(NewXRPLNumberScaled(1, 0, l, RoundToNearest))
	require.Equal(t, "9223372036854775807", maxPlus1.String())
	require.Equal(t, "-9223372036854775807", maxPlus1.Negate().String())
}

func TestXRPLNumber_ToInt64_Rounding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mant                    int64
		exp                     int
		nearest, zero, down, up int64
	}{
		{13, -1, 1, 1, 1, 2},
		{23, -1, 2, 2, 2, 3},
		{15, -1, 2, 1, 1, 2},
		{25, -1, 2, 2, 2, 3},
		{152, -2, 2, 1, 1, 2},
		{252, -2, 3, 2, 2, 3},
		{17, -1, 2, 1, 1, 2},
		{27, -1, 3, 2, 2, 3},
		{-13, -1, -1, -1, -2, -1},
		{-23, -1, -2, -2, -3, -2},
		{-15, -1, -2, -1, -2, -1},
		{-25, -1, -2, -2, -3, -2},
		{-152, -2, -2, -1, -2, -1},
		{-252, -2, -3, -2, -3, -2},
		{-17, -1, -2, -1, -2, -1},
		{-27, -1, -3, -2, -3, -2},
	}
	for _, scale := range []MantissaScale{MantissaScaleSmall, MantissaScaleLarge} {
		for _, tc := range cases {
			n := NewXRPLNumberScaled(tc.mant, tc.exp, scale, RoundToNearest)
			require.Equalf(t, tc.nearest, n.ToInt64WithMode(RoundToNearest), "nearest scale=%d %d,%d", scale, tc.mant, tc.exp)
			require.Equalf(t, tc.zero, n.ToInt64WithMode(RoundTowardsZero), "zero scale=%d %d,%d", scale, tc.mant, tc.exp)
			require.Equalf(t, tc.down, n.ToInt64WithMode(RoundDownward), "down scale=%d %d,%d", scale, tc.mant, tc.exp)
			require.Equalf(t, tc.up, n.ToInt64WithMode(RoundUpward), "up scale=%d %d,%d", scale, tc.mant, tc.exp)
		}
	}
}

// TestXRPLNumber_Int64_ExactRepresentation checks the defining property of the
// large scale: every int64 value round-trips exactly, while the small scale
// truncates values above its 16-digit precision.
func TestXRPLNumber_Int64_ExactRepresentation(t *testing.T) {
	t.Parallel()

	// Large scale: int64 max is inside the mantissa range, exponent stays <= 0.
	maxInt64 := NewXRPLNumberScaled(math.MaxInt64, 0, MantissaScaleLarge, RoundToNearest)
	require.LessOrEqual(t, maxInt64.Exponent(), 0)
	require.Equal(t, int64(math.MaxInt64), maxInt64.ToInt64WithMode(RoundToNearest))

	// Small scale: int64 max exceeds 16-digit precision, so it gains a positive
	// exponent and cannot represent the low digits exactly.
	maxInt64Small := NewXRPLNumberScaled(math.MaxInt64, 0, MantissaScaleSmall, RoundToNearest)
	require.Greater(t, maxInt64Small.Exponent(), 0)

	// maxMantissa()/10 property for the large scale (rippled testInt64).
	const largeMax = uint64(9999999999999999999)
	maxN := newNumberInternal(false, largeMax, 0, MantissaScaleLarge, RoundTowardsZero)
	require.Equal(t, int64(largeMax/10), maxN.Mantissa())
	require.Equal(t, 1, maxN.Exponent())
	require.Equal(t, newNumberInternal(false, largeMax/10-1, 20, MantissaScaleLarge, RoundTowardsZero),
		maxN.MulRounded(maxN, RoundTowardsZero))
}

// TestXRPLNumber_Limits verifies min/max round-trip through arithmetic identities.
func TestXRPLNumber_Limits(t *testing.T) {
	t.Parallel()
	for _, scale := range []MantissaScale{MantissaScaleSmall, MantissaScaleLarge} {
		one := NewXRPLNumberScaled(1, 0, scale, RoundToNearest)
		mn := numberMin(scale)
		mx := numberMax(scale)
		// min * 1 == min, max * 1 == max (identity through Mul).
		require.True(t, mn.MulRounded(one, RoundToNearest).Equal(mn), "min*1 scale=%d", scale)
		require.True(t, mx.MulRounded(one, RoundToNearest).Equal(mx), "max*1 scale=%d", scale)
		// lowest == -max.
		require.True(t, numberLowest(scale).Equal(mx.Negate()), "lowest scale=%d", scale)
	}
}

// TestXRPLNumber_Root2_BothScales checks sqrt on perfect squares in both scales.
func TestXRPLNumber_Root2_BothScales(t *testing.T) {
	t.Parallel()
	for _, scale := range []MantissaScale{MantissaScaleSmall, MantissaScaleLarge} {
		four := NewXRPLNumberScaled(4, 0, scale, RoundToNearest)
		require.True(t, four.root2().Equal(NewXRPLNumberScaled(2, 0, scale, RoundToNearest)), "sqrt(4) scale=%d", scale)
		nine := NewXRPLNumberScaled(9, 0, scale, RoundToNearest)
		require.True(t, nine.root2().Equal(NewXRPLNumberScaled(3, 0, scale, RoundToNearest)), "sqrt(9) scale=%d", scale)
	}
}
