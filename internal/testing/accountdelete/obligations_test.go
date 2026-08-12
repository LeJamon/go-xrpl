package accountdelete_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/check"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/paychan"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func requireAccountDeleteObligation(
	t *testing.T,
	env *jtx.TestEnv,
	from, to *jtx.Account,
	entries ...keylet.Keylet,
) {
	t.Helper()
	before := captureDeleteBalances(env, from, to)
	sequence := env.Seq(from)
	result := env.Submit(newAccountDelete(env, from, to))
	jtx.RequireTxFail(t, result, jtx.TecHAS_OBLIGATIONS)
	require.True(t, result.Applied)
	require.Equal(t, before.fee, result.Fee)
	require.Equal(t, before.source-before.fee, env.Balance(from))
	require.Equal(t, before.destination, env.Balance(to))
	require.Equal(t, sequence+1, env.Seq(from))
	jtx.RequireAccountExists(t, env, from)
	for _, entryKey := range entries {
		jtx.RequireLedgerEntryExists(t, env, entryKey)
	}
}

func TestAccountDelete_BlockingBacklinks(t *testing.T) {
	t.Run("Check", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		destination := jtx.NewAccount("destination")
		env.Fund(alice, bob, destination)
		env.Close()

		checkKey := keylet.Check(alice.ID, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(
			check.CheckCreate(alice, bob, tx.NewXRPAmount(jtx.XRP(1))).Build()))
		env.Close()
		jtx.RequireOwnerDirectoryContains(t, env, alice, checkKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, bob, checkKey.Key, true)

		env.IncLedgerSeqForAccDel(alice)
		env.IncLedgerSeqForAccDel(bob)
		requireAccountDeleteObligation(t, env, alice, destination, checkKey)
		requireAccountDeleteObligation(t, env, bob, destination, checkKey)
	})

	t.Run("PayChannel", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		destination := jtx.NewAccount("destination")
		env.Fund(alice, bob, destination)
		env.Close()

		channelKey := keylet.PayChannel(alice.ID, bob.ID, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(
			paychan.ChannelCreate(alice, bob, jtx.XRP(1), 100, alice.PublicKeyHex()).Build()))
		env.Close()
		jtx.RequireOwnerDirectoryContains(t, env, alice, channelKey.Key, true)
		jtx.RequireOwnerDirectoryContains(t, env, bob, channelKey.Key, true)

		env.IncLedgerSeqForAccDel(alice)
		env.IncLedgerSeqForAccDel(bob)
		requireAccountDeleteObligation(t, env, alice, destination, channelKey)
		requireAccountDeleteObligation(t, env, bob, destination, channelKey)
	})
}

func TestAccountDelete_ImplicitTrustLineBlocks(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	gw := jtx.NewAccount("gw")
	destination := jtx.NewAccount("destination")
	env.Fund(alice, gw, destination)
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(
		offerbuild.OfferCreate(alice, gw.IOU("BUX", 30), jtx.XRPTxAmount(jtx.XRP(30))).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(
		offerbuild.OfferCreate(gw, jtx.XRPTxAmount(jtx.XRP(30)), gw.IOU("BUX", 30)).Build()))
	env.Close()
	lineKey := keylet.Line(alice.ID, gw.ID, "BUX")
	jtx.RequireLedgerEntryExists(t, env, lineKey)

	env.IncLedgerSeqForAccDel(alice)
	env.IncLedgerSeqForAccDel(gw)
	requireAccountDeleteObligation(t, env, alice, destination, lineKey)
	requireAccountDeleteObligation(t, env, gw, destination, lineKey)
}

func TestAccountDelete_NFTokenObligations(t *testing.T) {
	t.Run("issuer without token page", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		issuer := jtx.NewAccount("issuer")
		minter := jtx.NewAccount("minter")
		destination := jtx.NewAccount("destination")
		env.Fund(issuer, minter, destination)
		env.Close()

		jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).AuthorizedMinter(minter).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(minter, 0).Issuer(issuer).Transferable().Build()))
		env.Close()
		require.Equal(t, uint32(1), env.MintedCount(issuer))
		require.Zero(t, env.BurnedCount(issuer))

		env.IncLedgerSeqForAccDel(issuer)
		requireAccountDeleteObligation(t, env, issuer, destination)
	})

	t.Run("owned token page without issuer count", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		issuer := jtx.NewAccount("issuer")
		destination := jtx.NewAccount("destination")
		env.Fund(alice, issuer, destination)
		env.Close()

		nftID := nft.GetNextNFTokenID(env, issuer, 0, 8, 0)
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(issuer, 0).Transferable().Build()))
		env.Close()
		sellOffer := keylet.NFTokenOffer(issuer.ID, env.Seq(issuer))
		jtx.RequireTxSuccess(t, env.Submit(
			nft.NFTokenCreateSellOffer(issuer, nftID, tx.NewXRPAmount(0)).Destination(alice).Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(
			nft.NFTokenAcceptSellOffer(alice, strings.ToUpper(hex.EncodeToString(sellOffer.Key[:]))).Build()))
		env.Close()
		require.Zero(t, env.MintedCount(alice))
		require.Zero(t, env.BurnedCount(alice))

		env.IncLedgerSeqForAccDel(alice)
		requireAccountDeleteObligation(t, env, alice, destination)
	})

	t.Run("burned issuer token at remint boundary", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		issuer := jtx.NewAccount("issuer")
		minter := jtx.NewAccount("minter")
		destination := jtx.NewAccount("destination")
		env.Fund(issuer, minter, destination)
		env.Close()

		jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).AuthorizedMinter(minter).Build()))
		env.Close()
		nftID := nft.GetNextNFTokenID(env, issuer, 0, 8, 0)
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(minter, 0).Issuer(issuer).Transferable().Build()))
		env.Close()
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenBurn(minter, nftID).Build()))
		env.Close()
		require.Equal(t, uint32(1), env.MintedCount(issuer))
		require.Equal(t, uint32(1), env.BurnedCount(issuer))

		info := env.AccountInfo(issuer)
		require.NotNil(t, info.FirstNFTokenSequence)
		nftBoundary := *info.FirstNFTokenSequence + info.MintedNFTokens + 255
		for env.LedgerSeq() < nftBoundary {
			env.Close()
		}
		env.IncLedgerSeqForAccDel(issuer)
		submitAccountDeleteSuccess(t, env, issuer, destination)
	})
}
