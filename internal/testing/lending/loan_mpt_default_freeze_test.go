package lending_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLoanDefaultMPTFreezeAmendments(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			for _, mptV2 := range []bool{false, true} {
				for _, global := range []bool{false, true} {
					t.Run(fmt.Sprintf("cash=%t/cleanup=%t/MPTokensV2=%t/global=%t", cash, cleanup, mptV2, global), func(t *testing.T) {
						env := newLegacyLendingEnvWithCleanup(t, cleanup)
						if cash {
							env.EnableFeature("LendingProtocolV1_1")
						}
						if mptV2 {
							env.EnableFeature("MPTokensV2")
						} else {
							env.DisableFeature("MPTokensV2")
						}
						env.Close()
						f := newLoanSetAssetFixtureWithEnv(t, env, "MPT", cash, mpttest.TfMPTCanLock)
						f.createHolding(f.owner)
						f.fundHolding(f.owner, 10_000)

						brokerSequence := env.Seq(f.owner)
						brokerSet := lending.NewLoanBrokerSet(f.owner.Address, f.vaultID)
						coverRate := uint32(100_000)
						brokerSet.CoverRateMinimum = &coverRate
						brokerSet.CoverRateLiquidation = &coverRate
						jtx.RequireTxSuccess(t, env.Submit(brokerSet))
						f.brokerID = brokerID(f.owner, brokerSequence)
						brokerKey := keylet.LoanBroker(f.owner.AccountID(), brokerSequence)
						f.brokerKey = brokerKey.Key
						jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(f.owner.Address, f.brokerID, f.amount(10_000))))
						loanSet := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000")
						interval, payments, gracePeriod := uint32(60), uint32(2), uint32(60)
						loanSet.PaymentInterval, loanSet.PaymentTotal, loanSet.GracePeriod = &interval, &payments, &gracePeriod
						loanSet.Counterparty = f.owner.Address
						loanSet.Fee, loanSet.SigningPubKey = "20", f.borrower.PublicKeyHex()
						signature, err := txsign.SignCounterpartyWithRules(loanSet, f.owner.PublicKeyHex(), "00"+f.owner.PrivateKeyHex(), env.Ledger().Rules())
						require.NoError(t, err)
						loanSet.CounterpartySignature = signature
						jtx.RequireTxSuccess(t, env.Submit(loanSet))
						loanKey := keylet.Loan(f.brokerKey, 1)
						loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
						brokerFields := decodeLendingEntry(t, env, brokerKey)
						vaultFields := decodeLendingEntry(t, env, f.vaultKey)
						brokerAddress, ok := brokerFields["Account"].(string)
						require.True(t, ok)
						vaultAddress, ok := vaultFields["Account"].(string)
						require.True(t, ok)
						brokerAccount := jtx.NewAccountWithAddress("broker", brokerAddress)
						vaultAccount := jtx.NewAccountWithAddress("vault", vaultAddress)
						if global {
							f.token.Set(mpttest.SetOpts{Flags: mpttest.TfMPTLock})
						} else {
							f.token.Set(mpttest.SetOpts{Holder: brokerAccount, Flags: mpttest.TfMPTLock})
							f.token.Set(mpttest.SetOpts{Holder: vaultAccount, Flags: mpttest.TfMPTLock})
						}

						loanBefore := decodeLendingEntry(t, env, loanKey)
						due, ok := loanBefore["NextPaymentDueDate"].(uint32)
						require.True(t, ok)
						grace, ok := loanBefore["GracePeriod"].(uint32)
						require.True(t, ok)
						env.CloseToParentCloseTime(due + grace + 1)
						brokerHolding := decodeLendingEntry(t, env, f.holdingKey(brokerAccount))
						vaultHolding := decodeLendingEntry(t, env, f.holdingKey(vaultAccount))
						balance, sequence := env.Balance(f.owner), env.Seq(f.owner)
						manage := lending.NewLoanManage(f.owner.Address, loanID)
						flags := lending.TfLoanDefault
						manage.Flags, manage.Fee = &flags, "20"
						result := env.Submit(manage)
						require.Equal(t, balance-20, env.Balance(f.owner))
						require.Equal(t, sequence+1, env.Seq(f.owner))
						if mptV2 && !cleanup {
							jtx.RequireTxClaimed(t, result, jtx.TecINVARIANT_FAILED)
							require.Equal(t, loanBefore, decodeLendingEntry(t, env, loanKey))
							require.Equal(t, brokerFields, decodeLendingEntry(t, env, brokerKey))
							require.Equal(t, vaultFields, decodeLendingEntry(t, env, f.vaultKey))
							require.Equal(t, brokerHolding, decodeLendingEntry(t, env, f.holdingKey(brokerAccount)))
							require.Equal(t, vaultHolding, decodeLendingEntry(t, env, f.holdingKey(vaultAccount)))
							return
						}
						jtx.RequireTxSuccess(t, result)
						brokerAfter := decodeLendingEntry(t, env, brokerKey)
						vaultAfter := decodeLendingEntry(t, env, f.vaultKey)
						require.True(t, lossNumber(t, brokerAfter, "DebtTotal").IsZero())
						coverUsed := lossNumber(t, brokerFields, "CoverAvailable").Sub(lossNumber(t, brokerAfter, "CoverAvailable"))
						require.True(t, coverUsed.Signum() > 0)
						recovered := lossNumber(t, vaultAfter, "AssetsAvailable").Sub(lossNumber(t, vaultFields, "AssetsAvailable"))
						require.True(t, coverUsed.Equal(recovered))
					})
				}
			}
		}
	}
}
