package rpcenv_test

import (
	"maps"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/rpcenv"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

func TestAMMInfo_EndToEnd_ByAssetPair(t *testing.T) {
	amm.TestAMM(t, nil, 500, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)

		result, rpcErr := env.RPC("amm_info", map[string]any{
			"asset":  map[string]any{"currency": "XRP"},
			"asset2": map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address},
		})
		if rpcErr != nil {
			t.Fatalf("amm_info: %s (code=%d)", rpcErr.Message, rpcErr.Code)
		}

		resp, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("amm_info: response is %T, want map", result)
		}

		ammResult, ok := resp["amm"].(map[string]any)
		if !ok {
			t.Fatalf("amm_info: missing or wrong-typed `amm` field: %#v", resp["amm"])
		}

		if got, ok := ammResult["account"].(string); !ok || got != ammAcc.Address {
			t.Fatalf("amm.account = %v (%T), want %s", ammResult["account"], ammResult["account"], ammAcc.Address)
		}
		if got, ok := ammResult["trading_fee"]; !ok {
			t.Errorf("amm.trading_fee missing: %#v", got)
		}
		if _, ok := ammResult["lp_token"]; !ok {
			t.Errorf("amm.lp_token missing")
		}
		if _, ok := ammResult["amount"]; !ok {
			t.Errorf("amm.amount missing")
		}
		if _, ok := ammResult["amount2"]; !ok {
			t.Errorf("amm.amount2 missing")
		}

		if _, ok := resp["ledger_index"]; !ok {
			t.Errorf("response.ledger_index missing")
		}
		if validated, _ := resp["validated"].(bool); !validated {
			t.Errorf("response.validated = false, want true (closed ledger should be reported as validated in standalone)")
		}
	})
}

// TestAMMInfo_EndToEnd_ByAccount exercises lookup by AMM pseudo-account,
// which goes through the AMMID indirection in AccountRoot.
func TestAMMInfo_EndToEnd_ByAccount(t *testing.T) {
	amm.TestAMM(t, nil, 250, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)

		result, rpcErr := env.RPC("amm_info", map[string]any{
			"amm_account": ammAcc.Address,
		})
		if rpcErr != nil {
			t.Fatalf("amm_info: %s (code=%d)", rpcErr.Message, rpcErr.Code)
		}

		resp, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("amm_info: response is %T, want map", result)
		}
		ammResult, ok := resp["amm"].(map[string]any)
		if !ok {
			t.Fatalf("amm_info: missing or wrong-typed `amm` field: %#v", resp["amm"])
		}
		if got := ammResult["account"]; got != ammAcc.Address {
			t.Fatalf("amm.account = %v, want %s", got, ammAcc.Address)
		}
	})
}

func TestAMMInfo_EndToEnd_NotFound(t *testing.T) {
	env := rpcenv.New(t)
	gw := jtx.NewAccount("gw")
	env.FundAmount(gw, uint64(jtx.XRP(1000)))
	env.Close()

	_, rpcErr := env.RPC("amm_info", map[string]any{
		"asset":  map[string]any{"currency": "XRP"},
		"asset2": map[string]any{"currency": "USD", "issuer": gw.Address},
	})
	if rpcErr == nil {
		t.Fatalf("amm_info on missing pair: expected error, got nil")
	}
}

