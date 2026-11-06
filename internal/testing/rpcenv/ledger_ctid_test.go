package rpcenv

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

// Based on rippled LedgerRPC_test.cpp testLedgerExpandedTransactionsCTID.
func TestLedgerExpandedTransactionsCTID(t *testing.T) {
	env := New(t)
	env.Fund(jtx.NewAccount("alice"))
	env.Close()

	ledgerTransactions := func(params map[string]any, apiVersion int) (map[string]any, []any) {
		t.Helper()
		result, rpcErr := env.RPCAs("ledger", params, types.RoleGuest, apiVersion)
		require.Nil(t, rpcErr)
		ledger := didJSONMap(t, result)["ledger"].(map[string]any)
		txs, ok := ledger["transactions"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, txs)
		return ledger, txs
	}

	t.Run("api v2 json", func(t *testing.T) {
		ledger, txs := ledgerTransactions(map[string]any{
			"ledger_index": "validated", "transactions": true, "expand": true,
		}, types.ApiVersion2)
		ledgerIndex := uint32(ledger["ledger_index"].(float64))
		for _, entry := range txs {
			tx := entry.(map[string]any)
			txIndex := uint32(tx["meta"].(map[string]any)["TransactionIndex"].(float64))
			expected, ok := handlers.EncodeCTID(ledgerIndex, txIndex, 0)
			require.True(t, ok)
			require.Equal(t, expected, tx["ctid"])
		}
	})

	t.Run("api v1 json", func(t *testing.T) {
		_, txs := ledgerTransactions(map[string]any{
			"ledger_index": "validated", "transactions": true, "expand": true,
		}, types.ApiVersion1)
		for _, entry := range txs {
			require.Contains(t, entry.(map[string]any), "ctid")
		}
	})

	t.Run("api v2 binary", func(t *testing.T) {
		_, txs := ledgerTransactions(map[string]any{
			"ledger_index": "validated", "transactions": true, "expand": true, "binary": true,
		}, types.ApiVersion2)
		for _, entry := range txs {
			require.Contains(t, entry.(map[string]any), "ctid")
		}
	})

	t.Run("not expanded", func(t *testing.T) {
		_, txs := ledgerTransactions(map[string]any{
			"ledger_index": "validated", "transactions": true,
		}, types.ApiVersion2)
		for _, entry := range txs {
			require.IsType(t, "", entry)
		}
	})
}
