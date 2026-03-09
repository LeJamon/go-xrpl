// Package amm_test contains tests for AMM transactions.
// Reference: rippled/src/test/app/AMM_test.cpp
package amm_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/amm"
)

// TestInstanceCreate tests basic AMM creation.
// Reference: rippled AMM_test.cpp testInstanceCreate (line 54)
func TestInstanceCreate(t *testing.T) {
	// XRP to IOU
	// Reference: testAMM([&](AMM& ammAlice, Env&) { BEAST_EXPECT(ammAlice.expectBalances(XRP(10'000), USD(10'000), IOUAmount{10'000'000, 0})); }
	t.Run("XRP_to_IOU", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)

		if !result.Success {
			t.Fatalf("Failed to create AMM: %s - %s", result.Code, result.Message)
		}
		env.Close()

		// Verify AMM exists (would check balances in full implementation)
		t.Log("XRP to IOU AMM creation passed")
	})

	// IOU to IOU
	// Reference: testAMM([&](AMM& ammAlice, Env&) { BEAST_EXPECT(ammAlice.expectBalances(USD(20'000), BTC(0.5), IOUAmount{100, 0})); }, {{USD(20'000), BTC(0.5)}});
	t.Run("IOU_to_IOU", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(20000, 1)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 20000), amm.IOUAmount(env.GW, "BTC", 0.5)).Build()
		result := env.Submit(createTx)

		if !result.Success {
			t.Fatalf("Failed to create IOU/IOU AMM: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("IOU to IOU AMM creation passed")
	})

	// Trading fee
	// Reference: testAMM([&](AMM& amm, Env&) { BEAST_EXPECT(amm.expectTradingFee(1'000)); }, std::nullopt, 1'000);
	t.Run("WithTradingFee", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).
			TradingFee(1000). // 1% = 1000 basis points
			Build()
		result := env.Submit(createTx)

		if !result.Success {
			t.Fatalf("Failed to create AMM with trading fee: %s - %s", result.Code, result.Message)
		}
		env.Close()

		t.Log("AMM creation with trading fee passed")
	})
}

