package state

import (
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLarge330MantissaBoundary(t *testing.T) {
	max := NewXRPLNumberScaled(math.MaxInt64, 0, MantissaScaleLarge330, RoundToNearest)
	min := NewXRPLNumberScaled(math.MinInt64, 0, MantissaScaleLarge330, RoundToNearest)
	large320Min := NewXRPLNumberScaled(math.MinInt64, 0, MantissaScaleLarge320, RoundToNearest)

	require.Equal(t, "9223372036854775807", max.String())
	require.Equal(t, "-9223372036854775807", min.String())
	require.Equal(t, "-9223372036854775810", large320Min.String())

	boundary := new(big.Int).SetUint64(xrplNumMaxRep + 1)
	require.Equal(t, "9223372036854775810", normalizeFromBig(false, boundary, 0, MantissaScaleLarge330, RoundUpward).String())
	require.Equal(t, "9223372036854775807", normalizeFromBig(false, boundary, 0, MantissaScaleLarge330, RoundToNearest).String())
	require.Equal(t, "9223372036854775807", normalizeFromBig(false, boundary, 0, MantissaScaleLarge330, RoundDownward).String())
}

func TestLarge330CuspAdditionAndSubtraction(t *testing.T) {
	below330 := NewXRPLNumberScaled(math.MaxInt64, 0, MantissaScaleLarge330, RoundToNearest)
	above330 := newNumberInternal(false, xrplNumMaxRepUp, 0, MantissaScaleLarge330, RoundToNearest)
	operands := []int64{4, 5, 6, 14, 15, 16, 24, 25, 26}
	modes := []RoundingMode{RoundToNearest, RoundTowardsZero, RoundDownward, RoundUpward}

	for _, mode := range modes {
		for _, raw := range operands {
			operand := NewXRPLNumberScaled(raw, -1, MantissaScaleLarge330, RoundToNearest)

			wantAdd := above330
			if (mode == RoundToNearest && raw < 15) || mode == RoundTowardsZero || mode == RoundDownward {
				wantAdd = below330
			}
			require.Equal(t, wantAdd, below330.AddRounded(operand, mode), "add mode=%d operand=%d", mode, raw)

			wantSub := above330
			if (mode == RoundToNearest && raw > 15) || mode == RoundTowardsZero || mode == RoundDownward {
				wantSub = below330
			}
			require.Equal(t, wantSub, above330.AddRounded(operand.Negate(), mode), "subtract mode=%d operand=%d", mode, raw)
		}
	}

	below320 := NewXRPLNumberScaled(math.MaxInt64, 0, MantissaScaleLarge320, RoundToNearest)
	pointSix320 := NewXRPLNumberScaled(6, -1, MantissaScaleLarge320, RoundToNearest)
	require.Equal(t, "9223372036854775810", below320.Add(pointSix320).String())
}

func TestLarge330AddRecoversDiscardedDigits(t *testing.T) {
	x := NewXRPLNumberScaled(-1_074_956_551_220_905_975, 28, MantissaScaleLarge330, RoundToNearest)
	cases := []struct {
		y    XRPLNumber
		want int64
	}{
		{NewXRPLNumberScaled(5_175_909_259_972_499_745, 22, MantissaScaleLarge330, RoundToNearest), -1_074_951_375_311_646_003},
		{NewXRPLNumberScaled(1, 0, MantissaScaleLarge330, RoundToNearest), -1_074_956_551_220_905_975},
		{NewXRPLNumberScaled(1, 10, MantissaScaleLarge330, RoundToNearest), -1_074_956_551_220_905_975},
		{NewXRPLNumberScaled(1, 20, MantissaScaleLarge330, RoundToNearest), -1_074_956_551_220_905_975},
		{NewXRPLNumberScaled(1, 27, MantissaScaleLarge330, RoundToNearest), -1_074_956_551_220_905_975},
		{NewXRPLNumberScaled(1, 28, MantissaScaleLarge330, RoundToNearest), -1_074_956_551_220_905_974},
		{NewXRPLNumberScaled(1, 31, MantissaScaleLarge330, RoundToNearest), -1_074_956_551_220_904_975},
	}
	for _, tc := range cases {
		got := x.Add(tc.y)
		require.Equal(t, tc.want, got.Mantissa())
		require.Equal(t, 28, got.Exponent())
	}
}

func TestLarge330DirectedAdditionBySign(t *testing.T) {
	three := NewXRPLNumberScaled(3, 0, MantissaScaleLarge330, RoundToNearest)
	cases := []struct {
		name     string
		a, b     XRPLNumber
		wantDown string
		wantUp   string
	}{
		{
			name:     "two negative values",
			a:        NewXRPLNumberScaled(-6, 18, MantissaScaleLarge330, RoundToNearest),
			b:        NewXRPLNumberScaled(-6, 18, MantissaScaleLarge330, RoundToNearest).Sub(three),
			wantDown: "-12000000000000000010",
			wantUp:   "-12000000000000000000",
		},
		{
			name:     "two positive values",
			a:        NewXRPLNumberScaled(6, 18, MantissaScaleLarge330, RoundToNearest),
			b:        NewXRPLNumberScaled(6, 18, MantissaScaleLarge330, RoundToNearest).Add(three),
			wantDown: "12000000000000000000",
			wantUp:   "12000000000000000010",
		},
		{
			name:     "negative result",
			a:        NewXRPLNumberScaled(1, 18, MantissaScaleLarge330, RoundToNearest),
			b:        NewXRPLNumberScaled(-9, 18, MantissaScaleLarge330, RoundToNearest).Sub(three),
			wantDown: "-8000000000000000003",
			wantUp:   "-8000000000000000003",
		},
		{
			name:     "positive result",
			a:        NewXRPLNumberScaled(-1, 18, MantissaScaleLarge330, RoundToNearest),
			b:        NewXRPLNumberScaled(9, 18, MantissaScaleLarge330, RoundToNearest).Add(three),
			wantDown: "8000000000000000003",
			wantUp:   "8000000000000000003",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantDown, newNumberRat(tc.a.AddRounded(tc.b, RoundDownward)).FloatString(0))
			require.Equal(t, tc.wantUp, newNumberRat(tc.a.AddRounded(tc.b, RoundUpward)).FloatString(0))
		})
	}
}

