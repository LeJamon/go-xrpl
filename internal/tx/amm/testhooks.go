package amm

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// This file exposes a handful of unexported AMM internals so the
// internal/testing/amm package (a separate package, so export_test.go does not
// reach it) can assert against the exact math the transactors run. These shims
// are test-only; production code calls the unexported originals directly.

// ToIOUForCalcExported preserves the legacy test helper API while using the
// default large Number context.
func ToIOUForCalcExported(amt tx.Amount) tx.Amount {
	math := numberMath{ctx: state.NewNumberContext(state.MantissaScaleLarge)}
	return math.toAmount(math.fromAmount(amt), zeroIOU(), state.RoundToNearest)
}

// AMMAssetOutExported wraps ammAssetOut without fixAMMv1_3, computing the asset
// amount received for burning lpTokens.
func AMMAssetOutExported(assetBalance, lptBalance, lpTokens tx.Amount, tfee uint16) tx.Amount {
	math := numberMath{ctx: state.NewNumberContext(state.MantissaScaleLarge)}
	return ammAssetOut(math, assetBalance, lptBalance, lpTokens, tfee, false)
}

// IsOnlyLiquidityProviderExported wraps isOnlyLiquidityProvider.
func IsOnlyLiquidityProviderExported(view tx.LedgerView, lptCurrency string, ammAccountID, lpAccountID [20]byte) (bool, ter.Result) {
	return isOnlyLiquidityProvider(view, lptCurrency, ammAccountID, lpAccountID)
}
