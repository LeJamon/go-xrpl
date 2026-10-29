package rpcenv

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

// Based on rippled LedgerRPC_test.cpp testLedgerCurrent and
// AccountTx_test.cpp testParameters as of XRPLF/rippled#8345.
func TestLedgerSelectorShortcuts(t *testing.T) {
	env := New(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	result, rpcErr := env.RPC("ledger", map[string]any{})
	require.Nil(t, rpcErr)
	response := didJSONMap(t, result)
	require.NotContains(t, response, "ledger")
	closed := response["closed"].(map[string]any)["ledger"].(map[string]any)
	open := response["open"].(map[string]any)["ledger"].(map[string]any)
	require.Equal(t, true, closed["closed"])
	require.Equal(t, strconv.FormatUint(uint64(env.LastClosedLedger().Sequence()), 10), closed["ledger_index"])
	require.Equal(t, false, open["closed"])
	require.Equal(t, strconv.FormatUint(uint64(env.LedgerSeq()), 10), open["ledger_index"])

	result, rpcErr = env.RPC("account_info", map[string]any{"account": alice.Address, "ledger_index": ""})
	require.Nil(t, rpcErr, "an empty ledger_index selects the current ledger")
	require.EqualValues(t, env.LedgerSeq(), didJSONMap(t, result)["ledger_current_index"])

	for _, index := range []string{"validated", "closed"} {
		result, rpcErr := env.RPC("account_tx", map[string]any{"account": alice.Address, "ledger_index": index})
		require.Nil(t, rpcErr, index)
		require.NotEmpty(t, didJSONMap(t, result)["transactions"], index)
	}
	for _, index := range []string{"current", ""} {
		_, rpcErr := env.RPC("account_tx", map[string]any{"account": alice.Address, "ledger_index": index})
		require.NotNil(t, rpcErr, index)
		require.Equal(t, "lgrNotValidated", rpcErr.ErrorString, index)
	}
}

// Based on rippled TransactionEntry_test.cpp testBadInput as of
// XRPLF/rippled#8484.
func TestTransactionEntryDefaultLedgerAndHashShape(t *testing.T) {
	env := New(t)
	const hash = "E2FE8D4AF3FCC3944DDF6CD8CDDC5E3F0AD50863EF8919AFEF10CB6408CD4D05"

	_, rpcErr := env.RPC("transaction_entry", map[string]any{"tx_hash": hash})
	require.NotNil(t, rpcErr)
	require.Equal(t, "notYetImplemented", rpcErr.ErrorString)
	require.Contains(t, rpcErr.Extra, "ledger_current_index")
	require.NotContains(t, rpcErr.Extra, "ledger_hash")
	require.Equal(t, false, rpcErr.Extra["validated"])

	for _, txHash := range []any{map[string]any{}, []any{}} {
		_, rpcErr := env.RPC("transaction_entry", map[string]any{"ledger": "closed", "tx_hash": txHash})
		require.NotNil(t, rpcErr)
		require.Equal(t, "malformedRequest", rpcErr.ErrorString)
	}
}
