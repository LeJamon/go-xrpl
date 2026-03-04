package offer

// Offer authorization tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testRequireAuth (lines 4299-4347)
//   - testMissingAuth (lines 4350-4469)
//   - testSelfAuth (lines 4560-4630)

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
)

// TestOffer_RequireAuth tests that offers work when the issuer requires authorization.
// Reference: rippled Offer_test.cpp testRequireAuth (lines 4299-4347)
func TestOffer_RequireAuth(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testRequireAuth(t, fs.disabled)
		})
	}
}

func testRequireAuth(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(400000)))
	env.FundAmount(alice, uint64(jtx.XRP(400000)))
	env.FundAmount(bob, uint64(jtx.XRP(400000)))
	env.Close()

	// GW requires authorization for holders of its IOUs
	env.EnableRequireAuth(gw)
	env.Close()

	// Properly set trust and have gw authorize bob and alice
	env.AuthorizeTrustLine(gw, bob, "USD")
	env.Trust(bob, USD(100))
	env.AuthorizeTrustLine(gw, alice, "USD")
	env.Trust(alice, USD(100))

	// Alice is able to place the offer since the GW has authorized her
	result := env.Submit(OfferCreate(alice, USD(40), jtx.XRPTxAmountFromXRP(4000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 1)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 0)

	result = env.Submit(payment.PayIssued(gw, bob, USD(50)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 50)

	// Bob's offer should cross Alice's
	result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(4000), USD(40)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 40)

	RequireOfferCount(t, env, bob, 0)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 10)
}

// TestOffer_MissingAuth tests behavior when authorization is missing.
// Reference: rippled Offer_test.cpp testMissingAuth (lines 4350-4469)
func TestOffer_MissingAuth(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testMissingAuth(t, fs.disabled)
		})
	}
}

func testMissingAuth(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(400000)))
	env.FundAmount(alice, uint64(jtx.XRP(400000)))
	env.FundAmount(bob, uint64(jtx.XRP(400000)))
	env.Close()

	// Alice creates an offer to acquire USD/gw before gw sets RequireAuth
	result := env.Submit(OfferCreate(alice, USD(40), jtx.XRPTxAmountFromXRP(4000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 1)

	// Now gw sets RequireAuth
	env.EnableRequireAuth(gw)
	env.Close()

	// Authorize and set up bob
	env.AuthorizeTrustLine(gw, bob, "USD")
	env.Close()
	env.Trust(bob, USD(100))
	env.Close()

	result = env.Submit(payment.PayIssued(gw, bob, USD(50)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 50)

	// Bob's offer shouldn't cross and alice's unauthorized offer should be deleted
	bobOfferSeq := env.Seq(bob)
	result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(4000), USD(40)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 1)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 50)

	// Alice tries to create offer without authorization → tecNO_LINE
	result = env.Submit(OfferCreate(alice, USD(40), jtx.XRPTxAmountFromXRP(4000)).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecNO_LINE)
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 1)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 50)

	// Set up trust line for alice but don't authorize it
	env.Trust(alice, USD(100))
	env.Close()

	// Alice still can't create offer → tecNO_AUTH
	result = env.Submit(OfferCreate(alice, USD(40), jtx.XRPTxAmountFromXRP(4000)).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecNO_AUTH)
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 1)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 50)

	// Delete bob's offer so alice can create offer without crossing
	result = env.Submit(OfferCancel(bob, bobOfferSeq).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	RequireOfferCount(t, env, bob, 0)

	// Finally, authorize alice's trust line
	env.AuthorizeTrustLine(gw, alice, "USD")
	env.Close()

	// Now alice's offer should succeed
	result = env.Submit(OfferCreate(alice, USD(40), jtx.XRPTxAmountFromXRP(4000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 1)

	// Bob creates his offer again. Alice's offer should cross.
	result = env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(4000), USD(40)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 40)

	RequireOfferCount(t, env, bob, 0)
	jtx.RequireIOUBalance(t, env, bob, gw, "USD", 10)
}

// TestOffer_SelfAuth tests self-auth behavior with RequireAuth.
// Reference: rippled Offer_test.cpp testSelfAuth (lines 4560-4630)
func TestOffer_SelfAuth(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testSelfAuth(t, fs.disabled)
		})
	}
}

func testSelfAuth(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }

	env.FundAmount(gw, uint64(jtx.XRP(400000)))
	env.FundAmount(alice, uint64(jtx.XRP(400000)))
	env.Close()

	// Test that gw can create an offer to buy gw's currency
	gwOfferSeq := env.Seq(gw)
	result := env.Submit(OfferCreate(gw, USD(40), jtx.XRPTxAmountFromXRP(4000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	RequireOfferCount(t, env, gw, 1)

	// Since gw has an offer out, gw should not be able to set RequireAuth → tecOWNERS
	// We submit the AccountSet directly to check the result
	result = env.Submit(accountset.AccountSet(gw).RequireAuth().Build())
	jtx.RequireTxClaimed(t, result, jtx.TecOWNERS)
	env.Close()

	// Cancel gw's offer so we can set RequireAuth
	result = env.Submit(OfferCancel(gw, gwOfferSeq).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	RequireOfferCount(t, env, gw, 0)

	// gw now requires authorization
	env.EnableRequireAuth(gw)
	env.Close()

	// Check DepositPreauth feature for different behavior
	featPreauth := featureEnabled(disabledFeatures, "DepositPreauth")

	// Before DepositPreauth: account with lsfRequireAuth can't buy own currency → tecNO_LINE
	// After DepositPreauth: they can → tesSUCCESS
	result = env.Submit(OfferCreate(gw, USD(40), jtx.XRPTxAmountFromXRP(4000)).Build())
	if featPreauth {
		jtx.RequireTxSuccess(t, result)
	} else {
		jtx.RequireTxClaimed(t, result, jtx.TecNO_LINE)
	}
	env.Close()

	if featPreauth {
		RequireOfferCount(t, env, gw, 1)
	} else {
		RequireOfferCount(t, env, gw, 0)
		return // Rest of test is DepositPreauth only
	}

	// Set up authorized trust line and pay alice
	env.AuthorizeTrustLine(gw, alice, "USD")
	env.Trust(alice, USD(100))
	env.Close()

	result = env.Submit(payment.PayIssued(gw, alice, USD(50)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 50)

	// Alice's offer should cross gw's
	result = env.Submit(OfferCreate(alice, jtx.XRPTxAmountFromXRP(4000), USD(40)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 10)

	RequireOfferCount(t, env, gw, 0)
}
