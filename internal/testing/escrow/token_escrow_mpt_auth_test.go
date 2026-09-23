package escrow_test

import (
	"encoding/hex"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	escrowtest "github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestMPTEscrow_RequireAuthMissingHolder(t *testing.T) {
	t.Run("CreateRejectsMissingDestination", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(bob, uint64(jtx.XRP(5_000)))

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{Holders: []*jtx.Account{alice}})
		mptGw.Create(mpt.CreateOpts{
			Flags: mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer | mpt.TfMPTRequireAuth,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: gw, Holder: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()

		amount := mptAmount(10, gw.Address, mptGw.IssuanceID())
		seq := env.Seq(alice)
		escrowKey := keylet.Escrow(alice.ID, seq)
		destinationTokenKey := mptTokenKey(t, mptGw.IssuanceID(), bob)
		aliceOwnerCount := env.OwnerCount(alice)
		bobOwnerCount := env.OwnerCount(bob)

		result := env.Submit(
			escrowtest.EscrowCreate(alice, bob, 0).
				MPTAmount(amount).
				Condition(escrowtest.TestCondition1()).
				FinishTime(env.Now().Add(time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxClaimed(t, result, jtx.TecNO_AUTH)
		env.Close()

		require.False(t, env.LedgerEntryExists(escrowKey))
		require.False(t, env.LedgerEntryExists(destinationTokenKey))
		require.Equal(t, aliceOwnerCount, env.OwnerCount(alice))
		require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
		mptGw.RequireMPTokenAmount(alice, 10_000)
		require.Zero(t, mptGw.HolderLockedAmount(alice))
		require.Zero(t, mptGw.IssuanceLockedAmount())
		require.Equal(t, uint64(10_000), mptGw.IssuanceOutstandingAmount())
	})

	t.Run("FinishRejectsMissingDestination", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.EnableFeature("TokenEscrow")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		gw := jtx.NewAccount("gw")
		env.FundAmount(bob, uint64(jtx.XRP(5_000)))

		mptGw := mpt.NewMPTTester(t, env, gw, mpt.MPTInit{Holders: []*jtx.Account{alice}})
		mptGw.Create(mpt.CreateOpts{
			Flags: mpt.TfMPTCanEscrow | mpt.TfMPTCanTransfer,
		})
		mptGw.Authorize(mpt.AuthorizeOpts{Account: alice})
		mptGw.Pay(gw, alice, 10_000)
		env.Close()

		amount := mptAmount(10, gw.Address, mptGw.IssuanceID())
		seq := env.Seq(alice)
		escrowKey := keylet.Escrow(alice.ID, seq)
		result := env.Submit(
			escrowtest.EscrowCreate(alice, bob, 0).
				MPTAmount(amount).
				Condition(escrowtest.TestCondition1()).
				FinishTime(env.Now().Add(time.Second)).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		mptGw.Set(mpt.SetOpts{Flags: mpt.TfMPTSetRequireAuth})
		env.Close()
		bobOwnerCount := env.OwnerCount(bob)
		destinationTokenKey := mptTokenKey(t, mptGw.IssuanceID(), bob)

		result = env.Submit(
			escrowtest.EscrowFinish(bob, alice, seq).
				Condition(escrowtest.TestCondition1()).
				Fulfillment(escrowtest.TestFulfillment1()).
				Fee(env.BaseFee() * 150).
				Build())
		jtx.RequireTxClaimed(t, result, jtx.TecNO_AUTH)
		env.Close()

		require.True(t, env.LedgerEntryExists(escrowKey))
		require.False(t, env.LedgerEntryExists(destinationTokenKey))
		require.Equal(t, bobOwnerCount, env.OwnerCount(bob))
		mptGw.RequireMPTokenAmount(alice, 9_990)
		require.Zero(t, mptGw.HolderLockedAmount(bob))
		require.Equal(t, uint64(10), mptGw.HolderLockedAmount(alice))
		require.Equal(t, uint64(10), mptGw.IssuanceLockedAmount())
	})
}

func mptTokenKey(t testing.TB, issuanceID string, account *jtx.Account) keylet.Keylet {
	t.Helper()
	idBytes, err := hex.DecodeString(issuanceID)
	require.NoError(t, err)
	require.Len(t, idBytes, 24)
	var id [24]byte
	copy(id[:], idBytes)
	return keylet.MPToken(keylet.MPTIssuance(id).Key, account.ID)
}
