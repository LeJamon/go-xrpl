package vault_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestVaultEmptyDataHasNoTransactionEffects(t *testing.T) {
	for _, mode := range []string{"amendments off", "amendments on"} {
		t.Run(mode, func(t *testing.T) {
			env := newVaultEnv(t)
			if mode == "amendments on" {
				env.EnableFeature("LendingProtocolV1_1")
				env.EnableFeature("fixCleanup3_4_0")
			} else {
				env.DisableFeature("LendingProtocolV1_1")
				env.DisableFeature("fixCleanup3_4_0")
			}
			env.Close()
			owner := jtx.NewAccount("owner")
			env.Fund(owner)
			createSequence := env.Seq(owner)
			create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
			create.Common.Fee = createFee
			jtx.RequireTxSuccess(t, env.Submit(create))
			id := vaultID(owner, createSequence)
			vaultKey := keylet.Vault(owner.AccountID(), createSequence)
			before, err := env.Ledger().Read(vaultKey)
			require.NoError(t, err)
			sequence, balance, ownerCount := env.Seq(owner), env.Balance(owner), env.OwnerCount(owner)

			emptyCreate := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
			emptyCreate.Common.Fee = createFee
			emptyCreate.Common.SetPresentFields(map[string]bool{"Data": true})
			emptySet := vault.NewVaultSet(owner.Address, id)
			emptySet.Common.SetPresentFields(map[string]bool{"Data": true})
			for _, transaction := range []tx.Transaction{emptyCreate, emptySet} {
				result := env.Submit(transaction)
				jtx.RequireTxFail(t, result, jtx.TemMALFORMED)
				require.False(t, result.Applied)
				require.Nil(t, result.Metadata)
				require.Zero(t, result.Fee)
				require.Equal(t, sequence, env.Seq(owner))
				require.Equal(t, balance, env.Balance(owner))
				require.Equal(t, ownerCount, env.OwnerCount(owner))
				after, err := env.Ledger().Read(vaultKey)
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.False(t, env.LedgerEntryExists(keylet.Vault(owner.AccountID(), sequence)))
			}
		})
	}
}
