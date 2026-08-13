package accountdelete_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func setDelegate(t *testing.T, env *jtx.TestEnv, owner, authorized *jtx.Account) {
	t.Helper()
	ds := delegatetx.NewDelegateSet(owner.Address)
	ds.Authorize = authorized.Address
	ds.Permissions = append(ds.Permissions, delegatetx.NewPermission("Payment"))
	jtx.RequireTxSuccess(t, env.Submit(ds))
}

func TestAccountDelete_CleansDelegateWhenDeletingDelegator(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, carol)
	env.Close()

	setDelegate(t, env, alice, bob)
	env.Close()
	delegateKey := keylet.Delegate(alice.ID, bob.ID)
	jtx.RequireOwnerDirectoryContains(t, env, bob, delegateKey.Key, true)

	env.IncLedgerSeqForAccDel(alice)
	submitAccountDeleteSuccess(t, env, alice, carol, delegateKey)
	env.Close()

	jtx.RequireOwnerDirectoryContains(t, env, bob, delegateKey.Key, false)
}

func TestAccountDelete_CleansDelegateWhenDeletingDelegatee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.FundAmount(alice, uint64(jtx.XRP(100_000)))
	env.Fund(bob, carol)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 32).Build()))
	setDelegate(t, env, alice, bob)
	env.Close()
	delegateKey := keylet.Delegate(alice.ID, bob.ID)
	data, err := env.LedgerEntry(delegateKey)
	require.NoError(t, err)
	entry, err := state.ParseDelegate(data)
	require.NoError(t, err)
	require.Equal(t, uint64(1), entry.OwnerNode)
	require.Zero(t, entry.DestinationNode)

	env.IncLedgerSeqForAccDel(bob)
	submitAccountDeleteSuccess(t, env, bob, carol, delegateKey)
	env.Close()

	jtx.RequireOwnerDirectoryContains(t, env, alice, delegateKey.Key, false)
	require.Equal(t, uint32(32), env.OwnerCount(alice))
}

func TestAccountDelete_CleansMultipleInboundDelegates(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	destination := jtx.NewAccount("destination")
	env.Fund(alice, bob, carol, destination)
	env.Close()

	setDelegate(t, env, alice, bob)
	setDelegate(t, env, carol, bob)
	env.Close()
	aliceDelegate := keylet.Delegate(alice.ID, bob.ID)
	carolDelegate := keylet.Delegate(carol.ID, bob.ID)

	env.IncLedgerSeqForAccDel(bob)
	submitAccountDeleteSuccess(t, env, bob, destination, aliceDelegate, carolDelegate)
	env.Close()

	for _, owner := range []*jtx.Account{alice, carol} {
		delegateKey := keylet.Delegate(owner.ID, bob.ID)
		jtx.RequireOwnerDirectoryContains(t, env, owner, delegateKey.Key, false)
		require.Zero(t, env.OwnerCount(owner))
	}
}

func TestAccountDelete_DelegateCleanupFailureIsAtomic(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, carol)
	env.Close()

	setDelegate(t, env, alice, bob)
	env.Close()
	delegateKey := keylet.Delegate(alice.ID, bob.ID)
	destinationDirKey := keylet.OwnerDir(bob.ID)
	destinationData, err := env.LedgerEntry(destinationDirKey)
	require.NoError(t, err)
	destinationDir, err := state.ParseDirectoryNode(destinationData)
	require.NoError(t, err)
	destinationDir.Indexes = nil
	destinationData, err = state.SerializeDirectoryNode(destinationDir, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(destinationDirKey, destinationData))
	env.IncLedgerSeqForAccDel(alice)

	stateHash, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	delegateBefore, err := env.LedgerEntry(delegateKey)
	require.NoError(t, err)
	ownerDirBefore, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
	require.NoError(t, err)
	balanceBefore := env.Balance(alice)
	sequenceBefore := env.Seq(alice)

	result := env.Submit(newAccountDelete(env, alice, carol))
	jtx.RequireTxFail(t, result, jtx.TefBAD_LEDGER)
	require.Nil(t, result.Metadata)

	afterHash, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	require.Equal(t, stateHash, afterHash)
	require.Equal(t, balanceBefore, env.Balance(alice))
	require.Equal(t, sequenceBefore, env.Seq(alice))
	require.True(t, env.LedgerEntryExists(keylet.Account(alice.ID)))
	delegateAfter, err := env.LedgerEntry(delegateKey)
	require.NoError(t, err)
	require.Equal(t, delegateBefore, delegateAfter)
	ownerDirAfter, err := env.LedgerEntry(keylet.OwnerDir(alice.ID))
	require.NoError(t, err)
	require.Equal(t, ownerDirBefore, ownerDirAfter)
}
