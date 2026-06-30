package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreAmm "github.com/LeJamon/go-xrpl/internal/tx/amm"
)

// TestAMMTeardownPreservesHolderReserveFlag is a regression test for #1141: on a
// full AMM teardown, deleteAMMTrustLine must clear a trust-line reserve flag only
// on the AMM side. The LP-token line carries its reserve flag on the non-AMM
// (holder) side; clearing it forks the ledger hash via a spurious
// PreviousFields.Flags on that DeletedNode while mainnet leaves the flag intact.
//
// A non-zero limit on the holder's LP-token line keeps the reserve from being
// released during the LP-token burn — rippled (rippleCreditIOU) only collapses a
// default line (limit == 0) — so the flag survives to deleteAMMTrustLine where
// the fix applies. Without it the burn clears the reserve first and the gated
// branch is never exercised.
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

	lpLine := findDeletedRippleState(t, result, lptCurrency)
	if lpLine == nil {
		t.Fatal("no DeletedNode RippleState for the LP-token line")
	}

	holderReserve := holderReserveBit(lpLine, ammAcc.Address)
	finalFlags := metadata.ToUint32(metadata.GetFinalField(lpLine, "Flags"))
	if finalFlags&holderReserve == 0 {
		t.Errorf("holder reserve flag (0x%X) was cleared on the LP-token line: FinalFields.Flags=0x%X", holderReserve, finalFlags)
	}
	if lpLine.PreviousFields != nil {
		if v, ok := lpLine.PreviousFields["Flags"]; ok {
			t.Errorf("spurious PreviousFields.Flags=%v on the LP-token line DeletedNode", v)
		}
	}

	// Contrast: the AMM-side pool line (USD) does release its reserve — the gated
	// branch the fix keeps clearing.
	usdLine := findDeletedRippleState(t, result, "USD")
	if usdLine == nil {
		t.Fatal("no DeletedNode RippleState for the USD pool line")
	}
	usdFinal := metadata.ToUint32(metadata.GetFinalField(usdLine, "Flags"))
	if usdFinal&(state.LsfLowReserve|state.LsfHighReserve) != 0 {
		t.Errorf("AMM-side reserve flag not cleared on the USD pool line: FinalFields.Flags=0x%X", usdFinal)
	}
}

// findDeletedRippleState returns the DeletedNode RippleState whose Balance is in
// the given currency, or nil.
func findDeletedRippleState(t *testing.T, result jtx.TxResult, currency string) *tx.AffectedNode {
	t.Helper()
	for _, n := range metadata.FindNodes(result.Metadata, "DeletedNode", "RippleState") {
		bal, ok := n.FinalFields["Balance"].(map[string]any)
		if !ok {
			continue
		}
		if cur, _ := bal["currency"].(string); cur == currency {
			return n
		}
	}
	return nil
}

// holderReserveBit returns the reserve flag of a trust line's non-AMM side,
// given the AMM account address.
func holderReserveBit(node *tx.AffectedNode, ammAddr string) uint32 {
	low, _ := node.FinalFields["LowLimit"].(map[string]any)
	if lowIssuer, _ := low["issuer"].(string); lowIssuer == ammAddr {
		return state.LsfHighReserve
	}
	return state.LsfLowReserve
}
