package amm

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// checkAMMPrecisionLoss applies the transaction-layer AMM precision check.
// It must run against the post-transfer pool balances before the AMM entry is
// persisted. The invariant checker uses the same strong/weak comparison.
func checkAMMPrecisionLoss(
	amount1, amount2, newLPTokenBalance tx.Amount,
	ctx state.NumberContext,
) ter.Result {
	if newLPTokenBalance.Signum() <= 0 {
		return ter.TesSUCCESS
	}

	amount1Number := ctx.FromAmount(amount1, state.RoundToNearest)
	amount2Number := ctx.FromAmount(amount2, state.RoundToNearest)
	poolProductMean := amount1Number.MulRounded(amount2Number, state.RoundToNearest).Root2Rounded(state.RoundToNearest)
	newLPNumber := ctx.FromAmount(newLPTokenBalance, state.RoundToNearest)

	if poolProductMean.Cmp(newLPNumber) >= 0 || withinAMMRelativeDistance(ctx, poolProductMean, newLPNumber) {
		return ter.TesSUCCESS
	}
	return ter.TecPRECISION_LOSS
}

func withinAMMRelativeDistance(ctx state.NumberContext, calculated, requested state.XRPLNumber) bool {
	if calculated.Equal(requested) {
		return true
	}

	minNumber, maxNumber := calculated, requested
	if calculated.Cmp(requested) > 0 {
		minNumber, maxNumber = requested, calculated
	}
	difference := maxNumber.AddRounded(minNumber.Negate(), state.RoundToNearest)
	ratio := difference.DivRounded(maxNumber, state.RoundToNearest)
	return ratio.Cmp(ctx.Number(1, -11, state.RoundToNearest)) < 0
}

func addAMMPoolAmount(
	amount, delta tx.Amount,
	ctx state.NumberContext,
) (tx.Amount, error) {
	return amount.AddWithNumberContext(delta, ctx, state.RoundToNearest)
}

func subtractAMMPoolAmount(
	amount, delta tx.Amount,
	ctx state.NumberContext,
) (tx.Amount, error) {
	return amount.SubWithNumberContext(delta, ctx, state.RoundToNearest)
}
