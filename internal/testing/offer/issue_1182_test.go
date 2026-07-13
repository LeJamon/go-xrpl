package offer

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestOffer_IoCSell_GlobalFrozenTaker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frozen bool
	}{
		{name: "crosses", frozen: false},
		{name: "globally_frozen_taker", frozen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("ImmediateOfferKilled")

			const coreCurrency = "434F524500000000000000000000000000000000"
			issuer := jtx.NewAccount("core issuer")
			taker := jtx.NewAccount("taker")
			maker := jtx.NewAccount("maker")
			core := func(mantissa int64, exponent int) tx.Amount {
				return tx.NewIssuedAmount(mantissa, exponent, coreCurrency, issuer.Address)
			}

			env.FundAmount(issuer, uint64(jtx.XRP(1_000)))
			env.FundAmount(taker, uint64(jtx.XRP(1_000)))
			env.FundAmount(maker, uint64(jtx.XRP(1_000)))
			env.Close()

			env.Trust(taker, core(1_000_000_000_000_000, -11))
			env.Trust(maker, core(1_000_000_000_000_000, -11))
			takerGets := core(9_874_500_000_000_000, -16)
			result := env.Submit(payment.PayIssued(issuer, taker, takerGets).Build())
			jtx.RequireTxSuccess(t, result)

			makerOfferSeq := env.Seq(maker)
			result = env.Submit(OfferCreate(
				maker,
				core(3_382_126_595_660_040, -13),
				tx.NewXRPAmount(12_987_939),
			).Build())
			jtx.RequireTxSuccess(t, result)

			if tc.frozen {
				env.EnableGlobalFreeze(taker)
			}

			offerKey := keylet.Offer(maker.ID, makerOfferSeq)
			offerBefore, err := env.LedgerEntry(offerKey)
			require.NoError(t, err)
			takerXRPBefore := env.Balance(taker)
			makerXRPBefore := env.Balance(maker)

			result = env.Submit(OfferCreate(
				taker,
				tx.NewXRPAmount(34_127),
				takerGets,
			).ImmediateOrCancel().Sell().Build())

			if tc.frozen {
				jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
				require.Equal(t, takerXRPBefore-env.BaseFee(), env.Balance(taker))
				require.Equal(t, makerXRPBefore, env.Balance(maker))
				jtx.RequireIOUBalance(t, env, taker, issuer, coreCurrency, 0.98745)
				jtx.RequireIOUBalance(t, env, maker, issuer, coreCurrency, 0)

				offerAfter, err := env.LedgerEntry(offerKey)
				require.NoError(t, err)
				require.Equal(t, offerBefore, offerAfter)
				return
			}

			jtx.RequireTxSuccess(t, result)
			require.Equal(t, takerXRPBefore-env.BaseFee()+37_919, env.Balance(taker))
			require.Equal(t, makerXRPBefore-37_919, env.Balance(maker))
			jtx.RequireIOUBalance(t, env, taker, issuer, coreCurrency, 0)
			jtx.RequireIOUBalance(t, env, maker, issuer, coreCurrency, 0.98745)

			offerAfter, err := env.LedgerEntry(offerKey)
			require.NoError(t, err)
			require.NotEqual(t, offerBefore, offerAfter)
		})
	}
}
