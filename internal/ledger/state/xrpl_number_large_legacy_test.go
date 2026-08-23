package state

import (
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMantissaScaleForRulesWithFixes(t *testing.T) {
	require.Equal(t, MantissaScaleLarge330, MantissaScaleForRulesWithFixes(false, false, false, false, false))
	require.Equal(t, MantissaScaleSmall, MantissaScaleForRulesWithFixes(true, false, false, true, false))
	require.Equal(t, MantissaScaleLargeLegacy, MantissaScaleForRulesWithFixes(true, true, false, false, false))
	require.Equal(t, MantissaScaleLargeLegacy, MantissaScaleForRulesWithFixes(true, false, true, false, false))
	require.Equal(t, MantissaScaleLarge320, MantissaScaleForRulesWithFixes(true, true, false, true, false))
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
	tests := []struct {
		name        string
		numerator   int64
		denominator int64
		mode        RoundingMode
		legacyCmp   int
		fixedCmp    int
	}{
		{
			name:        "positive upward",
			numerator:   2,
			denominator: 1_000_000_000_000_000_007,
			mode:        RoundUpward,
			legacyCmp:   -1,
			fixedCmp:    1,
		},
		{
			name:        "negative downward",
			numerator:   -2,
			denominator: 1_000_000_000_000_000_007,
			mode:        RoundDownward,
			legacyCmp:   1,
			fixedCmp:    -1,
		},
		{
			name:        "nearest with trailing digits after half",
			numerator:   1_269_917_268_816_087_809,
			denominator: 3_458_525_013_821_685_511,
			mode:        RoundToNearest,
			legacyCmp:   -1,
			fixedCmp:    1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exact := new(big.Rat).SetFrac(big.NewInt(test.numerator), big.NewInt(test.denominator))
			for _, scale := range []MantissaScale{MantissaScaleLargeLegacy, MantissaScaleLarge320, MantissaScaleLarge330} {
				quotient := NewXRPLNumberScaled(test.numerator, 0, scale, test.mode).
					DivRounded(NewXRPLNumberScaled(test.denominator, 0, scale, test.mode), test.mode)
				wantCmp := test.fixedCmp
				if scale == MantissaScaleLargeLegacy {
					wantCmp = test.legacyCmp
				}
				require.Equal(t, wantCmp, numberRat(quotient).Cmp(exact), "scale=%d", scale)
			}
		})
	}
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
