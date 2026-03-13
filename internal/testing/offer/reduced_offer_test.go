package offer

// Reduced offer quality tests.
// These tests verify that the fixReducedOffersV1 and fixReducedOffersV2 amendments
// properly handle quality degradation when offers are partially crossed or underfunded.
// Reference: rippled/src/test/app/ReducedOffer_test.cpp

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	paymentBuilder "github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

// qualityRate returns the Quality of an offer (TakerPays/TakerGets).
// Uses exact integer Quality comparison matching rippled's Quality(Amounts{in, out}).rate().
// Higher quality value = worse rate for the taker.
func qualityRate(takerPays, takerGets tx.Amount) payment.Quality {
	return payment.QualityFromAmounts(
		payment.ToEitherAmount(takerPays),
		payment.ToEitherAmount(takerGets),
	)
}

// qualityWorseThan returns true if rate a is worse than rate b.
// This replaces the float64 computeRate comparison which has precision issues.
func qualityWorseThan(a, b payment.Quality) bool {
	return a.WorseThan(b)
}

// TestReducedOffer_PartialCrossNewXrpIouQChange exercises partial cross where
// a new offer partially crosses an old in-ledger offer, leaving a reduced new offer.
// Reference: ReducedOffer_test.cpp testPartialCrossNewXrpIouQChange (lines 71-225)
func TestReducedOffer_PartialCrossNewXrpIouQChange(t *testing.T) {
	testPartialCrossNewXrpIouQChange(t)
}