func TestLarge330SubtractionRounding(t *testing.T) {
	type subCase struct {
		offset int
		extraB bool
	}
	cases := []subCase{{offset: 2, extraB: true}, {offset: 2}, {offset: 30}}
	modes := []RoundingMode{RoundTowardsZero, RoundUpward, RoundDownward, RoundToNearest}
	scales := []MantissaScale{MantissaScaleSmall, MantissaScaleLargeLegacy, MantissaScaleLarge320, MantissaScaleLarge330}

	for _, scale := range scales {
		_, _, log := scale.params()
		for _, tc := range cases {
			a := NewXRPLNumberScaled(1, log+tc.offset, scale, RoundToNearest)
			b := NewXRPLNumberScaled(-1, 0, scale, RoundToNearest)
			if tc.extraB {
				b = NewXRPLNumberScaled(-1, log, scale, RoundToNearest).Sub(NewXRPLNumberScaled(1, 0, scale, RoundToNearest))
			}
			exact := new(big.Rat).Add(newNumberRat(a), newNumberRat(b))
			expectedExponent := tc.offset
			if scale == MantissaScaleSmall && tc.extraB {
				expectedExponent--
			}
			epsilon := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(expectedExponent)), nil)

			for _, mode := range modes {
				got := a.AddRounded(b, mode)
				diff := new(big.Rat).Sub(newNumberRat(got), exact)
				wantDiff := big.NewInt(1)
				if mode == RoundDownward || (scale == MantissaScaleLarge330 && mode == RoundTowardsZero) {
					wantDiff.Sub(big.NewInt(1), epsilon)
				}
				require.Equal(t, new(big.Rat).SetInt(wantDiff), diff, "scale=%d offset=%d extraB=%v mode=%d", scale, tc.offset, tc.extraB, mode)
			}
		}
	}
}

func TestXRPLNumberCmpDoesNotRound(t *testing.T) {
	scales := []MantissaScale{MantissaScaleSmall, MantissaScaleLargeLegacy, MantissaScaleLarge320, MantissaScaleLarge330}
	for _, scale := range scales {
		values := []XRPLNumber{
			NewXRPLNumberScaled(-5, 100, scale, RoundToNearest),
			NewXRPLNumberScaled(-1, 100, scale, RoundToNearest),
			NewXRPLNumberScaled(-7, -10, scale, RoundToNearest),
			NewXRPLNumberScaled(-2, -10, scale, RoundToNearest),
			NewXRPLNumberScaled(0, 0, scale, RoundToNearest),
			NewXRPLNumberScaled(2, -10, scale, RoundToNearest),
			NewXRPLNumberScaled(7, -10, scale, RoundToNearest),
			NewXRPLNumberScaled(1, 100, scale, RoundToNearest),
			NewXRPLNumberScaled(5, 100, scale, RoundToNearest),
		}
		for i := range values {
			for j := range values {
				want := 0
				if i < j {
					want = -1
				} else if i > j {
					want = 1
				}
				require.Equal(t, want, values[i].Cmp(values[j]), "scale=%d i=%d j=%d", scale, i, j)
			}
		}
	}
}

func newNumberRat(n XRPLNumber) *big.Rat {
	r := new(big.Rat).SetInt64(n.Mantissa())
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(absInt(n.Exponent()))), nil)
	if n.Exponent() >= 0 {
		return r.Mul(r, new(big.Rat).SetInt(factor))
	}
	return r.Quo(r, new(big.Rat).SetInt(factor))
}
