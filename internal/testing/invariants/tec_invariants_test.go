// Tests for the invariant pass on the tec-recovery path.
//
// rippled runs checkInvariants for every applied result — tesSUCCESS AND every
// tec claim — on the post-reset fee+cleanup state, escalating a violation to
// tecINVARIANT_FAILED (and tefINVARIANT_FAILED on the fee-only retry).
// Reference: rippled Transactor.cpp:1215-1243.
package invariants_test

import (
	"testing"

	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"

	offerbuild "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// TestInvariant_TecResult_CleanRecovery_KeepsTec verifies that a tec result
// whose post-recovery state is clean keeps its original tec — the new invariant
// pass on the tec path must not introduce a false positive. A tecUNFUNDED_OFFER
// (with offer cleanup) is the canonical case from the issue.
func TestInvariant_TecResult_CleanRecovery_KeepsTec(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	env.FundAmount(gw, uint64(jtx.XRP(1_000_000)))
	env.Close()

	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, offerbuild.Reserve(env, 0))
	env.Close()

	fee := env.BaseFee()
	before := env.Balance(alice)

	result := env.Submit(offerbuild.OfferCreate(alice, gw.IOU("USD", 1000), jtx.XRPTxAmountFromXRP(1000)).Build())

	jtx.RequireTxClaimed(t, result, jtx.TecUNFUNDED_OFFER)
	jtx.RequireBalance(t, env, alice, before-fee)
	jtx.RequireOwnerCount(t, env, alice, 0)
}

// TestInvariant_TecResult_EscalatesToTecINVARIANT_FAILED verifies that an
// invariant violation surfaced on the tec-recovery delta escalates the original
// tec to tecINVARIANT_FAILED, with the fee still claimed on the fee-only retry.
func TestInvariant_TecResult_EscalatesToTecINVARIANT_FAILED(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	env.FundAmount(gw, uint64(jtx.XRP(1_000_000)))
	env.Close()

	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, offerbuild.Reserve(env, 0))
	env.Close()

	fee := env.BaseFee()
	before := env.Balance(alice)

	// Force a violation only on the first (original-tec) pass. The fee-only
	// retry passes cleanly, so the result settles at tecINVARIANT_FAILED.
	env.SetInvariantViolationHook(func(result ter.Result, _ *tx.ApplyStateTable) *txengine.InvariantViolationValue {
		if result == ter.TecUNFUNDED_OFFER {
			return txengine.NewInvariantViolation("Injected", "forced tec-path violation")
		}
		return nil
	})
	defer env.SetInvariantViolationHook(nil)

	result := env.Submit(offerbuild.OfferCreate(alice, gw.IOU("USD", 1000), jtx.XRPTxAmountFromXRP(1000)).Build())

	jtx.RequireTxClaimed(t, result, jtx.TecINVARIANT_FAILED)
	jtx.RequireBalance(t, env, alice, before-fee)
}

// TestInvariant_TecResult_EscalatesToTefINVARIANT_FAILED verifies that an
// invariant violation that persists on BOTH passes escalates the original tec
// all the way to tefINVARIANT_FAILED — the transaction is fully rejected and no
// fee is claimed.
func TestInvariant_TecResult_EscalatesToTefINVARIANT_FAILED(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	env.FundAmount(gw, uint64(jtx.XRP(1_000_000)))
	env.Close()

	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, offerbuild.Reserve(env, 0))
	env.Close()

	before := env.Balance(alice)

	// Force a violation on every pass — the fee-only retry also violates, so the
	// result escalates to tefINVARIANT_FAILED.
	env.SetInvariantViolationHook(func(_ ter.Result, _ *tx.ApplyStateTable) *txengine.InvariantViolationValue {
		return txengine.NewInvariantViolation("Injected", "forced persistent violation")
	})
	defer env.SetInvariantViolationHook(nil)

	result := env.Submit(offerbuild.OfferCreate(alice, gw.IOU("USD", 1000), jtx.XRPTxAmountFromXRP(1000)).Build())

	if result.Code != jtx.TefINVARIANT_FAILED {
		t.Fatalf("expected tefINVARIANT_FAILED, got %s: %s", result.Code, result.Message)
	}
	// tef is not a claim: no fee charged, balance unchanged.
	jtx.RequireBalance(t, env, alice, before)
}
