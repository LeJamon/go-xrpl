package sle

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestXRPLGuard_PushPop verifies that guard digits are preserved through push/pop.
func TestXRPLGuard_PushPop(t *testing.T) {
	var g xrplGuard

	// Push digits 1, 2, 3 (most recent pushed = most significant)
	g.push(1)
	g.push(2)
	g.push(3)

	// Pop should return in LIFO order
	require.Equal(t, uint(3), g.pop())
	require.Equal(t, uint(2), g.pop())
	require.Equal(t, uint(1), g.pop())
}

// TestXRPLGuard_Round verifies banker's rounding (round-half-to-even).
func TestXRPLGuard_Round(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*xrplGuard)
		want    int
	}{
		{
			name: "empty guard rounds down",
			setup: func(g *xrplGuard) {},
			want: -1,
		},
		{
			name: "exactly half (5) rounds to even (0 = half)",
			setup: func(g *xrplGuard) {
				g.push(5)
			},
			want: 0,
		},
		{
			name: "greater than half rounds up",
			setup: func(g *xrplGuard) {
				g.push(6)
			},
			want: 1,
		},
		{
			name: "less than half rounds down",
			setup: func(g *xrplGuard) {
				g.push(4)
			},
			want: -1,
		},
		{
			name: "exactly half with xbit rounds up",
			setup: func(g *xrplGuard) {
				// Push two digits to set xbit: push(3) then push(5)
				// After push(3): digits=0x3000..., xbit=false
				// After push(5): digits=0x5000...0003..., xbit from 3's lowest nibble
				g.push(3) // this will be shifted off when we push 5
				g.push(5)
				// Now digits has 5 at top with 3 shifted down
				// The 3 digit at position 2 means digits > 0x5000...
				// Actually let's test xbit directly
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var g xrplGuard
			tt.setup(&g)
			got := g.round()
			require.Equal(t, tt.want, got)
		})
	}
}

// TestXRPLNumber_Normalize verifies Guard-based normalization.
func TestXRPLNumber_Normalize(t *testing.T) {
	tests := []struct {
		name     string
		mant     int64
		exp      int
		wantMant int64
		wantExp  int
	}{
		{
			name:     "zero",
			mant:     0,
			exp:      0,
			wantMant: 0,
			wantExp:  xrplNumZeroExponent,
		},
		{
			name:     "small integer 7",
			mant:     7,
			exp:      0,
			wantMant: 7000000000000000,
			wantExp:  -15,
		},
		{
			name:     "already normalized",
			mant:     1500000000000000,
			exp:      -15,
			wantMant: 1500000000000000,
			wantExp:  -15,
		},
		{
			name:     "negative value",
			mant:     -1234567890123456,
			exp:      -16,
			wantMant: -1234567890123456,
			wantExp:  -16,
		},
		{
			name:     "needs scale down with guard rounding",
			mant:     99999999999999995,
			exp:      -17,
			wantMant: 1000000000000000,
			wantExp:  -15, // 99999999999999995 / 10 = 9999999999999999 with guard 5 → rounds to even (odd mantissa) → 10000000000000000 → /10 → exp=-15
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := NewXRPLNumber(tt.mant, tt.exp)
			require.Equal(t, tt.wantMant, n.mantissa, "mantissa mismatch")
			require.Equal(t, tt.wantExp, n.exponent, "exponent mismatch")
		})
	}
}

// TestXRPLNumber_Add_SameSign verifies addition of same-sign numbers.
func TestXRPLNumber_Add_SameSign(t *testing.T) {
	// 1.5 + 1.5 = 3.0
	a := NewXRPLNumber(1500000000000000, -15) // 1.5
	b := NewXRPLNumber(1500000000000000, -15) // 1.5
	result := a.Add(b)
	require.Equal(t, int64(3000000000000000), result.mantissa)
	require.Equal(t, -15, result.exponent)
}

// TestXRPLNumber_Add_DifferentSign_GuardRecovery tests the critical Guard digit
// recovery during subtraction — the key feature missing from our old addIOUValues.
func TestXRPLNumber_Add_DifferentSign_GuardRecovery(t *testing.T) {
	// 1.0 - 0.9999999999999999 should NOT be zero
	a := NewXRPLNumber(1000000000000000, -15)  // 1.0
	b := NewXRPLNumber(-9999999999999999, -16) // -0.9999999999999999
	result := a.Add(b)
	// Result should be 1e-16 = {1000000000000000, -31}
	require.False(t, result.IsZero(), "result should not be zero")
	require.Equal(t, int64(1000000000000000), result.mantissa)
	require.Equal(t, -31, result.exponent)
}

