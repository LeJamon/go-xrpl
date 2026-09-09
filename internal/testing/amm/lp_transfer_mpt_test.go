package amm_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	offertest "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestLPTokenMPTTransferCapability(t *testing.T) {
	for _, transferable := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			for _, frozenLPFix := range []bool{false, true} {
				t.Run(fmt.Sprintf("transfer=%t/reverse=%t/frozenLPFix=%t", transferable, reverse, frozenLPFix), func(t *testing.T) {
					env := ammtest.NewAMMTestEnv(t)
					env.EnableFeature("MPTokensV2")
					if frozenLPFix {
						env.EnableFeature("fixFrozenLPTokenTransfer")
					} else {
						env.DisableFeature("fixFrozenLPTokenTransfer")
					}
					for _, account := range []*jtx.Account{env.GW, env.Alice, env.Bob} {
						env.FundAmount(account, uint64(jtx.XRP(30_000)))
					}
					env.Close()
					token := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
					flags := mpttest.TfMPTCanTrade
					if transferable {
						flags |= mpttest.TfMPTCanTransfer
					}
					token.Create(mpttest.CreateOpts{Flags: flags})
					token.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
					token.Pay(env.GW, env.Alice, 1_000)
					asset1, asset2 := ammtest.XRP(), tx.Asset{MPTIssuanceID: token.IssuanceID()}
					amount1, amount2 := ammtest.XRPAmount(10_000), token.MPTAmount(10_000)
					if reverse {
						asset1, asset2 = asset2, asset1
						amount1, amount2 = amount2, amount1
					}
					jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMCreate(env.GW, amount1, amount2).Build()))
					env.Close()
					limit := env.LPTokenAmountFromLedger(asset1, asset2, 100_000)
					for _, holder := range []*jtx.Account{env.Alice, env.Bob} {
						jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(holder, limit).Build()))
					}
					env.Close()
					pool := env.ReadAMMAccount(asset1, asset2)
					require.NotNil(t, pool)
					beforeXRP := env.Balance(pool)
					beforeLP := env.ReadAMMData(asset1, asset2).LPTokenBalance.Value()
					jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(env.GW, env.Alice, env.LPTokenAmountFromLedger(asset1, asset2, 1_000)).Build()))
					env.Close()
					jtx.RequireIOUBalance(t, env.TestEnv, env.Alice, pool, limit.Currency, 1_000)

					balance, sequence, owners := env.Balance(env.Alice), env.Seq(env.Alice), env.OwnerCount(env.Alice)
					result := env.Submit(payment.PayIssued(env.Alice, env.Bob, env.LPTokenAmountFromLedger(asset1, asset2, 100)).Build())
					wantAlice, wantBob := 1_000.0, 0.0
					if transferable {
						jtx.RequireTxSuccess(t, result)
						wantAlice, wantBob = 900, 100
					} else {
						jtx.RequireTxClaimed(t, result, jtx.TecNO_AUTH)
					}
					jtx.RequireIOUBalance(t, env.TestEnv, env.Alice, pool, limit.Currency, wantAlice)
					jtx.RequireIOUBalance(t, env.TestEnv, env.Bob, pool, limit.Currency, wantBob)
					jtx.RequireBalance(t, env.TestEnv, env.Alice, balance-env.BaseFee())
					jtx.RequireSequence(t, env.TestEnv, env.Alice, sequence+1)
					jtx.RequireOwnerCount(t, env.TestEnv, env.Alice, owners)
					env.Close()

					balance, sequence = env.Balance(env.Alice), env.Seq(env.Alice)
					result = env.Submit(offertest.OfferCreate(env.Alice, ammtest.XRPAmount(10), env.LPTokenAmountFromLedger(asset1, asset2, 10)).Passive().Build())
					if transferable {
						jtx.RequireTxSuccess(t, result)
						jtx.RequireOffers(t, env.TestEnv, env.Alice, 1)
						jtx.RequireOwnerCount(t, env.TestEnv, env.Alice, owners+1)
					} else {
						jtx.RequireTxClaimed(t, result, jtx.TecUNFUNDED_OFFER)
						jtx.RequireOffers(t, env.TestEnv, env.Alice, 0)
						jtx.RequireOwnerCount(t, env.TestEnv, env.Alice, owners)
					}
					jtx.RequireBalance(t, env.TestEnv, env.Alice, balance-env.BaseFee())
					jtx.RequireSequence(t, env.TestEnv, env.Alice, sequence+1)
					require.Equal(t, beforeXRP, env.Balance(pool))
					require.Equal(t, beforeLP, env.ReadAMMData(asset1, asset2).LPTokenBalance.Value())
					token.RequireMPTokenAmount(pool, 10_000)
					token.RequireMPTokenAmount(env.Alice, 1_000)
				})
			}
		}
	}
}
