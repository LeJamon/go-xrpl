// Metadata/state-completeness regression test for NFTokenCreateOffer.
//
// rippled's NFTokenOffer ledger object uses sfOwner (ledger_entries.macro
// ltNFTOKEN_OFFER; NFTokenUtils.cpp:1074 (*offer)[sfOwner] = acctID).
// goXRPL serialized the creator as sfAccount instead, which diverged the SLE
// bytes (account_hash fork) and emitted "Account" rather than "Owner" in the
// CreatedNode NewFields. This fixes the serialization to sfOwner and asserts
// the created-offer NewFields match rippled (Owner, NFTokenID, Amount, Flags).
package nft_test

import (
	"encoding/hex"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	nft "github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestNFTokenCreateSellOffer_Meta_UsesOwner(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// transferable mint (flag 8) so the NFT id matches GetNextNFTokenID.
	nftID := nft.GetNextNFTokenID(env, alice, 0, 8, 0)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
	env.Close()

	res := env.Submit(nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(1000000)).Build())
	jtx.RequireTxSuccess(t, res)
	require.NotNil(t, res.Metadata)

	var nf map[string]any
	var offerIdx string
	for _, n := range res.Metadata.AffectedNodes {
		if n.NodeType == "CreatedNode" && n.LedgerEntryType == "NFTokenOffer" {
			nf = n.NewFields
			offerIdx = n.LedgerIndex
		}
	}
	require.NotNil(t, nf, "NFTokenOffer CreatedNode expected")

	owner, hasOwner := nf["Owner"]
	require.True(t, hasOwner, "NewFields must contain Owner (rippled sfOwner)")
	require.Equal(t, alice.Address, owner)
	_, hasAccount := nf["Account"]
	require.False(t, hasAccount, "NewFields must NOT contain Account (rippled NFTokenOffer has no sfAccount)")

	// A sell offer carries lsfSellNFToken (1, non-default), the NFTokenID, and
	// the non-default sell Amount. rippled NewFields includes every present
	// non-default field carrying sMD_Create (ApplyStateTable.cpp:251).
	require.Equal(t, uint32(1), nf["Flags"], "sell offer Flags must be lsfSellNFToken=1")
	gotID, _ := nf["NFTokenID"].(string)
	require.Equal(t, strings.ToUpper(nftID), strings.ToUpper(gotID))
	require.Equal(t, "1000000", nf["Amount"], "NewFields.Amount must be the 1 XRP sell price")

	raw, err := hex.DecodeString(offerIdx)
	require.NoError(t, err)
	var k [32]byte
	copy(k[:], raw)
	data, err := env.LedgerEntry(keylet.Keylet{Key: k})
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	_, sleHasOwner := fields["Owner"]
	require.True(t, sleHasOwner, "serialized NFTokenOffer SLE must contain sfOwner")
	_, sleHasAccount := fields["Account"]
	require.False(t, sleHasAccount, "serialized NFTokenOffer SLE must NOT contain sfAccount")
}
