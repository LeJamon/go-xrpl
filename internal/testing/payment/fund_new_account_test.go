package payment

import (
	"testing"

	xrplgoTesting "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestRipplePayment_NewDest covers the missing-destination branching on the
// cross-currency-to-XRP (flow) path, where the delivered Amount is native XRP
// but the payment rides the order book via SendMax.
// Reference: rippled Payment.cpp:296-332 (preclaim) + :407-419 (doApply).
func TestRipplePayment_NewDest(t *testing.T) {
	// telNO_DST_PARTIAL: an open-ledger partial payment cannot fund a new
	// account. The check fires before any liquidity is consumed, so no order
	// book is needed.
	t.Run("partial payment to new account -> telNO_DST_PARTIAL", func(t *testing.T) {
		env := xrplgoTesting.NewTestEnv(t)
		gw := xrplgoTesting.NewAccount("gateway")
		alice := xrplgoTesting.NewAccount("alice")
		carol := xrplgoTesting.NewAccount("carol") // never funded
		env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
		env.Close()

		usd := tx.NewIssuedAmountFromFloat64(300, "USD", gw.Address)
		result := env.Submit(Pay(alice, carol, uint64(xrplgoTesting.XRP(250))).
			SendMax(usd).
			PartialPayment().
			Build())
		xrplgoTesting.RequireTxFail(t, result, "telNO_DST_PARTIAL")
		require.False(t, env.Exists(carol), "carol must not be created")
	})

	// tecNO_DST_INSUF_XRP: a non-partial payment that delivers less than the
	// account reserve cannot create the account. Also fires before liquidity.
	t.Run("below-reserve delivery to new account -> tecNO_DST_INSUF_XRP", func(t *testing.T) {
		env := xrplgoTesting.NewTestEnv(t)
		gw := xrplgoTesting.NewAccount("gateway")
		alice := xrplgoTesting.NewAccount("alice")
		carol := xrplgoTesting.NewAccount("carol") // never funded
		env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
		env.Close()

		usd := tx.NewIssuedAmountFromFloat64(300, "USD", gw.Address)
		// 100 XRP < 200 XRP base reserve.
		result := env.Submit(Pay(alice, carol, uint64(xrplgoTesting.XRP(100))).
			SendMax(usd).
			Build())
		xrplgoTesting.RequireTxClaimed(t, result, "tecNO_DST_INSUF_XRP")
		require.False(t, env.Exists(carol), "carol must not be created")
	})

	// Success: a non-partial cross-currency payment delivering at-or-above the
	// reserve creates the destination and funds it with the delivered XRP.
	t.Run("cross-currency delivery funds a new account", func(t *testing.T) {
		env := xrplgoTesting.NewTestEnv(t)
		gw := xrplgoTesting.NewAccount("gateway")
		alice := xrplgoTesting.NewAccount("alice")
		mm := xrplgoTesting.NewAccount("marketmaker")
		carol := xrplgoTesting.NewAccount("carol") // never funded
		env.FundAmount(gw, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(alice, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(mm, uint64(xrplgoTesting.XRP(10000)))
		env.Close()

		// alice and the market maker both hold USD.
		result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		result = env.Submit(trustset.TrustLine(mm, "USD", gw, "1000").Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		usd300 := tx.NewIssuedAmountFromFloat64(300, "USD", gw.Address)
		result = env.Submit(PayIssued(gw, alice, usd300).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Market maker offers to sell XRP for USD (1:1).
		xrp300 := tx.NewXRPAmount(xrplgoTesting.XRP(300))
		result = env.CreateOffer(mm, xrp300, usd300) // TakerGets=XRP, TakerPays=USD
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, env.Exists(carol), "carol must not exist before payment")

		// alice delivers 250 XRP to the brand-new carol, paying in USD.
		result = env.Submit(Pay(alice, carol, uint64(xrplgoTesting.XRP(250))).
			SendMax(usd300).
			PathsXRP().
			Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		require.True(t, env.Exists(carol), "carol must be created by the payment")
		require.Equal(t, uint64(xrplgoTesting.XRP(250)), env.Balance(carol),
			"carol should be funded with the delivered XRP")
	})
}
