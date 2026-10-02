package state

import (
	"math"
	"math/big"
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
	ctx := NewNumberContext(MantissaScaleSmall, false)
	_, err := a.Add(b)
	require.ErrorContains(t, err, "MPT addition overflow")

	other := NewMPTAmountWithIssuanceID(1, "rIssuer", "00000005AE123A8556F3CF91154711376AFB0F894F832B3D")
	_, err = b.Add(other)
	require.ErrorContains(t, err, "different MPT issuances")
	require.PanicsWithValue(t, "MPT value overflow", func() {
		_ = a.MulWithNumberContext(NewMPTAmountWithIssuanceID(2, "rIssuer", arithmeticMPTID), ctx, false, RoundToNearest)
	})
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
	ctx := NewNumberContext(MantissaScaleSmall, false)
	product := a.MulWithNumberContext(b, ctx, false, RoundToNearest)
	require.Equal(t, int64(42), mustMPTRaw(t, product))
	require.Equal(t, arithmeticMPTID, product.MPTIssuanceID())

	half := NewIssuedAmountFromValue(5, -1, "", "")
	scaled := a.MulWithNumberContext(
		half,
		NewNumberContext(MantissaScaleSmall, true),
		false,
		RoundToNearest,
	)
	require.Equal(t, int64(4), mustMPTRaw(t, scaled))
	require.Equal(t, arithmeticMPTID, scaled.MPTIssuanceID())
}

func TestAmountMPTMulAsIntegralOperand(t *testing.T) {
	for _, switchover := range []bool{false, true} {
		ctx := NewNumberContext(MantissaScaleSmall, switchover)
		rate := NewIssuedAmountFromValue(100_000, 0, "", "")
		mpt := NewMPTAmountWithIssuanceID(80, "rIssuer", arithmeticMPTID)
		product := rate.MulWithNumberContext(mpt, ctx, false, RoundToNearest)
		require.Equal(t, 0, product.Compare(NewIssuedAmountFromValue(8_000_000, 0, "", "")), "switchover=%v, product=%v", switchover, product.Float64())
	}
}

func TestAmountMPTDivPreservesIntegralIssue(t *testing.T) {
	for _, switchover := range []bool{false, true} {
		ctx := NewNumberContext(MantissaScaleSmall, switchover)
		amount := NewMPTAmountWithIssuanceID(100, "rIssuer", arithmeticMPTID)
		quotient := amount.DivWithNumberContext(NewXRPAmountFromInt(12), ctx, false)
		require.Equal(t, int64(8), mustMPTRaw(t, quotient), "switchover=%v", switchover)
		require.Equal(t, arithmeticMPTID, quotient.MPTIssuanceID())

		zero := NewMPTAmountWithIssuanceID(0, "rIssuer", arithmeticMPTID).
			DivWithNumberContext(NewXRPAmountFromInt(12), ctx, false)
		require.Zero(t, mustMPTRaw(t, zero))
		require.Equal(t, arithmeticMPTID, zero.MPTIssuanceID())
	}
}

func TestMPTRoundHelpersUseIntegralRounding(t *testing.T) {
	mpt := NewMPTAmountWithIssuanceID(5, "rIssuer", arithmeticMPTID)
	half := NewIssuedAmountFromValue(5, -1, "", "")
	two := NewIssuedAmountFromValue(2, 0, "", "")
	ctx := NewNumberContext(MantissaScaleSmall, false)

	require.Equal(t, int64(3), MulRoundMPTWithNumberContext(mpt, half, ctx, true))
	require.Equal(t, int64(3), MulRoundMPTStrictWithNumberContext(mpt, half, ctx, true))
	require.Equal(t, int64(2), MulRoundMPTStrictWithNumberContext(mpt, half, ctx, false))
	require.Equal(t, int64(3), DivRoundMPTWithNumberContext(mpt, two, ctx, true))
	require.Equal(t, int64(2), DivRoundMPTStrictWithNumberContext(mpt, two, ctx, false))
}