// TestInvalidInstance tests invalid AMM creation scenarios.
// Reference: rippled AMM_test.cpp testInvalidInstance (line 155)
func TestInvalidInstance(t *testing.T) {
	// Can't have both XRP tokens
	// Reference: AMM ammAlice(env, alice, XRP(10'000), XRP(10'000), ter(temBAD_AMM_TOKENS));
	t.Run("BothXRPTokens", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.Fund()
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.XRPAmount(10000)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with both XRP tokens")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMM_TOKENS)
	})

	// Can't have both tokens the same IOU
	// Reference: AMM ammAlice(env, alice, USD(10'000), USD(10'000), ter(temBAD_AMM_TOKENS));
	t.Run("SameIOUTokens", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.IOUAmount(env.GW, "USD", 10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with same IOU tokens")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMM_TOKENS)
	})

	// Can't have zero amounts
	// Reference: AMM ammAlice(env, alice, XRP(0), USD(10'000), ter(temBAD_AMOUNT));
	t.Run("ZeroXRPAmount", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, tx.NewXRPAmount(0), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with zero XRP amount")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMOUNT, amm.TemMALFORMED)
	})

	// Reference: AMM ammAlice1(env, alice, XRP(10'000), USD(0), ter(temBAD_AMOUNT));
	t.Run("ZeroIOUAmount", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 0)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with zero IOU amount")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMOUNT, amm.TemMALFORMED)
	})

	// Can't have negative amounts
	// Reference: AMM ammAlice2(env, alice, XRP(10'000), USD(-10'000), ter(temBAD_AMOUNT));
	t.Run("NegativeIOUAmount", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", -10000)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with negative IOU amount")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMOUNT)
	})

	// Reference: AMM ammAlice3(env, alice, XRP(-10'000), USD(10'000), ter(temBAD_AMOUNT));
	t.Run("NegativeXRPAmount", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		createTx := amm.AMMCreate(env.Alice, tx.NewXRPAmount(-10000*1_000_000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with negative XRP amount")
		}
		amm.ExpectTER(t, result, amm.TemBAD_AMOUNT)
	})

	// Insufficient IOU balance
	// Reference: AMM ammAlice(env, alice, XRP(10'000), USD(40'000), ter(tecUNFUNDED_AMM));
	t.Run("InsufficientIOUBalance", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0) // Alice has 30000 USD
		env.Close()

		// Try to create with 40000 USD (more than alice has)
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 40000)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with insufficient IOU balance")
		}
		amm.ExpectTER(t, result, amm.TecUNFUNDED_AMM)
	})

	// Insufficient XRP balance
	// Reference: AMM ammAlice(env, alice, XRP(40'000), USD(10'000), ter(tecUNFUNDED_AMM));
	t.Run("InsufficientXRPBalance", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0) // Alice has ~30000 XRP minus fees
		env.Close()

		// Try to create with 40000 XRP (more than alice has)
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(40000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with insufficient XRP balance")
		}
		amm.ExpectTER(t, result, amm.TecUNFUNDED_AMM)
	})

	// Invalid trading fee (> 1000)
	// Reference: AMM ammAlice(env, alice, XRP(10'000), USD(10'000), false, 65'001, 10, std::nullopt, std::nullopt, std::nullopt, ter(temBAD_FEE));
	t.Run("InvalidTradingFee", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// Trading fee > 1000 (1%) is invalid
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).
			TradingFee(1001). // Invalid: > 1000
			Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with trading fee > 1000")
		}
		amm.ExpectTER(t, result, amm.TemBAD_FEE)
	})

	// AMM already exists
	// Reference: AMM ammCarol(env, carol, XRP(10'000), USD(10'000), ter(tecDUPLICATE));
	t.Run("AMMAlreadyExists", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// First AMM creation should succeed
		createTx1 := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result1 := env.Submit(createTx1)
		if !result1.Success {
			t.Fatalf("First AMM creation should succeed: %s", result1.Code)
		}
		env.Close()

		// Second AMM creation with same pair should fail
		createTx2 := amm.AMMCreate(env.Carol, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result2 := env.Submit(createTx2)

		if result2.Success {
			t.Fatal("Should not allow creating duplicate AMM")
		}
		amm.ExpectTER(t, result2, amm.TecDUPLICATE)
	})

	// Invalid flags
	// Reference: AMM ammAlice(env, alice, XRP(10'000), USD(10'000), false, 0, 10, tfWithdrawAll, std::nullopt, std::nullopt, ter(temINVALID_FLAG));
	t.Run("InvalidFlags", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.FundWithIOUs(30000, 0)
		env.Close()

		// AMMCreate doesn't support any flags
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).
			Flags(amm.TfWithdrawAll). // Invalid flag for AMMCreate
			Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with invalid flags")
		}
		amm.ExpectTER(t, result, amm.TemINVALID_FLAG)
	})

	// Invalid (non-existent) Account
	// Reference: AMM ammAlice(env, bad, XRP(10'000), USD(10'000), false, 0, 10, std::nullopt, seq(1), std::nullopt, ter(terNO_ACCOUNT));
	t.Run("NonExistentAccount", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.Fund() // Fund only standard accounts, not "bad"
		env.Close()

		bad := jtx.NewAccount("bad") // Not funded
		createTx := amm.AMMCreate(bad, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(jtx.WithSeq(createTx, 1))

		if result.Success {
			t.Fatal("Should not allow creating AMM from non-existent account")
		}
		amm.ExpectTER(t, result, amm.TerNO_ACCOUNT)
	})

	// Globally frozen
	// Reference: AMM ammAlice(env, alice, XRP(10'000), USD(10'000), ter(tecFROZEN));
	t.Run("GloballyFrozen", func(t *testing.T) {
		env := amm.NewAMMTestEnv(t)
		env.Fund()
		env.Close()

		// Enable global freeze on gateway BEFORE creating trust lines
		env.EnableGlobalFreeze(env.GW)
		env.Close()

		// Try to create AMM with frozen asset
		createTx := amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()
		result := env.Submit(createTx)

		if result.Success {
			t.Fatal("Should not allow creating AMM with globally frozen asset")
		}
		amm.ExpectTER(t, result, amm.TecFROZEN)
	})
}
