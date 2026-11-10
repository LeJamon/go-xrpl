package vault_test

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestVaultWithdrawSharesFromEmptyIOUVault(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, withdrawAsOwner := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup=%t/owner=%t", cleanup, withdrawAsOwner), func(t *testing.T) {
				env := newVaultEnv(t)
				if cleanup {
					env.EnableFeature("fixCleanup3_1_3")
				} else {
					env.DisableFeature("fixCleanup3_1_3")
				}
				env.Close()
				require.Equal(t, cleanup, env.Rules().Enabled(amendment.FeatureFixCleanup3_1_3))

				issuer := jtx.NewAccount("empty-vault-issuer")
				owner := jtx.NewAccount("empty-vault-owner")
				other := jtx.NewAccount("empty-vault-other")
				env.Fund(issuer, owner, other)
				createSequence := env.Seq(owner)
				create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: issuer.Address})
				create.Common.Fee = createFee
				jtx.RequireTxSuccess(t, env.Submit(create))
				env.Close()

				vaultKey := keylet.Vault(owner.ID, createSequence)
				info, err := vault.ReadVaultInfo(env.Ledger(), vaultKey)
				require.NoError(t, err)
				require.NotNil(t, info)
				beforeVault, err := env.LedgerEntry(vaultKey)
				require.NoError(t, err)
				issuanceKey := keylet.MPTIssuance(info.ShareMPTID)
				beforeIssuance, err := env.LedgerEntry(issuanceKey)
				require.NoError(t, err)
				issuance, err := state.ParseMPTokenIssuance(beforeIssuance)
				require.NoError(t, err)
				require.Zero(t, issuance.OutstandingAmount)

				account := other
				if withdrawAsOwner {
					account = owner
				}
				beforeBalance := env.Balance(account)
				beforeSequence := env.Seq(account)
				beforeOwnerCount := env.OwnerCount(account)
				withdraw := vault.NewVaultWithdraw(
					account.Address,
					vaultID(owner, createSequence),
					state.NewMPTAmountWithIssuanceID(1, "", hex.EncodeToString(info.ShareMPTID[:])),
				)
				result := env.Submit(withdraw)
				jtx.RequireTxClaimed(t, result, "tecINSUFFICIENT_FUNDS")
				require.True(t, result.Applied)
				require.Equal(t, env.BaseFee(), result.Fee)
				requireAccountMetadata(t, result, beforeBalance, beforeSequence, beforeOwnerCount, env, account)
				require.Equal(t, []string{"ModifiedNode:AccountRoot"}, metadataKinds(result.Metadata))

				afterVault, err := env.LedgerEntry(vaultKey)
				require.NoError(t, err)
				require.Equal(t, beforeVault, afterVault)
				afterIssuance, err := env.LedgerEntry(issuanceKey)
				require.NoError(t, err)
				require.Equal(t, beforeIssuance, afterIssuance)
			})
		}
	}
}
