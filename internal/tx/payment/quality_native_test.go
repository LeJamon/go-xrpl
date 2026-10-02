package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestQualityFromAmountsPreservesIntegralOperands(t *testing.T) {
	var id [24]byte
	id[23] = 1
	book := NewIOUEitherAmount(tx.NewIssuedAmount(1_158_744_139_885_105, -15, "424F4F4B00000000000000000000000000000000", "rET2LC3hkiofP4xDQbRUsu9g3ooYmQnaph"))
	one := NewIOUEitherAmount(tx.NewIssuedAmount(1, 0, "USD", "rIssuer"))
	for _, test := range []struct {
		name    string
		in, out EitherAmount
		want    uint64
	}{
		{"testnet 21212185 XRP denominator", book, NewXRPEitherAmount(99_016_193_120_564_080), 0x44042857bd58f5f5},
		{"same integral MPT denominator", book, NewMPTEitherAmount(99_016_193_120_564_080, id), 0x44042857bd58f5f5},
		{"large XRP numerator", NewXRPEitherAmount(95_720_761_450_600_688), one, uint64(101)<<56 | 8_874_667_928_649_483},
		{"large MPT numerator", NewMPTEitherAmount(95_720_761_450_600_688, id), one, uint64(101)<<56 | 8_874_667_928_649_483},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, QualityFromAmounts(test.in, test.out).Value)
		})
	}
}

func TestQualityLimitsPreserveNativeOperands(t *testing.T) {
	ctx := state.NewNumberContext(state.MantissaScaleSmall, true)
	in := NewXRPEitherAmount(100_000_000_000_000_000)
	out := in
	limit := NewXRPEitherAmount(99_016_193_120_564_081)
	t.Run("ceil in", func(t *testing.T) {
		gotIn, gotOut := qualityOne.CeilInWithNumberContext(in, out, limit, ctx)
		require.Equal(t, limit, gotIn)
		require.Equal(t, NewXRPEitherAmount(99_016_193_120_564_082), gotOut)
	})
	t.Run("ceil in strict", func(t *testing.T) {
		gotIn, gotOut := qualityOne.CeilInStrictWithNumberContext(in, out, limit, false, ctx)
		require.Equal(t, limit, gotIn)
		require.Equal(t, limit, gotOut)
	})
	t.Run("ceil out strict", func(t *testing.T) {
		gotIn, gotOut := qualityOne.CeilOutStrictWithNumberContext(in, out, limit, false, ctx)
		require.Equal(t, limit, gotIn)
		require.Equal(t, limit, gotOut)
	})
}

func TestQualityCeilIntegerBoundaries(t *testing.T) {
	ctx := state.NewNumberContext(state.MantissaScaleSmall, false)
	tests := []struct {
		name                     string
		ceilIn                   bool
		rateIn, rateOut          int64
		inputIn, inputOut, limit int64
		wantIn, wantOut          int64
	}{
		{"ceil in 1:1 equal", true, 1, 1, 1, 1, 1, 1, 1},
		{"ceil in 1:1 below", true, 1, 1, 10, 10, 5, 5, 5},
		{"ceil in 1:1 above", true, 1, 1, 5, 5, 10, 5, 5},
		{"ceil in 2:1 below", true, 2, 1, 40, 20, 20, 20, 10},
		{"ceil in 2:1 equal", true, 2, 1, 40, 20, 40, 40, 20},
		{"ceil in 2:1 above", true, 2, 1, 40, 20, 50, 40, 20},
		{"ceil in 1:2 equal", true, 1, 2, 40, 80, 40, 40, 80},
		{"ceil in 1:2 below", true, 1, 2, 40, 80, 20, 20, 40},
		{"ceil in 1:2 above", true, 1, 2, 40, 80, 60, 40, 80},
		{"ceil out 1:1 equal", false, 1, 1, 1, 1, 1, 1, 1},
		{"ceil out 1:1 below", false, 1, 1, 10, 10, 5, 5, 5},
		{"ceil out 1:1 above", false, 1, 1, 10, 10, 20, 10, 10},
		{"ceil out 2:1 equal", false, 2, 1, 40, 20, 20, 40, 20},
		{"ceil out 2:1 below", false, 2, 1, 40, 20, 10, 20, 10},
		{"ceil out 2:1 above", false, 2, 1, 40, 20, 40, 40, 20},
		{"ceil out 1:2 equal", false, 1, 2, 40, 80, 80, 40, 80},
		{"ceil out 1:2 below", false, 1, 2, 40, 80, 40, 20, 40},
		{"ceil out 1:2 above", false, 1, 2, 40, 80, 100, 40, 80},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quality := QualityFromAmountsWithNumberContext(
				NewXRPEitherAmount(test.rateIn),
				NewXRPEitherAmount(test.rateOut),
				ctx,
			)
			input := NewXRPEitherAmount(test.inputIn)
			output := NewXRPEitherAmount(test.inputOut)
			limit := NewXRPEitherAmount(test.limit)
			wantIn := NewXRPEitherAmount(test.wantIn)
			wantOut := NewXRPEitherAmount(test.wantOut)

			if test.ceilIn {
				gotIn, gotOut := quality.CeilInWithNumberContext(input, output, limit, ctx)
				require.Equal(t, wantIn, gotIn)
				require.Equal(t, wantOut, gotOut)
				gotIn, gotOut = quality.CeilInStrictWithNumberContext(input, output, limit, true, ctx)
				require.Equal(t, wantIn, gotIn)
				require.Equal(t, wantOut, gotOut)
				return
			}

			gotIn, gotOut := quality.CeilOutStrictWithNumberContext(input, output, limit, true, ctx)
			require.Equal(t, wantIn, gotIn)
			require.Equal(t, wantOut, gotOut)
		})
	}
}