func testPartialCrossNewXrpIouQChange(t *testing.T) {
	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	// Test with and without fixReducedOffersV1
	for _, withFix := range []bool{false, true} {
		name := "withoutFixReducedOffersV1"
		if withFix {
			name = "withFixReducedOffersV1"
		}
		t.Run(name, func(t *testing.T) {
			var disabled []string
			if !withFix {
				disabled = []string{"fixReducedOffersV1"}
			}
			env := newEnvWithFeatures(t, disabled)

			// Fund generously so no offers are underfunded
			env.FundAmount(gw, uint64(jtx.XRP(10000000)))
			env.FundAmount(alice, uint64(jtx.XRP(10000000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000000)))
			env.Close()

			env.Trust(alice, USD(10000000))
			env.Trust(bob, USD(10000000))
			env.Close()

			result := env.Submit(paymentBuilder.PayIssued(gw, bob, USD(10000000)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// bob's offer (new offer) is the same every time:
			// TakerPays = XRP(1) = 1000000 drops, TakerGets = USD(1)
			bobTakerPays := tx.NewXRPAmount(1000000)                    // XRP(1)
			bobTakerGets := tx.NewIssuedAmount(1, 0, "USD", gw.Address) // USD(1)

			var blockedCount uint32
			// alice's offer has a slightly smaller TakerPays with each iteration
			for mantissaReduce := uint64(1000000000); mantissaReduce <= 5000000000; mantissaReduce += 20000000 {
				// alice's offer: takerPays = USD with reduced mantissa, takerGets = XRP(1) - 1 drop
				aliceUSD := tx.NewIssuedAmount(bobTakerGets.Mantissa()-int64(mantissaReduce), bobTakerGets.Exponent(), "USD", gw.Address)
				aliceXRP := tx.NewXRPAmount(bobTakerPays.Drops() - 1)

				// Put alice's offer in the ledger
				aliceOfferSeq := env.Seq(alice)
				result := env.Submit(OfferCreate(alice, aliceUSD, aliceXRP).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// bob's offer partially crosses alice's
				initialQuality := qualityRate(bobTakerPays, bobTakerGets)
				bobOfferSeq := env.Seq(bob)
				result = env.Submit(OfferCreate(bob, bobTakerPays, bobTakerGets).Sell().Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// alice's offer should be fully consumed
				if OfferInLedger(env, alice, aliceOfferSeq) {
					blockedCount++
					// Clean up
					env.Submit(OfferCancel(alice, aliceOfferSeq).Build())
					env.Submit(OfferCancel(bob, bobOfferSeq).Build())
					env.Close()
					continue
				}

				// Check bob's remaining offer
				bobOffer := GetOffer(env, bob, bobOfferSeq)
				if bobOffer != nil {
					reducedQuality := qualityRate(bobOffer.TakerPays, bobOffer.TakerGets)
					if qualityWorseThan(reducedQuality, initialQuality) {
						blockedCount++
					}
				}

				// Clean up
				env.Submit(OfferCancel(alice, aliceOfferSeq).Build())
				env.Submit(OfferCancel(bob, bobOfferSeq).Build())
				env.Close()
			}

			if withFix {
				require.Equal(t, uint32(0), blockedCount,
					"With fixReducedOffersV1, no offers should have bad rates")
			} else {
				require.True(t, blockedCount >= 170,
					"Without fixReducedOffersV1, expected >= 170 bad rates, got %d", blockedCount)
			}
		})
	}
}

// TestReducedOffer_PartialCrossOldXrpIouQChange exercises partial cross where
// a new offer partially crosses an old offer, leaving a reduced old offer.
// Reference: ReducedOffer_test.cpp testPartialCrossOldXrpIouQChange (lines 227-384)
func TestReducedOffer_PartialCrossOldXrpIouQChange(t *testing.T) {
	testPartialCrossOldXrpIouQChange(t)
}

func testPartialCrossOldXrpIouQChange(t *testing.T) {
	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	for _, withFix := range []bool{false, true} {
		name := "withoutFixReducedOffersV1"
		if withFix {
			name = "withFixReducedOffersV1"
		}
		t.Run(name, func(t *testing.T) {
			var disabled []string
			if !withFix {
				disabled = []string{"fixReducedOffersV1"}
			}
			env := newEnvWithFeatures(t, disabled)

			env.FundAmount(gw, uint64(jtx.XRP(10000000)))
			env.FundAmount(alice, uint64(jtx.XRP(10000000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000000)))
			env.Close()

			env.Trust(alice, USD(10000000))
			env.Trust(bob, USD(10000000))
			env.Close()

			result := env.Submit(paymentBuilder.PayIssued(gw, alice, USD(10000000)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// alice's offer (old offer) is the same every time:
			aliceTakerPays := tx.NewXRPAmount(1000000) // XRP(1)
			aliceTakerGets := tx.NewIssuedAmount(1, 0, "USD", gw.Address)

			var blockedCount uint32
			for mantissaReduce := uint64(1000000000); mantissaReduce <= 4000000000; mantissaReduce += 20000000 {
				// bob's offer: slightly smaller TakerPays (USD)
				bobUSD := tx.NewIssuedAmount(aliceTakerGets.Mantissa()-int64(mantissaReduce), aliceTakerGets.Exponent(), "USD", gw.Address)
				bobXRP := tx.NewXRPAmount(aliceTakerPays.Drops() - 1)

				initialQuality := qualityRate(aliceTakerPays, aliceTakerGets)

				// Put alice's offer in the ledger
				aliceOfferSeq := env.Seq(alice)
				result := env.Submit(OfferCreate(alice, aliceTakerPays, aliceTakerGets).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// bob's offer partially crosses alice's
				bobOfferSeq := env.Seq(bob)
				result = env.Submit(OfferCreate(bob, bobUSD, bobXRP).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// bob's offer should not have made it into the ledger
				if OfferInLedger(env, bob, bobOfferSeq) {
					blockedCount++
					env.Submit(OfferCancel(alice, aliceOfferSeq).Build())
					env.Submit(OfferCancel(bob, bobOfferSeq).Build())
					env.Close()
					continue
				}

				// Check alice's remaining offer
				aliceOffer := GetOffer(env, alice, aliceOfferSeq)
				if aliceOffer != nil {
					reducedQuality := qualityRate(aliceOffer.TakerPays, aliceOffer.TakerGets)
					if qualityWorseThan(reducedQuality, initialQuality) {
						blockedCount++
					}
				}

				// Clean up
				env.Submit(OfferCancel(alice, aliceOfferSeq).Build())
				env.Submit(OfferCancel(bob, bobOfferSeq).Build())
				env.Close()
			}

			if withFix {
				require.Equal(t, uint32(0), blockedCount,
					"With fixReducedOffersV1, no offers should have bad rates")
			} else {
				require.True(t, blockedCount > 10,
					"Without fixReducedOffersV1, expected > 10 bad rates, got %d", blockedCount)
			}
		})
	}
}

// TestReducedOffer_UnderFundedXrpIouQChange exercises underfunded XRP/IOU offers.
// Reference: ReducedOffer_test.cpp testUnderFundedXrpIouQChange (lines 386-487)
func TestReducedOffer_UnderFundedXrpIouQChange(t *testing.T) {
	testUnderFundedXrpIouQChange(t)
}

func testUnderFundedXrpIouQChange(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	for _, withFix := range []bool{false, true} {
		name := "withoutFixReducedOffersV1"
		if withFix {
			name = "withFixReducedOffersV1"
		}
		t.Run(name, func(t *testing.T) {
			var disabled []string
			if !withFix {
				disabled = []string{"fixReducedOffersV1"}
			}
			env := newEnvWithFeatures(t, disabled)

			env.FundAmount(gw, uint64(jtx.XRP(10000)))
			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.Close()
			env.Trust(alice, USD(1000))
			env.Trust(bob, USD(1000))
			env.Close()

			var blockedOrderBookCount int
			// Loop from USD(0.45) to USD(1) in steps of USD(0.025)
			for initialBobUSDFloat := 0.45; initialBobUSDFloat <= 1.0; initialBobUSDFloat += 0.025 {
				initialBobUSD := USD(initialBobUSDFloat)

				// Underfund bob's offer
				result := env.Submit(paymentBuilder.PayIssued(gw, bob, initialBobUSD).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				bobOfferSeq := env.Seq(bob)
				result = env.Submit(OfferCreate(bob, tx.NewXRPAmount(2), USD(1)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// alice places a crossing offer
				aliceOfferSeq := env.Seq(alice)
				result = env.Submit(OfferCreate(alice, USD(1), tx.NewXRPAmount(2)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// Check for order book blocking:
				// If bob's offer is still in the ledger AND alice received no USD
				bobsOfferGone := !OfferInLedger(env, bob, bobOfferSeq)
				aliceBalanceUSD := env.IOUBalance(alice, gw, "USD")
				aliceHasUSD := aliceBalanceUSD != nil && aliceBalanceUSD.Signum() > 0

				if aliceHasUSD {
					require.Equal(t, 0, aliceBalanceUSD.Compare(initialBobUSD),
						"alice should have received exactly initialBobUSD, got %v", aliceBalanceUSD)
					bobBalanceUSD := env.IOUBalance(bob, gw, "USD")
					require.True(t, bobBalanceUSD == nil || bobBalanceUSD.Signum() == 0,
						"bob's USD balance should be 0 after crossing")
					require.True(t, bobsOfferGone, "bob's offer should be gone when alice got USD")
				}

				if !bobsOfferGone && !aliceHasUSD {
					blockedOrderBookCount++
					fmt.Printf("[UNDERFUND-DBG] BLOCKED iter=%.3f bobsOfferGone=%v aliceHasUSD=%v aliceBal=%v\n",
						initialBobUSDFloat, bobsOfferGone, aliceHasUSD, aliceBalanceUSD)
				}

				// Clean up offers, zero out balances, then close (matching rippled order)
				env.Submit(OfferCancel(alice, aliceOfferSeq).Build())
				env.Submit(OfferCancel(bob, bobOfferSeq).Build())
				if bal := env.IOUBalance(alice, gw, "USD"); bal != nil && bal.Signum() > 0 {
					env.Submit(paymentBuilder.PayIssued(alice, gw, *bal).Build())
				}
				if bal := env.IOUBalance(bob, gw, "USD"); bal != nil && bal.Signum() > 0 {
					env.Submit(paymentBuilder.PayIssued(bob, gw, *bal).Build())
				}
				env.Close()
			}

			if withFix {
				require.Equal(t, 0, blockedOrderBookCount,
					"With fixReducedOffersV1, no order book blocking should occur")
			} else {
				require.True(t, blockedOrderBookCount > 15,
					"Without fixReducedOffersV1, expected > 15 blocked, got %d", blockedOrderBookCount)
			}
		})
	}
}

// TestReducedOffer_UnderFundedIouIouQChange exercises underfunded IOU/IOU offers.
// Reference: ReducedOffer_test.cpp testUnderFundedIouIouQChange (lines 489-611)
func TestReducedOffer_UnderFundedIouIouQChange(t *testing.T) {
	testUnderFundedIouIouQChange(t)
}

func testUnderFundedIouIouQChange(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	gw := jtx.NewAccount("gw")
	EUR := func(amount float64) tx.Amount { return jtx.EUR(gw, amount) }

	// tinyUSD: STAmount(USD.issue(), 1, -81)
	tinyUSD := jtx.IssuedCurrencyFromMantissa(gw, "USD", 1, -81)

	// eurOffer and usdOffer amounts for the offer
	eurOffer := jtx.IssuedCurrencyFromMantissa(gw, "EUR", 2957, -76)
	usdOffer := jtx.IssuedCurrencyFromMantissa(gw, "USD", 7109, -76)

	endLoop := jtx.IssuedCurrencyFromMantissa(gw, "USD", 50, -81)

	for _, withFix := range []bool{false, true} {
		name := "withoutFixReducedOffersV1"
		if withFix {
			name = "withFixReducedOffersV1"
		}
		t.Run(name, func(t *testing.T) {
			var disabled []string
			if !withFix {
				disabled = []string{"fixReducedOffersV1"}
			}
			env := newEnvWithFeatures(t, disabled)

			env.FundAmount(gw, uint64(jtx.XRP(10000)))
			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.Close()
			env.Trust(alice, jtx.USD(gw, 1000))
			env.Trust(bob, jtx.USD(gw, 1000))
			env.Trust(alice, EUR(1000))
			env.Trust(bob, EUR(1000))
			env.Close()

			var blockedOrderBookCount int
			// Loop from tinyUSD to endLoop in increments of tinyUSD
			currentBobUSD := tinyUSD
			for currentBobUSD.Compare(endLoop) <= 0 {
				// Underfund bob's offer
				result := env.Submit(paymentBuilder.PayIssued(gw, bob, currentBobUSD).Build())
				jtx.RequireTxSuccess(t, result)
				result = env.Submit(paymentBuilder.PayIssued(gw, alice, EUR(100)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// This offer is underfunded
				bobOfferSeq := env.Seq(bob)
				result = env.Submit(OfferCreate(bob, eurOffer, usdOffer).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// alice places a crossing offer
				aliceOfferSeq := env.Seq(alice)
				result = env.Submit(OfferCreate(alice, usdOffer, eurOffer).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// Check for order book blocking
				bobsOfferGone := !OfferInLedger(env, bob, bobOfferSeq)
				aliceBalanceUSD := env.IOUBalance(alice, gw, "USD")
				aliceHasUSD := aliceBalanceUSD != nil && aliceBalanceUSD.Signum() > 0

				if aliceHasUSD {
					require.Equal(t, 0, aliceBalanceUSD.Compare(currentBobUSD),
						"alice should have received exactly initialBobUSD, got %v", aliceBalanceUSD)
					bobBalanceUSD := env.IOUBalance(bob, gw, "USD")
					require.True(t, bobBalanceUSD == nil || bobBalanceUSD.Signum() == 0,
						"bob's USD balance should be 0 after crossing")
					require.True(t, bobsOfferGone, "bob's offer should be gone when alice got USD")
				}

				if !bobsOfferGone && !aliceHasUSD {
					blockedOrderBookCount++
				}

				// Clean up offers, zero out balances, then close (matching rippled order)
				env.Submit(OfferCancel(alice, aliceOfferSeq).Build())
				env.Submit(OfferCancel(bob, bobOfferSeq).Build())

				// Zero out IOU balances
				if bal := env.IOUBalance(alice, gw, "EUR"); bal != nil && bal.Signum() > 0 {
					env.Submit(paymentBuilder.PayIssued(alice, gw, *bal).Build())
				}
				if bal := env.IOUBalance(alice, gw, "USD"); bal != nil && bal.Signum() > 0 {
					env.Submit(paymentBuilder.PayIssued(alice, gw, *bal).Build())
				}
				if bal := env.IOUBalance(bob, gw, "EUR"); bal != nil && bal.Signum() > 0 {
					env.Submit(paymentBuilder.PayIssued(bob, gw, *bal).Build())
				}
				if bal := env.IOUBalance(bob, gw, "USD"); bal != nil && bal.Signum() > 0 {
					env.Submit(paymentBuilder.PayIssued(bob, gw, *bal).Build())
				}
				env.Close()

				// Increment
				var err error
				currentBobUSD, err = currentBobUSD.Add(tinyUSD)
				if err != nil {
					break
				}
			}

			if withFix {
				require.Equal(t, 0, blockedOrderBookCount,
					"With fixReducedOffersV1, no order book blocking should occur")
			} else {
				require.True(t, blockedOrderBookCount > 20,
					"Without fixReducedOffersV1, expected > 20 blocked, got %d", blockedOrderBookCount)
			}
		})
	}
}

// TestReducedOffer_SellPartialCrossOldXrpIouQChange exercises tfSell partial
// crossing of an old offer where quality changes.
// Reference: ReducedOffer_test.cpp testSellPartialCrossOldXrpIouQChange (lines 623-790)
func TestReducedOffer_SellPartialCrossOldXrpIouQChange(t *testing.T) {
	testSellPartialCrossOldXrpIouQChange(t)
}

func testSellPartialCrossOldXrpIouQChange(t *testing.T) {
	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	for _, withFix := range []bool{false, true} {
		name := "withoutFixReducedOffersV2"
		if withFix {
			name = "withFixReducedOffersV2"
		}
		t.Run(name, func(t *testing.T) {
			var disabled []string
			if !withFix {
				disabled = []string{"fixReducedOffersV2"}
			}
			env := newEnvWithFeatures(t, disabled)

			env.FundAmount(gw, uint64(jtx.XRP(10000000)))
			env.FundAmount(alice, uint64(jtx.XRP(10000000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000000)))
			env.FundAmount(carol, uint64(jtx.XRP(10000000)))
			env.Close()

			env.Trust(alice, USD(10000000))
			env.Trust(bob, USD(10000000))
			env.Trust(carol, USD(10000000))
			env.Close()

			result := env.Submit(paymentBuilder.PayIssued(gw, alice, USD(10000000)).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(paymentBuilder.PayIssued(gw, bob, USD(10000000)).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(paymentBuilder.PayIssued(gw, carol, USD(10000000)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			const loopCount = 100
			var blockedCount uint32

			increaseGetsFloat := 0.0
			step := 0.00000001 // 1e-8

			for i := 0; i < loopCount; i++ {
				// alice submits an offer that may become a blocker
				aliceOfferSeq := env.Seq(alice)
				// alice's initial offer: USD(2) for drops(3382562)
				result := env.Submit(OfferCreate(alice, USD(2), tx.NewXRPAmount(3382562)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// Get alice's offer to compute initial quality
				aliceOffer := GetOffer(env, alice, aliceOfferSeq)
				var initialQuality payment.Quality
				if aliceOffer != nil {
					initialQuality = qualityRate(aliceOffer.TakerPays, aliceOffer.TakerGets)
				}

				// bob submits a more desirable offer
				bobOfferSeq := env.Seq(bob)
				result = env.Submit(OfferCreate(bob, USD(0.97086565812384), tx.NewXRPAmount(1642020)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// carol's offer consumes bob's and partially crosses alice's (with tfSell)
				carolOfferSeq := env.Seq(carol)
				carolGetsUSD := USD(1.0 + increaseGetsFloat)
				result = env.Submit(OfferCreate(carol, tx.NewXRPAmount(1642020), carolGetsUSD).Sell().Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// carol's and bob's offers should not be in the ledger
				carolStillExists := OfferInLedger(env, carol, carolOfferSeq)
				bobStillExists := OfferInLedger(env, bob, bobOfferSeq)

				if carolStillExists || bobStillExists {
					blockedCount++
				} else {
					// Check alice's remaining offer quality
					aliceReducedOffer := GetOffer(env, alice, aliceOfferSeq)
					if aliceReducedOffer != nil {
						reducedQuality := qualityRate(aliceReducedOffer.TakerPays, aliceReducedOffer.TakerGets)
						if qualityWorseThan(reducedQuality, initialQuality) {
							blockedCount++
						}
					}
				}

				// Clean up
				env.Submit(OfferCancel(alice, aliceOfferSeq).Build())
				env.Submit(OfferCancel(bob, bobOfferSeq).Build())
				env.Submit(OfferCancel(carol, carolOfferSeq).Build())
				env.Close()

				increaseGetsFloat += step
			}

			if withFix {
				require.Equal(t, uint32(0), blockedCount,
					"With fixReducedOffersV2, no offers should have bad rates")
			} else {
				require.True(t, blockedCount > 80,
					"Without fixReducedOffersV2, expected > 80 bad rates, got %d", blockedCount)
			}
		})
	}
}
