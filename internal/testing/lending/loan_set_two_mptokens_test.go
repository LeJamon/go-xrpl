package lending_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func newTwoHoldingLoanEnv(t *testing.T, cashBasis, cleanup bool) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.EnableFeature("LendingProtocol")
	if cashBasis {
		env.EnableFeature("LendingProtocolV1_1")
	} else {
		env.DisableFeature("LendingProtocolV1_1")
	}
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()

	require.True(t, env.FeatureEnabled("LendingProtocol"))
	require.Equal(t, cashBasis, env.FeatureEnabled("LendingProtocolV1_1"))
	require.Equal(t, cleanup, env.FeatureEnabled("fixCleanup3_4_0"))
	return env
}

func loanSetMetadataNode(result jtx.TxResult, key keylet.Keylet) *tx.AffectedNode {
	index := strings.ToUpper(hex.EncodeToString(key.Key[:]))
	for i := range result.Metadata.AffectedNodes {
		node := &result.Metadata.AffectedNodes[i]
		if node.LedgerIndex == index {
			return node
		}
	}
	return nil
}

func requireLoanSetAccountMetadata(t *testing.T, node *tx.AffectedNode, beforeBalance uint64, beforeSequence, beforeOwnerCount uint32, fee uint64, ownerDelta int) {
	t.Helper()
	require.NotNil(t, node)
	require.Equal(t, "ModifiedNode", node.NodeType)
	require.Equal(t, "AccountRoot", node.LedgerEntryType)
	if fee != 0 {
		require.Equal(t, fmt.Sprint(beforeBalance), node.PreviousFields["Balance"])
		require.Equal(t, fmt.Sprint(beforeBalance-fee), node.FinalFields["Balance"])
		require.Equal(t, beforeSequence, node.PreviousFields["Sequence"])
		require.Equal(t, beforeSequence+1, node.FinalFields["Sequence"])
	} else {
		require.NotContains(t, node.PreviousFields, "Balance")
		require.NotContains(t, node.PreviousFields, "Sequence")
		require.Equal(t, fmt.Sprint(beforeBalance), node.FinalFields["Balance"])
		require.Equal(t, beforeSequence, node.FinalFields["Sequence"])
	}
	require.Equal(t, beforeOwnerCount, node.PreviousFields["OwnerCount"])
	require.Equal(t, uint32(int64(beforeOwnerCount)+int64(ownerDelta)), node.FinalFields["OwnerCount"])
}

func requireLoanSetFailureMetadata(t *testing.T, result jtx.TxResult, account *jtx.Account, beforeBalance uint64, beforeSequence uint32) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	require.Equal(t, jtx.TecINVARIANT_FAILED, result.Metadata.TransactionResult.String())
	require.Len(t, result.Metadata.AffectedNodes, 1)
	node := &result.Metadata.AffectedNodes[0]
	require.Equal(t, "ModifiedNode", node.NodeType)
	require.Equal(t, "AccountRoot", node.LedgerEntryType)
	accountKey := keylet.Account(account.AccountID())
	require.Equal(t, strings.ToUpper(hex.EncodeToString(accountKey.Key[:])), node.LedgerIndex)
	require.Equal(t, fmt.Sprint(beforeBalance), node.PreviousFields["Balance"])
	require.Equal(t, fmt.Sprint(beforeBalance-result.Fee), node.FinalFields["Balance"])
	require.Equal(t, beforeSequence, node.PreviousFields["Sequence"])
	require.Equal(t, beforeSequence+1, node.FinalFields["Sequence"])
}

func requireLoanSetLedgerRollback(t *testing.T, f *loanSetAssetFixture, before map[[32]byte][]byte) {
	t.Helper()
	after := loanSetLedgerState(t, f.env)
	delete(before, keylet.Account(f.borrower.AccountID()).Key)
	delete(after, keylet.Account(f.borrower.AccountID()).Key)
	require.Equal(t, before, after)
}

