package trustset

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

// TestTrustSet_CreateFrozenLineFromNoFreezeAccount covers tfSetFreeze on a new
// line from an lsfNoFreeze account. DeepFreeze rejects it in preclaim; without
// DeepFreeze the line is created frozen, since lsfNoFreeze only filters
// freeze changes on an existing line.
func TestTrustSet_CreateFrozenLineFromNoFreezeAccount(t *testing.T) {
	for _, deepFreeze := range []bool{true, false} {
		name := "DeepFreeze enabled"
		if !deepFreeze {
			name = "DeepFreeze disabled"
		}
		t.Run(name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if !deepFreeze {
				env.DisableFeature("DeepFreeze")
			}
			gw := jtx.NewAccount("gw")
			alice := jtx.NewAccount("alice")
			env.Fund(gw, alice)
			env.Close()
			env.EnableNoFreeze(alice)
			env.Close()

			result := env.Submit(TrustLine(alice, "USD", gw, "100").Freeze().Build())
			if deepFreeze {
				jtx.RequireTxFail(t, result, "tecNO_PERMISSION")
				if env.TrustLineExists(alice, gw, "USD") {
					t.Fatal("trust line must not be created")
				}
				return
			}
			jtx.RequireTxSuccess(t, result)
			env.Close()

			freezeFlag := state.LsfLowFreeze
			if state.CompareAccountIDs(alice.ID, gw.ID) > 0 {
				freezeFlag = state.LsfHighFreeze
			}
			if env.TrustLineFlags(alice, gw, "USD")&freezeFlag == 0 {
				t.Fatal("new trust line must carry alice's freeze flag")
			}
		})
	}
}

// TestTrustSet_MissingDestinationOrdering covers a TrustSet to an account that
// does not exist. With AMM or SingleAssetVault the missing destination is
// rejected first; with both off the deep-freeze invariants are evaluated
// before it.
func TestTrustSet_MissingDestinationOrdering(t *testing.T) {
	tests := []struct {
		name     string
		disable  []string
		freeze   bool
		wantCode string
	}{
		{name: "AMM enabled", freeze: true, disable: []string{"SingleAssetVault"}, wantCode: "tecNO_DST"},
		{name: "SingleAssetVault enabled", freeze: true, disable: []string{"AMM"}, wantCode: "tecNO_DST"},
		{name: "both disabled with freeze", freeze: true, disable: []string{"AMM", "SingleAssetVault"}, wantCode: "tecNO_PERMISSION"},
		{name: "both disabled without freeze", disable: []string{"AMM", "SingleAssetVault"}, wantCode: "tecNO_DST"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			for _, feature := range tc.disable {
				env.DisableFeature(feature)
			}
			gw := jtx.NewAccount("gw")
			alice := jtx.NewAccount("alice")
			env.Fund(alice)
			env.Close()
			env.EnableNoFreeze(alice)
			env.Close()

			builder := TrustLine(alice, "USD", gw, "100")
			if tc.freeze {
				builder = builder.Freeze()
			}
			jtx.RequireTxFail(t, env.Submit(builder.Build()), tc.wantCode)
		})
	}
}
