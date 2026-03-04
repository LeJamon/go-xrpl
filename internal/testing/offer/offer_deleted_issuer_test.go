package offer

// RippleConnect Smoketest and Deleted Offer Issuer tests.
// Reference: rippled/src/test/app/Offer_test.cpp
//   - testRCSmoketest (lines 4471-4558)
//   - testDeletedOfferIssuer (lines 4632-4736)

import (
	"fmt"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/account"
	paymentPkg "github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/testing/trustset"
)

// ---------------------------------------------------------------------------
// testRCSmoketest
// ---------------------------------------------------------------------------

// TestOffer_RCSmoketest tests the RippleConnect Smoketest payment flow.
// This involves US and EU gateways with hot/cold wallets, a market maker
// that provides USD<->EUR offers, and a cross-currency payment from the
// US hot wallet to the EU cold wallet.
// Reference: rippled Offer_test.cpp testRCSmoketest (lines 4471-4558)
func TestOffer_RCSmoketest(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testRCSmoketest(t, fs.disabled)
		})
	}
}

func testRCSmoketest(t *testing.T, disabledFeatures []string) {
	env := newEnvWithFeatures(t, disabledFeatures)

	hotUS := jtx.NewAccount("hotUS")
	coldUS := jtx.NewAccount("coldUS")
	hotEU := jtx.NewAccount("hotEU")
	coldEU := jtx.NewAccount("coldEU")
	mm := jtx.NewAccount("mm")

	USD := func(amount float64) tx.Amount { return jtx.USD(coldUS, amount) }
	EUR := func(amount float64) tx.Amount { return jtx.EUR(coldEU, amount) }

	env.FundAmount(hotUS, uint64(jtx.XRP(100000)))
	env.FundAmount(coldUS, uint64(jtx.XRP(100000)))
	env.FundAmount(hotEU, uint64(jtx.XRP(100000)))
	env.FundAmount(coldEU, uint64(jtx.XRP(100000)))
	env.FundAmount(mm, uint64(jtx.XRP(100000)))
	env.Close()

	// Cold wallets require auth.
	// Note: DefaultRipple is already enabled by FundAmount.
	// In rippled, both fset(cold, asfRequireAuth) and fset(cold, asfDefaultRipple)
	// are set explicitly. Here FundAmount already enables DefaultRipple, so we
	// only need to enable RequireAuth.
	env.EnableRequireAuth(coldUS)
	env.EnableRequireAuth(coldEU)
	env.Close()

	// Each hot wallet trusts the related cold wallet for a large amount, with NoRipple.
	// Market maker trusts both cold wallets for a large amount, with NoRipple.
	result := env.Submit(trustset.TrustSet(hotUS, USD(10000000)).NoRipple().Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustSet(hotEU, EUR(10000000)).NoRipple().Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustSet(mm, USD(10000000)).NoRipple().Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustSet(mm, EUR(10000000)).NoRipple().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Gateways authorize the trustlines of hot wallets and market maker.
	// trust(coldUS, USD(0), hotUS, tfSetfAuth) => env.AuthorizeTrustLine(coldUS, hotUS, "USD")
	env.AuthorizeTrustLine(coldUS, hotUS, "USD")
	env.AuthorizeTrustLine(coldEU, hotEU, "EUR")
	env.AuthorizeTrustLine(coldUS, mm, "USD")
	env.AuthorizeTrustLine(coldEU, mm, "EUR")
	env.Close()

	// Issue currency from cold wallets to hot wallets and market maker.
	result = env.Submit(payment.PayIssued(coldUS, hotUS, USD(5000000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(coldEU, hotEU, EUR(5000000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(coldUS, mm, USD(5000000)).Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(payment.PayIssued(coldEU, mm, EUR(5000000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// MM places offers.
	// rate = 0.9: 0.9 USD = 1 EUR
	// Note: rippled uses C++ float (32-bit) arithmetic. We use float32 to match
	// the same precision so offer amounts and sendmax values are consistent.
	// First offer: mm wants EUR(4000000*0.9)=EUR(3600000), gives USD(4000000), with tfSell
	//   OfferCreate(mm, takerPays=EUR(3600000), takerGets=USD(4000000)).Sell()
	rate := float32(0.9)
	result = env.Submit(
		OfferCreate(mm, EUR(float64(4000000*rate)), USD(4000000)).Sell().Build())
	jtx.RequireTxSuccess(t, result)

	// reverseRate = (1.0/0.9) * 1.00101 ≈ 1.11223...
	// Second offer: mm wants USD(4000000*reverseRate), gives EUR(4000000), with tfSell
	//   OfferCreate(mm, takerPays=USD(4000000*reverseRate), takerGets=EUR(4000000)).Sell()
	reverseRate := float32(1.0) / rate * float32(1.00101)
	result = env.Submit(
		OfferCreate(mm, USD(float64(4000000*reverseRate)), EUR(4000000)).Sell().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Send the cross-currency payment: hotUS pays coldEU EUR(10) with sendmax USD(11.1223326).
	// The path goes: hotUS (has USD from coldUS) -> USD/EUR order book (mm's offer) -> coldEU (receives EUR).
	// We need an explicit path step through EUR/coldEU so the strand builder can find the book.
	result = env.Submit(
		payment.PayIssued(hotUS, coldEU, EUR(10)).
			SendMax(USD(11.1223326)).
			Paths([][]paymentPkg.PathStep{
				{{Currency: "EUR", Issuer: coldEU.Address}},
			}).
			Build())
	jtx.RequireTxSuccess(t, result)
}

// ---------------------------------------------------------------------------
// testDeletedOfferIssuer
// ---------------------------------------------------------------------------

// TestOffer_DeletedOfferIssuer tests that an offer whose issuer has been deleted
// cannot be crossed. When the issuer account no longer exists in the ledger,
// crossing attempts should fail with tecNO_ISSUER.
// Reference: rippled Offer_test.cpp testDeletedOfferIssuer (lines 4632-4736)
func TestOffer_DeletedOfferIssuer(t *testing.T) {
	for _, fs := range offerFeatureSets {
		t.Run(fs.name, func(t *testing.T) {
			testDeletedOfferIssuer(t, fs.disabled)
		})
	}
}

func testDeletedOfferIssuer(t *testing.T, disabledFeatures []string) {
	t.Skip("TODO: Requires AccountDelete transaction to fully cascade-delete owner objects (offers, trust lines)")

	env := newEnvWithFeatures(t, disabledFeatures)

	alice := jtx.NewAccount("alice")
	becky := jtx.NewAccount("becky")
	carol := jtx.NewAccount("carol")
	gw := jtx.NewAccount("gateway")

	USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
	BUX := func(amount float64) tx.Amount { return jtx.IssuedCurrency(alice, "BUX", amount) }

	// Fund accounts. gw is funded without DefaultRipple (noripple).
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(becky, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.FundAmountNoRipple(gw, uint64(jtx.XRP(10000)))
	env.Close()

	// Set up trust and issue USD to becky.
	env.Trust(becky, USD(1000))
	result := env.Submit(payment.PayIssued(gw, becky, USD(5)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify trust line exists between gw and becky.
	jtx.RequireTrustLineExists(t, env, gw, becky, "USD")

	// Make offers that produce USD and can be crossed two ways:
	// 1. direct XRP -> USD (passive)
	result = env.Submit(OfferCreate(becky, jtx.XRPTxAmountFromXRP(2), USD(2)).Passive().Build())
	jtx.RequireTxSuccess(t, result)

	// 2. direct BUX -> USD (passive)
	beckyBuxUsdSeq := env.Seq(becky)
	result = env.Submit(OfferCreate(becky, BUX(3), USD(3)).Passive().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Becky keeps the offers, but removes the trust line.
	result = env.Submit(payment.PayIssued(becky, gw, USD(5)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Trust(becky, USD(0))
	env.Close()

	// Verify trust line no longer exists.
	jtx.RequireTrustLineNotExists(t, env, gw, becky, "USD")
	// Verify offers still exist.
	RequireIsOffer(t, env, becky, jtx.XRPTxAmountFromXRP(2), USD(2))
	RequireIsOffer(t, env, becky, BUX(3), USD(3))

	// Delete gw's account.
	// The ledger sequence needs to be far enough ahead of the account sequence.
	env.IncLedgerSeqForAccDel(gw)

	// AccountDelete has a high fee equal to the reserve increment.
	acctDel := account.NewAccountDelete(gw.Address, alice.Address)
	acctDel.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
	result = env.Submit(acctDel)
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// Verify that gw's account root is gone from the ledger.
	jtx.RequireAccountNotExists(t, env, gw)

	// alice crosses becky's first offer. The offer create fails because
	// the USD issuer is not in the ledger.
	result = env.Submit(OfferCreate(alice, USD(2), jtx.XRPTxAmountFromXRP(2)).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecNO_ISSUER)
	env.Close()
	RequireOfferCount(t, env, alice, 0)
	RequireIsOffer(t, env, becky, jtx.XRPTxAmountFromXRP(2), USD(2))
	RequireIsOffer(t, env, becky, BUX(3), USD(3))

	// alice crosses becky's second offer. Again, the offer create fails
	// because the USD issuer is not in the ledger.
	result = env.Submit(OfferCreate(alice, USD(3), BUX(3)).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecNO_ISSUER)
	RequireOfferCount(t, env, alice, 0)
	RequireIsOffer(t, env, becky, jtx.XRPTxAmountFromXRP(2), USD(2))
	RequireIsOffer(t, env, becky, BUX(3), USD(3))

	// Cancel becky's BUX -> USD offer so we can try auto-bridging.
	result = env.Submit(OfferCancel(becky, beckyBuxUsdSeq).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	RequireNoOffer(t, env, becky, BUX(3), USD(3))

	// alice creates an offer that can be auto-bridged with becky's remaining offer.
	env.Trust(carol, BUX(1000))
	result = env.Submit(payment.PayIssued(alice, carol, BUX(2)).Build())
	jtx.RequireTxSuccess(t, result)

	result = env.Submit(OfferCreate(alice, BUX(2), jtx.XRPTxAmountFromXRP(2)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// carol attempts the auto-bridge. Again, the offer create fails
	// because the USD issuer is not in the ledger.
	result = env.Submit(OfferCreate(carol, USD(2), BUX(2)).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecNO_ISSUER)
	env.Close()
	RequireIsOffer(t, env, alice, BUX(2), jtx.XRPTxAmountFromXRP(2))
	RequireIsOffer(t, env, becky, jtx.XRPTxAmountFromXRP(2), USD(2))
}
