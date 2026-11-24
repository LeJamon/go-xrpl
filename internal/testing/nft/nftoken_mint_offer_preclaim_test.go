package nft_test

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requireSerializedResult(t *testing.T, result jtx.TxResult, expectedCode string) {
	t.Helper()
	assert.Equal(t, expectedCode, result.Code)
	require.True(t, result.Applied)
	require.NotNil(t, result.Metadata)

	metadataBlob, err := tx.SerializeMetadata(result.Metadata)
	require.NoError(t, err)
	decoded, err := binarycodec.Decode(hex.EncodeToString(metadataBlob))
	require.NoError(t, err)
	assert.Equal(t, expectedCode, decoded["TransactionResult"])
}

func TestNFTokenMintOfferPreclaimOrder(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("NFTokenMintOffer")
	env.EnableFeature("fixEnforceNFTokenTrustlineV2")

	issuer := jtx.NewAccount("issuer")
	minter := jtx.NewAccount("minter")
	refusing := jtx.NewAccount("refusing")
	eligible := jtx.NewAccount("eligible")
	missing := jtx.NewAccount("missing")
	env.Fund(issuer, minter, refusing, eligible)
	env.Close()

	// Leave the minter's USD trust line unauthorized so the destination decides which error wins.
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).RequireAuth().Build()))
	jtx.RequireTxSuccess(t, env.Submit(trustset.TrustSet(minter, issuer.IOU("USD", 1000)).Build()))
	env.EnableDisallowIncomingNFTokenOffer(refusing)
	env.Close()

	nftID := nft.GetNextNFTokenID(env, minter, 0, 0, 0)
	ordinaryMint := env.SubmitSigned(nft.NFTokenMint(minter, 0).Build())
	requireSerializedResult(t, ordinaryMint, "tesSUCCESS")
	env.Close()

	amount := issuer.IOU("USD", 10)
	for _, tc := range []struct {
		name        string
		destination *jtx.Account
		wantCode    string
	}{
		{"missing", missing, "tecNO_DST"},
		{"refusing", refusing, "tecNO_PERMISSION"},
		{"eligible", eligible, "tecNO_AUTH"},
	} {
		t.Run("standaloneOffer/"+tc.name, func(t *testing.T) {
			offer := nft.NFTokenCreateSellOffer(minter, nftID, amount).Destination(tc.destination)
			requireSerializedResult(t, env.SubmitSigned(offer.Build()), tc.wantCode)
		})
		t.Run("mintWithOffer/"+tc.name, func(t *testing.T) {
			mint := nft.NFTokenMint(minter, 0).Amount(amount).Destination(tc.destination)
			requireSerializedResult(t, env.SubmitSigned(mint.Build()), tc.wantCode)
		})
	}
}
