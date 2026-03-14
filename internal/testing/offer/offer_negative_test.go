package offer

// Offer negative balance and no-ripple enforcement tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testNegativeBalance (lines 1394-1501)
//   - testEnforceNoRipple (lines 634-708)

import (
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
	"github.com/LeJamon/goXRPLd/internal/tx"
	paymentPkg "github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_NegativeBalance tests offer crossing with negative balance,
// transfer fees and miniscule funds.
// This is one of the few tests where fixReducedOffersV2 changes the results.
// Reference: rippled Offer_test.cpp testNegativeBalance (lines 1394-1501)
func TestOffer_NegativeBalance(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testNegativeBalance(t, fs.disabled)
		})
	}
}

func testNegativeBalance(t *testing.T, disabledFeatures []string) {
	// Test both with and without fixReducedOffersV2
	for _, withFixV2 := range []bool{false, true} {
		name := "withoutFixReducedOffersV2"
		if withFixV2 {
			name = "withFixReducedOffersV2"
		}
		t.Run(name, func(t *testing.T) {
			// Adjust disabled features for fixReducedOffersV2
			localDisabled := make([]string, len(disabledFeatures))
			copy(localDisabled, disabledFeatures)
			if !withFixV2 {
				localDisabled = append(localDisabled, "fixReducedOffersV2")
			}

			env := newEnvWithFeatures(t, localDisabled)

			gw := jtx.NewAccount("gateway")
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

			// These interesting amounts were taken from the original JS test
			gwInitialBalance := uint64(1149999730)
			aliceInitialBalance := uint64(499946999680)
			bobInitialBalance := uint64(10199999920)
			smallAmount := jtx.IssuedCurrencyFromMantissa(bob, "USD", 2710505431213761, -33)

			env.FundAmount(gw, gwInitialBalance)
			env.FundAmount(alice, aliceInitialBalance)
			env.FundAmount(bob, bobInitialBalance)
			env.Close()

			env.SetTransferRate(gw, 1005000000) // rate 1.005

			env.Trust(alice, USD(500))
			env.Trust(bob, USD(50))
			// gw trusts alice's USD (gateway trusts the holder)
			result := env.Submit(trustset.TrustSet(gw, jtx.IssuedCurrency(alice, "USD", 100)).Build())
			jtx.RequireTxSuccess(t, result)

			result = env.Submit(payment.PayIssued(gw, alice, jtx.IssuedCurrency(alice, "USD", 50)).Build())
			jtx.RequireTxSuccess(t, result)
			result = env.Submit(payment.PayIssued(gw, bob, smallAmount).Build())
			jtx.RequireTxSuccess(t, result)

			result = env.Submit(OfferCreate(alice, USD(50), jtx.XRPTxAmountFromXRP(150000)).Build())
			jtx.RequireTxSuccess(t, result)

			// Unfund the offer
			result = env.Submit(payment.PayIssued(alice, gw, USD(100)).Build())
			jtx.RequireTxSuccess(t, result)

			// Drop the trust line (set to 0)
			result = env.Submit(trustset.TrustSet(gw, jtx.IssuedCurrency(alice, "USD", 0)).Build())
			jtx.RequireTxSuccess(t, result)

			f := env.BaseFee()

			// Create crossing offer
			bobOfferSeq := env.Seq(bob)
			result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(2000), USD(1)).Build())
			jtx.RequireTxSuccess(t, result)

			featV2 := featureEnabled(localDisabled, "fixReducedOffersV2")

			if featV2 {
				// With fixReducedOffersV2, bob's offer does not cross
				// and goes straight into the ledger
				require.True(t, OfferInLedger(env, bob, bobOfferSeq),
					"Bob's offer should be in the ledger")
			} else {
				// Without fixReducedOffersV2, crossing happens with tiny amounts
				crossingDelta := uint64(1) // 1 drop

				// alice XRP: initial - 3*fee - crossingDelta
				jtx.RequireBalance(t, env, alice, aliceInitialBalance-f*3-crossingDelta)

				// bob XRP: initial - 2*fee + crossingDelta
				jtx.RequireBalance(t, env, bob, bobInitialBalance-f*2+crossingDelta)
			}

			_ = bobOfferSeq
		})
	}
}

// TestOffer_EnforceNoRipple tests that NoRipple flag on trust lines
// prevents rippling through an account for payments.
// Reference: rippled Offer_test.cpp testEnforceNoRipple (lines 634-708)
func TestOffer_EnforceNoRipple(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testEnforceNoRipple(t, fs.disabled)
		})
	}
}