func TestMPTRoundHelpersUseMPTokensV2NumberArithmetic(t *testing.T) {
	mpt := NewMPTAmountWithIssuanceID(1_000_000_000_000_000_000, "rIssuer", arithmeticMPTID)
	one := NewIssuedAmountFromValue(1_000_000_000_000_000, -15, "", "")
	two := NewIssuedAmountFromValue(2_000_000_000_000_000, -15, "", "")
	v2 := NewNumberContext(MantissaScaleSmall, false).WithMPTokensV2(true)
	legacy := NewNumberContext(MantissaScaleSmall, false)
	legacyUniversal := NewNumberContext(MantissaScaleSmall, true)

	for _, test := range []struct {
		name    string
		roundUp bool
	}{
		{name: "down", roundUp: false},
		{name: "up", roundUp: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, int64(2_000_000_000_000_000_000), MulRoundMPTWithNumberContext(mpt, two, v2, test.roundUp))
			require.Equal(t, int64(1_000_000_000_000_000_000), DivRoundMPTStrictWithNumberContext(mpt, one, v2, test.roundUp))
			require.PanicsWithValue(t, "muldivRound overflow", func() {
				_ = MulRoundMPTWithNumberContext(mpt, two, legacy, test.roundUp)
			})
		})
	}

	require.PanicsWithValue(t, "muldivRound overflow", func() {
		_ = DivRoundMPTStrictWithNumberContext(mpt, one, legacy, false)
	})
	require.Equal(t, int64(1_000_000_000_000_000_000), MulRoundMPTWithNumberContext(mpt, one, legacyUniversal, false))
	negativeMPT := NewMPTAmountWithIssuanceID(-1_000_000_000_000_000_000, "rIssuer", arithmeticMPTID)
	require.Equal(t, int64(-1_000_000_000_000_000_000), MulRoundMPTWithNumberContext(negativeMPT, one, legacyUniversal, true))
	cuspMPT := NewMPTAmountWithIssuanceID(1<<59, "rIssuer", arithmeticMPTID)
	sixteen := NewIssuedAmountFromValue(16, 0, "", "")
	require.Equal(t, int64(9_223_372_036_854_775_800), MulRoundMPTWithNumberContext(cuspMPT, sixteen, legacyUniversal, false))
	require.Equal(t, int64(9_223_372_036_854_775_800), MulRoundMPTStrictWithNumberContext(cuspMPT, sixteen, legacyUniversal, true))

	fractional := NewMPTAmountWithIssuanceID(5, "rIssuer", arithmeticMPTID)
	half := NewIssuedAmountFromValue(5, -1, "", "")
	twoInteger := NewIssuedAmountFromValue(2, 0, "", "")
	require.Equal(t, int64(3), MulRoundMPTWithNumberContext(fractional, half, v2, true))
	require.Equal(t, int64(2), MulRoundMPTWithNumberContext(fractional, half, v2, false))
	require.Equal(t, int64(3), DivRoundMPTWithNumberContext(fractional, twoInteger, v2, true))
	require.Equal(t, int64(2), DivRoundMPTWithNumberContext(fractional, twoInteger, v2, false))

	maxMPT := NewMPTAmountWithIssuanceID(math.MaxInt64, "rIssuer", arithmeticMPTID)
	require.Panics(t, func() {
		_ = MulRoundMPTWithNumberContext(maxMPT, two, v2, true)
	})
}

func TestMPTRoundHelpersMatchMPTokensV2RateOracle(t *testing.T) {
	largeAmount := NewMPTAmountWithIssuanceID(1_230_000_000_000_000_000, "rIssuer", arithmeticMPTID)
	scaledAmount := NewMPTAmountWithIssuanceID(1_845_000_000_000_000_000, "rIssuer", arithmeticMPTID)
	transferRate := NewIssuedAmountFromValue(1_500_000_000_000_000, -15, "", "")
	v2 := NewNumberContext(MantissaScaleSmall, false).WithMPTokensV2(true)
	legacy := NewNumberContext(MantissaScaleSmall, false)

	require.Equal(t, int64(1_845_000_000_000_000_000), MulRoundMPTWithNumberContext(largeAmount, transferRate, v2, true))
	require.Equal(t, int64(1_230_000_000_000_000_000), DivRoundMPTWithNumberContext(scaledAmount, transferRate, v2, true))
	require.PanicsWithValue(t, "muldivRound overflow", func() {
		_ = MulRoundMPTWithNumberContext(largeAmount, transferRate, legacy, true)
	})
	require.PanicsWithValue(t, "muldivRound overflow", func() {
		_ = DivRoundMPTWithNumberContext(scaledAmount, transferRate, legacy, true)
	})

	one := NewMPTAmountWithIssuanceID(1, "rIssuer", arithmeticMPTID)
	two := NewMPTAmountWithIssuanceID(2, "rIssuer", arithmeticMPTID)
	require.Equal(t, int64(2), MulRoundMPTWithNumberContext(one, transferRate, v2, true))
	require.Equal(t, int64(1), MulRoundMPTWithNumberContext(one, transferRate, v2, false))
	require.Equal(t, int64(2), DivRoundMPTWithNumberContext(two, transferRate, v2, true))
	require.Equal(t, int64(1), DivRoundMPTWithNumberContext(two, transferRate, v2, false))
}

