package state

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestXRPLNumber_CmpTwoNegatives locks in parity with rippled 3.2.0's Number
// operator< fix: two negative Numbers with equal exponents compare by magnitude
// correctly (the larger magnitude is the smaller number). goXRPL compares via
// Sub().Signum(), so it was never subject to rippled's pre-3.2.0 inversion, but
// this guards against a regression.
func TestXRPLNumber_CmpTwoNegatives(t *testing.T) {
	neg5 := NewXRPLNumber(-5, 0)
	neg3 := NewXRPLNumber(-3, 0)
	require.Equal(t, -1, neg5.Cmp(neg3), "-5 < -3")
	require.Equal(t, 1, neg3.Cmp(neg5), "-3 > -5")
	require.Equal(t, 0, neg5.Cmp(NewXRPLNumber(-5, 0)))
}

// TestXRPLGuard_PushPop verifies that guard digits are preserved through push/pop.
func TestXRPLGuard_PushPop(t *testing.T) {
	t.Parallel()
	var g xrplGuard

	g.push(1)
	g.push(2)
	g.push(3)

	require.Equal(t, uint(3), g.pop())
	require.Equal(t, uint(2), g.pop())
	require.Equal(t, uint(1), g.pop())
}

// TestXRPLGuard_Round verifies banker's rounding (round-half-to-even).
func TestXRPLGuard_Round(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(*xrplGuard)
		want  int
	}{
		{"empty guard rounds down", func(g *xrplGuard) {}, -1},
		{"exactly half (5) rounds to even (0 = half)", func(g *xrplGuard) { g.push(5) }, 0},
		{"greater than half rounds up", func(g *xrplGuard) { g.push(6) }, 1},
		{"less than half rounds down", func(g *xrplGuard) { g.push(4) }, -1},
		{"exactly half with xbit rounds up", func(g *xrplGuard) { g.push(3); g.push(5) }, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var g xrplGuard
			tt.setup(&g)
			require.Equal(t, tt.want, g.round(RoundToNearest, cuspRoundingDisabled))
		})
	}
}

// TestXRPLNumber_Normalize verifies Guard-based normalization.
func TestXRPLNumber_Normalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mant     int64
		exp      int
		wantMant int64
		wantExp  int
	}{
		{"zero", 0, 0, 0, xrplNumZeroExponent},
		{"small integer 7", 7, 0, 7000000000000000, -15},
		{"already normalized", 1500000000000000, -15, 1500000000000000, -15},
		{"negative value", -1234567890123456, -16, -1234567890123456, -16},
		// 99999999999999995 / 10 = 9999999999999999 with guard 5 → ties to even
		// (odd mantissa) → 10000000000000000 → /10 → exp -15.
		{"needs scale down with guard rounding", 99999999999999995, -17, 1000000000000000, -15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := NewXRPLNumber(tt.mant, tt.exp)
			require.Equal(t, tt.wantMant, n.Mantissa(), "mantissa mismatch")
			require.Equal(t, tt.wantExp, n.Exponent(), "exponent mismatch")
		})
	}
}

// TestXRPLNumber_Add_SameSign verifies addition of same-sign numbers.
func TestXRPLNumber_Add_SameSign(t *testing.T) {
	t.Parallel()
	a := NewXRPLNumber(1500000000000000, -15) // 1.5
	b := NewXRPLNumber(1500000000000000, -15) // 1.5
	result := a.Add(b)
	require.Equal(t, int64(3000000000000000), result.Mantissa())
	require.Equal(t, -15, result.Exponent())
}

// TestXRPLNumber_Add_DifferentSign_GuardRecovery tests Guard digit recovery
// during subtraction: 1.0 - 0.9999999999999999 must not be zero.
func TestXRPLNumber_Add_DifferentSign_GuardRecovery(t *testing.T) {
	t.Parallel()
	a := NewXRPLNumber(1000000000000000, -15)  // 1.0
	b := NewXRPLNumber(-9999999999999999, -16) // -0.9999999999999999
	result := a.Add(b)
	require.False(t, result.IsZero(), "result should not be zero")
	require.Equal(t, int64(1000000000000000), result.Mantissa())
	require.Equal(t, -31, result.Exponent())
}

