package state

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

const arithmeticMPTID = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"

func TestAmountMPTArithmeticPreservesIntegralValueAndIssue(t *testing.T) {
	issuer := "rIssuer"
	a := NewMPTAmountWithIssuanceID(math.MaxInt64-10, issuer, arithmeticMPTID)
	b := NewMPTAmountWithIssuanceID(5, issuer, arithmeticMPTID)

	sum, err := a.Add(b)
	require.NoError(t, err)
	require.Equal(t, int64(math.MaxInt64-5), mustMPTRaw(t, sum))
	require.Equal(t, arithmeticMPTID, sum.MPTIssuanceID())

	difference, err := b.Sub(a)
	require.NoError(t, err)
	require.Equal(t, int64(15-math.MaxInt64), mustMPTRaw(t, difference))
	require.Equal(t, arithmeticMPTID, difference.MPTIssuanceID())

	negated := b.Negate()
	require.Equal(t, int64(-5), mustMPTRaw(t, negated))
	require.Equal(t, arithmeticMPTID, negated.MPTIssuanceID())
}

func TestAmountMPTArithmeticRejectsMismatchedIssuesAndOverflow(t *testing.T) {
	a := NewMPTAmountWithIssuanceID(math.MaxInt64, "rIssuer", arithmeticMPTID)
	b := NewMPTAmountWithIssuanceID(1, "rIssuer", arithmeticMPTID)
	_, err := a.Add(b)
	require.ErrorContains(t, err, "MPT addition overflow")

	other := NewMPTAmountWithIssuanceID(1, "rIssuer", "00000005AE123A8556F3CF91154711376AFB0F894F832B3D")
	_, err = b.Add(other)
	require.ErrorContains(t, err, "different MPT issuances")
	require.PanicsWithValue(t, "MPT value overflow", func() { _ = a.Mul(NewMPTAmountWithIssuanceID(2, "rIssuer", arithmeticMPTID), false) })
}

func TestAmountMPTMulRatioUsesIntegralDirectionalRounding(t *testing.T) {
	a := NewMPTAmountWithIssuanceID(math.MaxInt64, "rIssuer", arithmeticMPTID)
	require.Equal(t, int64(3_074_457_345_618_258_602), mustMPTRaw(t, a.MulRatio(1, 3, false)))
	require.Equal(t, int64(3_074_457_345_618_258_603), mustMPTRaw(t, a.MulRatio(1, 3, true)))

	negative := NewMPTAmountWithIssuanceID(-5, "rIssuer", arithmeticMPTID)
	require.Equal(t, int64(-3), mustMPTRaw(t, negative.MulRatio(1, 2, false)))
	require.Equal(t, int64(-2), mustMPTRaw(t, negative.MulRatio(1, 2, true)))
}

func TestAmountMPTMulPreservesIssue(t *testing.T) {
	a := NewMPTAmountWithIssuanceID(7, "rIssuer", arithmeticMPTID)
	b := NewMPTAmountWithIssuanceID(6, "rIssuer", arithmeticMPTID)
	product := a.Mul(b, false)
	require.Equal(t, int64(42), mustMPTRaw(t, product))
	require.Equal(t, arithmeticMPTID, product.MPTIssuanceID())

	previous := GetNumberSwitchover()
	SetNumberSwitchover(true)
	t.Cleanup(func() { SetNumberSwitchover(previous) })
	half := NewIssuedAmountFromValue(5, -1, "", "")
	scaled := a.Mul(half, false)
	require.Equal(t, int64(4), mustMPTRaw(t, scaled))
	require.Equal(t, arithmeticMPTID, scaled.MPTIssuanceID())
}

func TestAmountMPTMulAsIntegralOperand(t *testing.T) {
	previous := GetNumberSwitchover()
	t.Cleanup(func() { SetNumberSwitchover(previous) })

	for _, switchover := range []bool{false, true} {
		SetNumberSwitchover(switchover)
		rate := NewIssuedAmountFromValue(100_000, 0, "", "")
		mpt := NewMPTAmountWithIssuanceID(80, "rIssuer", arithmeticMPTID)
		product := rate.Mul(mpt, false)
		require.Equal(t, 0, product.Compare(NewIssuedAmountFromValue(8_000_000, 0, "", "")), "switchover=%v, product=%v", switchover, product.Float64())
	}
}

func TestAmountMPTDivPreservesIntegralIssue(t *testing.T) {
	previous := GetNumberSwitchover()
	t.Cleanup(func() { SetNumberSwitchover(previous) })

	for _, switchover := range []bool{false, true} {
		SetNumberSwitchover(switchover)
		amount := NewMPTAmountWithIssuanceID(100, "rIssuer", arithmeticMPTID)
		quotient := amount.Div(NewXRPAmountFromInt(12), false)
		require.Equal(t, int64(8), mustMPTRaw(t, quotient), "switchover=%v", switchover)
		require.Equal(t, arithmeticMPTID, quotient.MPTIssuanceID())

		zero := NewMPTAmountWithIssuanceID(0, "rIssuer", arithmeticMPTID).
			Div(NewXRPAmountFromInt(12), false)
		require.Zero(t, mustMPTRaw(t, zero))
		require.Equal(t, arithmeticMPTID, zero.MPTIssuanceID())
	}
}

func TestMPTRoundHelpersUseIntegralRounding(t *testing.T) {
	mpt := NewMPTAmountWithIssuanceID(5, "rIssuer", arithmeticMPTID)
	half := NewIssuedAmountFromValue(5, -1, "", "")
	two := NewIssuedAmountFromValue(2, 0, "", "")

	require.Equal(t, int64(3), MulRoundMPT(mpt, half, true))
	require.Equal(t, int64(3), MulRoundMPTStrict(mpt, half, true))
	require.Equal(t, int64(2), MulRoundMPTStrict(mpt, half, false))
	require.Equal(t, int64(3), DivRoundMPT(mpt, two, true))
	require.Equal(t, int64(2), DivRoundMPTStrict(mpt, two, false))
}

func mustMPTRaw(t *testing.T, amount Amount) int64 {
	t.Helper()
	value, ok := amount.MPTRaw()
	require.True(t, ok)
	return value
}
