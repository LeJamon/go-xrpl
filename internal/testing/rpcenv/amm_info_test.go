package rpcenv_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/testing/amm"
	"github.com/LeJamon/goXRPLd/internal/testing/rpcenv"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
)

// TestAMMInfo_EndToEnd_ByAssetPair is the canary integration test for the
// rpcenv harness. It builds a real XRP/USD AMM through the tx engine, then
// asks the production amm_info handler for it via the same dispatcher the
// HTTP server uses.
//
// This is exactly the bug class issue #482 exposed: amm_info passed its
// mock-backed unit tests while shipping the wrong field shape against real
// ledger state. With this test in place, future regressions in the
// success path are caught in CI.
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

		resp, ok := result.(map[string]interface{})
		if !ok {
			t.Fatalf("amm_info: response is %T, want map", result)
		}

		ammResult, ok := resp["amm"].(map[string]interface{})
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

// TestAMMInfo_EndToEnd_ByAccount verifies the second discovery path —
// look up the AMM by its pseudo-account, exercising the AMMID indirection
// in the AccountRoot SLE that bug #482 also touched.
func TestAMMInfo_EndToEnd_ByAccount(t *testing.T) {
	amm.TestAMM(t, nil, 250, func(ammEnv *amm.AMMTestEnv, ammAcc *jtx.Account) {
		env := rpcenv.Wrap(t, ammEnv.TestEnv)

		result, rpcErr := env.RPC("amm_info", map[string]any{
			"amm_account": ammAcc.Address,
		})
		if rpcErr != nil {
			t.Fatalf("amm_info: %s (code=%d)", rpcErr.Message, rpcErr.Code)
		}

		resp := result.(map[string]interface{})
		ammResult := resp["amm"].(map[string]interface{})
		if got := ammResult["account"]; got != ammAcc.Address {
			t.Fatalf("amm.account = %v, want %s", got, ammAcc.Address)
		}
	})
}

// TestAMMInfo_EndToEnd_NotFound checks that querying a non-existent asset
// pair surfaces actNotFound instead of crashing — confirming that the
// adapter's error mapping behaves like rippled.
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

