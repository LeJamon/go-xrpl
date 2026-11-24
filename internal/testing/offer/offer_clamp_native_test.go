package offer

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	paymentPkg "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

// TestOffer_RmSmallIncreasedQOffersNativeMaximum reduces carol's underfunded
// offer, which asks for the maximum amount of XRP, to her funds. The reduced
// input is not representable as XRP, so crossing her offer fails with
// tecINTERNAL: the fee is claimed and the order book is left as it was.
// Reference: rippled Offer_test.cpp testRmSmallIncreasedQOffersClampNative
func TestOffer_RmSmallIncreasedQOffersNativeMaximum(t *testing.T) {
	for _, viaPayment := range []bool{false, true} {
		name := "offerCrossing"
		if viaPayment {
			name = "payment"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			alice, bob, carol, dan, gw := jtx.NewAccount("alice"), jtx.NewAccount("bob"),
				jtx.NewAccount("carol"), jtx.NewAccount("dan"), jtx.NewAccount("gw")
			for _, acc := range []*jtx.Account{alice, bob, carol, dan, gw} {
				env.FundAmount(acc, uint64(jtx.XRP(10000)))
			}
			env.Close()
			USD := func(amount float64) tx.Amount { return jtx.USD(gw, amount) }
			for _, acc := range []*jtx.Account{alice, bob, carol, dan} {
				env.Trust(acc, USD(1000))
			}
			env.Close()

			carolFunds := tx.NewIssuedAmount(9_999_999_999_999_994, -15, "USD", gw.Address)
			carolGets := tx.NewIssuedAmount(9_999_999_999_999_995, -15, "USD", gw.Address)
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, carol, carolFunds).Build()))
			jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, bob, USD(10)).Build()))
			env.Close()
			if viaPayment {
				// bob's offer is better than carol's, so it is taken first.
				jtx.RequireTxSuccess(t, env.Submit(OfferCreate(bob, tx.NewXRPAmount(jtx.XRP(10)), USD(10)).Build()))
			}
			jtx.RequireTxSuccess(t, env.Submit(OfferCreate(carol, tx.NewXRPAmount(100_000_000_000_000_000), carolGets).Build()))
			env.Close()

			balance := env.Balance(alice)
			var result jtx.TxResult
			if viaPayment {
				result = env.Submit(payment.PayIssued(alice, dan, USD(20)).
					PathsCurrency("USD", gw).
					SendMax(tx.NewXRPAmount(jtx.XRP(5000))).
					Flags(paymentPkg.PaymentFlagPartialPayment).
					Build())
			} else {
				result = env.Submit(OfferCreate(alice, tx.NewIssuedAmount(1, -7, "USD", gw.Address), tx.NewXRPAmount(jtx.XRP(5000))).Build())
			}
			jtx.RequireTxClaimed(t, result, "tecINTERNAL")
			env.Close()

			require.Equal(t, balance-result.Fee, env.Balance(alice))
			RequireOfferCount(t, env, alice, 0)
			RequireOfferCount(t, env, carol, 1)
			if viaPayment {
				RequireOfferCount(t, env, bob, 1)
				jtx.RequireIOUBalance(t, env, dan, gw, "USD", 0)
			}
		})
	}
}
