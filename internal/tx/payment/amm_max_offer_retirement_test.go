package payment

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestAMMMaxOfferRetiredBehavior(t *testing.T) {
	poolIn := state.NewIssuedAmountFromValue(100, 0, "USD", "issuer")
	for _, test := range []struct {
		name    string
		poolOut tx.Amount
		wantOut tx.Amount
	}{
		{"zero drops", state.NewXRPAmountFromInt(0), state.NewXRPAmountFromInt(0)},
		{"one drop", state.NewXRPAmountFromInt(1), state.NewXRPAmountFromInt(0)},
		{"two drops", state.NewXRPAmountFromInt(2), state.NewXRPAmountFromInt(1)},
		{"XRP cap", state.NewXRPAmountFromInt(100), state.NewXRPAmountFromInt(99)},
		{"IOU cap", state.NewIssuedAmountFromValue(100, 0, "EUR", "issuer"), state.NewIssuedAmountFromValue(99, 0, "EUR", "issuer")},
	} {
		t.Run(test.name, func(t *testing.T) {
			liq := &AMMLiquidity{ammContext: NewAMMContext([20]byte{}, false, legacyNumberMath().ctx)}
			offer := liq.safeMaxOffer(poolIn, test.poolOut)
			if test.wantOut.IsZero() {
				require.Nil(t, offer)
				return
			}
			require.NotNil(t, offer)
			require.Zero(t, offer.amountOut.Compare(test.wantOut))
			require.Positive(t, offer.amountIn.Signum())
			require.Less(t, offer.amountOut.Compare(test.poolOut), 0)
			require.Equal(t, QualityFromAmounts(toEitherAmt(poolIn), toEitherAmt(test.poolOut)), offer.quality)
			require.True(t, offer.CheckInvariant(offer.amountIn, offer.amountOut))
		})
	}
}

func TestAMMMaxOfferSuppressesInputOverflow(t *testing.T) {
	liq := &AMMLiquidity{ammContext: NewAMMContext([20]byte{}, false, legacyNumberMath().ctx)}
	poolIn := state.NewIssuedAmountFromValue(9_999_999_999_999_999, 80, "USD", "issuer")
	poolOut := state.NewXRPAmountFromInt(100)
	require.Nil(t, liq.safeMaxOffer(poolIn, poolOut))
}