func requireLoanSetSuccessMetadata(t *testing.T, result jtx.TxResult, f *loanSetAssetFixture, loanKey keylet.Keylet, vaultAccount *jtx.Account, beforeBorrowerBalance uint64, beforeBorrowerSequence, beforeBorrowerOwnerCount uint32, beforeOwnerBalance uint64, beforeOwnerSequence, beforeOwnerOwnerCount uint32) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	require.Equal(t, jtx.TesSUCCESS, result.Metadata.TransactionResult.String())
	requireLoanSetAccountMetadata(t, loanSetMetadataNode(result, keylet.Account(f.borrower.AccountID())), beforeBorrowerBalance, beforeBorrowerSequence, beforeBorrowerOwnerCount, result.Fee, 2)
	requireLoanSetAccountMetadata(t, loanSetMetadataNode(result, keylet.Account(f.owner.AccountID())), beforeOwnerBalance, beforeOwnerSequence, beforeOwnerOwnerCount, 0, 1)

	loanNode := loanSetMetadataNode(result, loanKey)
	require.NotNil(t, loanNode)
	require.Equal(t, "CreatedNode", loanNode.NodeType)
	require.Equal(t, "Loan", loanNode.LedgerEntryType)
	require.Equal(t, f.borrower.Address, loanNode.NewFields["Borrower"])
	require.Equal(t, "1", loanNode.NewFields["LoanOriginationFee"])
	require.Equal(t, "1000", loanNode.NewFields["PrincipalOutstanding"])
	require.Equal(t, uint32(1), loanNode.NewFields["PaymentRemaining"])

	brokerNode := loanSetMetadataNode(result, keylet.Keylet{Key: f.brokerKey})
	require.NotNil(t, brokerNode)
	require.Equal(t, "ModifiedNode", brokerNode.NodeType)
	require.Equal(t, "LoanBroker", brokerNode.LedgerEntryType)
	require.Equal(t, "1000", brokerNode.FinalFields["DebtTotal"])
	require.Equal(t, uint32(2), brokerNode.FinalFields["LoanSequence"])
	require.Equal(t, uint32(1), brokerNode.FinalFields["OwnerCount"])

	vaultNode := loanSetMetadataNode(result, f.vaultKey)
	require.NotNil(t, vaultNode)
	require.Equal(t, "ModifiedNode", vaultNode.NodeType)
	require.Equal(t, "Vault", vaultNode.LedgerEntryType)
	require.Equal(t, "9000", vaultNode.FinalFields["AssetsAvailable"])
	require.Equal(t, "10000", vaultNode.FinalFields["AssetsTotal"])

	for _, account := range []*jtx.Account{f.borrower, f.owner} {
		node := loanSetMetadataNode(result, f.holdingKey(account))
		require.NotNil(t, node)
		require.Equal(t, "CreatedNode", node.NodeType)
		require.Equal(t, "MPToken", node.LedgerEntryType)
		require.Equal(t, account.Address, node.NewFields["Account"])
		wantAmount := "999"
		if account == f.owner {
			wantAmount = "1"
		}
		require.Equal(t, wantAmount, node.NewFields["MPTAmount"])
	}
	vaultHoldingNode := loanSetMetadataNode(result, f.holdingKey(vaultAccount))
	require.NotNil(t, vaultHoldingNode)
	require.Equal(t, "ModifiedNode", vaultHoldingNode.NodeType)
	require.Equal(t, "MPToken", vaultHoldingNode.LedgerEntryType)
	require.Equal(t, "9000", vaultHoldingNode.FinalFields["MPTAmount"])

	createdMPTokens := 0
	deletedMPTokens := 0
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerEntryType == "MPToken" {
			switch node.NodeType {
			case "CreatedNode":
				createdMPTokens++
			case "DeletedNode":
				deletedMPTokens++
			}
		}
		require.NotEqual(t, "DeletedNode", node.NodeType, "LoanSet must not delete ledger objects")
	}
	require.Equal(t, 2, createdMPTokens)
	require.Zero(t, deletedMPTokens)
}

