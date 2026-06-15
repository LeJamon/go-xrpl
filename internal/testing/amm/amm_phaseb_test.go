// Package amm_test contains Phase B TER/behaviour-parity tests for AMM
// transactions (go-xrpl issue #889). Each test pins a specific rippled-parity
// fix in internal/tx/amm.
package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// badXRPCurrency is the 40-hex standard-form encoding of the letters "XRP",
// which rippled rejects via badCurrency() (UintTypes.cpp:135). The codec renders
// this 160-bit value as hex rather than the ISO "XRP" string.
const badXRPCurrency = "0000000000000000000000005852500000000000"

// Item 12: invalidAMMAsset badCurrency / badIssuer checks.
// Reference: rippled AMMCore.cpp invalidAMMAsset (lines 65-77).
func TestPhaseB_InvalidAMMAsset(t *testing.T) {
	// An asset using the bad "XRP" 160-bit currency code is temBAD_CURRENCY.
	t.Run("BadCurrency_Create", func(t *testing.T) {
		env := setupAMM(t)

		badAsset := tx.NewIssuedAmountFromFloat64(1000, badXRPCurrency, env.GW.Address)
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(1000), badAsset).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("AMMCreate with bad XRP currency must fail")
		}
		amm.ExpectTER(t, result, amm.TemBAD_CURRENCY)
	})

	// An XRP amount carrying a non-zero issuer is temBAD_ISSUER. AMMDeposit's
	// asset-pair validation runs invalidAMMAsset on both members.
	t.Run("BadIssuer_Deposit", func(t *testing.T) {
		env := setupAMM(t)

		// Asset is XRP but carries a (non-zero) issuer.
		badXRP := tx.Asset{Currency: "XRP", Issuer: env.GW.Address}
		depositTx := amm.AMMDeposit(env.Carol, badXRP, env.USD).
			Amount(amm.XRPAmount(1000)).
			SingleAsset().
			Build()
		result := env.Submit(depositTx)

		if result.Success {
			t.Fatal("AMMDeposit with XRP asset + issuer must fail")
		}
		amm.ExpectTER(t, result, amm.TemBAD_ISSUER)
	})

	// The bad XRP currency in a deposit Amount is temBAD_CURRENCY.
	t.Run("BadCurrency_DepositAmount", func(t *testing.T) {
		env := setupAMM(t)

		badAmt := tx.NewIssuedAmountFromFloat64(100, badXRPCurrency, env.GW.Address)
		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			Amount(badAmt).
			SingleAsset().
			Build()
		result := env.Submit(depositTx)

		if result.Success {
			t.Fatal("AMMDeposit with bad XRP currency amount must fail")
		}
		amm.ExpectTER(t, result, amm.TemBAD_CURRENCY)
	})
}

// Item 6: AMMDeposit LPTokenOut issue must match the AMM's LP token issue —
// both currency AND issuer. Reference: rippled AMMDeposit.cpp preclaim 343-349.
func TestPhaseB_DepositLPTokenOutWrongIssuer(t *testing.T) {
	env := setupAMM(t)

	// Correct LP token currency but a bogus issuer (the gateway, not the AMM
	// pseudo-account). This is temBAD_AMM_TOKENS now that issuer is compared.
	wrongIssuerLPT := amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)
	wrongIssuerLPT = tx.NewIssuedAmountFromFloat64(
		1000000, wrongIssuerLPT.Currency, env.GW.Address)

	depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
		LPTokenOut(wrongIssuerLPT).
		LPToken().
		Build()
	result := env.Submit(depositTx)

	if result.Success {
		t.Fatal("AMMDeposit with wrong LPTokenOut issuer must fail")
	}
	amm.ExpectTER(t, result, amm.TemBAD_AMM_TOKENS)
}

