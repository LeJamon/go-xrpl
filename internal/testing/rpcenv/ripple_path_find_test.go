package rpcenv_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/rpcenv"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// newPathFindEnv builds the standard ripple_path_find fixture: alice and bob
// trust gw for USD, alice holds 50 gw/USD.
func newPathFindEnv(t *testing.T) (*rpcenv.Env, *jtx.Account, *jtx.Account, *jtx.Account) {
	t.Helper()
	env := rpcenv.New(t)

	gw := jtx.NewAccount("gateway")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(gw, uint64(jtx.XRP(10000)))
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	result := env.Submit(trustset.TrustLine(alice, "USD", gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)
	result = env.Submit(trustset.TrustLine(bob, "USD", gw, "1000").Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	result = env.Submit(payment.PayIssued(gw, alice, tx.NewIssuedAmountFromFloat64(50, "USD", gw.Address)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	return env, gw, alice, bob
}

// asJSONMap round-trips a handler response through JSON so tests can inspect
// it the way a client would.
func asJSONMap(t *testing.T, result any) map[string]any {
	t.Helper()
	b, err := json.Marshal(result)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func iouAmount(value, currency, issuer string) map[string]any {
	return map[string]any{"currency": currency, "issuer": issuer, "value": value}
}

// TestRipplePathFind_ParseErrors checks each parseJson-stage error token in
// rippled's validation order (PathRequest::parseJson).
func TestRipplePathFind_ParseErrors(t *testing.T) {
	env, gw, alice, bob := newPathFindEnv(t)

	usd5 := iouAmount("5", "USD", gw.Address)
	usdAll := iouAmount("-1", "USD", gw.Address)

	manySourceCurrencies := func(n int) []map[string]any {
		sc := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			sc = append(sc, map[string]any{"currency": strconv.Itoa(i + 100)})
		}
		return sc
	}

	cases := []struct {
		name      string
		params    map[string]any
		wantError string
		wantCode  int
	}{
		{
			name:      "missing source_account",
			params:    map[string]any{},
			wantError: "srcActMissing", wantCode: 66,
		},
		{
			name:      "missing destination_account",
			params:    map[string]any{"source_account": alice.Address},
			wantError: "dstActMissing", wantCode: 49,
		},
		{
			name: "missing destination_amount",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
			},
			wantError: "dstAmtMissing", wantCode: 52,
		},
		{
			name: "malformed source_account",
			params: map[string]any{
				"source_account":      "not-an-address",
				"destination_account": bob.Address,
				"destination_amount":  usd5,
			},
			wantError: "srcActMalformed", wantCode: 65,
		},
		{
			name: "non-string source_account",
			params: map[string]any{
				"source_account":      42,
				"destination_account": bob.Address,
				"destination_amount":  usd5,
			},
			wantError: "srcActMalformed", wantCode: 65,
		},
		{
			name: "malformed destination_account",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": "not-an-address",
				"destination_amount":  usd5,
			},
			wantError: "dstActMalformed", wantCode: 48,
		},
		{
			name: "unparseable destination_amount",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  "not-a-number",
			},
			wantError: "dstAmtMalformed", wantCode: 51,
		},
		{
			name: "XRP destination_amount as object",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  iouAmount("5", "XRP", gw.Address),
			},
			wantError: "dstAmtMalformed", wantCode: 51,
		},
		{
			name: "zero destination_amount",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  iouAmount("0", "USD", gw.Address),
			},
			wantError: "dstAmtMalformed", wantCode: 51,
		},
		{
			name: "negative non-convert-all destination_amount",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  iouAmount("-5", "USD", gw.Address),
			},
			wantError: "dstAmtMalformed", wantCode: 51,
		},
		{
			name: "send_max without convert-all destination_amount",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usd5,
				"send_max":            "100000000",
			},
			wantError: "dstAmtMalformed", wantCode: 51,
		},
		{
			name: "zero send_max",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usdAll,
				"send_max":            "0",
			},
			wantError: "sendMaxMalformed", wantCode: 64,
		},
		{
			name: "unparseable send_max",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usdAll,
				"send_max":            "not-a-number",
			},
			wantError: "sendMaxMalformed", wantCode: 64,
		},
		{
			name: "empty source_currencies",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usd5,
				"source_currencies":   []any{},
			},
			wantError: "srcCurMalformed", wantCode: 69,
		},
		{
			name: "source_currencies over max_src_cur",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usd5,
				"source_currencies":   manySourceCurrencies(19),
			},
			wantError: "srcCurMalformed", wantCode: 69,
		},
		{
			name: "source_currencies entry without currency",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usd5,
				"source_currencies":   []map[string]any{{"issuer": gw.Address}},
			},
			wantError: "srcCurMalformed", wantCode: 69,
		},
		{
			name: "source_currencies invalid currency code",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usd5,
				"source_currencies":   []map[string]any{{"currency": "TOOLONG"}},
			},
			wantError: "srcCurMalformed", wantCode: 69,
		},
		{
			name: "source_currencies XRP with issuer",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usd5,
				"source_currencies":   []map[string]any{{"currency": "XRP", "issuer": gw.Address}},
			},
			wantError: "srcCurMalformed", wantCode: 69,
		},
		{
			name: "source_currencies malformed issuer",
			params: map[string]any{
				"source_account":      alice.Address,
				"destination_account": bob.Address,
				"destination_amount":  usd5,
				"source_currencies":   []map[string]any{{"currency": "USD", "issuer": "junk"}},
			},
			wantError: "srcIsrMalformed", wantCode: 70,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, rpcErr := env.RPC("ripple_path_find", tc.params)
			require.NotNil(t, rpcErr, "expected %s", tc.wantError)
			require.Equal(t, tc.wantError, rpcErr.ErrorString)
			require.Equal(t, tc.wantCode, rpcErr.Code)
		})
	}

	// Max allowed source_currencies (18) parses fine.
	result, rpcErr := env.RPC("ripple_path_find", map[string]any{
		"source_account":      alice.Address,
		"destination_account": bob.Address,
		"destination_amount":  usd5,
		"source_currencies":   manySourceCurrencies(18),
	})
	require.Nil(t, rpcErr)
	require.NotNil(t, result)
}

