package offer

// Offer reserve and unfunded tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testInsufficientReserve (lines 710-816)
//   - testUnfundedCross (lines 1223-1281)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// TestOffer_InsufficientReserve tests reserve requirements for offers.
// If balance before tx isn't high enough for reserve after, no offer goes on books.
// But if the offer crosses (partially or fully), the tx succeeds.
// Reference: rippled Offer_test.cpp testInsufficientReserve (lines 710-816)
func TestOffer_InsufficientReserve(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testInsufficientReserve(t, fs.disabled)
		})
	}
}

func testInsufficientReserve(t *testing.T, disabledFeatures []string) {
	USD := func(gw *jtx.Account, amount float64) tx.Amount { return jtx.USD(gw, amount) }

	usdOffer := func(gw *jtx.Account) tx.Amount { return USD(gw, 1000) }
	xrpOffer := jtx.XRPTxAmountFromXRP(1000)

	// No crossing:
	t.Run("NoCrossing", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")

		env.FundAmount(gw, uint64(jtx.XRP(1000000)))

		f := env.BaseFee()
		r := Reserve(env, 0)

		env.FundAmount(alice, r+f)

		env.Trust(alice, usdOffer(gw))
		result := env.Submit(payment.PayIssued(gw, alice, usdOffer(gw)).Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(OfferCreate(alice, xrpOffer, usdOffer(gw)).Build())
		jtx.RequireTxClaimed(t, result, jtx.TecINSUF_RESERVE_OFFER)

		jtx.RequireBalance(t, env, alice, r-f)
		jtx.RequireOwnerCount(t, env, alice, 1)
	})

	// Partial cross:
	t.Run("PartialCross", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		env.FundAmount(gw, uint64(jtx.XRP(1000000)))

		f := env.BaseFee()
		r := Reserve(env, 0)

		usdOffer2 := USD(gw, 500)
		xrpOffer2 := jtx.XRPTxAmountFromXRP(500)

		env.FundAmount(bob, r+f+uint64(jtx.XRP(1000)))

		result := env.Submit(OfferCreate(bob, usdOffer2, xrpOffer2).Build())
		jtx.RequireTxSuccess(t, result)

		env.FundAmount(alice, r+f)

		env.Trust(alice, usdOffer(gw))
		result = env.Submit(payment.PayIssued(gw, alice, usdOffer(gw)).Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(OfferCreate(alice, xrpOffer, usdOffer(gw)).Build())
		jtx.RequireTxSuccess(t, result)

		jtx.RequireBalance(t, env, alice, r-f+uint64(jtx.XRP(500)))
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 500)
		jtx.RequireOwnerCount(t, env, alice, 1)

		jtx.RequireBalance(t, env, bob, r+uint64(jtx.XRP(500)))
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 500)
		jtx.RequireOwnerCount(t, env, bob, 1)
	})

	// Full cross: account has enough reserve as is, but not enough if an
	// offer were added. Attempt to sell IOUs to buy XRP. If it fully crosses,
	// we succeed.
	t.Run("FullCross", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw := jtx.NewAccount("gateway")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")

		env.FundAmount(gw, uint64(jtx.XRP(1000000)))

		f := env.BaseFee()
		r := Reserve(env, 0)

		usdOffer2 := USD(gw, 500)
		xrpOffer2 := jtx.XRPTxAmountFromXRP(500)

		env.FundAmount(bob, r+f+uint64(jtx.XRP(1000)))
		env.FundAmount(carol, r+f+uint64(jtx.XRP(1000)))

		result := env.Submit(OfferCreate(bob, usdOffer2, xrpOffer2).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(OfferCreate(carol, usdOffer(gw), xrpOffer).Build())
		jtx.RequireTxSuccess(t, result)

		env.FundAmount(alice, r+f)

		env.Trust(alice, usdOffer(gw))
		result = env.Submit(payment.PayIssued(gw, alice, usdOffer(gw)).Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(OfferCreate(alice, xrpOffer, usdOffer(gw)).Build())
		jtx.RequireTxSuccess(t, result)

		jtx.RequireBalance(t, env, alice, r-f+uint64(jtx.XRP(1000)))
		jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)
		jtx.RequireOwnerCount(t, env, alice, 1)

		jtx.RequireBalance(t, env, bob, r+uint64(jtx.XRP(500)))
		jtx.RequireIOUBalance(t, env, bob, gw, "USD", 500)
		jtx.RequireOwnerCount(t, env, bob, 1)

		jtx.RequireBalance(t, env, carol, r+uint64(jtx.XRP(500)))
		jtx.RequireIOUBalance(t, env, carol, gw, "USD", 500)
		jtx.RequireOwnerCount(t, env, carol, 2)
	})
}

// TestOffer_UnfundedCross tests various funding scenarios for offers.
// Reference: rippled Offer_test.cpp testUnfundedCross (lines 1223-1281)
func TestOffer_UnfundedCross(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testUnfundedCross(t, fs.disabled)
		})
	}
}

func testUnfundedCross(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	usdOffer := USD(1000)
	xrpOffer := jtx.XRPTxAmountFromXRP(1000)

	env.FundAmount(gw, uint64(jtx.XRP(1000000)))
	env.Close()

	f := env.BaseFee()

	// Account is at the reserve, and will dip below once fees are subtracted.
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, Reserve(env, 0))
	env.Close()
	result := env.Submit(OfferCreate(alice, usdOffer, xrpOffer).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecUNFUNDED_OFFER)
	jtx.RequireBalance(t, env, alice, Reserve(env, 0)-f)
	jtx.RequireOwnerCount(t, env, alice, 0)

	// Account has just enough for the reserve and the fee.
	bob := jtx.NewAccount("bob")
	env.FundAmount(bob, Reserve(env, 0)+f)
	env.Close()
	result = env.Submit(OfferCreate(bob, usdOffer, xrpOffer).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecUNFUNDED_OFFER)
	jtx.RequireBalance(t, env, bob, Reserve(env, 0))
	jtx.RequireOwnerCount(t, env, bob, 0)

	// Account has enough for the reserve, the fee and the offer, and a bit
	// more, but not enough for the reserve after the offer is placed.
	carol := jtx.NewAccount("carol")
	env.FundAmount(carol, Reserve(env, 0)+f+uint64(jtx.XRP(1)))
	env.Close()
	result = env.Submit(OfferCreate(carol, usdOffer, xrpOffer).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecINSUF_RESERVE_OFFER)
	jtx.RequireBalance(t, env, carol, Reserve(env, 0)+uint64(jtx.XRP(1)))
	jtx.RequireOwnerCount(t, env, carol, 0)

	// Account has enough for the reserve plus one offer, and the fee.
	dan := jtx.NewAccount("dan")
	env.FundAmount(dan, Reserve(env, 1)+f)
	env.Close()
	result = env.Submit(OfferCreate(dan, usdOffer, xrpOffer).Build())
	jtx.RequireTxSuccess(t, result)
	jtx.RequireBalance(t, env, dan, Reserve(env, 1))
	jtx.RequireOwnerCount(t, env, dan, 1)

	// Account has enough for the reserve plus one offer, the fee and
	// the entire offer amount.
	eve := jtx.NewAccount("eve")
	env.FundAmount(eve, Reserve(env, 1)+f+uint64(jtx.XRP(1000)))
	env.Close()
	result = env.Submit(OfferCreate(eve, usdOffer, xrpOffer).Build())
	jtx.RequireTxSuccess(t, result)
	jtx.RequireBalance(t, env, eve, Reserve(env, 1)+uint64(jtx.XRP(1000)))
	jtx.RequireOwnerCount(t, env, eve, 1)
}
