package offer

// Tests for removal of small increased quality offers.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testRmSmallIncreasedQOffersXRP (lines 323-469)
//   - testRmSmallIncreasedQOffersIOU (lines 471-633)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	paymentPkg "github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_RmSmallIncreasedQOffersXRP tests that small underfunded offers
// with increased quality are properly removed from the order book (XRP/IOU case).
// Carol places an offer, but cannot fully fund it. When her funding is taken
// into account, the offer's quality drops below its initial quality and has an
// input amount of 1 drop. This is removed as an offer that may block offer books.
// Reference: rippled Offer_test.cpp testRmSmallIncreasedQOffersXRP (lines 323-469)
func TestOffer_RmSmallIncreasedQOffersXRP(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testRmSmallIncreasedQOffersXRP(t, fs.disabled)
		})
	}
}

func testRmSmallIncreasedQOffersXRP(t *testing.T, disabledFeatures []string) {
	// Part 1: Offer crossing
	for _, crossBothOffers := range []bool{false, true} {
		name := "crossOne"
		if crossBothOffers {
			name = "crossBoth"
		}
		t.Run(name, func(t *testing.T) {
			env := newEnvWithFeatures(t, disabledFeatures)

			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			carol := jtx.NewAccount("carol")
			gw := jtx.NewAccount("gw")

			USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.FundAmount(carol, uint64(jtx.XRP(10000)))
			env.FundAmount(gw, uint64(jtx.XRP(10000)))
			env.Close()

			env.Trust(alice, USD(1000))
			env.Trust(bob, USD(1000))
			env.Trust(carol, USD(1000))

			// Underfund carol's offer
			initialCarolUSD := USD(0.499)
			result := env.Submit(payment.PayIssued(gw, carol, initialCarolUSD).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(payment.PayIssued(gw, bob, USD(100)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// This offer is underfunded
			// offer(carol, drops(1), USD(1))
			result = env.Submit(OfferCreate(carol, tx.NewXRPAmount(1), USD(1)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// Offer at a lower quality (passive)
			// offer(bob, drops(2), USD(1), tfPassive)
			result = env.Submit(OfferCreate(bob, tx.NewXRPAmount(2), USD(1)).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			RequireOfferCount(t, env, bob, 1)
			RequireOfferCount(t, env, carol, 1)

			// Alice places an offer that crosses carol's; depending on
			// crossBothOffers it may cross bob's as well.
			// offer(alice, USD(1), aliceTakerGets)
			var aliceTakerGets tx.Amount
			if crossBothOffers {
				aliceTakerGets = tx.NewXRPAmount(2)
			} else {
				aliceTakerGets = tx.NewXRPAmount(1)
			}
			result = env.Submit(OfferCreate(alice, USD(1), aliceTakerGets).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			if featureEnabled(disabledFeatures, "fixRmSmallIncreasedQOffers") {
				RequireOfferCount(t, env, carol, 0)
				// Carol's offer is removed but not taken
				jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0.499)

				if crossBothOffers {
					RequireOfferCount(t, env, alice, 0)
					// Alice's offer is crossed
					jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1)
				} else {
					RequireOfferCount(t, env, alice, 1)
					// Alice's offer is not crossed
					jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
				}
			} else {
				RequireOfferCount(t, env, alice, 1)
				RequireOfferCount(t, env, bob, 1)
				RequireOfferCount(t, env, carol, 1)
				jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
				// Carol's offer is not crossed at all
				jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0.499)
			}
		})
	}

	// Part 2: Payments
	for _, isPartialPayment := range []bool{false, true} {
		name := "fullPayment"
		if isPartialPayment {
			name = "partialPayment"
		}
		t.Run(name, func(t *testing.T) {
			env := newEnvWithFeatures(t, disabledFeatures)

			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			carol := jtx.NewAccount("carol")
			gw := jtx.NewAccount("gw")

			USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.FundAmount(carol, uint64(jtx.XRP(10000)))
			env.FundAmount(gw, uint64(jtx.XRP(10000)))
			env.Close()

			env.Trust(alice, USD(1000))
			env.Trust(bob, USD(1000))
			env.Trust(carol, USD(1000))
			env.Close()

			initialCarolUSD := USD(0.999)
			result := env.Submit(payment.PayIssued(gw, carol, initialCarolUSD).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			result = env.Submit(payment.PayIssued(gw, bob, USD(100)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// This offer is underfunded
			// offer(carol, drops(1), USD(1))
			result = env.Submit(OfferCreate(carol, tx.NewXRPAmount(1), USD(1)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// offer(bob, drops(2), USD(2), tfPassive)
			result = env.Submit(OfferCreate(bob, tx.NewXRPAmount(2), USD(2)).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			RequireOfferCount(t, env, bob, 1)
			RequireOfferCount(t, env, carol, 1)

			// Build payment flags
			payBuilder := payment.PayIssued(alice, bob, USD(5)).
				Paths([][]paymentPkg.PathStep{
					{{Currency: "USD", Issuer: gw.Address}},
				}).
				SendMax(tx.NewXRPAmount(int64(jtx.XRP(1)))).
				NoDirectRipple()

			if isPartialPayment {
				payBuilder = payBuilder.PartialPayment()
			}

			result = env.Submit(payBuilder.Build())

			if isPartialPayment {
				jtx.RequireTxSuccess(t, result)
			} else {
				jtx.RequireTxClaimed(t, result, jtx.TecPATH_PARTIAL)
			}
			env.Close()

			if featureEnabled(disabledFeatures, "fixRmSmallIncreasedQOffers") {
				if isPartialPayment {
					// tesSUCCESS case
					RequireOfferCount(t, env, carol, 0)
					// Carol's offer is removed but not taken
					jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0.999)
				}
				// else: TODO in rippled - offers are not removed when payments fail.
				// If that is addressed, carol's offer should be removed but not taken.
			} else {
				if isPartialPayment {
					RequireOfferCount(t, env, carol, 0)
					// Carol's offer is removed and taken
					jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
				} else {
					// Offer is not removed or taken
					require.True(t, IsOffer(env, carol, tx.NewXRPAmount(1), USD(1)))
				}
			}
		})
	}
}

// TestOffer_RmSmallIncreasedQOffersIOU tests that small underfunded offers
// with increased quality are properly removed from the order book (IOU/IOU case).
// Same pattern as the XRP test but with IOU/IOU offers instead of XRP/IOU.
// Reference: rippled Offer_test.cpp testRmSmallIncreasedQOffersIOU (lines 471-633)
func TestOffer_RmSmallIncreasedQOffersIOU(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testRmSmallIncreasedQOffersIOU(t, fs.disabled)
		})
	}
}

func testRmSmallIncreasedQOffersIOU(t *testing.T, disabledFeatures []string) {
	// Part 1: Offer crossing
	for _, crossBothOffers := range []bool{false, true} {
		name := "crossOne"
		if crossBothOffers {
			name = "crossBoth"
		}
		t.Run(name, func(t *testing.T) {
			env := newEnvWithFeatures(t, disabledFeatures)

			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			carol := jtx.NewAccount("carol")
			gw := jtx.NewAccount("gw")

			USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
			EUR := func(amount float64) tx.Amount { return jtx.EUR(gw, amount) }

			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.FundAmount(carol, uint64(jtx.XRP(10000)))
			env.FundAmount(gw, uint64(jtx.XRP(10000)))
			env.Close()

			env.Trust(alice, USD(1000))
			env.Trust(bob, USD(1000))
			env.Trust(carol, USD(1000))
			env.Trust(alice, EUR(1000))
			env.Trust(bob, EUR(1000))
			env.Trust(carol, EUR(1000))

			// Underfund carol's offer with a tiny amount: STAmount(USD.issue(), 1, -81)
			initialCarolUSD := jtx.IssuedCurrencyFromMantissa(gw, "USD", 1, -81)
			result := env.Submit(payment.PayIssued(gw, carol, initialCarolUSD).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(payment.PayIssued(gw, bob, USD(100)).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(payment.PayIssued(gw, alice, EUR(100)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// This offer is underfunded
			// offer(carol, EUR(1), USD(10))
			result = env.Submit(OfferCreate(carol, EUR(1), USD(10)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// Offer at a lower quality (passive)
			// offer(bob, EUR(1), USD(5), tfPassive)
			result = env.Submit(OfferCreate(bob, EUR(1), USD(5)).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			RequireOfferCount(t, env, bob, 1)
			RequireOfferCount(t, env, carol, 1)

			// Alice places an offer that crosses carol's; depending on
			// crossBothOffers it may cross bob's as well.
			// offer(alice, USD(1), aliceTakerGets)
			var aliceTakerGets tx.Amount
			if crossBothOffers {
				aliceTakerGets = EUR(0.2)
			} else {
				aliceTakerGets = EUR(0.1)
			}
			result = env.Submit(OfferCreate(alice, USD(1), aliceTakerGets).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			if featureEnabled(disabledFeatures, "fixRmSmallIncreasedQOffers") {
				RequireOfferCount(t, env, carol, 0)
				// Carol's offer is removed but not taken; balance should remain unchanged
				carolBalance := env.IOUBalance(carol, gw, "USD")
				require.NotNil(t, carolBalance)
				require.Equal(t, 0, carolBalance.Compare(initialCarolUSD),
					"Carol's USD balance should equal initialCarolUSD (1e-81)")

				if crossBothOffers {
					RequireOfferCount(t, env, alice, 0)
					// Alice's offer is crossed
					jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1)
				} else {
					RequireOfferCount(t, env, alice, 1)
					// Alice's offer is not crossed
					jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
				}
			} else {
				RequireOfferCount(t, env, alice, 1)
				RequireOfferCount(t, env, bob, 1)
				RequireOfferCount(t, env, carol, 1)
				jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
				// Carol's offer is not crossed at all; balance unchanged
				carolBalance := env.IOUBalance(carol, gw, "USD")
				require.NotNil(t, carolBalance)
				require.Equal(t, 0, carolBalance.Compare(initialCarolUSD),
					"Carol's USD balance should equal initialCarolUSD (1e-81)")
			}
		})
	}

	// Part 2: IOU Payments
	for _, isPartialPayment := range []bool{false, true} {
		name := "fullPayment"
		if isPartialPayment {
			name = "partialPayment"
		}
		t.Run(name, func(t *testing.T) {
			env := newEnvWithFeatures(t, disabledFeatures)

			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			carol := jtx.NewAccount("carol")
			gw := jtx.NewAccount("gw")

			USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
			EUR := func(amount float64) tx.Amount { return jtx.EUR(gw, amount) }

			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.FundAmount(carol, uint64(jtx.XRP(10000)))
			env.FundAmount(gw, uint64(jtx.XRP(10000)))
			env.Close()

			env.Trust(alice, USD(1000))
			env.Trust(bob, USD(1000))
			env.Trust(carol, USD(1000))
			env.Trust(alice, EUR(1000))
			env.Trust(bob, EUR(1000))
			env.Trust(carol, EUR(1000))
			env.Close()

			// Underfund carol's offer with a tiny amount
			initialCarolUSD := jtx.IssuedCurrencyFromMantissa(gw, "USD", 1, -81)
			result := env.Submit(payment.PayIssued(gw, carol, initialCarolUSD).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(payment.PayIssued(gw, bob, USD(100)).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(payment.PayIssued(gw, alice, EUR(100)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// This offer is underfunded
			// offer(carol, EUR(1), USD(2))
			result = env.Submit(OfferCreate(carol, EUR(1), USD(2)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// offer(bob, EUR(2), USD(4), tfPassive)
			result = env.Submit(OfferCreate(bob, EUR(2), USD(4)).Passive().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			RequireOfferCount(t, env, bob, 1)
			RequireOfferCount(t, env, carol, 1)

			// Build payment: pay(alice, bob, USD(5)), path(~USD), sendmax(EUR(10))
			payBuilder := payment.PayIssued(alice, bob, USD(5)).
				Paths([][]paymentPkg.PathStep{
					{{Currency: "USD", Issuer: gw.Address}},
				}).
				SendMax(EUR(10)).
				NoDirectRipple()

			if isPartialPayment {
				payBuilder = payBuilder.PartialPayment()
			}

			result = env.Submit(payBuilder.Build())

			if isPartialPayment {
				jtx.RequireTxSuccess(t, result)
			} else {
				jtx.RequireTxClaimed(t, result, jtx.TecPATH_PARTIAL)
			}
			env.Close()

			if featureEnabled(disabledFeatures, "fixRmSmallIncreasedQOffers") {
				if isPartialPayment {
					// tesSUCCESS case
					RequireOfferCount(t, env, carol, 0)
					// Carol's offer is removed but not taken
					carolBalance := env.IOUBalance(carol, gw, "USD")
					require.NotNil(t, carolBalance)
					require.Equal(t, 0, carolBalance.Compare(initialCarolUSD),
						"Carol's USD balance should equal initialCarolUSD (1e-81)")
				}
				// else: TODO in rippled - offers are not removed when payments fail.
				// If that is addressed, carol's offer should be removed but not taken.
			} else {
				if isPartialPayment {
					RequireOfferCount(t, env, carol, 0)
					// Carol's offer is removed and taken
					jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
				} else {
					// Offer is not removed or taken
					require.True(t, IsOffer(env, carol, EUR(1), USD(2)))
				}
			}
		})
	}
}
