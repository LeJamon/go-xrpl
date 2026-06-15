package trustset

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
)

// TestTrustSet_BothNoRippleFlags_NoOp verifies that a TrustSet carrying both
// tfSetNoRipple and tfClearNoRipple succeeds and leaves the NoRipple state
// unchanged, matching rippled.
//
// rippled's SetTrust::preflight has no contradictory-flag rejection; doApply
// treats the pair as a no-op via mutually-exclusive branches:
//
//	if (bSetNoRipple && !bClearNoRipple) ...
//	else if (bClearNoRipple && !bSetNoRipple) ...
//
// With both set, neither branch fires, so the existing flag is preserved.
func TestTrustSet_BothNoRippleFlags_NoOp(t *testing.T) {
	env := jtx.NewTestEnv(t)
	gw := jtx.NewAccount("gw")
	alice := jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	limit := tx.NewIssuedAmountFromFloat64(1000, "USD", gw.Address)

	// Create the trust line with NoRipple set on alice's side.
	jtx.RequireTxSuccess(t, env.Submit(
		TrustSet(alice, limit).NoRipple().Build(),
	))
	env.Close()

	if !env.HasNoRipple(alice, gw, "USD") {
		t.Fatal("expected NoRipple to be set on alice's side after first TrustSet")
	}

	// Submit a TrustSet with BOTH tfSetNoRipple and tfClearNoRipple.
	both := trustsettx.NewTrustSet(alice.Address, limit)
	both.Fee = "10"
	both.SetFlags(trustsettx.TrustSetFlagSetNoRipple | trustsettx.TrustSetFlagClearNoRipple)

	// Must succeed (rippled has no temINVALID_FLAG rejection for this pair).
	jtx.RequireTxSuccess(t, env.Submit(both))
	env.Close()

	// NoRipple must be unchanged (no-op): still set.
	if !env.HasNoRipple(alice, gw, "USD") {
		t.Error("contradictory NoRipple flags must be a no-op; NoRipple was cleared")
	}
}
