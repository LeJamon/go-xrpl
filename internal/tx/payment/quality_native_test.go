package payment

import (
	"testing"

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
