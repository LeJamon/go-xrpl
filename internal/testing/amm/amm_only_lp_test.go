package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
)

// TestIsOnlyLiquidityProvider_DustSecondLP verifies the structural single-LP
// detection: a holder is the only provider only when the AMM owner directory
// contains exactly one LPToken trust line. A second LP holding a dust fraction
// of the pool still owns an LPToken trust line, so the big LP is NOT the only
// provider — even though a value-distance heuristic would call them so.
func TestIsOnlyLiquidityProvider_DustSecondLP(t *testing.T) {
	env := setupAMM(t)

	ammData := env.ReadAMMData(amm.XRP(), env.USD)
	if ammData == nil {
		t.Fatal("AMM not found")
	}
	lptCurrency := coreAmm.GenerateAMMLPTCurrency(amm.XRP().Currency, env.USD.Currency)

	// Alice is the sole LP at this point.
	if only, res := coreAmm.IsOnlyLiquidityProviderExported(
		env.Ledger(), lptCurrency, ammData.Account, env.Alice.ID); res != 0 || !only {
		t.Fatalf("Alice should be the only LP before Carol deposits (only=%v res=%v)", only, res)
	}

	// Carol deposits a dust amount (~0.05% of the ~10,000,000-token pool).
	depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
		LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 5000)).
		LPToken().
		Build()
	if r := env.Submit(depositTx); !r.Success {
		t.Fatalf("Carol dust deposit failed: %s - %s", r.Code, r.Message)
	}
	env.Close()

	// With a second LPToken trust line in the directory, the big LP is no longer
	// the only provider.
	only, res := coreAmm.IsOnlyLiquidityProviderExported(
		env.Ledger(), lptCurrency, ammData.Account, env.Alice.ID)
	if res != 0 {
		t.Fatalf("isOnlyLiquidityProvider returned error result %v", res)
	}
	if only {
		t.Fatal("big LP must not be the only liquidity provider when a dust second LP holds tokens")
	}

	// Sanity: Carol, the dust LP, is also not the only provider.
	if only, res := coreAmm.IsOnlyLiquidityProviderExported(
		env.Ledger(), lptCurrency, ammData.Account, env.Carol.ID); res != 0 || only {
		t.Fatalf("dust LP must not be the only provider either (only=%v res=%v)", only, res)
	}
}