// TestAMMInfo_LPAccountParam verifies that the account (LP holder) param
// switches lp_token from the pool total to that account's LP token balance.
// Reference: rippled AMMInfo.cpp:195-197, AMMInfo_test.cpp testSimpleRpc.
func TestAMMInfo_LPAccountParam(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)

		base := map[string]any{
			"asset":  map[string]any{"currency": "XRP"},
			"asset2": map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address},
		}

		lpToken := func(params map[string]any) map[string]any {
			t.Helper()
			result, rpcErr := env.RPC("amm_info", params)
			if rpcErr != nil {
				t.Fatalf("amm_info: %s (code=%d)", rpcErr.Message, rpcErr.Code)
			}
			ammResult, ok := result.(map[string]any)["amm"].(map[string]any)
			if !ok {
				t.Fatalf("amm_info: missing amm field: %#v", result)
			}
			tok, ok := ammResult["lp_token"].(map[string]any)
			if !ok {
				t.Fatalf("amm_info: lp_token is %#v, want map", ammResult["lp_token"])
			}
			return tok
		}

		withAccount := func(addr string) map[string]any {
			params := map[string]any{"account": addr}
			maps.Copy(params, base)
			return params
		}

		total := lpToken(base)

		// alice created the AMM and holds all LP tokens, so her balance
		// equals the pool total.
		aliceTok := lpToken(withAccount(ammEnv.Alice.Address))
		if aliceTok["currency"] != total["currency"] ||
			aliceTok["issuer"] != total["issuer"] ||
			aliceTok["value"] != total["value"] {
			t.Errorf("lp_token for alice = %#v, want pool total %#v", aliceTok, total)
		}

		// carol holds no LP tokens: same currency/issuer, zero value.
		carolTok := lpToken(withAccount(ammEnv.Carol.Address))
		if carolTok["currency"] != total["currency"] || carolTok["issuer"] != total["issuer"] {
			t.Errorf("lp_token for carol = %#v, want currency/issuer of %#v", carolTok, total)
		}
		if got := carolTok["value"]; got != "0" {
			t.Errorf("lp_token.value for carol = %v, want 0", got)
		}
	})
}

// TestAMMInfo_ErrorPrecedenceByApiVersion ports the invalid-parameter loops
// from rippled AMMInfo_test.cpp testErrors: the asset/amm_account combination
// check runs before the per-field account checks for api_version < 3 and
// after them for api_version >= 3.
func TestAMMInfo_ErrorPrecedenceByApiVersion(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)
		bogie := jtx.NewAccount("bogie") // valid address, not in the ledger

		xrpAsset := map[string]any{"currency": "XRP"}
		usdAsset := map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address}

		// Invalid {asset, asset2, amm_account} combinations; ammAccount fills
		// the amm_account slot of the rows that have one.
		invalidCombos := func(ammAccount string) []map[string]any {
			return []map[string]any{
				{"asset": xrpAsset},
				{"asset2": usdAsset},
				{"asset": xrpAsset, "amm_account": ammAccount},
				{"asset2": usdAsset, "amm_account": ammAccount},
				{"asset": xrpAsset, "asset2": usdAsset, "amm_account": ammAccount},
				{},
			}
		}

		withAccount := func(row map[string]any, account string) map[string]any {
			params := map[string]any{"account": account}
			maps.Copy(params, row)
			return params
		}

		expectErr := func(params map[string]any, apiVersion int, wantCode int, wantMsg string) {
			t.Helper()
			_, rpcErr := env.RPCAs("amm_info", params, types.RoleAdmin, apiVersion)
			if rpcErr == nil {
				t.Fatalf("amm_info(%#v) v%d: expected error, got nil", params, apiVersion)
			}
			if rpcErr.Code != wantCode || rpcErr.Message != wantMsg {
				t.Errorf("amm_info(%#v) v%d = %q (code=%d), want %q (code=%d)",
					params, apiVersion, rpcErr.Message, rpcErr.Code, wantMsg, wantCode)
			}
		}

		// Valid combination + nonexistent LP account → actMalformed.
		expectErr(withAccount(map[string]any{
			"asset":  xrpAsset,
			"asset2": usdAsset,
		}, bogie.Address), types.ApiVersion2, types.RpcACT_MALFORMED, "Account malformed.")

		// Unparseable asset (IOU missing its issuer) → issueMalformed on both
		// versions when the combination is valid.
		badAsset := map[string]any{"currency": "USD"}
		validComboBadAsset := map[string]any{"asset": badAsset, "asset2": usdAsset}
		expectErr(validComboBadAsset, types.ApiVersion2, types.RpcISSUE_MALFORMED, "Issue is malformed.")
		expectErr(validComboBadAsset, types.ApiVersion3, types.RpcISSUE_MALFORMED, "Issue is malformed.")

		// Unparseable asset in an invalid combination: the combination check
		// wins below v3, the asset parse wins from v3 on.
		invalidComboBadAsset := map[string]any{"asset": badAsset}
		expectErr(invalidComboBadAsset, types.ApiVersion2, types.RpcINVALID_PARAMS, "Invalid parameters.")
		expectErr(invalidComboBadAsset, types.ApiVersion3, types.RpcISSUE_MALFORMED, "Issue is malformed.")

		// Invalid combinations (amm_account = the AMM's pseudo-account).
		for _, row := range invalidCombos(ammAcc.Address) {
			expectErr(row, types.ApiVersion2, types.RpcINVALID_PARAMS, "Invalid parameters.")

			// With a bad LP account on top: combination wins below v3, the
			// malformed account wins from v3 on.
			expectErr(withAccount(row, bogie.Address), types.ApiVersion2, types.RpcINVALID_PARAMS, "Invalid parameters.")
			expectErr(withAccount(row, bogie.Address), types.ApiVersion3, types.RpcACT_MALFORMED, "Account malformed.")
		}

		// Invalid combinations with a nonexistent amm_account.
		for _, row := range invalidCombos(bogie.Address) {
			expectErr(row, types.ApiVersion2, types.RpcINVALID_PARAMS, "Invalid parameters.")

			wantCode, wantMsg := types.RpcINVALID_PARAMS, "Invalid parameters."
			if _, hasAMMAccount := row["amm_account"]; hasAMMAccount {
				wantCode, wantMsg = types.RpcACT_MALFORMED, "Account malformed."
			}
			expectErr(row, types.ApiVersion3, wantCode, wantMsg)
		}
	})
}

