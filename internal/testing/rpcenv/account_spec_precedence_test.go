package rpcenv

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

type precedenceCase struct {
	name    string
	params  map[string]any
	code    string
	message string
}

func runPrecedenceCases(t *testing.T, env *Env, method string, cases []precedenceCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(method+"/"+tc.name, func(t *testing.T) {
			_, rpcErr := env.RPC(method, tc.params)
			require.NotNil(t, rpcErr)
			require.Equal(t, tc.code, rpcErr.ErrorString)
			require.Equal(t, tc.message, rpcErr.Message)
		})
	}
}

func requireLedgerFieldsOnError(t *testing.T, rpcErr *rpcerrors.RpcError, want bool) {
	t.Helper()
	require.NotNil(t, rpcErr)
	_, hasCurrent := rpcErr.Extra["ledger_current_index"]
	require.Equal(t, want, hasCurrent)
	if want {
		require.Equal(t, false, rpcErr.Extra["validated"])
	}
}

const badLedgerHash = "Invalid field 'ledger_hash', not hex string."

// Based on rippled AccountCurrencies_test.cpp, AccountObjects_test.cpp
// (account_nfts) and AccountOffers_test.cpp as of XRPLF/rippled#8561.
func TestAccountSpecPrecedence8561(t *testing.T) {
	env := New(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()
	bogus := jtx.NewAccount("bogus").Address

	cases := []precedenceCase{
		{"bad ledger without account", map[string]any{"ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"bad ledger before non-string account", map[string]any{"account": 1, "ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"bad ledger before malformed account", map[string]any{"account": "llIIOO", "ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"bad ledger before non-string ident", map[string]any{"ident": 1, "ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"bad ledger before malformed ident", map[string]any{"ident": "llIIOO", "ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"missing account", map[string]any{}, "invalidParams", "Missing field 'account'."},
		{"non-string ident", map[string]any{"ident": 1}, "invalidParams", "Invalid field 'ident'."},
	}
	runPrecedenceCases(t, env, "account_currencies", cases)
	_, rpcErr := env.RPC("account_currencies", map[string]any{"account": "llIIOO"})
	require.Equal(t, "actMalformed", rpcErr.ErrorString)
	requireLedgerFieldsOnError(t, rpcErr, true)

	runPrecedenceCases(t, env, "account_nfts", []precedenceCase{
		{"missing account", map[string]any{}, "invalidParams", "Missing field 'account'."},
		{"bad ledger before malformed account", map[string]any{"account": "llIIOO", "ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"limit before missing account root", map[string]any{"account": bogus, "limit": 0}, "invalidParams", "Invalid field 'limit'."},
		{"limit before missing ledger", map[string]any{"account": alice.Address, "ledger_index": 1000, "limit": 0}, "invalidParams", "Invalid field 'limit'."},
		{"string limit", map[string]any{"account": alice.Address, "limit": "1"}, "invalidParams", "Invalid field 'limit', not unsigned integer."},
		{"negative limit", map[string]any{"account": alice.Address, "limit": -1}, "invalidParams", "Invalid field 'limit', not unsigned integer."},
		{"boolean limit", map[string]any{"account": alice.Address, "limit": true}, "invalidParams", "Invalid field 'limit', not unsigned integer."},
		{"marker before missing account root", map[string]any{"account": bogus, "marker": "NOT_A_MARKER"}, "invalidParams", "Invalid field 'marker'."},
		{"marker type before missing ledger", map[string]any{"account": alice.Address, "ledger_index": 1000, "marker": 17}, "invalidParams", "Invalid field 'marker', not string."},
	})
	_, rpcErr = env.RPC("account_nfts", map[string]any{"account": "llIIOO"})
	require.Equal(t, "actMalformed", rpcErr.ErrorString)
	requireLedgerFieldsOnError(t, rpcErr, false)

	runPrecedenceCases(t, env, "account_offers", []precedenceCase{
		{"limit before malformed account", map[string]any{"account": "", "limit": "0"}, "invalidParams", "Invalid field 'limit', not unsigned integer."},
		{"limit before missing account root", map[string]any{"account": bogus, "limit": "0"}, "invalidParams", "Invalid field 'limit', not unsigned integer."},
		{"marker before missing ledger", map[string]any{"account": alice.Address, "ledger_index": 10, "marker": "NOT_A_MARKER"}, "invalidParams", "Invalid field 'marker'."},
		{"bad ledger before malformed account", map[string]any{"account": "llIIOO", "ledger_hash": 1}, "invalidParams", badLedgerHash},
	})
	_, rpcErr = env.RPC("account_offers", map[string]any{"account": "llIIOO"})
	require.Equal(t, "actMalformed", rpcErr.ErrorString)
	requireLedgerFieldsOnError(t, rpcErr, true)
	_, rpcErr = env.RPC("account_offers", map[string]any{"account": bogus})
	require.Equal(t, "actNotFound", rpcErr.ErrorString)
}

// Based on rippled AccountInfo_test.cpp, AccountLines_test.cpp and
// PayChan_test.cpp (account_channels) as of XRPLF/rippled#8573.
func TestAccountSpecPrecedence8573(t *testing.T) {
	env := New(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()
	master := env.MasterAccount().Address
	bogie := jtx.NewAccount("bogie").Address

	runPrecedenceCases(t, env, "account_info", []precedenceCase{
		{"bad ledger before missing account", map[string]any{"ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"missing account", map[string]any{}, "invalidParams", "Missing field 'account'."},
		{"non-string ident", map[string]any{"ident": 1}, "invalidParams", "Invalid field 'ident'."},
	})

	result, rpcErr := env.RPC("account_info", map[string]any{"account": master, "ident": 1})
	require.Nil(t, rpcErr, "ident is ignored when account is present")
	require.Equal(t, master, didJSONMap(t, result)["account_data"].(map[string]any)["Account"])

	_, rpcErr = env.RPC("account_info", map[string]any{"account": "llIIOO"})
	require.Equal(t, "actMalformed", rpcErr.ErrorString)
	requireLedgerFieldsOnError(t, rpcErr, true)

	_, rpcErr = env.RPC("account_info", map[string]any{"account": bogie})
	require.Equal(t, "actNotFound", rpcErr.ErrorString)
	require.Equal(t, bogie, rpcErr.Extra["account"])
	requireLedgerFieldsOnError(t, rpcErr, true)

	_, rpcErr = env.RPC("account_info", map[string]any{"account": master, "ledger_index": "validated", "queue": "false"})
	require.NotNil(t, rpcErr)
	require.Equal(t, "invalidParams", rpcErr.ErrorString)
	require.Equal(t, "Invalid parameters.", rpcErr.Message)
	require.Contains(t, rpcErr.Extra, "ledger_index")
	require.NotContains(t, rpcErr.Extra, "account_data")

	runPrecedenceCases(t, env, "account_lines", []precedenceCase{
		{"bad ledger before missing account", map[string]any{"ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"limit before missing account root", map[string]any{"account": bogie, "limit": -1}, "invalidParams", "Invalid field 'limit', not unsigned integer."},
		{"limit before malformed account", map[string]any{"account": "llIIOO", "limit": 0}, "invalidParams", "Invalid field 'limit'."},
		{"peer type before missing ledger", map[string]any{"account": alice.Address, "peer": 1, "ledger_index": 1000}, "invalidParams", "Invalid field 'peer'."},
		{"marker before malformed peer", map[string]any{"account": alice.Address, "peer": "llIIOO", "marker": "NOT_A_MARKER"}, "invalidParams", "Invalid parameters."},
		{"marker type", map[string]any{"account": alice.Address, "marker": 1}, "invalidParams", "Invalid field 'marker', not string."},
		{"malformed marker", map[string]any{"account": alice.Address, "marker": "NOT_A_MARKER"}, "invalidParams", "Invalid parameters."},
		{"missing account root before malformed peer", map[string]any{"account": bogie, "peer": "llIIOO"}, "actNotFound", "Account not found."},
	})
	_, rpcErr = env.RPC("account_lines", map[string]any{"account": "llIIOO"})
	require.Equal(t, "actMalformed", rpcErr.ErrorString)
	requireLedgerFieldsOnError(t, rpcErr, true)
	_, rpcErr = env.RPC("account_lines", map[string]any{"account": alice.Address, "peer": "llIIOO"})
	require.Equal(t, "actMalformed", rpcErr.ErrorString)
	requireLedgerFieldsOnError(t, rpcErr, true)

	runPrecedenceCases(t, env, "account_channels", []precedenceCase{
		{"bad ledger before missing account", map[string]any{"ledger_hash": 1}, "invalidParams", badLedgerHash},
		{"malformed account before missing ledger", map[string]any{"account": "llIIOO", "ledger_index": 1000}, "actMalformed", "Account malformed."},
		{"destination before missing account root", map[string]any{"account": bogie, "destination_account": "llIIOO"}, "actMalformed", "Account malformed."},
		{"destination type before missing ledger", map[string]any{"account": alice.Address, "destination_account": 1, "ledger_index": 1000}, "invalidParams", "Invalid field 'destination_account'."},
		{"limit before missing account root", map[string]any{"account": bogie, "limit": 0}, "invalidParams", "Invalid field 'limit'."},
		{"marker type", map[string]any{"account": alice.Address, "marker": 1}, "invalidParams", "Invalid field 'marker', not string."},
		{"malformed marker before missing ledger", map[string]any{"account": alice.Address, "marker": "NOT_A_MARKER", "ledger_index": 1000}, "invalidParams", "Invalid parameters."},
		{"missing account root", map[string]any{"account": bogie, "destination_account": ""}, "actNotFound", "Account not found."},
	})
	_, rpcErr = env.RPC("account_channels", map[string]any{"account": "llIIOO"})
	require.Equal(t, "actMalformed", rpcErr.ErrorString)
	requireLedgerFieldsOnError(t, rpcErr, false)
}