// Item 11: tfLPToken deposit minimums are compared against the POST-adjustment
// deposit amounts. A high Amount minimum that the proportional deposit cannot
// meet yields tecAMM_FAILED; a satisfiable minimum succeeds.
// Reference: rippled AMMDeposit.cpp deposit() lines 553-565.
func TestPhaseB_DepositLPTokenMinimums(t *testing.T) {
	// Requesting 1,000,000 of ~10,000,000 LP tokens deposits ~1000 USD; a
	// USD(2000) minimum is not met → tecAMM_FAILED.
	t.Run("MinimumNotMet", func(t *testing.T) {
		env := setupAMM(t)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			Amount(amm.XRPAmount(2000)).
			Amount2(amm.IOUAmount(env.GW, "USD", 2000)).
			LPToken().
			Build()
		result := env.Submit(depositTx)

		if result.Success {
			t.Fatal("tfLPToken deposit with unmet minimum must fail tecAMM_FAILED")
		}
		amm.ExpectTER(t, result, amm.TecAMM_FAILED)
	})

	// A minimum below the proportional deposit succeeds.
	t.Run("MinimumMet", func(t *testing.T) {
		env := setupAMM(t)

		depositTx := amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			Amount(amm.XRPAmount(500)).
			Amount2(amm.IOUAmount(env.GW, "USD", 500)).
			LPToken().
			Build()
		result := env.Submit(depositTx)

		if !result.Success {
			t.Fatalf("tfLPToken deposit with met minimum should succeed, got %s", result.Code)
		}
	})
}

// Item 10: AMMBid duplicate / self AuthAccounts rejection (gated on fixAMMv1_3,
// which the test env enables by default). Reference: rippled AMMBid.cpp 81-95.
func TestPhaseB_BidAuthAccounts(t *testing.T) {
	makeLP := func(env *amm.AMMTestEnv, acc *jtx.Account) {
		dep := amm.AMMDeposit(acc, amm.XRP(), env.USD).
			LPTokenOut(amm.LPTokenAmount(env, amm.XRP(), env.USD, 1000000)).
			LPToken().
			Build()
		if r := env.Submit(dep); !r.Success {
			t.Fatalf("deposit failed: %s", r.Code)
		}
		env.Close()
	}

	t.Run("Duplicate", func(t *testing.T) {
		env := setupAMM(t)
		makeLP(env, env.Carol)

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			AuthAccounts(env.Bob.Address, env.Bob.Address).
			Build()
		result := env.Submit(bidTx)

		if result.Success {
			t.Fatal("Bid with duplicate AuthAccounts must fail")
		}
		amm.ExpectTER(t, result, amm.TemMALFORMED)
	})

	t.Run("Self", func(t *testing.T) {
		env := setupAMM(t)
		makeLP(env, env.Carol)

		bidTx := amm.AMMBid(env.Carol, amm.XRP(), env.USD).
			BidMin(amm.LPTokenAmount(env, amm.XRP(), env.USD, 100)).
			AuthAccounts(env.Carol.Address).
			Build()
		result := env.Submit(bidTx)

		if result.Success {
			t.Fatal("Bid authorizing self must fail")
		}
		amm.ExpectTER(t, result, amm.TemMALFORMED)
	})
}

// Item 15: AMMClawback must NOT reject on a (non-rippled) empty-currency check;
// the only rippled malformed checks are holder==issuer, isXRP(asset), the
// tfClawTwoAssets issuer match, and the asset-issuer-must-be-Account rule.
// Reference: rippled AMMClawback.cpp preflight lines 36-92.
func TestPhaseB_ClawbackPreflight(t *testing.T) {
	// holder == issuer is temMALFORMED.
	t.Run("HolderEqualsIssuer", func(t *testing.T) {
		env := setupAMM(t)
		clawTx := amm.AMMClawback(env.GW, env.GW.Address, env.USD, amm.XRP()).Build()
		result := env.Submit(clawTx)
		if result.Success {
			t.Fatal("clawback with holder==issuer must fail")
		}
		amm.ExpectTER(t, result, amm.TemMALFORMED)
	})

	// Asset being XRP is temMALFORMED (asset must be an issued currency).
	t.Run("AssetIsXRP", func(t *testing.T) {
		env := setupAMM(t)
		clawTx := amm.AMMClawback(env.GW, env.Carol.Address, amm.XRP(), env.USD).Build()
		result := env.Submit(clawTx)
		if result.Success {
			t.Fatal("clawback with XRP asset must fail")
		}
		amm.ExpectTER(t, result, amm.TemMALFORMED)
	})

	// Asset issuer not matching Account is temMALFORMED (not the removed
	// empty-currency temMALFORMED — exercised via a non-issuer Account).
	t.Run("AssetIssuerMismatch", func(t *testing.T) {
		env := setupAMM(t)
		// Alice is not the USD issuer; asset.issuer (gw) != Account (alice).
		clawTx := amm.AMMClawback(env.Alice, env.Carol.Address, env.USD, amm.XRP()).Build()
		result := env.Submit(clawTx)
		if result.Success {
			t.Fatal("clawback with asset issuer != Account must fail")
		}
		amm.ExpectTER(t, result, amm.TemMALFORMED)
	})
}
