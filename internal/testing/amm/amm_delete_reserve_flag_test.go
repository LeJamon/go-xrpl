package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
)

// TestAMMTeardownPreservesHolderReserveFlag is a regression test for #1141:
// deleteAMMTrustLine must clear a trust-line reserve flag only on the AMM side.
// The LP-token line holds its reserve on the non-AMM (holder) side; clearing it
// forks the ledger hash with a spurious PreviousFields.Flags on that DeletedNode.
//
// The non-zero limit on the holder's LP-token line is load-bearing: it stops the
// LP-token burn from releasing the reserve (rippled collapses only a default,
// limit==0 line), so the flag survives to deleteAMMTrustLine. Without it the test
// is vacuous — the burn clears the flag before the gated branch runs.
func TestAMMTeardownPreservesHolderReserveFlag(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.FundWithIOUs(30000, 0)
	env.Close()

	if r := env.Submit(amm.AMMCreate(env.Alice, amm.XRPAmount(10000), amm.IOUAmount(env.GW, "USD", 10000)).Build()); !r.Success {
		t.Fatalf("AMMCreate: %s - %s", r.Code, r.Message)
	}
	env.Close()

	ammAcc := env.ReadAMMAccount(amm.XRP(), env.USD)
	lptCurrency := coreAmm.GenerateAMMLPTCurrency("XRP", "USD")

	limit := tx.NewIssuedAmountFromFloat64(1_000_000_000, lptCurrency, ammAcc.Address)
	if r := env.Submit(trustset.TrustSet(env.Alice, limit).Build()); !r.Success {
		t.Fatalf("TrustSet LP limit: %s - %s", r.Code, r.Message)
	}
	env.Close()

	// Redeem all LP tokens; this deletes the AMM account and its trust lines.
	result := env.Submit(amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).WithdrawAll().Build())
	if !result.Success {
		t.Fatalf("WithdrawAll: %s - %s", result.Code, result.Message)
	}
	if env.ReadAMMData(amm.XRP(), env.USD) != nil {
		t.Fatal("AMM should be deleted after WithdrawAll")
	}

	deletedLine := func(currency string) *tx.AffectedNode {
		for _, n := range metadata.FindNodes(result.Metadata, "DeletedNode", "RippleState") {
			if bal, ok := n.FinalFields["Balance"].(map[string]any); ok {
				if cur, _ := bal["currency"].(string); cur == currency {
					return n
				}
			}
		}
		return nil
	}

	lpLine := deletedLine(lptCurrency)
	if lpLine == nil {
		t.Fatal("no DeletedNode RippleState for the LP-token line")
	}

	// The holder is the non-AMM side; its reserve flag must remain set.
	holderReserve := uint32(state.LsfLowReserve)
	if low, _ := lpLine.FinalFields["LowLimit"].(map[string]any); low != nil {
		if iss, _ := low["issuer"].(string); iss == ammAcc.Address {
			holderReserve = state.LsfHighReserve
		}
	}
	if flags := metadata.ToUint32(metadata.GetFinalField(lpLine, "Flags")); flags&holderReserve == 0 {
		t.Errorf("holder reserve flag (0x%X) cleared on the LP-token line: FinalFields.Flags=0x%X", holderReserve, flags)
	}
	if lpLine.PreviousFields != nil {
		if v, ok := lpLine.PreviousFields["Flags"]; ok {
			t.Errorf("spurious PreviousFields.Flags=%v on the LP-token line DeletedNode", v)
		}
	}

	// Complementary case: the AMM-side pool line (USD) does release its reserve —
	// the gated branch the fix keeps clearing.
	usdLine := deletedLine("USD")
	if usdLine == nil {
		t.Fatal("no DeletedNode RippleState for the USD pool line")
	}
	if flags := metadata.ToUint32(metadata.GetFinalField(usdLine, "Flags")); flags&(state.LsfLowReserve|state.LsfHighReserve) != 0 {
		t.Errorf("AMM-side reserve flag not cleared on the USD pool line: FinalFields.Flags=0x%X", flags)
	}
}
