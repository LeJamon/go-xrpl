package amm

import "github.com/LeJamon/go-xrpl/internal/tx"

// This file exposes a handful of unexported AMM internals so the
// internal/testing/amm package (a separate package, so export_test.go does not
// reach it) can assert against the exact math the transactors run. These shims
// are test-only; production code calls the unexported originals directly.

// ToIOUForCalcExported wraps toIOUForCalc.
func ToIOUForCalcExported(amt tx.Amount) tx.Amount {
	return toIOUForCalc(amt)
}

// AMMAssetOutExported wraps ammAssetOut without fixAMMv1_3, computing the asset
// amount received for burning lpTokens.
func AMMAssetOutExported(assetBalance, lptBalance, lpTokens tx.Amount, tfee uint16) tx.Amount {
	return ammAssetOut(assetBalance, lptBalance, lpTokens, tfee, false)
}

// IsOnlyLiquidityProviderExported wraps isOnlyLiquidityProvider.
func IsOnlyLiquidityProviderExported(view tx.LedgerView, lptCurrency string, ammAccountID, lpAccountID [20]byte) (bool, tx.Result) {
	return isOnlyLiquidityProvider(view, lptCurrency, ammAccountID, lpAccountID)
}