// TestRipplePathFind_LedgerChecks covers PathRequest::isValid: source must
// exist, destination existence rules, and reserve enforcement for funding
// payments.
func TestRipplePathFind_LedgerChecks(t *testing.T) {
	env, gw, alice, bob := newPathFindEnv(t)
	ghost := jtx.NewAccount("ghost") // never funded

	t.Run("source account not found", func(t *testing.T) {
		_, rpcErr := env.RPC("ripple_path_find", map[string]any{
			"source_account":      ghost.Address,
			"destination_account": bob.Address,
			"destination_amount":  iouAmount("5", "USD", gw.Address),
		})
		require.NotNil(t, rpcErr)
		require.Equal(t, "srcActNotFound", rpcErr.ErrorString)
		require.Equal(t, 67, rpcErr.Code)
	})

	t.Run("IOU to non-existent destination", func(t *testing.T) {
		_, rpcErr := env.RPC("ripple_path_find", map[string]any{
			"source_account":      alice.Address,
			"destination_account": ghost.Address,
			"destination_amount":  iouAmount("5", "USD", gw.Address),
		})
		require.NotNil(t, rpcErr)
		require.Equal(t, "actNotFound", rpcErr.ErrorString)
		require.Equal(t, 19, rpcErr.Code)
	})

	t.Run("XRP below reserve to non-existent destination", func(t *testing.T) {
		below := strconv.FormatUint(env.ReserveBase()-1, 10)
		_, rpcErr := env.RPC("ripple_path_find", map[string]any{
			"source_account":      alice.Address,
			"destination_account": ghost.Address,
			"destination_amount":  below,
		})
		require.NotNil(t, rpcErr)
		require.Equal(t, "dstAmtMalformed", rpcErr.ErrorString)
	})

	t.Run("XRP at reserve to non-existent destination", func(t *testing.T) {
		atReserve := strconv.FormatUint(env.ReserveBase(), 10)
		result, rpcErr := env.RPC("ripple_path_find", map[string]any{
			"source_account":      alice.Address,
			"destination_account": ghost.Address,
			"destination_amount":  atReserve,
		})
		require.Nil(t, rpcErr)
		resp := asJSONMap(t, result)
		// XRP to XRP never needs explicit path steps.
		for _, a := range resp["alternatives"].([]any) {
			require.Empty(t, a.(map[string]any)["paths_computed"])
		}
	})
}

