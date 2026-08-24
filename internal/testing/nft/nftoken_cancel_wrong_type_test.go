package nft_test

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestNFTokenCancelOfferWrongType(t *testing.T) {
	t.Run("AccountRoot", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		accountKey := keylet.Account(alice.ID)
		wrongTypeID := hex.EncodeToString(accountKey.Key[:])
		balanceBefore := env.Balance(alice)
		sequenceBefore := env.Seq(alice)

		result := env.Submit(nft.NFTokenCancelOffer(alice, wrongTypeID).Build())

		jtx.RequireTxClaimed(t, result, jtx.TecNO_PERMISSION)
		require.True(t, result.Applied)
		require.Equal(t, env.BaseFee(), result.Fee)
		require.Equal(t, balanceBefore-env.BaseFee(), env.Balance(alice))
		require.Equal(t, sequenceBefore+1, env.Seq(alice))
		require.True(t, env.LedgerEntryExists(accountKey))
	})

	t.Run("MixedListIsAtomic", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()

		nftID := nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
		env.Close()

		offerKey := keylet.NFTokenOffer(alice.ID, env.Seq(alice))
		jtx.RequireTxSuccess(t, env.Submit(
			nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(0)).Build()))
		env.Close()

		accountKey := keylet.Account(alice.ID)
		wrongTypeID := hex.EncodeToString(accountKey.Key[:])
		offerID := hex.EncodeToString(offerKey.Key[:])
		balanceBefore := env.Balance(alice)
		sequenceBefore := env.Seq(alice)
		ownerCountBefore := env.OwnerCount(alice)

		result := env.Submit(nft.NFTokenCancelOffer(alice, wrongTypeID, offerID).Build())

		jtx.RequireTxClaimed(t, result, jtx.TecNO_PERMISSION)
		require.True(t, result.Applied)
		require.Equal(t, env.BaseFee(), result.Fee)
		require.Equal(t, balanceBefore-env.BaseFee(), env.Balance(alice))
		require.Equal(t, sequenceBefore+1, env.Seq(alice))
		require.Equal(t, ownerCountBefore, env.OwnerCount(alice))
		require.True(t, env.LedgerEntryExists(offerKey))
	})
}
