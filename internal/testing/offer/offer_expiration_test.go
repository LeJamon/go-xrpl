package offer

// Offer expiration tests.
// Reference: rippled/src/test/app/Offer_test.cpp - testExpiration (lines 1145-1221)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// TestOffer_Expiration tests offer expiration behavior.
// Reference: rippled Offer_test.cpp testExpiration (lines 1145-1221)
func TestOffer_Expiration(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testExpiration(t, fs.disabled)
		})
	}
}

func testExpiration(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	startBalance := uint64(jtx.XRP(1000000))
	usdOffer := USD(1000)
	xrpOffer := jtx.XRPTxAmountFromXRP(1000)

	env.FundAmount(gw, startBalance)
	env.FundAmount(alice, startBalance)
	env.FundAmount(bob, startBalance)
	env.Close()

	f := env.BaseFee()

	env.Trust(alice, usdOffer)
	result := env.Submit(payment.PayIssued(gw, alice, usdOffer).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireBalance(t, env, alice, startBalance-f)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
	RequireOfferCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, alice, 1)

	// Place an offer that should have already expired.
	// The DepositPreauth amendment changes the return code; adapt to that.
	featPreauth := featureEnabled(disabledFeatures, "DepositPreauth")

	expiredOffer := OfferCreate(alice, xrpOffer, usdOffer).
		Expiration(LastClose(env)).Build()
	result = env.Submit(expiredOffer)
	if featPreauth {
		jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)
	} else {
		jtx.RequireTxSuccess(t, result)
	}

	jtx.RequireBalance(t, env, alice, startBalance-f-f)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
	RequireOfferCount(t, env, alice, 0)
	jtx.RequireOwnerCount(t, env, alice, 1)
	env.Close()

	// Add an offer that expires before the next ledger close
	futureOffer := OfferCreate(alice, xrpOffer, usdOffer).
		Expiration(LastClose(env) + 1).Build()
	result = env.Submit(futureOffer)
	jtx.RequireTxSuccess(t, result)

	jtx.RequireBalance(t, env, alice, startBalance-f-f-f)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
	RequireOfferCount(t, env, alice, 1)
	jtx.RequireOwnerCount(t, env, alice, 2)

	// The offer expires (it's not removed yet)
	env.Close()
	jtx.RequireBalance(t, env, alice, startBalance-f-f-f)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
	RequireOfferCount(t, env, alice, 1) // Still in ledger even though expired
	jtx.RequireOwnerCount(t, env, alice, 2)

	// Add offer by bob - the expired offer is removed during crossing
	result = env.Submit(OfferCreate(bob, usdOffer, xrpOffer).Build())
	jtx.RequireTxSuccess(t, result)

	jtx.RequireBalance(t, env, alice, startBalance-f-f-f)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 1000)
	RequireOfferCount(t, env, alice, 0) // Expired offer removed
	jtx.RequireOwnerCount(t, env, alice, 1)

	jtx.RequireBalance(t, env, bob, startBalance-f)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 0)
	RequireOfferCount(t, env, bob, 1)   // Bob's offer stays (no cross)
	jtx.RequireOwnerCount(t, env, bob, 1)
}
