package rpcenv

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/check"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/paychan"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/keylet"
)

func keyHex(key [32]byte) string {
	return strings.ToUpper(hex.EncodeToString(key[:]))
}

func requireLedgerEntryNode(t *testing.T, env *Env, params map[string]any, wantIndex string) map[string]any {
	t.Helper()
	result, rpcErr := env.RPC("ledger_entry", params)
	require.Nil(t, rpcErr)
	response := didJSONMap(t, result)
	require.Equal(t, wantIndex, response["index"])
	return response["node"].(map[string]any)
}

// Based on rippled LedgerEntry_test.cpp testCheck, testNFTokenOffer,
// testPayChan and testSignerList (object selectors).
func TestLedgerEntryObjectSelectors(t *testing.T) {
	env := New(t)
	alice := jtx.NewAccount("alice")
	issuer := jtx.NewAccount("issuer")
	buyer := jtx.NewAccount("buyer")
	master := env.MasterAccount()
	env.Fund(alice, issuer, buyer)
	env.Close()

	checkSeq := env.Seq(master)
	jtx.RequireTxSuccess(t, env.Submit(check.CheckCreate(master, alice, jtx.XRPTxAmount(jtx.XRP(100))).Build()))

	nftokenID := strings.ToUpper(nft.GetNextNFTokenID(env.TestEnv, issuer, 0, nftoken.NFTokenFlagTransferable, 0))
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(issuer, 0).Transferable().Build()))
	env.Close()
	offerSeq := env.Seq(issuer)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(issuer, nftokenID, jtx.XRPTxAmount(1)).Destination(buyer).Build()))

	payChanSeq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(paychan.ChannelCreate(alice, master, jtx.XRP(57), 18, alice.PublicKeyHex()).Build()))

	env.SetSignerList(alice, 1, []jtx.TestSigner{
		{Account: jtx.NewAccount("bogie"), Weight: 1},
		{Account: jtx.NewAccount("demon"), Weight: 1},
	})
	env.Close()

	t.Run("check", func(t *testing.T) {
		index := keyHex(keylet.Check(master.ID, checkSeq).Key)
		node := requireLedgerEntryNode(t, env, map[string]any{
			"check": map[string]any{"account": master.Address, "seq": checkSeq}, "ledger_index": "validated",
		}, index)
		require.Equal(t, "Check", node["LedgerEntryType"])
		require.Equal(t, "100000000", node["SendMax"])

		_, rpcErr := env.RPC("ledger_entry", map[string]any{
			"check": map[string]any{"account": master.Address, "seq": checkSeq + 1000}, "ledger_index": "validated",
		})
		requireRPCError(t, rpcErr, "entryNotFound")
	})

	t.Run("nft_offer", func(t *testing.T) {
		index := keyHex(keylet.NFTokenOffer(issuer.ID, offerSeq).Key)
		node := requireLedgerEntryNode(t, env, map[string]any{
			"nft_offer": map[string]any{"owner": issuer.Address, "seq": offerSeq},
		}, index)
		require.Equal(t, "NFTokenOffer", node["LedgerEntryType"])
		require.Equal(t, issuer.Address, node["Owner"])
		require.Equal(t, nftokenID, node["NFTokenID"])
		require.Equal(t, "1", node["Amount"])

		_, rpcErr := env.RPC("ledger_entry", map[string]any{
			"nft_offer": map[string]any{"owner": issuer.Address, "seq": offerSeq + 1000},
		})
		requireRPCError(t, rpcErr, "entryNotFound")
	})

	t.Run("payment_channel", func(t *testing.T) {
		index := keyHex(keylet.PayChannel(alice.ID, master.ID, payChanSeq).Key)
		node := requireLedgerEntryNode(t, env, map[string]any{
			"payment_channel": map[string]any{"account": alice.Address, "destination": master.Address, "seq": payChanSeq},
			"ledger_index":    "validated",
		}, index)
		require.Equal(t, "57000000", node["Amount"])
		require.Equal(t, "0", node["Balance"])
		require.EqualValues(t, 18, node["SettleDelay"])

		_, rpcErr := env.RPC("ledger_entry", map[string]any{
			"payment_channel": map[string]any{"account": alice.Address, "destination": master.Address, "seq": payChanSeq + 1000},
			"ledger_index":    "validated",
		})
		requireRPCError(t, rpcErr, "entryNotFound")
	})

	t.Run("signer_list", func(t *testing.T) {
		index := keyHex(keylet.SignerList(alice.ID).Key)
		node := requireLedgerEntryNode(t, env, map[string]any{"signer_list": index, "ledger_index": "validated"}, index)
		require.Equal(t, "SignerList", node["LedgerEntryType"])
		require.EqualValues(t, 1, node["SignerQuorum"])

		node = requireLedgerEntryNode(t, env, map[string]any{
			"signer_list": map[string]any{"account": alice.Address}, "ledger_index": "validated",
		}, index)
		require.Equal(t, "SignerList", node["LedgerEntryType"])
		require.EqualValues(t, 1, node["SignerQuorum"])

		_, rpcErr := env.RPC("ledger_entry", map[string]any{
			"signer_list": map[string]any{"account": master.Address}, "ledger_index": "validated",
		})
		requireRPCError(t, rpcErr, "entryNotFound")

		closed := env.LastClosedLedger().Hash()
		_, rpcErr = env.RPC("ledger_entry", map[string]any{"signer_list": keyHex(closed), "ledger_index": "validated"})
		requireRPCError(t, rpcErr, "entryNotFound")
	})
}
