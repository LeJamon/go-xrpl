package rpcenv_test

import (
	"maps"
	"testing"

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
