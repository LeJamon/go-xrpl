package accountdelete_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	oracletest "github.com/LeJamon/go-xrpl/internal/testing/oracle"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestAccountDelete_CascadeDeletesOwnedEntries(t *testing.T) {
	t.Run("DepositPreauth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		destination := jtx.NewAccount("destination")
		env.Fund(alice, bob, destination)
		env.Close()

		preauthKey := keylet.DepositPreauth(alice.ID, bob.ID)
		jtx.RequireTxSuccess(t, env.Submit(depositpreauth.Auth(alice, bob).Build()))
		env.Close()
		jtx.RequireLedgerEntryExists(t, env, preauthKey)
		jtx.RequireOwnerDirectoryContains(t, env, alice, preauthKey.Key, true)

		env.IncLedgerSeqForAccDel(alice)
		submitAccountDeleteSuccess(t, env, alice, destination, preauthKey)
	})

	t.Run("NFTokenOffer", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		destination := jtx.NewAccount("destination")
		env.Fund(alice, bob, destination)
		env.Close()

		nftID := nft.GetNextNFTokenID(env, bob, 0, 8, 0)
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(bob, 0).Transferable().Build()))
		env.Close()
		offerKey := keylet.NFTokenOffer(alice.ID, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(
			nft.NFTokenCreateBuyOffer(alice, nftID, tx.NewXRPAmount(1), bob).Build()))
		env.Close()

		offerData, err := env.LedgerEntry(offerKey)
		require.NoError(t, err)
		offer, err := state.ParseNFTokenOffer(offerData)
		require.NoError(t, err)
		tokenDirectory := keylet.NFTBuys(offer.NFTokenID)
		jtx.RequireLedgerEntryExists(t, env, offerKey)
		jtx.RequireLedgerEntryExists(t, env, tokenDirectory)
		jtx.RequireOwnerDirectoryContains(t, env, alice, offerKey.Key, true)

		env.IncLedgerSeqForAccDel(alice)
		submitAccountDeleteSuccess(t, env, alice, destination, offerKey)
		jtx.RequireLedgerEntryNotExists(t, env, tokenDirectory)
	})

	t.Run("Oracle", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		destination := jtx.NewAccount("destination")
		env.Fund(alice, destination)
		env.Close()

		const documentID = uint32(7)
		oracleKey := keylet.Oracle(alice.ID, documentID)
		jtx.RequireTxSuccess(t, env.Submit(
			oracletest.OracleSet(alice, documentID, oracletest.DefaultLastUpdateTime(env)).
				ProviderHex(32).
				AssetClassHex(8).
				AddPrice("XRP", "USD", 740, 1).
				Build()))
		env.Close()
		jtx.RequireLedgerEntryExists(t, env, oracleKey)
		jtx.RequireOwnerDirectoryContains(t, env, alice, oracleKey.Key, true)

		env.IncLedgerSeqForAccDel(alice)
		submitAccountDeleteSuccess(t, env, alice, destination, oracleKey)
	})

	t.Run("Oracle corrupt owner node", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		destination := jtx.NewAccount("destination")
		env.Fund(alice, destination)
		env.Close()

		const documentID = uint32(7)
		oracleKey := keylet.Oracle(alice.ID, documentID)
		jtx.RequireTxSuccess(t, env.Submit(
			oracletest.OracleSet(alice, documentID, oracletest.DefaultLastUpdateTime(env)).
				ProviderHex(32).
				AssetClassHex(8).
				AddPrice("XRP", "USD", 740, 1).
				Build()))
		env.Close()
		oracleData, err := env.LedgerEntry(oracleKey)
		require.NoError(t, err)
		oracle, err := state.ParseOracle(oracleData)
		require.NoError(t, err)
		oracle.OwnerNode++
		oracleData, err = state.SerializeOracle(oracle)
		require.NoError(t, err)
		require.NoError(t, env.Ledger().Update(oracleKey, oracleData))

		env.IncLedgerSeqForAccDel(alice)
		stateBefore, err := env.Ledger().StateMapHash()
		require.NoError(t, err)
		balanceBefore := env.Balance(alice)
		sequenceBefore := env.Seq(alice)
		result := env.Submit(newAccountDelete(env, alice, destination))
		jtx.RequireTxFail(t, result, jtx.TefBAD_LEDGER)
		require.False(t, result.Applied)
		require.Zero(t, result.Fee)
		require.Nil(t, result.Metadata)
		stateAfter, err := env.Ledger().StateMapHash()
		require.NoError(t, err)
		require.Equal(t, stateBefore, stateAfter)
		require.Equal(t, balanceBefore, env.Balance(alice))
		require.Equal(t, sequenceBefore, env.Seq(alice))
		jtx.RequireLedgerEntryExists(t, env, oracleKey)
	})

	t.Run("CredentialIssuer", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		issuer := jtx.NewAccount("issuer")
		subject := jtx.NewAccount("subject")
		destination := jtx.NewAccount("destination")
		env.Fund(issuer, subject, destination)
		env.Close()

		const credentialType = "account-delete"
		credentialKey := keylet.Credential(subject.ID, issuer.ID, []byte(credentialType))
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialCreateText(issuer, subject, credentialType).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialAcceptText(subject, issuer, credentialType).Build()))
		env.Close()
		jtx.RequireOwnerDirectoryContains(t, env, issuer, credentialKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, subject, credentialKey.Key, true)

		env.IncLedgerSeqForAccDel(issuer)
		submitAccountDeleteSuccess(t, env, issuer, destination, credentialKey)
		jtx.RequireOwnerDirectoryContains(t, env, subject, credentialKey.Key, false)
		require.Zero(t, env.OwnerCount(subject))
	})

	t.Run("CredentialSubject", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		issuer := jtx.NewAccount("issuer")
		subject := jtx.NewAccount("subject")
		destination := jtx.NewAccount("destination")
		env.Fund(issuer, subject, destination)
		env.Close()

		const credentialType = "account-delete"
		credentialKey := keylet.Credential(subject.ID, issuer.ID, []byte(credentialType))
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialCreateText(issuer, subject, credentialType).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(
			credentialtest.CredentialAcceptText(subject, issuer, credentialType).Build()))
		env.Close()
		jtx.RequireOwnerDirectoryContains(t, env, issuer, credentialKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, subject, credentialKey.Key, true)

		env.IncLedgerSeqForAccDel(subject)
		submitAccountDeleteSuccess(t, env, subject, destination, credentialKey)
		jtx.RequireOwnerDirectoryContains(t, env, issuer, credentialKey.Key, false)
		require.Zero(t, env.OwnerCount(issuer))
	})
}

func TestAccountDelete_WithTicketCleansAllTickets(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	destination := jtx.NewAccount("destination")
	env.FundAmount(alice, uint64(jtx.XRP(100_000)))
	env.Fund(destination)
	env.Close()

	const ticketCount = uint32(250)
	firstTicket := env.CreateTickets(alice, ticketCount)
	env.Close()
	jtx.RequireTicketCount(t, env, alice, ticketCount)
	ticketKeys := make([]keylet.Keylet, 0, ticketCount)
	for offset := uint32(0); offset < ticketCount; offset++ {
		ticketKey := keylet.Ticket(alice.ID, firstTicket+offset)
		ticketKeys = append(ticketKeys, ticketKey)
		jtx.RequireLedgerEntryExists(t, env, ticketKey)
	}

	env.IncLedgerSeqForAccDel(alice)
	before := captureDeleteBalances(env, alice, destination)
	d := jtx.WithTicketSeq(newAccountDelete(env, alice, destination), firstTicket)
	result := env.Submit(d)
	requireAccountDeleteSuccess(t, env, result, alice, destination, before, ticketKeys...)
}
