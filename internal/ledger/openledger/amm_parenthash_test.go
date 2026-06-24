package openledger_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreamm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TestApplyTxs_BuildLedgerMode_AMMCreateUsesParentHash is a regression test for
// the consensus fork where the open-ledger apply path — including
// BuildLedgerMode, the canonical closed-ledger build during consensus — failed
// to thread the view's parent hash into the engine config, leaving it the zero
// value. AMMCreate derives its pseudo-account from the parent hash, so a zero
// parent hash yields a different account ID than the rest of the network,
// forking on any ledger containing an AMMCreate.
//
// The test creates an XRP/USD AMM through ApplyTxs in BuildLedgerMode and
// asserts the pseudo-account lands at the address derived from the view's real
// parent hash, not the all-zero-hash variant.
func TestApplyTxs_BuildLedgerMode_AMMCreateUsesParentHash(t *testing.T) {
	env := testenv.NewTestEnv(t)

	gw := testenv.NewAccount("gateway")
	alice := testenv.NewAccount("alice")
	env.Fund(gw, alice)

	// alice trusts and holds USD so she can fund the AMM's second asset.
	env.Trust(alice, gw.IOU("USD", 1000))
	env.PayIOU(gw, alice, gw, "USD", 500)

	// Close once and anchor a brand-new open view on the closed parent so its
	// ParentHash() is the real (non-zero) prior-ledger hash.
	view := freshView(t, env)

	amount1 := ammtest.XRPAmount(100)
	amount2 := gw.IOU("USD", 100)
	asset1 := tx.Asset{Currency: amount1.Currency, Issuer: amount1.Issuer}
	asset2 := tx.Asset{Currency: amount2.Currency, Issuer: amount2.Issuer}

	// Derive both candidate pseudo-account addresses against the clean,
	// pre-apply view: the correct one (real parent hash) and the buggy one
	// (zero parent hash). On an unoccupied view both resolve to the i=0
	// candidate, so they differ exactly when the parent hash is non-zero.
	ammKeylet := coreamm.ComputeAMMKeylet(asset1, asset2)
	wantAddr := coreamm.PseudoAccountAddress(view, view.ParentHash(), ammKeylet.Key)
	zeroAddr := coreamm.PseudoAccountAddress(view, [32]byte{}, ammKeylet.Key)
	if wantAddr == zeroAddr {
		t.Fatal("view.ParentHash() is zero — test cannot distinguish the fix")
	}

	aliceSeq := env.Seq(alice)
	ammTx := ammtest.AMMCreate(alice, amount1, amount2).Build()
	ammTx.GetCommon().Sequence = &aliceSeq

	blob := buildSignedBlob(t, env, ammTx, alice)
	pt, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	var retries []openledger.PendingTx
	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   view.Sequence(),
		Rules:            amendment.AllSupportedRules(),
		Mode:             openledger.BuildLedgerMode,
		// The blob carries a dummy signature; the fork under test is about the
		// parent hash, not signature checks.
		SkipSignatureVerification: true,
	}
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{pt}, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if !view.TxExists(pt.Hash) {
		t.Fatal("AMMCreate did not commit to the view")
	}
	if ok, _ := view.Exists(ammKeylet); !ok {
		t.Fatal("AMM ledger entry was not created — check test setup")
	}
	if ok, _ := view.Exists(keylet.Account(wantAddr)); !ok {
		t.Errorf("AMM pseudo-account missing at the real-parent-hash address %x — apply path used the wrong parent hash", wantAddr)
	}
	if ok, _ := view.Exists(keylet.Account(zeroAddr)); ok {
		t.Errorf("AMM pseudo-account created at the zero-parent-hash address %x — parent hash was not threaded into EngineConfig", zeroAddr)
	}
}

// TestTxqAdapter_ApplyTransaction_AMMCreateUsesParentHash is the open-ledger
// counterpart of the BuildLedgerMode test above. It drives an AMMCreate through
// TxqAdapter.ApplyTransaction — the engine call behind TxQ.Apply / TxQ.Accept
// that backs the client-facing current/open ledger — and asserts the
// pseudo-account lands at the real-parent-hash address. Before the fix this path
// left EngineConfig.ParentHash unset, so the open ledger derived a different AMM
// account than both rippled and goXRPL's own canonical build until the next
// close.
func TestTxqAdapter_ApplyTransaction_AMMCreateUsesParentHash(t *testing.T) {
	env := testenv.NewTestEnv(t)

	gw := testenv.NewAccount("gateway")
	alice := testenv.NewAccount("alice")
	env.Fund(gw, alice)

	env.Trust(alice, gw.IOU("USD", 1000))
	env.PayIOU(gw, alice, gw, "USD", 500)

	view := freshView(t, env)

	amount1 := ammtest.XRPAmount(100)
	amount2 := gw.IOU("USD", 100)
	asset1 := tx.Asset{Currency: amount1.Currency, Issuer: amount1.Issuer}
	asset2 := tx.Asset{Currency: amount2.Currency, Issuer: amount2.Issuer}

	ammKeylet := coreamm.ComputeAMMKeylet(asset1, asset2)
	wantAddr := coreamm.PseudoAccountAddress(view, view.ParentHash(), ammKeylet.Key)
	zeroAddr := coreamm.PseudoAccountAddress(view, [32]byte{}, ammKeylet.Key)
	if wantAddr == zeroAddr {
		t.Fatal("view.ParentHash() is zero — test cannot distinguish the fix")
	}

	aliceSeq := env.Seq(alice)
	ammTx := ammtest.AMMCreate(alice, amount1, amount2).Build()
	ammTx.GetCommon().Sequence = &aliceSeq

	blob := buildSignedBlob(t, env, ammTx, alice)
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	parsed.SetRawBytes(blob)

	adapter := openledger.NewTxqAdapter(view, openledger.ApplyConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
	})

	result, applied := adapter.ApplyTransaction(parsed)
	if !applied {
		t.Fatalf("AMMCreate not applied through TxqAdapter: %v", result)
	}

	if ok, _ := view.Exists(ammKeylet); !ok {
		t.Fatal("AMM ledger entry was not created — check test setup")
	}
	if ok, _ := view.Exists(keylet.Account(wantAddr)); !ok {
		t.Errorf("AMM pseudo-account missing at the real-parent-hash address %x — TxQ apply path used the wrong parent hash", wantAddr)
	}
	if ok, _ := view.Exists(keylet.Account(zeroAddr)); ok {
		t.Errorf("AMM pseudo-account created at the zero-parent-hash address %x — ParentHash not threaded into TxqAdapter EngineConfig", zeroAddr)
	}
}
