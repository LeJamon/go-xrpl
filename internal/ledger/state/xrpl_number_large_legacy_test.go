package state

import (
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMantissaScaleForRulesWithFix(t *testing.T) {
	require.Equal(t, MantissaScaleLarge330, MantissaScaleForRulesWithFixes(false, false, false, false, false))
	require.Equal(t, MantissaScaleSmall, MantissaScaleForRulesWithFix(true, false, false, true))
	require.Equal(t, MantissaScaleLargeLegacy, MantissaScaleForRulesWithFix(true, true, false, false))
	require.Equal(t, MantissaScaleLargeLegacy, MantissaScaleForRulesWithFix(true, false, true, false))
	require.Equal(t, MantissaScaleLarge320, MantissaScaleForRulesWithFix(true, true, false, true))
	require.Equal(t, MantissaScaleLarge330, MantissaScaleForRulesWithFixes(true, true, false, true, true))
}

func TestLargeLegacyPreservesCuspRounding(t *testing.T) {
	const (
		a = int64(1_000_000_000_000_049_863)
		b = int64(9_223_372_036_854_315_903)
	)

	legacy := NewXRPLNumberScaled(a, 0, MantissaScaleLargeLegacy, RoundUpward).
		MulRounded(NewXRPLNumberScaled(b, 0, MantissaScaleLargeLegacy, RoundUpward), RoundUpward)
	fixed := NewXRPLNumberScaled(a, 0, MantissaScaleLarge, RoundUpward).
		MulRounded(NewXRPLNumberScaled(b, 0, MantissaScaleLarge, RoundUpward), RoundUpward)

	require.Equal(t, (int64(math.MaxInt64)/100)*100, legacy.Mantissa())
	require.Equal(t, 18, legacy.Exponent())
	require.Equal(t, int64(math.MaxInt64)/10+1, fixed.Mantissa())
	require.Equal(t, 19, fixed.Exponent())

	// Constructing and operating in one mode must not mutate the other mode.
	require.Equal(t, (int64(math.MaxInt64)/100)*100, legacy.Mantissa())
}

func TestLargeDivisionDroppedRemainderFix(t *testing.T) {
	const denominator = int64(1_000_000_000_000_000_007)
	legacy := NewXRPLNumberScaled(2, 0, MantissaScaleLargeLegacy, RoundUpward).
		DivRounded(NewXRPLNumberScaled(denominator, 0, MantissaScaleLargeLegacy, RoundUpward), RoundUpward)
	fixed := NewXRPLNumberScaled(2, 0, MantissaScaleLarge, RoundUpward).
		DivRounded(NewXRPLNumberScaled(denominator, 0, MantissaScaleLarge, RoundUpward), RoundUpward)
	exact := new(big.Rat).SetFrac(big.NewInt(2), big.NewInt(denominator))

	require.Negative(t, numberRat(legacy).Cmp(exact))
	require.GreaterOrEqual(t, numberRat(fixed).Cmp(exact), 0)
	require.NotEqual(t, legacy, fixed)
}

func TestParseXRPLNumberRejectsOutOfRangeMantissa(t *testing.T) {
	_, err := ParseXRPLNumber("9223372036854775807.6", MantissaScaleLarge, RoundToNearest)
	require.Error(t, err)
}

func TestParseXRPLNumberRejectsInt32ExponentMagnitudeOverflow(t *testing.T) {
	for _, value := range []string{"1e2147483648", "1e-2147483648"} {
		_, err := ParseXRPLNumber(value, MantissaScaleLarge, RoundToNearest)
		require.Error(t, err, value)
	}
}

func numberRat(number XRPLNumber) *big.Rat {
	value := new(big.Rat).SetInt64(number.Mantissa())
	exponent := number.Exponent()
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absInt(exponent))), nil)
	if exponent >= 0 {
		return value.Mul(value, new(big.Rat).SetInt(factor))
	}
	return value.Quo(value, new(big.Rat).SetInt(factor))
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
