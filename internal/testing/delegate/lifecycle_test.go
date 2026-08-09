package delegate_test

import (
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func ownerDirectoryContains(t *testing.T, env *jtx.TestEnv, owner [20]byte, item [32]byte) bool {
	t.Helper()
	found := false
	require.NoError(t, state.DirForEach(env.Ledger(), keylet.OwnerDir(owner), func(key [32]byte) error {
		if key == item {
			found = true
		}
		return nil
	}))
	return found
}

func TestDelegate_LedgerLifecycle(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	delegateKey := keylet.Delegate(alice.ID, bob.ID)
	require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "Payment", "TrustSet").Code)
	env.Close()

	data, err := env.LedgerEntry(delegateKey)
	require.NoError(t, err)
	entry, err := state.ParseDelegate(data)
	require.NoError(t, err)
	require.Equal(t, alice.ID, entry.Account)
	require.Equal(t, bob.ID, entry.Authorize)
	require.Equal(t, []uint32{state.LookupPermissionValue("Payment"), state.LookupPermissionValue("TrustSet")}, entry.Permissions)
	require.True(t, entry.HasDestinationNode)
	require.True(t, ownerDirectoryContains(t, env, alice.ID, delegateKey.Key))
	require.True(t, ownerDirectoryContains(t, env, bob.ID, delegateKey.Key))
	require.Equal(t, uint32(1), env.OwnerCount(alice))
	require.Zero(t, env.OwnerCount(bob))

	ownerNode := entry.OwnerNode
	destinationNode := entry.DestinationNode
	require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "TrustSet").Code)
	env.Close()

	data, err = env.LedgerEntry(delegateKey)
	require.NoError(t, err)
	entry, err = state.ParseDelegate(data)
	require.NoError(t, err)
	require.Equal(t, []uint32{state.LookupPermissionValue("TrustSet")}, entry.Permissions)
	require.Equal(t, ownerNode, entry.OwnerNode)
	require.Equal(t, destinationNode, entry.DestinationNode)
	require.Equal(t, uint32(1), env.OwnerCount(alice))

	deleteTx := delegatetx.NewDelegateSet(alice.Address)
	deleteTx.Authorize = bob.Address
	require.Equal(t, "tesSUCCESS", env.SubmitSignedWith(deleteTx, alice).Code)
	blob, err := tx.SerializeTransaction(deleteTx)
	require.NoError(t, err)
	roundTripped, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	require.NotNil(t, roundTripped.(*delegatetx.DelegateSet).Permissions)
	require.Empty(t, roundTripped.(*delegatetx.DelegateSet).Permissions)
	env.Close()

	require.False(t, env.LedgerEntryExists(delegateKey))
	require.False(t, ownerDirectoryContains(t, env, alice.ID, delegateKey.Key))
	require.False(t, ownerDirectoryContains(t, env, bob.ID, delegateKey.Key))
	require.Zero(t, env.OwnerCount(alice))
	require.Zero(t, env.OwnerCount(bob))
}

func TestDelegate_OrdinaryPaymentEffectsAndFeePayer(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, carol)
	env.Close()

	require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "Payment").Code)
	env.Close()

	aliceBalance := env.Balance(alice)
	bobBalance := env.Balance(bob)
	carolBalance := env.Balance(carol)
	aliceSequence := env.Seq(alice)
	bobSequence := env.Seq(bob)
	amount := int64(1_000_000)

	payment := paymenttx.NewPayment(alice.Address, carol.Address, tx.NewXRPAmount(amount))
	payment.Delegate = bob.Address
	result := env.SubmitSignedWith(payment, bob)
	jtx.RequireTxSuccess(t, result)
	fee, err := strconv.ParseUint(payment.Fee, 10, 64)
	require.NoError(t, err)

	require.Equal(t, aliceBalance-uint64(amount), env.Balance(alice))
	require.Equal(t, bobBalance-fee, env.Balance(bob))
	require.Equal(t, carolBalance+uint64(amount), env.Balance(carol))
	require.Equal(t, aliceSequence+1, env.Seq(alice))
	require.Equal(t, bobSequence, env.Seq(bob))
}

func TestDelegate_OrdinarySubmitUsesDelegateRegularKey(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	regularKey := jtx.NewAccount("regularKey")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, regularKey, carol)
	env.Close()

	jtx.RequireTxSuccess(t, grantPermissions(env, alice, bob, "Payment"))
	env.SetRegularKey(bob, regularKey)
	env.DisableMasterKey(bob)
	env.Close()

	env.SetVerifySignatures(true)
	payment := paymenttx.NewPayment(alice.Address, carol.Address, tx.NewXRPAmount(1_000_000))
	payment.Delegate = bob.Address
	jtx.RequireTxSuccess(t, env.Submit(payment))
	require.Equal(t, regularKey.PublicKeyHex(), payment.SigningPubKey)
}

func TestDelegate_DeniedPaymentIsAtomic(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("PermissionDelegationV1_1")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, carol)
	env.Close()

	require.Equal(t, "tesSUCCESS", grantPermissions(env, alice, bob, "TrustSet").Code)
	env.Close()

	stateHash, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	aliceBalance := env.Balance(alice)
	bobBalance := env.Balance(bob)
	carolBalance := env.Balance(carol)
	aliceSequence := env.Seq(alice)
	bobSequence := env.Seq(bob)

	result := payDelegated(env, alice, bob, carol, tx.NewXRPAmount(1_000_000))
	jtx.RequireTxFail(t, result, "terNO_DELEGATE_PERMISSION")

	afterHash, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	require.Equal(t, stateHash, afterHash)
	require.Equal(t, aliceBalance, env.Balance(alice))
	require.Equal(t, bobBalance, env.Balance(bob))
	require.Equal(t, carolBalance, env.Balance(carol))
	require.Equal(t, aliceSequence, env.Seq(alice))
	require.Equal(t, bobSequence, env.Seq(bob))
}
