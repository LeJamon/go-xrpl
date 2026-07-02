package amm_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestAMMWithdraw_LastLPDeletionRecordsReconciledLPTokenBalance is a regression
// test for the reconcile-persist fix: when the last LP withdraws all and the AMM
// is deleted, the DeletedNode's FinalFields.LPTokenBalance must be the RECONCILED
// value (the LP's trustline balance), not the stale stored value.
//
// verifyAndAdjustLPTokenBalance reconciles amm.LPTokenBalance to the sole LP's
// trustline balance and persists the SLE (view.Update). On the withdraw-all
// deletion path nothing else re-writes the SLE, so the erased entry — and thus
// the DeletedNode metadata — carries whatever was last written. Without the
// persist the DeletedNode records the pre-adjustment balance, forking the ledger
// by 1 ULP. AMMClawback shares the same verifyAndAdjustLPTokenBalance persist, so
// this exercises the fix for both callers.
//
// Scenario mirrors rippled AMM_test.cpp testLPTokenBalance ("Last Liquidity
// Provider is the issuer of one token", line 7183): gw mints
// sqrt(XRP(2)*USD(1)) = 1414.2135623730951 at create and never touches it again,
// while alice/carol's large tfLPToken deposits+withdrawals drive the stored
// balance through magnitude ~1e6, losing low-order mantissa digits so it lands a
// sub-1e-3 distance away — exactly the rounding drift the reconcile targets.
//
// The gap is asserted before the final withdraw: if trustline == stored the
// reconcile is a no-op and the test would be vacuous (it fails loudly instead).
func TestAMMWithdraw_LastLPDeletionRecordsReconciledLPTokenBalance(t *testing.T) {
	env := amm.NewAMMTestEnv(t)

	// Fund gw/alice/carol with ample XRP and USD (gw is the USD issuer).
	env.TestEnv.FundAmount(env.GW, uint64(jtx.XRP(1_000_000)))
	env.TestEnv.FundAmount(env.Alice, uint64(jtx.XRP(1_000_000)))
	env.TestEnv.FundAmount(env.Carol, uint64(jtx.XRP(1_000_000)))
	env.Close()
	env.Trust(env.Alice, env.GW, "USD", 1_000_000_000)
	env.Trust(env.Carol, env.GW, "USD", 1_000_000_000)
	env.Close()
	env.PayIOU(env.GW, env.Alice, "USD", 1_000_000)
	env.PayIOU(env.GW, env.Carol, "USD", 1_000_000)
	env.Close()

	// gw creates AMM XRP(2)/USD(1): mints sqrt(2_000_000 drops * 1) = 1414.2135623730951.
	if r := env.Submit(amm.AMMCreate(env.GW, amm.XRPAmount(2), amm.IOUAmount(env.GW, "USD", 1)).Build()); !r.Success {
		t.Fatalf("AMMCreate: %s - %s", r.Code, r.Message)
	}
	env.Close()

	lptRef := amm.LPTokenAmount(env, amm.XRP(), env.USD, 0)

	// alice deposits 1.876123487565916 LP tokens (tfLPToken).
	aliceTokens := tx.NewIssuedAmount(1_876123487565916, -15, lptRef.Currency, lptRef.Issuer)
	if r := env.Submit(amm.AMMDeposit(env.Alice, amm.XRP(), env.USD).LPTokenOut(aliceTokens).LPToken().Build()); !r.Success {
		t.Fatalf("alice deposit: %s - %s", r.Code, r.Message)
	}
	env.Close()

	// carol deposits 1,000,000 LP tokens (tfLPToken) — the dominant precision-loss driver.
	carolTokens := tx.NewIssuedAmount(1_000_000, 0, lptRef.Currency, lptRef.Issuer)
	if r := env.Submit(amm.AMMDeposit(env.Carol, amm.XRP(), env.USD).LPTokenOut(carolTokens).LPToken().Build()); !r.Success {
		t.Fatalf("carol deposit: %s - %s", r.Code, r.Message)
	}
	env.Close()

	// alice and carol fully exit; gw becomes the sole LP.
	if r := env.Submit(amm.AMMWithdraw(env.Alice, amm.XRP(), env.USD).WithdrawAll().Build()); !r.Success {
		t.Fatalf("alice withdrawAll: %s - %s", r.Code, r.Message)
	}
	env.Close()
	if r := env.Submit(amm.AMMWithdraw(env.Carol, amm.XRP(), env.USD).WithdrawAll().Build()); !r.Success {
		t.Fatalf("carol withdrawAll: %s - %s", r.Code, r.Message)
	}
	env.Close()

	// Capture the stored LPTokenBalance and gw's trustline balance before the
	// final withdraw. The reconcile will overwrite the stored value with the
	// trustline value, so these must differ for the test to be meaningful.
	ammData := env.ReadAMMData(amm.XRP(), env.USD)
	if ammData == nil {
		t.Fatal("AMM not found before final withdraw")
	}
	stale := ammData.LPTokenBalance.Value()

	ammAcc := env.ReadAMMAccount(amm.XRP(), env.USD)
	if ammAcc == nil {
		t.Fatal("AMM account not found")
	}
	gwLPT := env.IOUBalance(env.GW, ammAcc, lptRef.Currency)
	reconciled := gwLPT.Value()

	t.Logf("gw trustline (reconciled)=%s  stored (stale)=%s", reconciled, stale)
	if reconciled == stale {
		t.Fatalf("VACUOUS: gw trustline (%s) == stored LPTokenBalance (%s); the reconcile is a no-op so this test cannot distinguish the fix", reconciled, stale)
	}

	// gw (sole LP) withdraws all → reconcile fires and the AMM is deleted.
	result := env.Submit(amm.AMMWithdraw(env.GW, amm.XRP(), env.USD).WithdrawAll().Build())
	if !result.Success {
		t.Fatalf("gw withdrawAll: %s - %s", result.Code, result.Message)
	}
	if env.ReadAMMData(amm.XRP(), env.USD) != nil {
		t.Fatal("AMM should be deleted after the last LP withdraws all")
	}

	node := metadata.FindNode(result.Metadata, "DeletedNode", "AMM")
	if node == nil {
		t.Fatal("no DeletedNode for the AMM in the withdraw metadata")
	}
	finalLPT, ok := metadata.GetFinalField(node, "LPTokenBalance").(map[string]any)
	if !ok {
		t.Fatalf("AMM DeletedNode has no FinalFields.LPTokenBalance: %+v", node.FinalFields)
	}
	got, _ := finalLPT["value"].(string)
	if got != reconciled {
		t.Errorf("AMM DeletedNode FinalFields.LPTokenBalance = %s; want reconciled %s (pre-fix would record the stale %s)", got, reconciled, stale)
	}
	if got == stale {
		t.Errorf("AMM DeletedNode FinalFields.LPTokenBalance = %s is the stale pre-adjustment value; the reconcile was not persisted", got)
	}
}
