package offer

// Offer fill mode tests (FillOrKill, ImmediateOrCancel, Passive).
// Reference: rippled/src/test/app/Offer_test.cpp - testFillModes (lines 833-1065)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_FillModes tests FillOrKill, ImmediateOrCancel, and Passive offer modes.
// Reference: rippled Offer_test.cpp testFillModes (lines 833-1065)
func TestOffer_FillModes(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testFillModes(t, fs.disabled)
		})
	}
}

func testFillModes(t *testing.T, disabledFeatures []string) {
	startBalance := uint64(jtx.XRP(1000000))

	// =========================================================================
	// Fill or Kill - unless we fully cross, just charge a fee and don't place
	// the offer on the books. But also clean up expired offers.
	// fix1578 changes the return code. Verify expected behavior.
	// =========================================================================
	t.Run("FillOrKill", func(t *testing.T) {
		// Inner loop: test with fix1578 disabled and enabled.
		for _, fix1578Name := range []string{"fix1578_disabled", "fix1578_enabled"} {
			t.Run(fix1578Name, func(t *testing.T) {
				env := newEnvWithFeatures(t, disabledFeatures)

				// Tweak fix1578 for this sub-test.
				if fix1578Name == "fix1578_disabled" {
					env.DisableFeature("fix1578")
				} else {
					env.EnableFeature("fix1578")
				}

				fix1578Enabled := env.FeatureEnabled("fix1578")

				gw := jtx.NewAccount("gateway")
				alice := jtx.NewAccount("alice")
				bob := jtx.NewAccount("bob")

				USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

				f := env.BaseFee()

				env.FundAmount(gw, startBalance)
				env.FundAmount(alice, startBalance)
				env.FundAmount(bob, startBalance)
				env.Close()

				// bob creates an offer that expires before the next ledger close.
				result := env.Submit(
					OfferCreate(bob, USD(500), jtx.XRPTxAmountFromXRP(500)).
						Expiration(LastClose(env) + 1).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()
				jtx.RequireOwnerCount(t, env, bob, 1)
				RequireOfferCount(t, env, bob, 1)

				// bob creates the offer that will be crossed.
				result = env.Submit(
					OfferCreate(bob, USD(500), jtx.XRPTxAmountFromXRP(500)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()
				jtx.RequireOwnerCount(t, env, bob, 2)
				RequireOfferCount(t, env, bob, 2)

				env.Trust(alice, USD(1000))
				result = env.Submit(payment.PayIssued(gw, alice, USD(1000)).Build())
				jtx.RequireTxSuccess(t, result)

				// Order that can't be filled but will remove bob's expired offer:
				{
					offerTx := OfferCreate(alice, jtx.XRPTxAmountFromXRP(1000), USD(1000)).
						FillOrKill().Build()
					result = env.Submit(offerTx)
					if fix1578Enabled {
						jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
					} else {
						jtx.RequireTxSuccess(t, result)
					}
				}
				jtx.RequireBalance(t, env, alice, startBalance-(f*2))
				jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
				jtx.RequireOwnerCount(t, env, alice, 1)
				RequireOfferCount(t, env, alice, 0)

				jtx.RequireBalance(t, env, bob, startBalance-(f*2))
				jtx.RequireIOUBalance(t, env, bob, gw, "USD", 0)
				jtx.RequireOwnerCount(t, env, bob, 1)
				RequireOfferCount(t, env, bob, 1)

				// Order that can be filled
				result = env.Submit(
					OfferCreate(alice, jtx.XRPTxAmountFromXRP(500), USD(500)).
						FillOrKill().Build())
				jtx.RequireTxSuccess(t, result)

				jtx.RequireBalance(t, env, alice, startBalance-(f*3)+uint64(jtx.XRP(500)))
				jtx.RequireIOUBalance(t, env, alice, gw, "USD", 500)
				jtx.RequireOwnerCount(t, env, alice, 1)
				RequireOfferCount(t, env, alice, 0)

				jtx.RequireBalance(t, env, bob, startBalance-(f*2)-uint64(jtx.XRP(500)))
				jtx.RequireIOUBalance(t, env, bob, gw, "USD", 500)
				jtx.RequireOwnerCount(t, env, bob, 1)
				RequireOfferCount(t, env, bob, 0)
			})
		}
	})

	// =========================================================================
	// Immediate or Cancel - cross as much as possible and add nothing on books.
	// =========================================================================
	t.Run("ImmediateOrCancel", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

		f := env.BaseFee()

		env.FundAmount(gw, startBalance)
		env.FundAmount(alice, startBalance)
		env.FundAmount(bob, startBalance)
		env.Close()

		env.Trust(alice, USD(1000))
		result := env.Submit(payment.PayIssued(gw, alice, USD(1000)).Build())
		jtx.RequireTxSuccess(t, result)

		// No cross:
		{
			iocEnabled := featureEnabled(disabledFeatures, "ImmediateOfferKilled")
			offerTx := OfferCreate(alice, jtx.XRPTxAmountFromXRP(1000), USD(1000)).
				ImmediateOrCancel().Build()
			result = env.Submit(offerTx)
			if iocEnabled {
				jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
			} else {
				jtx.RequireTxSuccess(t, result)
			}
		}
		jtx.RequireBalance(t, env, alice, startBalance-f-f)
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
		jtx.RequireOwnerCount(t, env, alice, 1)
		RequireOfferCount(t, env, alice, 0)

		// Partially cross:
		result = env.Submit(
			OfferCreate(bob, USD(50), jtx.XRPTxAmountFromXRP(50)).Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(
			OfferCreate(alice, jtx.XRPTxAmountFromXRP(1000), USD(1000)).
				ImmediateOrCancel().Build())
		jtx.RequireTxSuccess(t, result)

		jtx.RequireBalance(t, env, alice, startBalance-f-f-f+uint64(jtx.XRP(50)))
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 950)
		jtx.RequireOwnerCount(t, env, alice, 1)
		RequireOfferCount(t, env, alice, 0)

		jtx.RequireBalance(t, env, bob, startBalance-f-uint64(jtx.XRP(50)))
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 50)
		jtx.RequireOwnerCount(t, env, bob, 1)
		RequireOfferCount(t, env, bob, 0)

		// Fully cross:
		result = env.Submit(
			OfferCreate(bob, USD(50), jtx.XRPTxAmountFromXRP(50)).Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(
			OfferCreate(alice, jtx.XRPTxAmountFromXRP(50), USD(50)).
				ImmediateOrCancel().Build())
		jtx.RequireTxSuccess(t, result)

		jtx.RequireBalance(t, env, alice, startBalance-f-f-f-f+uint64(jtx.XRP(100)))
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 900)
		jtx.RequireOwnerCount(t, env, alice, 1)
		RequireOfferCount(t, env, alice, 0)

		jtx.RequireBalance(t, env, bob, startBalance-f-f-uint64(jtx.XRP(100)))
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 100)
		jtx.RequireOwnerCount(t, env, bob, 1)
		RequireOfferCount(t, env, bob, 0)
	})

	// =========================================================================
	// tfPassive -- place the offer without crossing it.
	// =========================================================================
	t.Run("Passive", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

		env.FundAmount(gw, startBalance)
		env.FundAmount(alice, startBalance)
		env.FundAmount(bob, startBalance)
		env.Close()

		env.Trust(bob, USD(1000))
		env.Close()
		result := env.Submit(payment.PayIssued(gw, bob, USD(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(
			OfferCreate(alice, USD(1000), jtx.XRPTxAmountFromXRP(2000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		aliceOffers := OffersOnAccount(env, alice)
		require.Equal(t, 1, len(aliceOffers))
		for _, offer := range aliceOffers {
			require.True(t, amountsEqual(offer.TakerGets, jtx.XRPTxAmountFromXRP(2000)),
				"Expected TakerGets to be XRP(2000)")
			require.True(t, amountsEqual(offer.TakerPays, USD(1000)),
				"Expected TakerPays to be USD(1000)")
		}

		// bob creates a passive offer that could cross alice's. bob's stays.
		result = env.Submit(
			OfferCreate(bob, jtx.XRPTxAmountFromXRP(2000), USD(1000)).
				Passive().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		RequireOfferCount(t, env, alice, 1)

		bobOffers := OffersOnAccount(env, bob)
		require.Equal(t, 1, len(bobOffers))
		for _, offer := range bobOffers {
			require.True(t, amountsEqual(offer.TakerGets, USD(1000)),
				"Expected TakerGets to be USD(1000)")
			require.True(t, amountsEqual(offer.TakerPays, jtx.XRPTxAmountFromXRP(2000)),
				"Expected TakerPays to be XRP(2000)")
		}

		// gw can cross both.
		result = env.Submit(
			OfferCreate(gw, jtx.XRPTxAmountFromXRP(2000), USD(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		RequireOfferCount(t, env, alice, 0)
		RequireOfferCount(t, env, gw, 0)
		RequireOfferCount(t, env, bob, 1)

		result = env.Submit(
			OfferCreate(gw, USD(1000), jtx.XRPTxAmountFromXRP(2000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		RequireOfferCount(t, env, bob, 0)
		RequireOfferCount(t, env, gw, 0)
	})

	// =========================================================================
	// tfPassive -- cross only offers of better quality.
	// =========================================================================
	t.Run("PassiveBetterQuality", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

		env.FundAmount(gw, startBalance)
		env.FundAmount(alice, startBalance)
		env.FundAmount(bob, startBalance)
		env.Close()

		env.Trust(bob, USD(1000))
		env.Close()
		result := env.Submit(payment.PayIssued(gw, bob, USD(1000)).Build())
		jtx.RequireTxSuccess(t, result)

		// alice creates two offers at different quality levels.
		// offer1: USD(500) for XRP(1001) -- worse quality (more XRP per USD)
		result = env.Submit(
			OfferCreate(alice, USD(500), jtx.XRPTxAmountFromXRP(1001)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// offer2: USD(500) for XRP(1000) -- better quality (less XRP per USD)
		result = env.Submit(
			OfferCreate(alice, USD(500), jtx.XRPTxAmountFromXRP(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		aliceOffers := OffersOnAccount(env, alice)
		require.Equal(t, 2, len(aliceOffers))

		// bob's passive offer crosses one of alice's (better quality) and leaves other.
		// bob wants XRP(2000) and offers USD(1000).
		// Quality is 2000/1000 = 2.0 XRP/USD.
		// alice's offer1: 1001/500 = 2.002 XRP/USD (better for bob, worse quality from alice)
		// alice's offer2: 1000/500 = 2.0 XRP/USD (equal quality -- passive won't cross equal)
		// Passive crosses only strictly better quality, so it crosses offer1 only.
		result = env.Submit(
			OfferCreate(bob, jtx.XRPTxAmountFromXRP(2000), USD(1000)).
				Passive().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		RequireOfferCount(t, env, alice, 1)

		bobOffers := OffersOnAccount(env, bob)
		require.Equal(t, 1, len(bobOffers))
		for _, offer := range bobOffers {
			// bob's remaining offer should be partially filled.
			// bob offered USD(1000) for XRP(2000).
			// Crossed alice's offer1 which wanted USD(500) for XRP(1001).
			// bob paid USD(500) to alice and received XRP(1001).
			// bob's remaining: USD(1000-500.5) = USD(499.5) for XRP(2000-1001) = XRP(999).
			// (500.5 because at alice's quality ratio, 1001 XRP costs 500.5 USD)
			require.True(t, amountsEqual(offer.TakerGets, USD(499.5)),
				"Expected TakerGets to be USD(499.5), got %v", offer.TakerGets)
			require.True(t, amountsEqual(offer.TakerPays, jtx.XRPTxAmountFromXRP(999)),
				"Expected TakerPays to be XRP(999), got %v", offer.TakerPays)
		}
	})
}

// TestOffer_FillOrKill tests the fixFillOrKill amendment behavior with complex
// balance tracking across XRP, USD, and EUR currency pairs.
// Reference: rippled Offer_test.cpp testFillOrKill (lines 5113-5298)
func TestOffer_FillOrKill(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testFillOrKill(t, fs.disabled)
		})
	}
}

func testFillOrKill(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	issuer := jtx.NewAccount("issuer")
	maker := jtx.NewAccount("maker")
	taker := jtx.NewAccount("taker")

	USD := func(amount float64) tx.Amount { return jtx.USD(issuer, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(issuer, amount) }

	env.FundAmount(issuer, uint64(jtx.XRP(1000)))
	env.FundAmount(maker, uint64(jtx.XRP(1000)))
	env.FundAmount(taker, uint64(jtx.XRP(1000)))
	env.Close()

	env.Trust(maker, USD(1000))
	env.Trust(taker, USD(1000))
	env.Trust(maker, EUR(1000))
	env.Trust(taker, EUR(1000))
	env.Close()

	result := env.Submit(payment.PayIssued(issuer, maker, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(issuer, taker, USD(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(issuer, maker, EUR(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Track running balances
	makerUSDBalance := env.BalanceIOU(maker, "USD", issuer)
	takerUSDBalance := env.BalanceIOU(taker, "USD", issuer)
	makerEURBalance := env.BalanceIOU(maker, "EUR", issuer)
	takerEURBalance := env.BalanceIOU(taker, "EUR", issuer)
	makerXRPBalance := env.Balance(maker)
	takerXRPBalance := env.Balance(taker)

	fee := env.BaseFee()

	fixFillOrKillEnabled := env.FeatureEnabled("fixFillOrKill")

	// =========================================================================
	// Section 1: tfFillOrKill, TakerPays must be filled
	// fixFillOrKill changes behavior: with amendment, taker gets 100 even when
	// asking for 101. Without amendment, the offer is killed.
	// =========================================================================
	{
		var expectedCode string
		if fixFillOrKillEnabled {
			expectedCode = jtx.TesSUCCESS
		} else {
			expectedCode = jtx.TecKILLED
		}

		// Sub-test 1a: maker offers XRP(100) for USD(100), taker FoK: USD(100) for XRP(101)
		result = env.Submit(OfferCreate(maker, jtx.XRPTxAmountFromXRP(100), USD(100)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(taker, USD(100), jtx.XRPTxAmountFromXRP(101)).FillOrKill().Build())
		if expectedCode == jtx.TesSUCCESS {
			jtx.RequireTxSuccess(t, result)
		} else {
			jtx.RequireTxClaimed(t, result, expectedCode)
		}
		env.Close()

		makerXRPBalance -= fee
		takerXRPBalance -= fee
		if expectedCode == jtx.TesSUCCESS {
			makerUSDBalance -= 100
			takerUSDBalance += 100
			makerXRPBalance += uint64(jtx.XRP(100))
			takerXRPBalance -= uint64(jtx.XRP(100))
		}
		RequireOfferCount(t, env, taker, 0)

		// Sub-test 1b: maker offers USD(100) for XRP(100), taker FoK: XRP(100) for USD(101)
		result = env.Submit(OfferCreate(maker, USD(100), jtx.XRPTxAmountFromXRP(100)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(taker, jtx.XRPTxAmountFromXRP(100), USD(101)).FillOrKill().Build())
		if expectedCode == jtx.TesSUCCESS {
			jtx.RequireTxSuccess(t, result)
		} else {
			jtx.RequireTxClaimed(t, result, expectedCode)
		}
		env.Close()

		makerXRPBalance -= fee
		takerXRPBalance -= fee
		if expectedCode == jtx.TesSUCCESS {
			makerUSDBalance += 100
			takerUSDBalance -= 100
			makerXRPBalance -= uint64(jtx.XRP(100))
			takerXRPBalance += uint64(jtx.XRP(100))
		}
		RequireOfferCount(t, env, taker, 0)

		// Sub-test 1c: maker offers USD(100) for EUR(100), taker FoK: EUR(100) for USD(101)
		result = env.Submit(OfferCreate(maker, USD(100), EUR(100)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(taker, EUR(100), USD(101)).FillOrKill().Build())
		if expectedCode == jtx.TesSUCCESS {
			jtx.RequireTxSuccess(t, result)
		} else {
			jtx.RequireTxClaimed(t, result, expectedCode)
		}
		env.Close()

		makerXRPBalance -= fee
		takerXRPBalance -= fee
		if expectedCode == jtx.TesSUCCESS {
			makerUSDBalance += 100
			takerUSDBalance -= 100
			makerEURBalance -= 100
			takerEURBalance += 100
		}
		RequireOfferCount(t, env, taker, 0)
	}

	// =========================================================================
	// Section 2: tfFillOrKill + tfSell, TakerGets must be filled
	// These always succeed regardless of fixFillOrKill (full cross at 101).
	// =========================================================================
	{
		// Sub-test 2a: maker offers XRP(101) for USD(101),
		// taker FoK+Sell: USD(100) for XRP(101) -> always succeeds
		result = env.Submit(OfferCreate(maker, jtx.XRPTxAmountFromXRP(101), USD(101)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(taker, USD(100), jtx.XRPTxAmountFromXRP(101)).FillOrKill().Sell().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		makerUSDBalance -= 101
		takerUSDBalance += 101
		makerXRPBalance += uint64(jtx.XRP(101)) - fee
		takerXRPBalance -= uint64(jtx.XRP(101)) + fee
		RequireOfferCount(t, env, taker, 0)

		// Sub-test 2b: maker offers USD(101) for XRP(101),
		// taker FoK+Sell: XRP(100) for USD(101) -> always succeeds
		result = env.Submit(OfferCreate(maker, USD(101), jtx.XRPTxAmountFromXRP(101)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(taker, jtx.XRPTxAmountFromXRP(100), USD(101)).FillOrKill().Sell().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		makerUSDBalance += 101
		takerUSDBalance -= 101
		makerXRPBalance -= uint64(jtx.XRP(101)) + fee
		takerXRPBalance += uint64(jtx.XRP(101)) - fee
		RequireOfferCount(t, env, taker, 0)

		// Sub-test 2c: maker offers USD(101) for EUR(101),
		// taker FoK+Sell: EUR(100) for USD(101) -> always succeeds
		result = env.Submit(OfferCreate(maker, USD(101), EUR(101)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(OfferCreate(taker, EUR(100), USD(101)).FillOrKill().Sell().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		makerUSDBalance += 101
		takerUSDBalance -= 101
		makerEURBalance -= 101
		takerEURBalance += 101
		makerXRPBalance -= fee
		takerXRPBalance -= fee
		RequireOfferCount(t, env, taker, 0)
	}

	// =========================================================================
	// Section 3: Fail regardless of fixFillOrKill amendment
	// These fail because the taker asks for a worse quality (less output for
	// the same input) than what the maker offers, so the offer cannot cross.
	// =========================================================================
	for _, flags := range []struct {
		name  string
		build func(taker *jtx.Account, takerPays, takerGets tx.Amount) *OfferCreateBuilder
	}{
		{
			name: "FillOrKill",
			build: func(acc *jtx.Account, takerPays, takerGets tx.Amount) *OfferCreateBuilder {
				return OfferCreate(acc, takerPays, takerGets).FillOrKill()
			},
		},
		{
			name: "FillOrKill+Sell",
			build: func(acc *jtx.Account, takerPays, takerGets tx.Amount) *OfferCreateBuilder {
				return OfferCreate(acc, takerPays, takerGets).FillOrKill().Sell()
			},
		},
	} {
		t.Run("FailSection_"+flags.name, func(t *testing.T) {
			// Sub-test 3a: maker offers XRP(100) for USD(100), taker: USD(100) for XRP(99) -> tecKILLED
			result = env.Submit(OfferCreate(maker, jtx.XRPTxAmountFromXRP(100), USD(100)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			result = env.Submit(flags.build(taker, USD(100), jtx.XRPTxAmountFromXRP(99)).Build())
			jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
			env.Close()

			makerXRPBalance -= fee
			takerXRPBalance -= fee
			RequireOfferCount(t, env, taker, 0)

			// Sub-test 3b: maker offers USD(100) for XRP(100), taker: XRP(100) for USD(99) -> tecKILLED
			result = env.Submit(OfferCreate(maker, USD(100), jtx.XRPTxAmountFromXRP(100)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			result = env.Submit(flags.build(taker, jtx.XRPTxAmountFromXRP(100), USD(99)).Build())
			jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
			env.Close()

			makerXRPBalance -= fee
			takerXRPBalance -= fee
			RequireOfferCount(t, env, taker, 0)

			// Sub-test 3c: maker offers USD(100) for EUR(100), taker: EUR(100) for USD(99) -> tecKILLED
			result = env.Submit(OfferCreate(maker, USD(100), EUR(100)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			result = env.Submit(flags.build(taker, EUR(100), USD(99)).Build())
			jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
			env.Close()

			makerXRPBalance -= fee
			takerXRPBalance -= fee
			RequireOfferCount(t, env, taker, 0)
		})
	}

	// =========================================================================
	// Final: verify all tracked balances match actual balances
	// =========================================================================
	jtx.RequireIOUBalance(t, env, maker, issuer, "USD", makerUSDBalance)
	jtx.RequireIOUBalance(t, env, taker, issuer, "USD", takerUSDBalance)
	jtx.RequireIOUBalance(t, env, maker, issuer, "EUR", makerEURBalance)
	jtx.RequireIOUBalance(t, env, taker, issuer, "EUR", takerEURBalance)
	jtx.RequireBalance(t, env, maker, makerXRPBalance)
	jtx.RequireBalance(t, env, taker, takerXRPBalance)
}

// TestOffer_SellWithFillOrKill tests corner cases when both tfSell and
// tfFillOrKill flags are set on an offer.
// Reference: rippled Offer_test.cpp testSellWithFillOrKill (lines 2992-3075)
func TestOffer_SellWithFillOrKill(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSellWithFillOrKill(t, fs.disabled)
		})
	}
}

func testSellWithFillOrKill(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(10000000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000000)))
	env.Close()

	// Code returned if an offer is killed.
	// fix1578 changes the return code from tesSUCCESS to tecKILLED.
	fix1578Enabled := env.FeatureEnabled("fix1578")
	var killedCode string
	if fix1578Enabled {
		killedCode = jtx.TecKILLED
	} else {
		killedCode = jtx.TesSUCCESS
	}

	// bob offers XRP for USD.
	env.Trust(bob, USD(200))
	env.Close()
	result := env.Submit(payment.PayIssued(gw, bob, USD(100)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(2000), USD(20)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// =========================================================================
	// alice submits a tfSell | tfFillOrKill offer that does not cross.
	// =========================================================================
	{
		result = env.Submit(
			OfferCreate(alice, USD(21), jtx.XRPTxAmountFromXRP(2100)).FillOrKill().Sell().Build())
		if killedCode == jtx.TecKILLED {
			jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
		} else {
			jtx.RequireTxSuccess(t, result)
		}
		env.Close()
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
		RequireOfferCount(t, env, alice, 0)
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 100)
	}

	// =========================================================================
	// alice submits a tfSell | tfFillOrKill offer that crosses exactly.
	// Even though tfSell is present it doesn't matter this time.
	// =========================================================================
	{
		result = env.Submit(
			OfferCreate(alice, USD(20), jtx.XRPTxAmountFromXRP(2000)).FillOrKill().Sell().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 20)
		RequireOfferCount(t, env, alice, 0)
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 80)
	}

	// =========================================================================
	// alice submits a tfSell | tfFillOrKill offer that crosses and returns
	// more than was asked for (because of the tfSell flag).
	// bob creates another offer XRP(2000) for USD(20).
	// alice offers USD(10) for XRP(1500) with tfSell | tfFillOrKill.
	// tfSell means alice sells all 1500 XRP at the 2000/20 = 100 XRP/USD rate,
	// getting 15 USD (1500 / 100 = 15).
	// =========================================================================
	{
		result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(2000), USD(20)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		result = env.Submit(
			OfferCreate(alice, USD(10), jtx.XRPTxAmountFromXRP(1500)).FillOrKill().Sell().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 35)
		RequireOfferCount(t, env, alice, 0)
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 65)
	}

	// =========================================================================
	// alice submits a tfSell | tfFillOrKill offer that doesn't cross.
	// This would have succeeded with a regular tfSell, but the fillOrKill
	// prevents the transaction from crossing since not all of the offer is
	// consumed. We're using bob's left-over offer for XRP(500), USD(5).
	// =========================================================================
	{
		result = env.Submit(
			OfferCreate(alice, USD(1), jtx.XRPTxAmountFromXRP(501)).FillOrKill().Sell().Build())
		if killedCode == jtx.TecKILLED {
			jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
		} else {
			jtx.RequireTxSuccess(t, result)
		}
		env.Close()
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 35)
		RequireOfferCount(t, env, alice, 0)
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 65)
	}

	// =========================================================================
	// Alice submits a tfSell | tfFillOrKill offer that finishes off the
	// remainder of bob's offer. We're using bob's left-over offer for
	// XRP(500), USD(5).
	// =========================================================================
	{
		result = env.Submit(
			OfferCreate(alice, USD(1), jtx.XRPTxAmountFromXRP(500)).FillOrKill().Sell().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 40)
		RequireOfferCount(t, env, alice, 0)
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 60)
	}
}