func TestLoanSetOriginationFeeCreatesTwoMPTokens(t *testing.T) {
	for _, cashBasis := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			name := fmt.Sprintf("LendingProtocolV1_1=%t/fixCleanup3_4_0=%t", cashBasis, cleanup)
			t.Run(name, func(t *testing.T) {
				env := newTwoHoldingLoanEnv(t, cashBasis, cleanup)
				f := newLoanSetAssetFixtureWithEnv(t, env, "MPT", cashBasis)
				require.True(t, env.FeatureEnabled("LendingProtocol"))
				require.Equal(t, cashBasis, env.FeatureEnabled("LendingProtocolV1_1"))
				require.Equal(t, cleanup, env.FeatureEnabled("fixCleanup3_4_0"))

				borrowerHolding := f.holdingKey(f.borrower)
				ownerHolding := f.holdingKey(f.owner)
				require.False(t, env.LedgerEntryExists(borrowerHolding))
				require.False(t, env.LedgerEntryExists(ownerHolding))

				before := loanSetLedgerState(t, env)
				beforeBorrowerBalance := env.Balance(f.borrower)
				beforeOwnerBalance := env.Balance(f.owner)
				beforeBorrowerSequence := env.Seq(f.borrower)
				beforeOwnerSequence := env.Seq(f.owner)
				beforeBorrowerOwnerCount := env.OwnerCount(f.borrower)
				beforeOwnerOwnerCount := env.OwnerCount(f.owner)

				result := submitLoanSet(t, f, f.borrower, f.owner, true)
				require.True(t, result.Applied)
				require.Equal(t, uint64(20), result.Fee)

				if !cleanup {
					jtx.RequireTxClaimed(t, result, jtx.TecINVARIANT_FAILED)
					requireLoanSetFailureMetadata(t, result, f.borrower, beforeBorrowerBalance, beforeBorrowerSequence)
					require.Equal(t, beforeBorrowerBalance-result.Fee, env.Balance(f.borrower))
					require.Equal(t, beforeOwnerBalance, env.Balance(f.owner))
					require.Equal(t, beforeBorrowerSequence+1, env.Seq(f.borrower))
					require.Equal(t, beforeOwnerSequence, env.Seq(f.owner))
					require.Equal(t, beforeBorrowerOwnerCount, env.OwnerCount(f.borrower))
					require.Equal(t, beforeOwnerOwnerCount, env.OwnerCount(f.owner))
					require.False(t, env.LedgerEntryExists(borrowerHolding))
					require.False(t, env.LedgerEntryExists(ownerHolding))
					require.False(t, env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)))
					requireLoanSetLedgerRollback(t, f, before)
					return
				}

				jtx.RequireTxSuccess(t, result)
				loanKey := keylet.Loan(f.brokerKey, 1)
				vaultFields := decodeLendingEntry(t, env, f.vaultKey)
				vaultAccount := jtx.NewAccountWithAddress("loan-set-vault", vaultFields["Account"].(string))
				requireLoanSetSuccessMetadata(t, result, f, loanKey, vaultAccount, beforeBorrowerBalance, beforeBorrowerSequence, beforeBorrowerOwnerCount, beforeOwnerBalance, beforeOwnerSequence, beforeOwnerOwnerCount)

				require.Equal(t, beforeBorrowerBalance-result.Fee, env.Balance(f.borrower))
				require.Equal(t, beforeOwnerBalance, env.Balance(f.owner))
				require.Equal(t, beforeBorrowerSequence+1, env.Seq(f.borrower))
				require.Equal(t, beforeOwnerSequence, env.Seq(f.owner))
				require.Equal(t, beforeBorrowerOwnerCount+2, env.OwnerCount(f.borrower))
				require.Equal(t, beforeOwnerOwnerCount+1, env.OwnerCount(f.owner))
				f.token.RequireMPTokenAmount(f.borrower, 999)
				f.token.RequireMPTokenAmount(f.owner, 1)
				f.token.RequireMPTokenAmount(vaultAccount, 9000)
				require.True(t, env.LedgerEntryExists(borrowerHolding))
				require.True(t, env.LedgerEntryExists(ownerHolding))

				loan := decodeLendingEntry(t, env, loanKey)
				assertLendingField(t, "Loan", loan, "LoanOriginationFee", "1")
				assertLendingField(t, "Loan", loan, "PrincipalOutstanding", "1000")
				assertLendingField(t, "Loan", loan, "TotalValueOutstanding", "1000")
				assertLendingField(t, "Loan", loan, "PaymentRemaining", uint32(1))
				broker := decodeLendingEntry(t, env, keylet.LoanBrokerByID(f.brokerKey))
				assertLendingField(t, "LoanBroker", broker, "DebtTotal", "1000")
				assertLendingField(t, "LoanBroker", broker, "LoanSequence", uint32(2))
				assertLendingField(t, "LoanBroker", broker, "OwnerCount", uint32(1))
				assertLendingField(t, "Vault", vaultFields, "AssetsAvailable", "9000")
				assertLendingField(t, "Vault", vaultFields, "AssetsTotal", "10000")
				if cashBasis {
					assertLendingField(t, "Vault", vaultFields, "LEVersion", int(1))
				} else {
					require.NotContains(t, vaultFields, "LEVersion")
				}
			})
		}
	}
}