func TestMPTRoundHelpersUseDirectedSignedRounding(t *testing.T) {
	mpt := NewMPTAmountWithIssuanceID(-5, "rIssuer", arithmeticMPTID)
	half := NewIssuedAmountFromValue(5, -1, "", "")
	two := NewIssuedAmountFromValue(2, 0, "", "")
	v2 := NewNumberContext(MantissaScaleSmall, false).WithMPTokensV2(true)
	legacy := NewNumberContext(MantissaScaleSmall, false)
	legacyUniversal := NewNumberContext(MantissaScaleSmall, true)

	require.Equal(t, int64(-3), MulRoundMPTWithNumberContext(mpt, half, v2, true))
	require.Equal(t, int64(-2), MulRoundMPTWithNumberContext(mpt, half, v2, false))
	require.Equal(t, int64(-3), DivRoundMPTWithNumberContext(mpt, two, v2, true))
	require.Equal(t, int64(-2), DivRoundMPTWithNumberContext(mpt, two, v2, false))
	require.Equal(t, int64(-3), DivRoundMPTStrictWithNumberContext(mpt, two, legacyUniversal, true))
	require.Equal(t, int64(-3), DivRoundMPTStrictWithNumberContext(mpt, two, legacyUniversal, false))

	require.Equal(t, int64(-2), MulRoundMPTWithNumberContext(mpt, half, legacy, true))
	require.Equal(t, int64(-3), MulRoundMPTWithNumberContext(mpt, half, legacy, false))
}

func TestMPTRoundHelpersKeepMPTIntegralFastPath(t *testing.T) {
	ctx := NewNumberContext(MantissaScaleSmall, false).WithMPTokensV2(true)
	a := NewMPTAmountWithIssuanceID(2, "rIssuer", arithmeticMPTID)
	b := NewMPTAmountWithIssuanceID(3, "rIssuer", arithmeticMPTID)
	require.Equal(t, int64(6), MulRoundMPTWithNumberContext(a, b, ctx, false))

	negative := NewMPTAmountWithIssuanceID(-2, "rIssuer", arithmeticMPTID)
	require.PanicsWithValue(t, "MPT value overflow", func() {
		_ = MulRoundMPTWithNumberContext(negative, b, ctx, false)
	})
	large := NewMPTAmountWithIssuanceID(3_037_000_500, "rIssuer", arithmeticMPTID)
	require.PanicsWithValue(t, "MPT value overflow", func() {
		_ = MulRoundMPTWithNumberContext(large, large, ctx, false)
	})
	productOverflow := NewMPTAmountWithIssuanceID(1<<62, "rIssuer", arithmeticMPTID)
	require.Equal(t, int64(9_223_372_036_854_775_800), MulRoundMPTWithNumberContext(a, productOverflow, ctx, false))
}

func TestMPTRoundCanonicalizesBeforeIntegralUnderflow(t *testing.T) {
	legacy := NewNumberContext(MantissaScaleSmall, true)
	v2 := legacy.WithMPTokensV2(true)
	for _, test := range []struct {
		name        string
		round       func(Amount, Amount, NumberContext, bool) int64
		left, right Amount
	}{
		{"multiply", MulRoundMPTWithNumberContext,
			NewMPTAmountWithIssuanceID(-1_000_000_000_000_000_000, "rIssuer", arithmeticMPTID),
			NewIssuedAmountFromValue(1, -19, "", "")},
		{"multiply strict", MulRoundMPTStrictWithNumberContext,
			NewMPTAmountWithIssuanceID(-1_000_000_000_000_000_000, "rIssuer", arithmeticMPTID),
			NewIssuedAmountFromValue(1, -19, "", "")},
		{"divide", DivRoundMPTWithNumberContext,
			NewMPTAmountWithIssuanceID(-100_000_000_000_000_000, "rIssuer", arithmeticMPTID),
			NewIssuedAmountFromValue(1, 18, "", "")},
		{"divide strict", DivRoundMPTStrictWithNumberContext,
			NewMPTAmountWithIssuanceID(-100_000_000_000_000_000, "rIssuer", arithmeticMPTID),
			NewIssuedAmountFromValue(1, 18, "", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, int64(-1), test.round(test.left, test.right, legacy, false))
			require.Equal(t, int64(0), test.round(test.left, test.right, legacy, true))
			require.Equal(t, int64(0), test.round(test.left, test.right, v2, false))
			require.Equal(t, int64(-1), test.round(test.left, test.right, v2, true))
		})
	}
}

func TestMuldivRoundRejectsUint64Overflow(t *testing.T) {
	max := new(big.Int).SetUint64(^uint64(0))
	require.Equal(t, ^uint64(0), muldivRound(max, big.NewInt(1), big.NewInt(1), false))
	require.PanicsWithValue(t, "muldivRound overflow", func() {
		_ = muldivRound(max, big.NewInt(2), big.NewInt(1), false)
	})
}

func mustMPTRaw(t *testing.T, amount Amount) int64 {
	t.Helper()
	value, ok := amount.MPTRaw()
	require.True(t, ok)
	return value
}