// TestAMMInfo_IssueMalformedVariants covers the asset shapes rippled's
// issueFromJson rejects with issueMalformed (Issue.cpp:94-145): an issuer
// on XRP, invalid currency codes, mpt_issuance_id, a bad issuer address,
// null, and non-object values.
func TestAMMInfo_IssueMalformedVariants(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)
		usdAsset := map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address}

		badAssets := []any{
			map[string]any{"currency": "XRP", "issuer": ammEnv.GW.Address},
			map[string]any{"currency": "USDX", "issuer": ammEnv.GW.Address},
			map[string]any{"currency": "US-", "issuer": ammEnv.GW.Address},
			map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address, "mpt_issuance_id": "00"},
			map[string]any{"currency": "USD", "issuer": "not-an-address"},
			map[string]any{"currency": "USD"},
			nil,
			"USD",
		}
		for _, asset := range badAssets {
			_, rpcErr := env.RPC("amm_info", map[string]any{"asset": asset, "asset2": usdAsset})
			if rpcErr == nil {
				t.Fatalf("amm_info(asset=%#v): expected error, got nil", asset)
			}
			if rpcErr.Code != types.RpcISSUE_MALFORMED || rpcErr.Message != "Issue is malformed." {
				t.Errorf("amm_info(asset=%#v) = %q (code=%d), want issueMalformed",
					asset, rpcErr.Message, rpcErr.Code)
			}
		}

		// An explicit null issuer on XRP is tolerated, like rippled.
		result, rpcErr := env.RPC("amm_info", map[string]any{
			"asset":  map[string]any{"currency": "XRP", "issuer": nil},
			"asset2": usdAsset,
		})
		if rpcErr != nil {
			t.Fatalf("amm_info(XRP with null issuer): %s (code=%d)", rpcErr.Message, rpcErr.Code)
		}
		if _, ok := result.(map[string]any)["amm"]; !ok {
			t.Errorf("amm_info(XRP with null issuer): missing amm field: %#v", result)
		}
	})
}

// TestAMMInfo_IdenticalAssetPair ports rippled AMMInfo_test.cpp:53-60: a
// well-formed pair naming the same issue twice keys no AMM → actNotFound.
func TestAMMInfo_IdenticalAssetPair(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)
		usdAsset := map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address}

		_, rpcErr := env.RPC("amm_info", map[string]any{"asset": usdAsset, "asset2": usdAsset})
		if rpcErr == nil {
			t.Fatalf("amm_info(USD/USD): expected error, got nil")
		}
		if rpcErr.Code != types.RpcACT_NOT_FOUND || rpcErr.Message != "Account not found." {
			t.Errorf("amm_info(USD/USD) = %q (code=%d), want actNotFound", rpcErr.Message, rpcErr.Code)
		}
	})
}

