package offer

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestOffer_TickSizeZeroQuality pins tick-size rounding of an offer whose
// quality is 0 because in/out underflows. rippled rounds a buy by dividing
// TakerPays by the rounded rate, and dividing by a zero rate throws, so the
// transaction is discarded with tefEXCEPTION. A sell multiplies by the zero
// rate instead, rounds TakerPays to zero and succeeds without placing the offer.
func TestOffer_TickSizeZeroQuality(t *testing.T) {
	env := newEnvWithFeatures(t, nil)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	env.FundAmount(gw, uint64(jtx.XRP(100000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(gw).TickSize(5).Build()))
	env.Trust(alice, jtx.USD(gw, 1000000))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, alice, jtx.USD(gw, 1000000)).Build()))
	env.Close()

	takerPays := tx.NewIssuedAmount(1000000000000000, -96, "USD", gw.Address)
	takerGets := tx.NewXRPAmount(10_000_000_000)

	t.Run("buy", func(t *testing.T) {
		seq := env.Seq(alice)
		jtx.RequireTxFail(t, env.Submit(OfferCreate(alice, takerPays, takerGets).Build()), "tefEXCEPTION")
		env.Close()
		require.Equal(t, seq, env.Seq(alice), "a tef result must not consume the sequence")
		require.Nil(t, GetOffer(env, alice, seq))
	})

	t.Run("sell", func(t *testing.T) {
		seq := env.Seq(alice)
		jtx.RequireTxSuccess(t, env.Submit(OfferCreate(alice, takerPays, takerGets).Sell().Build()))
		env.Close()
		require.Equal(t, seq+1, env.Seq(alice))
		require.Nil(t, GetOffer(env, alice, seq), "an offer rounded to zero is not placed")
	})
}