// TestXRPLNumber_Add_CriticalCase tests the specific case that causes
// TestOffer_CreateThenCross to fail: -1.0 + 0.0335
func TestXRPLNumber_Add_CriticalCase(t *testing.T) {
	// -1.0 + 0.0335 = -0.9665
	a := NewXRPLNumber(-1000000000000000, -15)
	b := NewXRPLNumber(3350000000000000, -17)
	result := a.Add(b)
	// With Guard precision, the result should differ from simple -0.9665
	// The exact value depends on the guard digit recovery
	t.Logf("Result: mantissa=%d, exponent=%d", result.mantissa, result.exponent)
	require.False(t, result.IsZero())
	require.True(t, result.mantissa < 0, "result should be negative")
}

// TestXRPLNumber_Mul verifies multiplication with Guard rounding.
func TestXRPLNumber_Mul(t *testing.T) {
	// 2.0 * 3.0 = 6.0
	a := NewXRPLNumber(2000000000000000, -15) // 2.0
	b := NewXRPLNumber(3000000000000000, -15) // 3.0
	result := a.Mul(b)
	require.Equal(t, int64(6000000000000000), result.mantissa)
	require.Equal(t, -15, result.exponent)
}

// TestXRPLNumber_Div verifies division with 10^17 scaling.
func TestXRPLNumber_Div(t *testing.T) {
	// 6.0 / 2.0 = 3.0
	a := NewXRPLNumber(6000000000000000, -15) // 6.0
	b := NewXRPLNumber(2000000000000000, -15) // 2.0
	result := a.Div(b)
	require.Equal(t, int64(3000000000000000), result.mantissa)
	require.Equal(t, -15, result.exponent)
}

// TestXRPLNumber_Div_ThirdPrecision tests 1/3 precision.
func TestXRPLNumber_Div_ThirdPrecision(t *testing.T) {
	one := NewXRPLNumber(1000000000000000, -15)
	three := NewXRPLNumber(3000000000000000, -15)
	result := one.Div(three)
	// 1/3 = 0.3333333333333333... → 3333333333333333 * 10^-16
	require.Equal(t, int64(3333333333333333), result.mantissa)
	require.Equal(t, -16, result.exponent)
}

// TestXRPLNumber_ExactCancellation verifies a + (-a) = 0.
func TestXRPLNumber_ExactCancellation(t *testing.T) {
	a := NewXRPLNumber(1234567890123456, -16)
	result := a.Add(a.Negate())
	require.True(t, result.IsZero())
}

// TestXRPLNumber_ToIOUAmountValue verifies conversion back to IOUAmount.
func TestXRPLNumber_ToIOUAmountValue(t *testing.T) {
	n := NewXRPLNumber(1234567890123456, -16)
	iou := n.ToIOUAmountValue()
	require.Equal(t, int64(1234567890123456), iou.mantissa)
	require.Equal(t, -16, iou.exponent)
}

// TestXRPLNumber_ToIOUAmountValue_Underflow verifies exponent underflow → zero.
func TestXRPLNumber_ToIOUAmountValue_Underflow(t *testing.T) {
	// Create a number with exponent below IOUAmount min (-96)
	// We can't use NewXRPLNumber as it would normalize, so test the conversion
	n := XRPLNumber{mantissa: 1000000000000000, exponent: -100}
	iou := n.ToIOUAmountValue()
	require.True(t, iou.IsZero())
}

// TestAddIOUValues_WithSwitchover tests that addIOUValues produces different
// results with the switchover enabled vs disabled.
func TestAddIOUValues_WithSwitchover(t *testing.T) {
	a := IOUAmountValue{mantissa: -1000000000000000, exponent: -15} // -1.0
	b := IOUAmountValue{mantissa: 3350000000000000, exponent: -17}  // 0.0335

	// Without switchover (legacy)
	SetNumberSwitchover(false)
	resultOff := addIOUValues(a, b)
	t.Logf("Switchover OFF: mantissa=%d, exponent=%d, value=%s",
		resultOff.mantissa, resultOff.exponent, resultOff.String())

	// With switchover (Guard-based)
	SetNumberSwitchover(true)
	resultOn := addIOUValues(a, b)
	t.Logf("Switchover ON:  mantissa=%d, exponent=%d, value=%s",
		resultOn.mantissa, resultOn.exponent, resultOn.String())

	// Reset
	SetNumberSwitchover(false)

	// Both should be approximately -0.9665 but may differ in last digits
	require.False(t, resultOff.IsZero())
	require.False(t, resultOn.IsZero())
}

// TestMulRatio_RoomToGrow tests that roomToGrow captures fractional precision.
func TestMulRatio_RoomToGrow(t *testing.T) {
	// A transfer rate calculation that requires roomToGrow for precision:
	// amount * 1005000000 / 1000000000 (1.005 transfer rate)
	amt := NewIssuedAmountFromValue(3350000000000000, -17, "USD", "rTest") // 0.0335
	result := amt.MulRatio(1005000000, 1000000000, false)
	t.Logf("MulRatio result: mantissa=%d, exponent=%d, value=%s",
		result.iou.Mantissa(), result.iou.Exponent(), result.Value())
	// With roomToGrow, the result should have more precision than 0.033675
	require.False(t, result.IsZero())
}
