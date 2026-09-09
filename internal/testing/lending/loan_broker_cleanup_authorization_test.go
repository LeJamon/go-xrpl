package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestLoanBrokerDeleteAuthRequiredMPTUsesPreTransactionPseudoClassification
// verifies the authorized cover transfer while deletion erases its pseudo-account.
func TestLoanBrokerDeleteAuthRequiredMPTUsesPreTransactionPseudoClassification(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "without fixCleanup3_4_0"
		if cleanup {
			name = "with fixCleanup3_4_0"
		}
		t.Run(name, func(t *testing.T) {
			env := newLendingEnv(t)
			env.DisableFeature("MPTokensV2")
			if cleanup {
				env.EnableFeature("fixCleanup3_4_0")
			} else {
				env.DisableFeature("fixCleanup3_4_0")
			}
			env.Close()

			issuer := jtx.NewAccount("auth-delete-issuer")
			owner := jtx.NewAccount("auth-delete-owner")
			token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner}})
			token.Create(mpttest.CreateOpts{
				Flags: mpttest.TfMPTCanTransfer | mpttest.TfMPTCanLock | mpttest.TfMPTRequireAuth,
			})
			token.Authorize(mpttest.AuthorizeOpts{Account: owner})
			token.Authorize(mpttest.AuthorizeOpts{Account: issuer, Holder: owner})
			token.Pay(issuer, owner, 200)

			vaultSequence := env.Seq(owner)
			create := vault.NewVaultCreate(owner.Address, tx.Asset{MPTIssuanceID: token.IssuanceID()})
			create.Common.Fee = reserveIncrement
			jtx.RequireTxSuccess(t, env.Submit(create))
			vaultID := vaultID(owner, vaultSequence)

			brokerSequence := env.Seq(owner)
			jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
			brokerID := brokerID(owner, brokerSequence)
			brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)
			pseudoID := loanBrokerPseudoID(t, env, brokerKey)

			jtx.RequireTxSuccess(t, env.Submit(
				lending.NewLoanBrokerCoverDeposit(owner.Address, brokerID, token.MPTAmount(100)),
			))
			pseudoTokenKey := func() keylet.Keylet {
				idBytes, err := hex.DecodeString(token.IssuanceID())
				require.NoError(t, err)
				var issuanceID [24]byte
				copy(issuanceID[:], idBytes)
				return keylet.MPTokenByID(issuanceID, pseudoID)
			}()
			require.True(t, env.LedgerEntryExists(pseudoTokenKey), "cover deposit did not create pseudo MPT holding")

			beforeBalance := env.Balance(owner)
			beforeSequence := env.Seq(owner)
			beforeOwnerCount := env.OwnerCount(owner)
			result := env.Submit(lending.NewLoanBrokerDelete(owner.Address, brokerID))

			jtx.RequireTxSuccess(t, result)
			require.True(t, result.Applied)
			require.Equal(t, env.BaseFee(), result.Fee)
			require.NotNil(t, result.Metadata)
			require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(owner))
			require.Equal(t, beforeSequence+1, env.Seq(owner))
			require.Equal(t, beforeOwnerCount-2, env.OwnerCount(owner))
			token.RequireMPTokenAmount(owner, 200)

			for name, entryKey := range map[string]keylet.Keylet{
				"LoanBroker":     brokerKey,
				"pseudo-account": keylet.Account(pseudoID),
				"pseudo MPT":     pseudoTokenKey,
			} {
				require.False(t, env.LedgerEntryExists(entryKey), "%s remains after successful deletion", name)
			}

			pseudoAccountKey := keylet.Account(pseudoID)
			wantPseudoIndex := strings.ToUpper(hex.EncodeToString(pseudoAccountKey.Key[:]))
			wantTokenIndex := strings.ToUpper(hex.EncodeToString(pseudoTokenKey.Key[:]))
			var pseudoDeleted, tokenDeleted bool
			for _, node := range result.Metadata.AffectedNodes {
				if node.NodeType != "DeletedNode" {
					continue
				}
				if node.LedgerIndex == wantPseudoIndex && node.LedgerEntryType == "AccountRoot" {
					pseudoDeleted = true
				}
				if node.LedgerIndex == wantTokenIndex && node.LedgerEntryType == "MPToken" {
					tokenDeleted = true
				}
			}
			require.True(t, pseudoDeleted, "metadata missing deleted pseudo-account")
			require.True(t, tokenDeleted, "metadata missing deleted pseudo MPT holding")
		})
	}
}
