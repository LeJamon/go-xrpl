package rpcenv

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

func TestAccountTxPaginationKeepsMarkerOutsideChangedRange(t *testing.T) {
	env := New(t)
	account := jtx.NewAccount("account")
	destination := jtx.NewAccount("destination")
	env.Fund(account, destination)
	env.Close()
	ledgerMin := env.LedgerSeq()

	for range 3 {
		payment := paymenttx.NewPayment(account.Address, destination.Address, txcore.NewXRPAmount(1_000_000))
		jtx.RequireTxSuccess(t, env.SubmitSignedWith(payment, account))
		env.Close()
	}
	ledgerMax := env.LedgerSeq() - 1

	pageOneResult, rpcErr := env.RPCAs("account_tx", map[string]any{
		"account":          account.Address,
		"forward":          true,
		"ledger_index_min": ledgerMin,
		"ledger_index_max": ledgerMax,
		"limit":            1,
	}, types.RoleAdmin, types.ApiVersion2)
	require.Nil(t, rpcErr)
	pageOne := didJSONMap(t, pageOneResult)
	pageOneTransactions := pageOne["transactions"].([]any)
	require.Len(t, pageOneTransactions, 1)
	require.Equal(t, float64(ledgerMin), pageOneTransactions[0].(map[string]any)["ledger_index"])
	forwardMarker := pageOne["marker"].(map[string]any)
	forwardMarkerLedger := uint32(forwardMarker["ledger"].(float64))
	require.Equal(t, ledgerMin+1, forwardMarkerLedger)

	pageTwoResult, rpcErr := env.RPCAs("account_tx", map[string]any{
		"account":          account.Address,
		"forward":          true,
		"ledger_index_min": forwardMarkerLedger + 1,
		"ledger_index_max": ledgerMax,
		"limit":            2,
		"marker":           forwardMarker,
	}, types.RoleAdmin, types.ApiVersion2)
	require.Nil(t, rpcErr)
	pageTwo := didJSONMap(t, pageTwoResult)
	pageTwoTransactions := pageTwo["transactions"].([]any)
	require.Len(t, pageTwoTransactions, 2)
	require.Equal(t, float64(forwardMarkerLedger), pageTwoTransactions[0].(map[string]any)["ledger_index"])
	require.Equal(t, float64(ledgerMax), pageTwoTransactions[1].(map[string]any)["ledger_index"])
	require.NotContains(t, pageTwo, "marker")

	pageOneResult, rpcErr = env.RPCAs("account_tx", map[string]any{
		"account":          account.Address,
		"forward":          false,
		"ledger_index_min": ledgerMin,
		"ledger_index_max": ledgerMax,
		"limit":            1,
	}, types.RoleAdmin, types.ApiVersion2)
	require.Nil(t, rpcErr)
	pageOne = didJSONMap(t, pageOneResult)
	pageOneTransactions = pageOne["transactions"].([]any)
	require.Len(t, pageOneTransactions, 1)
	reverseMarker := pageOne["marker"].(map[string]any)
	reverseMarkerLedger := uint32(reverseMarker["ledger"].(float64))
	require.Equal(t, ledgerMax-1, reverseMarkerLedger)

	pageTwoResult, rpcErr = env.RPCAs("account_tx", map[string]any{
		"account":          account.Address,
		"forward":          false,
		"ledger_index_min": ledgerMin,
		"ledger_index_max": reverseMarkerLedger - 1,
		"limit":            2,
		"marker":           reverseMarker,
	}, types.RoleAdmin, types.ApiVersion2)
	require.Nil(t, rpcErr)
	pageTwo = didJSONMap(t, pageTwoResult)
	pageTwoTransactions = pageTwo["transactions"].([]any)
	require.Len(t, pageTwoTransactions, 2)
	require.Equal(t, float64(reverseMarkerLedger), pageTwoTransactions[0].(map[string]any)["ledger_index"])
	require.Equal(t, float64(ledgerMin), pageTwoTransactions[1].(map[string]any)["ledger_index"])
	require.NotContains(t, pageTwo, "marker")

	for _, markerLedger := range []uint32{0, ledgerMin, ledgerMax + 1} {
		unknownResult, rpcErr := env.RPCAs("account_tx", map[string]any{
			"account":          account.Address,
			"forward":          true,
			"ledger_index_min": ledgerMin,
			"marker": map[string]any{
				"ledger":   markerLedger,
				"seq":      99,
				"delegate": true,
			},
			"delegate": map[string]any{"delegate_filter": "actor"},
		}, types.RoleAdmin, types.ApiVersion2)
		require.Nil(t, rpcErr, "marker ledger %d", markerLedger)
		unknown := didJSONMap(t, unknownResult)
		require.Empty(t, unknown["transactions"], "marker ledger %d", markerLedger)
		require.NotContains(t, unknown, "marker")
	}
}
