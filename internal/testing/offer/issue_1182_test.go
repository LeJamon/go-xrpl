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
		{name: "crosses"},
		{name: "globally_frozen_taker", frozen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)

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
			takerOwnerCountBefore := env.OwnerCount(taker)
			makerOwnerCountBefore := env.OwnerCount(maker)

			result = env.Submit(OfferCreate(
				taker,
				tx.NewXRPAmount(34_127),
				takerGets,
			).ImmediateOrCancel().Sell().Build())

			if tc.frozen {
				jtx.RequireTxClaimed(t, result, jtx.TecKILLED)
			} else {
				jtx.RequireTxSuccess(t, result)
			}
			RequireOfferCount(t, env, taker, 0)
			jtx.RequireOwnerCount(t, env, taker, takerOwnerCountBefore)
			jtx.RequireOwnerCount(t, env, maker, makerOwnerCountBefore)

			if tc.frozen {
				require.Equal(t, takerXRPBefore-env.BaseFee(), env.Balance(taker))
				require.Equal(t, makerXRPBefore, env.Balance(maker))
				jtx.RequireIOUBalance(t, env, taker, issuer, coreCurrency, 0.98745)
				jtx.RequireIOUBalance(t, env, maker, issuer, coreCurrency, 0)
				RequireOfferCount(t, env, maker, 1)

				offerAfter, err := env.LedgerEntry(offerKey)
				require.NoError(t, err)
				require.Equal(t, offerBefore, offerAfter)
				return
			}

			require.Equal(t, takerXRPBefore-env.BaseFee()+37_919, env.Balance(taker))
			require.Equal(t, makerXRPBefore-37_919, env.Balance(maker))
			jtx.RequireIOUBalance(t, env, taker, issuer, coreCurrency, 0)
			jtx.RequireIOUBalance(t, env, maker, issuer, coreCurrency, 0.98745)
			RequireOfferCount(t, env, maker, 1)
			RequireIsOffer(t, env, maker,
				core(3_372_252_095_660_040, -13),
				tx.NewXRPAmount(12_950_020),
			)
		})
	}
}
