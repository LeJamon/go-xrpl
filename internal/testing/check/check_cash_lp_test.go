package check_test

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	checkbuilder "github.com/LeJamon/go-xrpl/internal/testing/check"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestCheckCashLPMPTTransferPrecedence(t *testing.T) {
	for _, transferable := range []bool{false, true} {
		for _, frozenLPFix := range []bool{false, true} {
			for _, deliverMin := range []bool{false, true} {
				t.Run(fmt.Sprintf("transfer=%t/frozenLPFix=%t/deliverMin=%t", transferable, frozenLPFix, deliverMin), func(t *testing.T) {
					env := ammtest.NewAMMTestEnv(t)
					env.EnableFeature("MPTokensV2")
					if frozenLPFix {
						env.EnableFeature("fixFrozenLPTokenTransfer")
					} else {
						env.DisableFeature("fixFrozenLPTokenTransfer")
					}
					for _, account := range []*jtx.Account{env.GW, env.Alice} {
						env.FundAmount(account, uint64(jtx.XRP(30_000)))
					}
					env.FundAmount(env.Bob, env.ReserveBase()+env.BaseFee())
					env.Close()
					require.True(t, env.FeatureEnabled("MPTokensV2"))
					require.Equal(t, frozenLPFix, env.FeatureEnabled("fixFrozenLPTokenTransfer"))

					token := mpttest.NewMPTTesterNoFund(t, env.TestEnv, env.GW)
					flags := mpttest.TfMPTCanTrade
					if transferable {
						flags |= mpttest.TfMPTCanTransfer
					}
					token.Create(mpttest.CreateOpts{Flags: flags})
					token.Authorize(mpttest.AuthorizeOpts{Account: env.Alice})
					token.Pay(env.GW, env.Alice, 1_000)
					asset1, asset2 := ammtest.XRP(), tx.Asset{MPTIssuanceID: token.IssuanceID()}
					jtx.RequireTxSuccess(t, env.Submit(ammtest.AMMCreate(env.GW, ammtest.XRPAmount(10_000), token.MPTAmount(10_000)).Build()))
					env.Close()
					limit := env.LPTokenAmountFromLedger(asset1, asset2, 100_000)
					jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(env.Alice, limit).Build()))
					amount := env.LPTokenAmountFromLedger(asset1, asset2, 100)
					jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(env.GW, env.Alice, amount).Build()))
					env.Close()
					pool := env.ReadAMMAccount(asset1, asset2)
					require.NotNil(t, pool)
					require.Equal(t, "100", env.IOUBalance(env.Alice, pool, amount.Currency).Value())
					destinationLine := keylet.Line(env.Bob.ID, pool.ID, amount.Currency)
					exists, err := env.Ledger().Exists(destinationLine)
					require.NoError(t, err)
					require.False(t, exists)

					checkID := checkbuilder.GetCheckID(env.Alice, env.Seq(env.Alice))
					jtx.RequireTxSuccess(t, env.Submit(checkbuilder.CheckCreate(env.Alice, env.Bob, amount).Build()))
					env.Close()
					cash := checkbuilder.CheckCashAmount(env.Bob, checkID, amount).Build()
					if deliverMin {
						cash = checkbuilder.CheckCashDeliverMin(env.Bob, checkID, amount).Build()
					}
					balance, sequence, owners := env.Balance(env.Bob), env.Seq(env.Bob), env.OwnerCount(env.Alice)
					require.Less(t, balance, env.ReserveBase()+env.ReserveIncrement())
					want := jtx.TecPATH_PARTIAL
					if transferable {
						want = jtx.TecNO_LINE_INSUF_RESERVE
					}
					jtx.RequireTxClaimed(t, env.Submit(cash), want)
					env.Close()
					requireCheckMPTExists(t, env.TestEnv, checkID, true)
					require.Equal(t, "100", env.IOUBalance(env.Alice, pool, amount.Currency).Value())
					jtx.RequireOwnerCount(t, env.TestEnv, env.Alice, owners)
					jtx.RequireOwnerCount(t, env.TestEnv, env.Bob, 0)
					jtx.RequireBalance(t, env.TestEnv, env.Bob, balance-env.BaseFee())
					jtx.RequireSequence(t, env.TestEnv, env.Bob, sequence+1)
					exists, err = env.Ledger().Exists(destinationLine)
					require.NoError(t, err)
					require.False(t, exists)
				})
			}
		}
	}
}
