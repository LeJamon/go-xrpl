// Metadata/state regression test for NFTokenBurn cleaning up the NFT buy/sell
// offer DirectoryNodes it empties.
//
// When an NFT with offers is burned, rippled cancels each offer via
// deleteTokenOffer, which issues a dirRemove on the nft_sells/nft_buys
// directory (NFTokenUtils.cpp:698-704). When that empties the offer-directory
// page, dirRemove erases it (keepRoot=false), so the burn's metadata carries a
// DeletedNode:DirectoryNode and the directory leaves state.
//
// goXRPL's burn-only deleteNFTokenOffers helper removed each offer from the
// owner directory and erased it but never removed it from the NFT offer
// directory, so the now-stale, empty DirectoryNode was left in state — forking
// both account_hash (stale entry) and transaction_hash (missing DeletedNode).
// Confirmed against the live differential at seq 207/211.
package nft_test

import (
	"encoding/hex"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	nft "github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// decodeDeletedNodes returns DeletedNode inner objects (keyed by
// LedgerEntryType → slice) decoded from the serialized binary metadata blob.
func decodeDeletedNodes(t *testing.T, meta *tx.Metadata) map[string][]map[string]any {
	t.Helper()
	blob, err := tx.SerializeMetadata(meta)
	require.NoError(t, err)
	decoded, err := binarycodec.Decode(hex.EncodeToString(blob))
	require.NoError(t, err)
	out := map[string][]map[string]any{}
	nodes, _ := decoded["AffectedNodes"].([]any)
	for _, raw := range nodes {
		m, _ := raw.(map[string]any)
		inner, ok := m["DeletedNode"].(map[string]any)
		if !ok {
			continue
		}
		let, _ := inner["LedgerEntryType"].(string)
		out[let] = append(out[let], inner)
	}
	return out
}

func nftIDBytes(t *testing.T, nftID string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(nftID)
	require.NoError(t, err)
	require.Len(t, b, 32)
	var out [32]byte
	copy(out[:], b)
	return out
}

func TestNFTokenBurn_DeletesEmptiedOfferDirectories(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()

	// alice mints a transferable NFT (transferable is required for bob, a
	// non-issuer, to create a buy offer).
	nftID := nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
	env.Close()

	// alice creates a sell offer → populates the NFT sell-offer directory.
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(1000000)).Build()))
	env.Close()

	// bob creates a buy offer → populates the NFT buy-offer directory.
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(bob, nftID, tx.NewXRPAmount(1000000), alice).Build()))
	env.Close()

	id := nftIDBytes(t, nftID)
	sellsKey := keylet.NFTSells(id)
	buysKey := keylet.NFTBuys(id)

	// Both offer directories exist before the burn.
	require.True(t, env.LedgerEntryExists(sellsKey), "sell-offer directory should exist pre-burn")
	require.True(t, env.LedgerEntryExists(buysKey), "buy-offer directory should exist pre-burn")

	// alice burns the NFT.
	res := env.Submit(nft.NFTokenBurn(alice, nftID).Build())
	jtx.RequireTxSuccess(t, res)
	require.NotNil(t, res.Metadata)

	// Metadata must carry a DeletedNode:DirectoryNode for each emptied offer dir.
	deleted := decodeDeletedNodes(t, res.Metadata)
	dirDeleted := map[string]bool{}
	for _, d := range deleted["DirectoryNode"] {
		idx, _ := d["LedgerIndex"].(string)
		dirDeleted[strings.ToUpper(idx)] = true
	}
	require.True(t, dirDeleted[strings.ToUpper(hex.EncodeToString(sellsKey.Key[:]))],
		"expected DeletedNode for the emptied sell-offer DirectoryNode")
	require.True(t, dirDeleted[strings.ToUpper(hex.EncodeToString(buysKey.Key[:]))],
		"expected DeletedNode for the emptied buy-offer DirectoryNode")

	env.Close()

	// State must no longer contain the offer directories.
	require.False(t, env.LedgerEntryExists(sellsKey), "sell-offer directory must be erased from state after burn")
	require.False(t, env.LedgerEntryExists(buysKey), "buy-offer directory must be erased from state after burn")
}
