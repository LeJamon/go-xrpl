package accountdelete_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAccountDelete_DeletableEntryLimitAndDirectoryPages(t *testing.T) {
	env := jtx.NewTestEnvBacked(t)
	alice := jtx.NewAccount("alice")
	gw := jtx.NewAccount("gw")
	env.FundAmount(alice, uint64(jtx.XRP(10_000_000)))
	env.Fund(gw)
	env.Close()

	const offerCount = 1001
	offerKeys := make([]keylet.Keylet, 0, offerCount)
	firstOfferSequence := env.Seq(alice)
	for range offerCount {
		offerKey := keylet.Offer(alice.ID, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(
			offerbuild.OfferCreate(alice, gw.IOU("USD", 1), jtx.XRPTxAmount(jtx.XRP(1))).Build()))
		offerKeys = append(offerKeys, offerKey)
	}
	env.Close()
	require.Equal(t, uint32(offerCount), env.OwnerCount(alice))
	for page := uint64(0); page <= offerCount/32; page++ {
		jtx.RequireLedgerEntryExists(t, env, keylet.OwnerDirPage(alice.ID, page))
	}

	env.IncLedgerSeqForAccDel(alice)
	stateBefore, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	sequenceBefore := env.Seq(alice)
	balanceBefore := env.Balance(alice)
	result := env.Submit(newAccountDelete(env, alice, gw))
	jtx.RequireTxFail(t, result, jtx.TefTOO_BIG)
	require.False(t, result.Applied)
	require.Zero(t, result.Fee)
	require.Nil(t, result.Metadata)
	stateAfter, err := env.Ledger().StateMapHash()
	require.NoError(t, err)
	require.Equal(t, stateBefore, stateAfter)
	require.Equal(t, sequenceBefore, env.Seq(alice))
	require.Equal(t, balanceBefore, env.Balance(alice))

	jtx.RequireTxSuccess(t, env.Submit(offerbuild.OfferCancel(alice, firstOfferSequence).Build()))
	env.Close()
	require.Equal(t, uint32(offerCount-1), env.OwnerCount(alice))
	env.IncLedgerSeqForAccDel(alice)
	before := captureDeleteBalances(env, alice, gw)
	result = env.Submit(newAccountDelete(env, alice, gw))
	requireAccountDeleteSuccess(t, env, result, alice, gw, before, offerKeys...)
	for page := uint64(0); page <= offerCount/32; page++ {
		jtx.RequireLedgerEntryNotExists(t, env, keylet.OwnerDirPage(alice.ID, page))
	}
}

func TestAccountDelete_TicketCountsTowardDeletableEntryLimit(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		offerCount                 int
		wantSuccess                bool
		withUnauthorizedCredential bool
	}{
		{name: "1000 entries", offerCount: 750, wantSuccess: true},
		{name: "1001 entries", offerCount: 751},
		{
			name:                       "1001 entries with unauthorized credential",
			offerCount:                 750,
			withUnauthorizedCredential: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnvBacked(t)
			alice := jtx.NewAccount("alice")
			gw := jtx.NewAccount("gw")
			issuer := jtx.NewAccount("issuer")
			env.FundAmount(alice, uint64(jtx.XRP(10_000_000)))
			env.Fund(gw, issuer)
			env.Close()

			const ticketCount = 250
			ticketSequence := env.CreateTickets(alice, ticketCount)
			firstOfferSequence := env.Seq(alice)
			for range tc.offerCount {
				jtx.RequireTxSuccess(t, env.Submit(
					offerbuild.OfferCreate(alice, gw.IOU("USD", 1), jtx.XRPTxAmount(jtx.XRP(1))).Build()))
			}
			env.Close()

			var credentialKey keylet.Keylet
			var credentialID string
			if tc.withUnauthorizedCredential {
				const credentialType = "account-delete"
				credentialKey = keylet.Credential(alice.ID, issuer.ID, []byte(credentialType))
				credentialID = depositpreauth.CredentialIndexHex(alice, issuer, credentialType)
				jtx.RequireTxSuccess(t, env.Submit(
					credentialtest.CredentialCreateText(issuer, alice, credentialType).Build()))
				env.Close()
				jtx.RequireTxSuccess(t, env.Submit(
					credentialtest.CredentialAcceptText(alice, issuer, credentialType).Build()))
				env.Close()
				env.EnableDepositAuth(gw)
				env.Close()
			}

			wantOwnerCount := uint32(ticketCount + tc.offerCount)
			if tc.withUnauthorizedCredential {
				wantOwnerCount++
			}
			require.Equal(t, wantOwnerCount, env.OwnerCount(alice))
			require.Equal(t, uint32(ticketCount), env.TicketCount(alice))
			env.IncLedgerSeqForAccDel(alice)

			firstOfferKey := keylet.Offer(alice.ID, firstOfferSequence)
			ticketKey := keylet.Ticket(alice.ID, ticketSequence)
			jtx.RequireLedgerEntryExists(t, env, firstOfferKey)
			jtx.RequireLedgerEntryExists(t, env, ticketKey)
			before := captureDeleteBalances(env, alice, gw)
			stateBefore, err := env.Ledger().StateMapHash()
			require.NoError(t, err)
			sequenceBefore := env.Seq(alice)

			deleteTx := newAccountDelete(env, alice, gw)
			if tc.withUnauthorizedCredential {
				deleteTx.CredentialIDs = []string{credentialID}
			}
			result := env.Submit(jtx.WithTicketSeq(deleteTx, ticketSequence))
			if tc.wantSuccess {
				requireAccountDeleteSuccess(t, env, result, alice, gw, before, firstOfferKey)
				return
			}

			jtx.RequireTxFail(t, result, jtx.TefTOO_BIG)
			require.False(t, result.Applied)
			require.Zero(t, result.Fee)
			require.Nil(t, result.Metadata)
			stateAfter, err := env.Ledger().StateMapHash()
			require.NoError(t, err)
			require.Equal(t, stateBefore, stateAfter)
			require.Equal(t, sequenceBefore, env.Seq(alice))
			require.Equal(t, before.source, env.Balance(alice))
			require.Equal(t, before.destination, env.Balance(gw))
			require.Equal(t, uint32(ticketCount), env.TicketCount(alice))
			require.Equal(t, wantOwnerCount, env.OwnerCount(alice))
			jtx.RequireAccountExists(t, env, alice)
			jtx.RequireLedgerEntryExists(t, env, firstOfferKey)
			jtx.RequireLedgerEntryExists(t, env, ticketKey)
			if tc.withUnauthorizedCredential {
				jtx.RequireLedgerEntryExists(t, env, credentialKey)
			}
		})
	}
}
