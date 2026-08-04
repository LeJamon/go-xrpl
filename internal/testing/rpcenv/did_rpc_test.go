package rpcenv

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
)

func TestDIDRPCLifecycle(t *testing.T) {
	env := New(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	_, rpcErr := env.RPC("ledger_entry", map[string]any{"did": alice.Address, "ledger_index": "validated"})
	requireRPCError(t, rpcErr, "entryNotFound")
	_, rpcErr = env.RPC("ledger_entry", map[string]any{"did": "not-an-address"})
	requireRPCError(t, rpcErr, "invalidParams")

	jtx.RequireTxSuccess(t, env.Submit(did.DIDSet(alice).URI("uri1").Document("doc1").Build()))
	current := requireDIDLedgerEntry(t, env, alice.Address, "current")
	require.Equal(t, false, current["validated"])
	requireDIDRPCFields(t, current["node"], "75726931", "646F6331", true)

	_, rpcErr = env.RPC("ledger_entry", map[string]any{"did": alice.Address, "ledger_index": "validated"})
	requireRPCError(t, rpcErr, "entryNotFound")

	env.Close()
	validated := requireDIDLedgerEntry(t, env, alice.Address, "validated")
	require.Equal(t, true, validated["validated"])
	requireDIDRPCFields(t, validated["node"], "75726931", "646F6331", true)
	requireDIDAccountObjects(t, env, alice.Address, "75726931", "646F6331", true)
	requireDIDLedgerData(t, env, "75726931", "646F6331", true)

	jtx.RequireTxSuccess(t, env.Submit(did.DIDSet(alice).URI("uri2").Document("").Build()))
	current = requireDIDLedgerEntry(t, env, alice.Address, "current")
	requireDIDRPCFields(t, current["node"], "75726932", "", false)
	validated = requireDIDLedgerEntry(t, env, alice.Address, "validated")
	requireDIDRPCFields(t, validated["node"], "75726931", "646F6331", true)

	env.Close()
	validated = requireDIDLedgerEntry(t, env, alice.Address, "validated")
	requireDIDRPCFields(t, validated["node"], "75726932", "", false)
	requireDIDAccountObjects(t, env, alice.Address, "75726932", "", false)
	requireDIDLedgerData(t, env, "75726932", "", false)

	jtx.RequireTxSuccess(t, env.Submit(did.DIDDelete(alice).Build()))
	_, rpcErr = env.RPC("ledger_entry", map[string]any{"did": alice.Address, "ledger_index": "current"})
	requireRPCError(t, rpcErr, "entryNotFound")
	validated = requireDIDLedgerEntry(t, env, alice.Address, "validated")
	requireDIDRPCFields(t, validated["node"], "75726932", "", false)

	env.Close()
	_, rpcErr = env.RPC("ledger_entry", map[string]any{"did": alice.Address, "ledger_index": "validated"})
	requireRPCError(t, rpcErr, "entryNotFound")
	objects, rpcErr := env.RPC("account_objects", map[string]any{
		"account":      alice.Address,
		"type":         "did",
		"ledger_index": "validated",
	})
	require.Nil(t, rpcErr)
	require.Empty(t, didJSONMap(t, objects)["account_objects"])
	data, rpcErr := env.RPC("ledger_data", map[string]any{"type": "did", "ledger_index": "validated"})
	require.Nil(t, rpcErr)
	require.Empty(t, didJSONMap(t, data)["state"])
}

func requireDIDLedgerEntry(t *testing.T, env *Env, account, ledgerIndex string) map[string]any {
	t.Helper()
	result, rpcErr := env.RPC("ledger_entry", map[string]any{
		"did":          account,
		"ledger_index": ledgerIndex,
	})
	require.Nil(t, rpcErr)
	return didJSONMap(t, result)
}

func requireDIDAccountObjects(t *testing.T, env *Env, account, uri, document string, documentPresent bool) {
	t.Helper()
	result, rpcErr := env.RPC("account_objects", map[string]any{
		"account":      account,
		"type":         "did",
		"ledger_index": "validated",
	})
	require.Nil(t, rpcErr)
	response := didJSONMap(t, result)
	require.Equal(t, true, response["validated"])
	objects := response["account_objects"].([]any)
	require.Len(t, objects, 1)
	requireDIDRPCFields(t, objects[0], uri, document, documentPresent)
}

func requireDIDLedgerData(t *testing.T, env *Env, uri, document string, documentPresent bool) {
	t.Helper()
	result, rpcErr := env.RPC("ledger_data", map[string]any{
		"type":         "did",
		"ledger_index": "validated",
	})
	require.Nil(t, rpcErr)
	response := didJSONMap(t, result)
	require.Equal(t, true, response["validated"])
	state := response["state"].([]any)
	require.Len(t, state, 1)
	requireDIDRPCFields(t, state[0], uri, document, documentPresent)
}

func requireDIDRPCFields(t *testing.T, value any, uri, document string, documentPresent bool) {
	t.Helper()
	fields := value.(map[string]any)
	require.Equal(t, "DID", fields["LedgerEntryType"])
	require.Equal(t, uri, fields["URI"])
	actualDocument, ok := fields["DIDDocument"]
	require.Equal(t, documentPresent, ok)
	if documentPresent {
		require.Equal(t, document, actualDocument)
	}
}

func requireRPCError(t *testing.T, rpcErr *types.RpcError, code string) {
	t.Helper()
	require.NotNil(t, rpcErr)
	require.Equal(t, code, rpcErr.ErrorString)
}

func didJSONMap(t *testing.T, result any) map[string]any {
	t.Helper()
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var object map[string]any
	require.NoError(t, json.Unmarshal(data, &object))
	return object
}
