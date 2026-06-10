// AMM-on-frozen-trust-line tests ported from rippled's Freeze_test.cpp
// testAMMWhenFreeze. Lives in package payment_test (not payment) because
// the AMM builders in internal/testing/amm import this package's payment
// builders — an in-package test importing them would form an import cycle.
package payment_test

import (
	"testing"

	xrplgoTesting "github.com/LeJamon/go-xrpl/internal/testing"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
)

// TestFreeze_AMMWhenFrozen tests AMM payments on frozen trust lines.
// From rippled: testAMMWhenFreeze
func TestFreeze_AMMWhenFrozen(t *testing.T) {
	setLineFreeze := func(t *testing.T, env *xrplgoTesting.TestEnv, issuer, holder *xrplgoTesting.Account, flags uint32) {
		t.Helper()
		ts := trustset.TrustLine(issuer, "USD", holder, "0").BuildTrustSet()
		ts.SetFlags(flags)
		xrplgoTesting.RequireTxSuccess(t, env.Submit(ts))
		env.Close()
	}

	run := func(t *testing.T, deepFreeze bool) {
		env := xrplgoTesting.NewTestEnv(t)
		if deepFreeze {
			env.EnableFeature("DeepFreeze")
		}

		G1 := xrplgoTesting.NewAccount("G1")
		A1 := xrplgoTesting.NewAccount("A1")
		A2 := xrplgoTesting.NewAccount("A2")

		env.FundAmount(G1, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(A1, uint64(xrplgoTesting.XRP(10000)))
		env.FundAmount(A2, uint64(xrplgoTesting.XRP(10000)))
		env.Close()

		xrplgoTesting.RequireTxSuccess(t, env.Submit(trustset.TrustLine(A1, "USD", G1, "10000").Build()))
		xrplgoTesting.RequireTxSuccess(t, env.Submit(trustset.TrustLine(A2, "USD", G1, "10000").Build()))
		env.Close()

		usd1000 := tx.NewIssuedAmountFromFloat64(1000, "USD", G1.Address)
		xrplgoTesting.RequireTxSuccess(t, env.Submit(paytest.PayIssued(G1, A1, usd1000).Build()))
		xrplgoTesting.RequireTxSuccess(t, env.Submit(paytest.PayIssued(G1, A2, usd1000).Build()))
		env.Close()

		// G1 creates an AMM with XRP(1000) and USD(1000).
		result := env.Submit(ammtest.AMMCreate(G1,
			tx.NewXRPAmount(xrplgoTesting.XRP(1000)), usd1000).Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// Freeze G1→A1's USD line (deep freeze additionally blocks
		// receiving USD).
		freezeFlags := trustsettx.TrustSetFlagSetFreeze
		clearFlags := trustsettx.TrustSetFlagClearFreeze
		if deepFreeze {
			freezeFlags |= trustsettx.TrustSetFlagSetDeepFreeze
			clearFlags |= trustsettx.TrustSetFlagClearDeepFreeze
		}
		setLineFreeze(t, env, G1, A1, freezeFlags)

		usd10 := tx.NewIssuedAmountFromFloat64(10, "USD", G1.Address)
		usd11 := tx.NewIssuedAmountFromFloat64(11, "USD", G1.Address)
		xrp11 := tx.NewXRPAmount(xrplgoTesting.XRP(11))

		// A1 can still use XRP to make a payment through the AMM.
		result = env.Submit(paytest.PayIssued(A1, A2, usd10).
			PathsCurrency("USD", G1).
			SendMax(xrp11).
			NoDirectRipple().
			Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		// A1 cannot spend frozen USD through the AMM.
		result = env.Submit(paytest.Pay(A1, A2, uint64(xrplgoTesting.XRP(10))).
			PathsCurrency("XRP", nil).
			SendMax(usd11).
			NoDirectRipple().
			Build())
		xrplgoTesting.RequireTxFail(t, result, xrplgoTesting.TecPATH_DRY)
		env.Close()

		// Receiving USD: blocked only by deep freeze.
		result = env.Submit(paytest.PayIssued(A2, A1, usd10).
			PathsCurrency("USD", G1).
			SendMax(xrp11).
			NoDirectRipple().
			Build())
		if deepFreeze {
			xrplgoTesting.RequireTxFail(t, result, xrplgoTesting.TecPATH_DRY)
		} else {
			xrplgoTesting.RequireTxSuccess(t, result)
		}
		env.Close()

		// A1 can still receive XRP payments funded by A2's USD.
		result = env.Submit(paytest.Pay(A2, A1, uint64(xrplgoTesting.XRP(10))).
			PathsCurrency("XRP", nil).
			SendMax(usd11).
			NoDirectRipple().
			Build())
		xrplgoTesting.RequireTxSuccess(t, result)
		env.Close()

		setLineFreeze(t, env, G1, A1, clearFlags)
	}

	t.Run("Freeze", func(t *testing.T) { run(t, false) })
	t.Run("DeepFreeze", func(t *testing.T) { run(t, true) })
}
