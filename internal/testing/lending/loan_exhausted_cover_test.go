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

func TestLoanDefaultExhaustedCoverAndBrokerDeletion(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			for _, kind := range []string{"IOU", "MPT", "MPT require auth"} {
				t.Run(fmt.Sprintf("cash=%t/cleanup=%t/%s", cash, cleanup, kind), func(t *testing.T) {
					env := newLegacyLendingEnvWithCleanup(t, cleanup)
					if cash {
						env.EnableFeature("LendingProtocolV1_1")
						env.Close()
					}
					assetKind, flags := kind, uint32(0)
					if kind == "MPT require auth" {
						assetKind, flags = "MPT", mpttest.TfMPTRequireAuth
					}
					f := newLoanSetAssetFixtureWithEnv(t, env, assetKind, cash, flags)
					f.createHolding(f.owner)
					f.createHolding(f.borrower)
					f.fundHolding(f.owner, 1000)

					brokerSequence := env.Seq(f.owner)
					brokerSet := lending.NewLoanBrokerSet(f.owner.Address, f.vaultID)
					coverRate := uint32(100_000)
					brokerSet.CoverRateMinimum = &coverRate
					brokerSet.CoverRateLiquidation = &coverRate
					jtx.RequireTxSuccess(t, env.Submit(brokerSet))
					bid := brokerID(f.owner, brokerSequence)
					brokerKey := keylet.LoanBroker(f.owner.AccountID(), brokerSequence)
					brokerFields := decodeLendingEntry(t, env, brokerKey)
					pseudoID := lendingAccountID(t, brokerFields, "LoanBroker")
					jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(f.owner.Address, bid, f.amount(1000))))

					loanSet := lending.NewLoanSet(f.borrower.Address, bid, "1000")
					interval, payments, grace := uint32(60), uint32(2), uint32(60)
					loanSet.PaymentInterval, loanSet.PaymentTotal, loanSet.GracePeriod = &interval, &payments, &grace
					loanSet.Counterparty = f.owner.Address
					loanSet.Fee, loanSet.SigningPubKey = "20", f.borrower.PublicKeyHex()
					signature, err := txsign.SignCounterparty(loanSet, f.owner.PublicKeyHex(), "00"+f.owner.PrivateKeyHex())
					require.NoError(t, err)
					loanSet.CounterpartySignature = signature
					jtx.RequireTxSuccess(t, env.Submit(loanSet))
					loanKey := keylet.Loan(brokerKey.Key, 1)
					loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))

					manage := func(flags uint32) jtx.TxResult {
						transaction := lending.NewLoanManage(f.owner.Address, loanID)
						transaction.Flags = &flags
						return env.Submit(transaction)
					}
					if cleanup {
						loanBefore := decodeLendingEntry(t, env, loanKey)
						due, ok := loanBefore["NextPaymentDueDate"].(uint32)
						require.True(t, ok)
						env.CloseToParentCloseTime(due - 1)
						jtx.RequireTxClaimed(t, manage(lending.TfLoanImpair), "tecTOO_SOON")
						env.CloseToParentCloseTime(due)
						jtx.RequireTxClaimed(t, manage(lending.TfLoanImpair), "tecTOO_SOON")
						env.CloseToParentCloseTime(due + 1)
					}
					jtx.RequireTxSuccess(t, manage(lending.TfLoanImpair))
					loan := decodeLendingEntry(t, env, loanKey)
					vaultBefore := decodeLendingEntry(t, env, f.vaultKey)
					brokerBefore := decodeLendingEntry(t, env, brokerKey)
					balance, sequence := env.Balance(f.owner), env.Seq(f.owner)
					result := manage(lending.TfLoanImpair)
					jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
					require.Equal(t, balance-result.Fee, env.Balance(f.owner))
					require.Equal(t, sequence+1, env.Seq(f.owner))
					require.Equal(t, loan, decodeLendingEntry(t, env, loanKey))
					require.Equal(t, vaultBefore, decodeLendingEntry(t, env, f.vaultKey))
					require.Equal(t, brokerBefore, decodeLendingEntry(t, env, brokerKey))

					nextDue, ok := loan["NextPaymentDueDate"].(uint32)
					require.True(t, ok)
					env.CloseToParentCloseTime(nextDue + grace + 1)
					jtx.RequireTxSuccess(t, manage(lending.TfLoanDefault))
					vaultAfter := decodeLendingEntry(t, env, f.vaultKey)
					brokerAfter := decodeLendingEntry(t, env, brokerKey)
					require.True(t, lossNumber(t, brokerAfter, "CoverAvailable").IsZero())
					require.True(t, lossNumber(t, brokerAfter, "DebtTotal").IsZero())
					require.True(t, lossNumber(t, vaultAfter, "LossUnrealized").IsZero())
					assertLendingField(t, "Vault", vaultAfter, "AssetsTotal", "10000")
					assertLendingField(t, "Vault", vaultAfter, "AssetsAvailable", "10000")
					loan = decodeLendingEntry(t, env, loanKey)
					balance, sequence = env.Balance(f.owner), env.Seq(f.owner)
					result = manage(lending.TfLoanDefault)
					jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
					require.Equal(t, balance-result.Fee, env.Balance(f.owner))
					require.Equal(t, sequence+1, env.Seq(f.owner))
					require.Equal(t, loan, decodeLendingEntry(t, env, loanKey))
					require.Equal(t, vaultAfter, decodeLendingEntry(t, env, f.vaultKey))
					require.Equal(t, brokerAfter, decodeLendingEntry(t, env, brokerKey))

					result = env.Submit(lending.NewLoanDelete(f.owner.Address, loanID))
					jtx.RequireTxSuccess(t, result)
					require.False(t, env.LedgerEntryExists(loanKey))
					requireLoanDeletedNode(t, result, loanKey)
					ownerCount := env.OwnerCount(f.owner)
					result = env.Submit(lending.NewLoanBrokerDelete(f.owner.Address, bid))
					jtx.RequireTxSuccess(t, result)
					require.False(t, env.LedgerEntryExists(brokerKey))
					require.False(t, env.LedgerEntryExists(keylet.Account(pseudoID)))
					require.Equal(t, ownerCount-2, env.OwnerCount(f.owner))
					requireLoanDeletedNode(t, result, brokerKey)
					requireLoanDeletedNode(t, result, keylet.Account(pseudoID))
					require.Equal(t, vaultAfter, decodeLendingEntry(t, env, f.vaultKey))
					require.True(t, env.LedgerEntryExists(keylet.LoanBrokerByID(f.brokerKey)))
				})
			}
		}
	}
}

func requireLoanDeletedNode(t *testing.T, result jtx.TxResult, key keylet.Keylet) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	index := strings.ToUpper(hex.EncodeToString(key.Key[:]))
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerIndex == index {
			require.Equal(t, "DeletedNode", node.NodeType)
			return
		}
	}
	t.Fatalf("deleted ledger entry %s missing from metadata", index)
}