func testEnforceNoRipple(t *testing.T, disabledFeatures []string) {
	// Section 1: No ripple with an implied account step after an offer
	t.Run("NoRippleBlocked", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw1 := jtx.NewAccount("gw1")
		gw2 := jtx.NewAccount("gw2")
		USD1 := func(amount float64) tx.Amount { return jtx.USD(gw1, amount) }
		USD2 := func(amount float64) tx.Amount { return jtx.USD(gw2, amount) }

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		dan := jtx.NewAccount("dan")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmountNoRipple(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(carol, uint64(jtx.XRP(10000)))
		env.FundAmount(dan, uint64(jtx.XRP(10000)))
		env.FundAmount(gw1, uint64(jtx.XRP(10000)))
		env.FundAmount(gw2, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(alice, USD1(1000))
		env.Trust(carol, USD1(1000))
		env.Trust(dan, USD1(1000))
		// bob trusts with NoRipple
		result := env.Submit(trustset.TrustSet(bob, USD1(1000)).NoRipple().Build())
		jtx.RequireTxSuccess(t, result)

		env.Trust(alice, USD2(1000))
		env.Trust(carol, USD2(1000))
		env.Trust(dan, USD2(1000))
		result = env.Submit(trustset.TrustSet(bob, USD2(1000)).NoRipple().Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(payment.PayIssued(gw1, dan, USD1(50)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw1, bob, USD1(50)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw2, bob, USD2(50)).Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(OfferCreate(dan, jtx.XRPTxAmountFromXRP(50), USD1(50)).Build())
		jtx.RequireTxSuccess(t, result)

		// Payment through ~USD1→bob should fail because bob has NoRipple
		result = env.Submit(payment.PayIssued(alice, carol, USD2(50)).
			SendMax(jtx.XRPTxAmountFromXRP(50)).
			Paths([][]paymentPkg.PathStep{{
				{Currency: "USD", Issuer: gw1.Address},
				{Account: bob.Address},
			}}).
			NoDirectRipple().
			Build())
		jtx.RequireTxClaimed(t, result, jtx.TecPATH_DRY)
	})

	// Section 2: Make sure payment works with default flags (no NoRipple)
	t.Run("DefaultFlagsWork", func(t *testing.T) {
		env := newEnvWithFeatures(t, disabledFeatures)

		gw1 := jtx.NewAccount("gw1")
		gw2 := jtx.NewAccount("gw2")
		USD1 := func(amount float64) tx.Amount { return jtx.USD(gw1, amount) }
		USD2 := func(amount float64) tx.Amount { return jtx.USD(gw2, amount) }

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")
		dan := jtx.NewAccount("dan")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(carol, uint64(jtx.XRP(10000)))
		env.FundAmount(dan, uint64(jtx.XRP(10000)))
		env.FundAmount(gw1, uint64(jtx.XRP(10000)))
		env.FundAmount(gw2, uint64(jtx.XRP(10000)))
		env.Close()

		env.Trust(alice, USD1(1000))
		env.Trust(bob, USD1(1000))
		env.Trust(carol, USD1(1000))
		env.Trust(dan, USD1(1000))
		env.Trust(alice, USD2(1000))
		env.Trust(bob, USD2(1000))
		env.Trust(carol, USD2(1000))
		env.Trust(dan, USD2(1000))

		result := env.Submit(payment.PayIssued(gw1, dan, USD1(50)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw1, bob, USD1(50)).Build())
		jtx.RequireTxSuccess(t, result)
		result = env.Submit(payment.PayIssued(gw2, bob, USD2(50)).Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(OfferCreate(dan, jtx.XRPTxAmountFromXRP(50), USD1(50)).Build())
		jtx.RequireTxSuccess(t, result)

		// Payment through ~USD1→bob should succeed with default flags
		result = env.Submit(payment.PayIssued(alice, carol, USD2(50)).
			SendMax(jtx.XRPTxAmountFromXRP(50)).
			Paths([][]paymentPkg.PathStep{{
				{Currency: "USD", Issuer: gw1.Address},
				{Account: bob.Address},
			}}).
			NoDirectRipple().
			Build())
		jtx.RequireTxSuccess(t, result)

		f := env.BaseFee()
		// alice pays 3 fees: 2 trust lines + 1 payment
		// (rippled's env.trust() refunds fees, ours doesn't)
		jtx.RequireBalance(t, env, alice, uint64(jtx.XRP(10000-50))-3*f)
		jtx.RequireIOUBalance(t, env, bob, gw1, "USD", 100)
		jtx.RequireIOUBalance(t, env, bob, gw2, "USD", 0)
		jtx.RequireIOUBalance(t, env, carol, gw2, "USD", 50)
	})
}
