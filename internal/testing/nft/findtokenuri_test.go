package nft_test

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/stretchr/testify/require"
)

// TestFindTokenURI verifies nftoken.FindTokenURI, which backs the SmartEscrow
// get_nft host function: it walks an owner's NFTokenPages and returns the raw
// URI of a minted token, and reports not-found for an unminted id.
func TestFindTokenURI(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	const uri = "https://example.com/nft/metadata.json"
	flags := nftoken.NFTokenFlagTransferable
	nftIDHex := nft.GetNextNFTokenID(env, alice, 0, flags, 0)

	env.Submit(nft.NFTokenMint(alice, 0).Transferable().URI(uri).Build())
	env.Close()

	var nftID [32]byte
	raw, err := hex.DecodeString(nftIDHex)
	require.NoError(t, err)
	require.Len(t, raw, 32)
	copy(nftID[:], raw)

	owner := alice.AccountID()

	got, found, hasURI := nftoken.FindTokenURI(env.Ledger(), owner, nftID)
	require.True(t, found, "minted NFT should be found")
	require.True(t, hasURI, "minted NFT should carry a URI")
	require.Equal(t, uri, string(got))

	// An unminted token id is not found.
	var missing [32]byte
	missing[0] = 0xAB
	_, found, _ = nftoken.FindTokenURI(env.Ledger(), owner, missing)
	require.False(t, found, "unminted NFT should not be found")
}
