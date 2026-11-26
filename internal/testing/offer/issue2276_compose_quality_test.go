package offer

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
)

// The bridged strand USD -> XRP -> EUR composes 1.5e-6 USD/drop with
// 6666666.666666667 drops/EUR to exactly 10 USD/EUR, which meets the taker's
// limit; rounding up at every canonicalisation step made it 10.00000000000001
// and left the bridge uncrossed.
func TestOffer_Issue2276AutoBridgeComposedQuality(t *testing.T) {
	env := newEnvWithFeatures(t, nil)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	for _, acc := range []*jtx.Account{gw, alice, bob, carol} {
		env.FundAmount(acc, uint64(jtx.XRP(10000)))
	}

	env.Trust(alice, jtx.USD(gw, 1000))
	env.Trust(bob, jtx.EUR(gw, 1000))
	env.Trust(carol, jtx.USD(gw, 1000))
	env.Trust(carol, jtx.EUR(gw, 1000))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, bob, jtx.EUR(gw, 3)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, carol, jtx.USD(gw, 30)).Build()))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(OfferCreate(alice, jtx.USD(gw, 30), jtx.XRPTxAmountFromXRP(20)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(OfferCreate(bob, jtx.XRPTxAmountFromXRP(20), jtx.EUR(gw, 3)).Build()))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(OfferCreate(carol, jtx.EUR(gw, 3), jtx.USD(gw, 30)).Build()))
	env.Close()

	RequireOfferCount(t, env, alice, 0)
	RequireOfferCount(t, env, bob, 0)
	RequireOfferCount(t, env, carol, 0)
	jtx.RequireIOUBalance(t, env, carol, gw, "USD", 0)
	jtx.RequireIOUBalance(t, env, carol, gw, "EUR", 3)
	jtx.RequireIOUBalance(t, env, alice, gw, "USD", 30)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 0)
}