// TestAMMInfo_PresenceSemantics verifies key presence drives the checks the
// way rippled's isMember does: empty-string account params count as
// supplied and fail account resolution instead of being ignored.
func TestAMMInfo_PresenceSemantics(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)

		expectActMalformed := func(params map[string]any) {
			t.Helper()
			_, rpcErr := env.RPC("amm_info", params)
			if rpcErr == nil {
				t.Fatalf("amm_info(%#v): expected error, got nil", params)
			}
			if rpcErr.Code != types.RpcACT_MALFORMED || rpcErr.Message != "Account malformed." {
				t.Errorf("amm_info(%#v) = %q (code=%d), want actMalformed",
					params, rpcErr.Message, rpcErr.Code)
			}
		}

		// Empty account with a valid pair: present, unresolvable.
		expectActMalformed(map[string]any{
			"asset":   map[string]any{"currency": "XRP"},
			"asset2":  map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address},
			"account": "",
		})

		// Empty amm_account alone is a valid combination, then fails the
		// account check.
		expectActMalformed(map[string]any{"amm_account": ""})
	})
}

// TestAMMInfo_AccountIdentForms verifies the account param accepts the
// identifier forms rippled's non-strict accountFromString does: a base58
// account public key and a seed/passphrase (RPCHelpers.cpp:43-85).
func TestAMMInfo_AccountIdentForms(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)

		lpToken := func(ident string) map[string]any {
			t.Helper()
			result, rpcErr := env.RPC("amm_info", map[string]any{
				"asset":   map[string]any{"currency": "XRP"},
				"asset2":  map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address},
				"account": ident,
			})
			if rpcErr != nil {
				t.Fatalf("amm_info(account=%q): %s (code=%d)", ident, rpcErr.Message, rpcErr.Code)
			}
			tok, ok := result.(map[string]any)["amm"].(map[string]any)["lp_token"].(map[string]any)
			if !ok {
				t.Fatalf("amm_info(account=%q): missing lp_token", ident)
			}
			return tok
		}

		byAddress := lpToken(ammEnv.Alice.Address)

		alicePubKey, err := addresscodec.EncodeAccountPublicKey(ammEnv.Alice.PublicKey)
		if err != nil {
			t.Fatalf("encode alice public key: %v", err)
		}
		if got := lpToken(alicePubKey); got["value"] != byAddress["value"] {
			t.Errorf("lp_token via public key = %#v, want %#v", got, byAddress)
		}

		// Test accounts derive from sha512half(name), the same derivation
		// rippled applies to a passphrase identifier.
		if got := lpToken(ammEnv.Alice.Name); got["value"] != byAddress["value"] {
			t.Errorf("lp_token via passphrase = %#v, want %#v", got, byAddress)
		}
	})
}

// TestAMMInfo_LedgerNotFoundPrecedence verifies a missing ledger outranks
// every param error, mirroring rippled's lookupLedger-first ordering
// (AMMInfo.cpp:81-84).
func TestAMMInfo_LedgerNotFoundPrecedence(t *testing.T) {
	amm.TestAMM(t, nil, 0, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)
		bogie := jtx.NewAccount("bogie")

		expectLgrNotFound := func(params map[string]any) {
			t.Helper()
			_, rpcErr := env.RPC("amm_info", params)
			if rpcErr == nil {
				t.Fatalf("amm_info(%#v): expected error, got nil", params)
			}
			if rpcErr.Code != types.RpcLGR_NOT_FOUND || rpcErr.Message != "Ledger not found." {
				t.Errorf("amm_info(%#v) = %q (code=%d), want lgrNotFound",
					params, rpcErr.Message, rpcErr.Code)
			}
		}

		// Over an invalid combination.
		expectLgrNotFound(map[string]any{"ledger_index": 9999999})

		// Over a bad account with a valid pair.
		expectLgrNotFound(map[string]any{
			"ledger_index": 9999999,
			"asset":        map[string]any{"currency": "XRP"},
			"asset2":       map[string]any{"currency": "USD", "issuer": ammEnv.GW.Address},
			"account":      bogie.Address,
		})
	})
}