// TestXRPLNumber_Add_CriticalCase tests -1.0 + 0.0335 (an offer-crossing case).
func TestXRPLNumber_Add_CriticalCase(t *testing.T) {
	t.Parallel()
	a := NewXRPLNumber(-1000000000000000, -15)
	b := NewXRPLNumber(3350000000000000, -17)
	result := a.Add(b)
	require.False(t, result.IsZero())
	require.True(t, result.Mantissa() < 0, "result should be negative")
}

// TestXRPLNumber_Mul verifies multiplication with Guard rounding.
func TestXRPLNumber_Mul(t *testing.T) {
	t.Parallel()
	a := NewXRPLNumber(2000000000000000, -15) // 2.0
	b := NewXRPLNumber(3000000000000000, -15) // 3.0
	result := a.Mul(b)
	require.Equal(t, int64(6000000000000000), result.Mantissa())
	require.Equal(t, -15, result.Exponent())
}

// TestXRPLNumber_Div verifies division with 10^17 scaling.
func TestXRPLNumber_Div(t *testing.T) {
	t.Parallel()
	a := NewXRPLNumber(6000000000000000, -15) // 6.0
	b := NewXRPLNumber(2000000000000000, -15) // 2.0
	result := a.Div(b)
	require.Equal(t, int64(3000000000000000), result.Mantissa())
	require.Equal(t, -15, result.Exponent())
}

// TestXRPLNumber_Div_ThirdPrecision tests 1/3 precision.
func TestXRPLNumber_Div_ThirdPrecision(t *testing.T) {
	t.Parallel()
	one := NewXRPLNumber(1000000000000000, -15)
	three := NewXRPLNumber(3000000000000000, -15)
	result := one.Div(three)
	require.Equal(t, int64(3333333333333333), result.Mantissa())
	require.Equal(t, -16, result.Exponent())
}

// TestXRPLNumber_ExactCancellation verifies a + (-a) = 0.
func TestXRPLNumber_ExactCancellation(t *testing.T) {
	t.Parallel()
	a := NewXRPLNumber(1234567890123456, -16)
	result := a.Add(a.Negate())
	require.True(t, result.IsZero())
}

// TestXRPLNumber_ToIOUAmountValue verifies conversion back to IOUAmount.
func TestXRPLNumber_ToIOUAmountValue(t *testing.T) {
	t.Parallel()
	n := NewXRPLNumber(1234567890123456, -16)
	iou := n.ToIOUAmountValue()
	require.Equal(t, int64(1234567890123456), iou.mantissa)
	require.Equal(t, -16, iou.exponent)
}

// TestXRPLNumber_ToIOUAmountValue_Underflow verifies exponent underflow → zero.
func TestXRPLNumber_ToIOUAmountValue_Underflow(t *testing.T) {
	t.Parallel()
	// An un-normalized number with exponent below IOUAmount min (-96).
	n := newXRPLNumberRaw(1000000000000000, -100)
	iou := n.ToIOUAmountValue()
	require.True(t, iou.IsZero())
}

func TestAddIOUValues_WithNumberContext(t *testing.T) {
	a := IOUAmountValue{mantissa: -1000000000000000, exponent: -15} // -1.0
	b := IOUAmountValue{mantissa: 3350000000000000, exponent: -17}  // 0.0335

	resultOff := addIOUValuesRoundedWithContext(
		a,
		b,
		RoundToNearest,
		NewNumberContext(MantissaScaleSmall, false),
	)
	resultOn := addIOUValuesRoundedWithContext(
		a,
		b,
		RoundToNearest,
		NewNumberContext(MantissaScaleSmall, true),
	)

	require.False(t, resultOff.IsZero())
	require.False(t, resultOn.IsZero())
}

// TestMulRatio_RoomToGrow tests that roomToGrow captures fractional precision.
func TestMulRatio_RoomToGrow(t *testing.T) {
	t.Parallel()
	amt := NewIssuedAmountFromValue(3350000000000000, -17, "USD", "rTest") // 0.0335
	result := amt.MulRatio(1005000000, 1000000000, false)
	require.False(t, result.IsZero())
}