// TestRipplePathFind_LedgerSelection covers explicit ledger_index /
// ledger_hash selection plus the merged ledger metadata fields.
func TestRipplePathFind_LedgerSelection(t *testing.T) {
	env, gw, alice, bob := newPathFindEnv(t)
	closed := env.LastClosedLedger()
	require.NotNil(t, closed)

	baseParams := func() map[string]any {
		return map[string]any{
			"source_account":      alice.Address,
			"destination_account": bob.Address,
			"destination_amount":  iouAmount("5", "USD", gw.Address),
		}
	}

	t.Run("by ledger_index", func(t *testing.T) {
		params := baseParams()
		params["ledger_index"] = closed.Sequence()
		result, rpcErr := env.RPC("ripple_path_find", params)
		require.Nil(t, rpcErr)
		resp := asJSONMap(t, result)
		require.EqualValues(t, closed.Sequence(), resp["ledger_index"])
		require.NotEmpty(t, resp["ledger_hash"])
		require.Equal(t, true, resp["validated"])
		require.NotEmpty(t, resp["alternatives"])
	})

	t.Run("by ledger_hash", func(t *testing.T) {
		params := baseParams()
		hash := closed.Hash()
		params["ledger_hash"] = fmt.Sprintf("%X", hash[:])
		result, rpcErr := env.RPC("ripple_path_find", params)
		require.Nil(t, rpcErr)
		resp := asJSONMap(t, result)
		require.EqualValues(t, closed.Sequence(), resp["ledger_index"])
	})

	t.Run("unknown ledger_index", func(t *testing.T) {
		params := baseParams()
		params["ledger_index"] = 999999
		_, rpcErr := env.RPC("ripple_path_find", params)
		require.NotNil(t, rpcErr)
		require.Equal(t, "lgrNotFound", rpcErr.ErrorString)
	})

	t.Run("unknown ledger_hash", func(t *testing.T) {
		params := baseParams()
		params["ledger_hash"] = "00000000000000000000000000000000000000000000000000000000000000AA"
		_, rpcErr := env.RPC("ripple_path_find", params)
		require.NotNil(t, rpcErr)
		require.Equal(t, "lgrNotFound", rpcErr.ErrorString)
	})

	t.Run("malformed ledger_hash", func(t *testing.T) {
		params := baseParams()
		params["ledger_hash"] = "zz"
		_, rpcErr := env.RPC("ripple_path_find", params)
		require.NotNil(t, rpcErr)
		require.Equal(t, "invalidParams", rpcErr.ErrorString)
	})
}

// TestRipplePathFind_Success covers the happy path: response shape, id echo,
// full_reply, destination_currencies, and source_amount.
func TestRipplePathFind_Success(t *testing.T) {
	env, gw, alice, bob := newPathFindEnv(t)

	result, rpcErr := env.RPC("ripple_path_find", map[string]any{
		"id":                  17,
		"source_account":      alice.Address,
		"destination_account": bob.Address,
		"destination_amount":  iouAmount("5", "USD", gw.Address),
	})
	require.Nil(t, rpcErr)
	resp := asJSONMap(t, result)

	require.Equal(t, alice.Address, resp["source_account"])
	require.Equal(t, bob.Address, resp["destination_account"])
	require.Equal(t, true, resp["full_reply"])
	require.EqualValues(t, 17, resp["id"])
	require.NotContains(t, resp, "ledger_hash", "no ledger fields without an explicit ledger selector")

	currencies, ok := resp["destination_currencies"].([]any)
	require.True(t, ok)
	require.Contains(t, currencies, "USD")
	require.Contains(t, currencies, "XRP")

	alternatives, ok := resp["alternatives"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, alternatives)

	alt := alternatives[0].(map[string]any)
	require.Contains(t, alt, "paths_computed")
	require.Contains(t, alt, "paths_canonical")
	require.NotContains(t, alt, "destination_amount", "destination_amount only present in convert-all mode")
	srcAmt, ok := alt["source_amount"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "USD", srcAmt["currency"])
	require.Equal(t, "5", srcAmt["value"])
}

// TestRipplePathFind_ConvertAll covers destination_amount: -1 (convert-all):
// the alternative reports the maximum deliverable amount.
func TestRipplePathFind_ConvertAll(t *testing.T) {
	env, gw, alice, bob := newPathFindEnv(t)

	result, rpcErr := env.RPC("ripple_path_find", map[string]any{
		"source_account":      alice.Address,
		"destination_account": bob.Address,
		"destination_amount":  iouAmount("-1", "USD", gw.Address),
	})
	require.Nil(t, rpcErr)
	resp := asJSONMap(t, result)

	echo, ok := resp["destination_amount"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "-1", echo["value"])

	alternatives, ok := resp["alternatives"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, alternatives)

	alt := alternatives[0].(map[string]any)
	srcAmt := alt["source_amount"].(map[string]any)
	require.Equal(t, "50", srcAmt["value"], "alice can send all 50 USD")
	dstAmt, ok := alt["destination_amount"].(map[string]any)
	require.True(t, ok, "convert-all alternatives must report destination_amount")
	require.Equal(t, "50", dstAmt["value"])
}