func TestQualityMPTokensV2UsesRulesNumberContext(t *testing.T) {
	var inputID, outputID [24]byte
	inputID[23] = 1
	outputID[23] = 2
	ctx := tx.NumberContextForRules(amendment.NewRules([][32]byte{amendment.FeatureMPTokensV2}))
	limit := NewMPTEitherAmount(1_000_000_000_000_000_000, inputID)
	input := NewMPTEitherAmount(2_000_000_000_000_000_000, inputID)
	output := NewMPTEitherAmount(2_000_000_000_000_000_000, outputID)
	outputLimit := NewMPTEitherAmount(1_000_000_000_000_000_000, outputID)

	gotIn, gotOut := qualityOne.CeilInWithNumberContext(input, output, limit, ctx)
	require.Equal(t, limit, gotIn)
	require.Equal(t, outputLimit, gotOut)

	gotIn, gotOut = qualityOne.CeilInStrictWithNumberContext(input, output, limit, false, ctx)
	require.Equal(t, limit, gotIn)
	require.Equal(t, outputLimit, gotOut)

	positiveRate := QualityFromAmountsWithNumberContext(
		NewXRPEitherAmount(2),
		NewXRPEitherAmount(1),
		ctx,
	)
	negativeRate := QualityFromAmountsWithNumberContext(
		NewXRPEitherAmount(-2),
		NewXRPEitherAmount(1),
		ctx,
	)
	require.Equal(t, positiveRate.Value, negativeRate.Value)

	quality := QualityFromAmountsWithNumberContext(
		NewXRPEitherAmount(2),
		NewXRPEitherAmount(1),
		ctx,
	)
	gotIn, gotOut = quality.CeilOutStrictWithNumberContext(input, output, outputLimit, false, ctx)
	require.Equal(t, input, gotIn)
	require.Equal(t, outputLimit, gotOut)

	legacy := tx.NumberContextForRules(amendment.EmptyRules())
	require.False(t, legacy.MPTokensV2Enabled())
	require.PanicsWithValue(t, "muldivRound overflow", func() {
		_, _ = qualityOne.CeilInStrictWithNumberContext(input, output, limit, false, legacy)
	})
}

func TestQualityFromAmountsUsesNumberContext(t *testing.T) {
	in := NewXRPEitherAmount(95_720_761_450_600_688)
	out := NewIOUEitherAmount(tx.NewIssuedAmount(1, 0, "USD", "rIssuer"))
	for _, test := range []struct {
		name     string
		ctx      state.NumberContext
		mantissa uint64
	}{
		{"legacy", state.NewNumberContext(state.MantissaScaleSmall, false), 8_874_667_928_649_482},
		{"universal", state.NewNumberContext(state.MantissaScaleSmall, true), 8_874_667_928_649_483},
		{"large", state.NewNumberContext(state.MantissaScaleLargeLegacy, true), 8_874_667_928_649_483},
		{"cleanup320", state.NewNumberContext(state.MantissaScaleLarge320, true), 8_874_667_928_649_483},
		{"cleanup330", state.NewNumberContext(state.MantissaScaleLarge330, true), 8_874_667_928_649_483},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, uint64(101)<<56|test.mantissa, QualityFromAmountsWithNumberContext(in, out, test.ctx).Value)
		})
	}
}

func TestQualityFromAmountsUnrepresentableRate(t *testing.T) {
	one := NewXRPEitherAmount(1)
	zero := NewXRPEitherAmount(0)
	minIOU := NewIOUEitherAmount(tx.NewIssuedAmount(1_000_000_000_000_000, -96, "USD", "rIssuer"))
	maxIOU := NewIOUEitherAmount(tx.NewIssuedAmount(9_999_999_999_999_999, 80, "USD", "rIssuer"))
	ctx := state.NewNumberContext(state.MantissaScaleSmall, true)
	for _, test := range []struct {
		name    string
		in, out EitherAmount
	}{
		{"zero input", zero, one},
		{"zero output", one, zero},
		{"underflow", minIOU, maxIOU},
		{"overflow", maxIOU, minIOU},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Zero(t, QualityFromAmountsWithNumberContext(test.in, test.out, ctx).Value)
		})
	}
}
