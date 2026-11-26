package offer

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// A cross that moves funds but is too small to change the taker's remainder at
// 16 significant digits does not count as crossed.
func TestIssue2275_CrossBelowRemainderPrecision(t *testing.T) {
	setup := func(t *testing.T, takerFunding func(*jtx.TestEnv) uint64) (*jtx.TestEnv, *jtx.Account, *jtx.Account) {
		t.Helper()
		env := jtx.NewTestEnv(t)
		gw := jtx.NewAccount("gateway")
		maker := jtx.NewAccount("maker")
		taker := jtx.NewAccount("taker")

		env.FundAmount(gw, uint64(jtx.XRP(10_000)))
		env.FundAmount(maker, uint64(jtx.XRP(10_000)))
		env.FundAmount(taker, takerFunding(env))
		env.Close()

		env.Trust(maker, jtx.USD(gw, 1_000))
		env.Trust(taker, jtx.USD(gw, 1_000))
		env.Trust(taker, jtx.EUR(gw, 1_000))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, maker, jtx.USD(gw, 10)).Build()))
		jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, taker, jtx.EUR(gw, 10)).Build()))
		env.Close()

		jtx.RequireTxSuccess(t, env.Submit(OfferCreate(maker, jtx.EUR(gw, 1), jtx.USD(gw, 1)).Build()))
		env.Close()
		return env, gw, taker
	}

	huge := func(gw *jtx.Account, currency string) tx.Amount {
		return jtx.IssuedCurrencyFromMantissa(gw, currency, 1_000_000_000_000_000, 15)
	}

	t.Run("ImmediateOrCancel is killed", func(t *testing.T) {
		env, gw, taker := setup(t, func(*jtx.TestEnv) uint64 { return uint64(jtx.XRP(10_000)) })

		result := env.Submit(OfferCreate(taker, huge(gw, "USD"), huge(gw, "EUR")).ImmediateOrCancel().Build())
		jtx.RequireTxClaimed(t, result, jtx.TecKILLED)

		jtx.RequireIOUBalance(t, env, taker, gw, "USD", 0)
		jtx.RequireIOUBalance(t, env, taker, gw, "EUR", 10)
		RequireOfferCount(t, env, taker, 0)
	})

	t.Run("Under-reserve offer is not placed", func(t *testing.T) {
		env, gw, taker := setup(t, func(env *jtx.TestEnv) uint64 { return Reserve(env, 2) + env.BaseFee() })

		result := env.Submit(OfferCreate(taker, huge(gw, "USD"), huge(gw, "EUR")).Build())
		jtx.RequireTxClaimed(t, result, jtx.TecINSUF_RESERVE_OFFER)

		jtx.RequireIOUBalance(t, env, taker, gw, "USD", 0)
		jtx.RequireIOUBalance(t, env, taker, gw, "EUR", 10)
		RequireOfferCount(t, env, taker, 0)
	})
}
