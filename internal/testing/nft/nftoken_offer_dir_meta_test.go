// Metadata/state regression test for the NFToken buy/sell offer DirectoryNode
// created by NFTokenCreateOffer.
//
// rippled stamps the NFT's offer-directory root with sfFlags
// (lsfNFTokenSellOffers=2 / lsfNFTokenBuyOffers=1) and sfNFTokenID via the
// dirInsert describe callback (NFTokenUtils.cpp:1059-1063). goXRPL passed a nil
// describe callback, so the created DirectoryNode lacked Flags and NFTokenID —
// its CreatedNode.NewFields (and SLE bytes) diverged from rippled. The owner
// directory likewise lacked sfOwner (describeOwnerDir). These assertions decode
// the produced binary meta blob (not just the in-memory struct).
package nft_test

import (
	"encoding/hex"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	nft "github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// decodeCreatedNodes returns CreatedNode inner objects (keyed by LedgerEntryType
// → slice) decoded from the serialized binary metadata blob.
func decodeCreatedNodes(t *testing.T, meta *tx.Metadata) map[string][]map[string]any {
	t.Helper()
	blob, err := tx.SerializeMetadata(meta)
	require.NoError(t, err)
	decoded, err := binarycodec.Decode(hex.EncodeToString(blob))
	require.NoError(t, err)
	out := map[string][]map[string]any{}
	nodes, _ := decoded["AffectedNodes"].([]any)
	for _, raw := range nodes {
		m, _ := raw.(map[string]any)
		inner, ok := m["CreatedNode"].(map[string]any)
		if !ok {
			continue
		}
		let, _ := inner["LedgerEntryType"].(string)
		out[let] = append(out[let], inner)
	}
	return out
}

func TestNFTokenCreateSellOffer_Meta_OfferDirectory(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	nftID := nft.GetNextNFTokenID(env, alice, 0, 8, 0)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
	env.Close()

	res := env.Submit(nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(1000000)).Build())
	jtx.RequireTxSuccess(t, res)
	require.NotNil(t, res.Metadata)

	created := decodeCreatedNodes(t, res.Metadata)
	dirs := created["DirectoryNode"]
	require.NotEmpty(t, dirs, "expected created DirectoryNode(s)")

	var offerDir, ownerDir map[string]any
	for _, d := range dirs {
		nf, _ := d["NewFields"].(map[string]any)
		if _, hasNFT := nf["NFTokenID"]; hasNFT {
			offerDir = nf
		} else {
			ownerDir = nf
		}
	}

	// The NFT sell-offer directory carries Flags=lsfNFTokenSellOffers(2) and the
	// NFTokenID, exactly as rippled's describe callback writes them.
	require.NotNil(t, offerDir, "NFT sell-offer DirectoryNode NewFields expected")
	require.Equal(t, uint32(2), offerDir["Flags"], "sell-offer dir Flags must be lsfNFTokenSellOffers=2")
	gotID, _ := offerDir["NFTokenID"].(string)
	require.Equal(t, strings.ToUpper(nftID), strings.ToUpper(gotID))

	// The owner directory carries sfOwner (describeOwnerDir) and never an
	// NFTokenID or non-zero Flags.
	require.NotNil(t, ownerDir, "owner DirectoryNode NewFields expected")
	require.Equal(t, alice.Address, ownerDir["Owner"], "owner dir must carry sfOwner")
	_, ownerHasFlags := ownerDir["Flags"]
	require.False(t, ownerHasFlags, "owner dir Flags=0 is default and must be omitted from NewFields")
}

func TestNFTokenCreateBuyOffer_Meta_OfferDirectory(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice)
	env.Fund(bob)
	env.Close()

	// alice mints a transferable NFT.
	nftID := nft.GetNextNFTokenID(env, alice, 0, 8, 0)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
	env.Close()

	// bob creates a buy offer for alice's NFT.
	res := env.Submit(nft.NFTokenCreateBuyOffer(bob, nftID, tx.NewXRPAmount(1000000), alice).Build())
	jtx.RequireTxSuccess(t, res)
	require.NotNil(t, res.Metadata)

	created := decodeCreatedNodes(t, res.Metadata)
	var offerDir map[string]any
	for _, d := range created["DirectoryNode"] {
		nf, _ := d["NewFields"].(map[string]any)
		if _, hasNFT := nf["NFTokenID"]; hasNFT {
			offerDir = nf
		}
	}
	require.NotNil(t, offerDir, "NFT buy-offer DirectoryNode NewFields expected")
	// Buy-offer directory carries Flags=lsfNFTokenBuyOffers(1).
	require.Equal(t, uint32(1), offerDir["Flags"], "buy-offer dir Flags must be lsfNFTokenBuyOffers=1")
	gotID, _ := offerDir["NFTokenID"].(string)
	require.Equal(t, strings.ToUpper(nftID), strings.ToUpper(gotID))
}
