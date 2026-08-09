package delegate_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestDelegate_DeleteRejectsCorruptDirectoryAtomically(t *testing.T) {
	for _, corruptOwner := range []bool{true, false} {
		name := "destination link"
		if corruptOwner {
			name = "owner link"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.EnableFeature("PermissionDelegationV1_1")
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			env.Fund(alice, bob)
			env.Close()

			require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "Payment").Code)
			env.Close()

			delegateKey := keylet.Delegate(alice.ID, bob.ID)
			delegateBefore, err := env.LedgerEntry(delegateKey)
			require.NoError(t, err)
			ownerDirKey := keylet.OwnerDir(alice.ID)
			destinationDirKey := keylet.OwnerDir(bob.ID)
			corruptKey := destinationDirKey
			if corruptOwner {
				corruptKey = ownerDirKey
			}
			corruptData, err := env.LedgerEntry(corruptKey)
			require.NoError(t, err)
			directory, err := state.ParseDirectoryNode(corruptData)
			require.NoError(t, err)
			directory.Indexes = nil
			corruptData, err = state.SerializeDirectoryNode(directory, false)
			require.NoError(t, err)
			require.NoError(t, env.Ledger().Update(corruptKey, corruptData))

			ownerBefore, err := env.LedgerEntry(ownerDirKey)
			require.NoError(t, err)
			destinationBefore, err := env.LedgerEntry(destinationDirKey)
			require.NoError(t, err)
			stateHash, err := env.Ledger().StateMapHash()
			require.NoError(t, err)
			balanceBefore := env.Balance(alice)
			sequenceBefore := env.Seq(alice)
			ownerCountBefore := env.OwnerCount(alice)

			result := grantPermissions(env, alice, bob)
			jtx.RequireTxFail(t, result, jtx.TefBAD_LEDGER)
			require.Nil(t, result.Metadata)

			afterHash, err := env.Ledger().StateMapHash()
			require.NoError(t, err)
			require.Equal(t, stateHash, afterHash)
			require.Equal(t, balanceBefore, env.Balance(alice))
			require.Equal(t, sequenceBefore, env.Seq(alice))
			require.Equal(t, ownerCountBefore, env.OwnerCount(alice))

			delegateAfter, err := env.LedgerEntry(delegateKey)
			require.NoError(t, err)
			require.Equal(t, delegateBefore, delegateAfter)
			ownerAfter, err := env.LedgerEntry(ownerDirKey)
			require.NoError(t, err)
			require.Equal(t, ownerBefore, ownerAfter)
			destinationAfter, err := env.LedgerEntry(destinationDirKey)
			require.NoError(t, err)
			require.Equal(t, destinationBefore, destinationAfter)
		})
	}
}
