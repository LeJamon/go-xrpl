package openledger_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func ownedApplyConfig(view *ledger.Ledger, mode openledger.Mode) openledger.ApplyConfig {
	return openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   view.Sequence(),
		NetworkID:        0,
		Rules:            amendment.AllSupportedRules(),
		Mode:             mode,
	}
}

func newOwnedApplyView(t *testing.T, parent *ledger.Ledger) *ledger.Ledger {
	t.Helper()
	view, err := ledger.NewOpen(parent, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	return view
}

func assertOwnedApplyResultsMatch(t *testing.T, want, got *ledger.Ledger, hash [32]byte) {
	t.Helper()
	closeTime := time.Unix(2_000_000_000, 0)
	if err := want.Close(closeTime, 0); err != nil {
		t.Fatalf("close control ledger: %v", err)
	}
	if err := got.Close(closeTime, 0); err != nil {
		t.Fatalf("close owned ledger: %v", err)
	}
	if want.Hash() != got.Hash() {
		t.Fatalf("ledger hash differs: want %x, got %x", want.Hash(), got.Hash())
	}
	wantHeader, gotHeader := want.Header(), got.Header()
	if wantHeader.AccountHash != gotHeader.AccountHash {
		t.Fatalf("account root differs: want %x, got %x", wantHeader.AccountHash, gotHeader.AccountHash)
	}
	if wantHeader.TxHash != gotHeader.TxHash {
		t.Fatalf("transaction root differs: want %x, got %x", wantHeader.TxHash, gotHeader.TxHash)
	}
	if want.TxCount() != got.TxCount() {
		t.Fatalf("transaction count differs: want %d, got %d", want.TxCount(), got.TxCount())
	}
	wantMeta, wantFound, err := want.GetTransaction(hash)
	if err != nil || !wantFound {
		t.Fatalf("control transaction metadata: found=%v err=%v", wantFound, err)
	}
	gotMeta, gotFound, err := got.GetTransaction(hash)
	if err != nil || !gotFound {
		t.Fatalf("owned transaction metadata: found=%v err=%v", gotFound, err)
	}
	if !bytes.Equal(wantMeta, gotMeta) {
		t.Fatalf("transaction metadata differs: want %x, got %x", wantMeta, gotMeta)
	}
}

func TestApplyTxs_DefaultPreservesOuterAndInnerParsedState(t *testing.T) {
	fixture := newBatchAdmissionBenchmarkFixture(t)
	pending, err := openledger.ParsePendingTx(fixture.blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	innerOuter, ok := pending.Parsed.(interface{ InnerTransactions() []tx.Transaction })
	if !ok {
		t.Fatalf("parsed transaction %T does not expose Batch inners", pending.Parsed)
	}
	inners := innerOuter.InnerTransactions()
	if len(inners) == 0 {
		t.Fatal("batch has no inner transactions")
	}

	rules := amendment.AllSupportedRules()
	wrongID := [32]byte{0xff}
	pending.Parsed.GetCommon().MarkSignatureVerifiedWithRules(wrongID, rules)
	pending.Parsed.GetCommon().MarkPreflightVerified(rules, wrongID)
	outerRaw := pending.Parsed.GetRawBytes()
	innerRaws := make([][]byte, len(inners))
	for i, inner := range inners {
		inner.GetCommon().MarkSignatureVerifiedWithRules(wrongID, rules)
		inner.GetCommon().MarkPreflightVerified(rules, wrongID)
		innerRaws[i] = inner.GetRawBytes()
	}

	view := newOwnedApplyView(t, fixture.parent)
	cfg := fixture.apply
	cfg.LedgerSequence = view.Sequence()
	var retries []openledger.PendingTx
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{pending}, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}
	if len(retries) != 0 {
		t.Fatalf("expected no retries, got %d", len(retries))
	}

	if !bytes.Equal(outerRaw, pending.Parsed.GetRawBytes()) {
		t.Fatal("default ApplyTxs changed the caller's outer raw bytes")
	}
	if pending.Parsed.GetCommon().SignatureVerifiedWithRules(pending.Hash, rules) {
		t.Fatal("default ApplyTxs populated the caller's outer signature cache")
	}
	if pending.Parsed.GetCommon().PreflightVerified(rules, pending.Hash) {
		t.Fatal("default ApplyTxs populated the caller's outer preflight cache")
	}
	if !pending.Parsed.GetCommon().SignatureVerifiedWithRules(wrongID, rules) ||
		!pending.Parsed.GetCommon().PreflightVerified(rules, wrongID) {
		t.Fatal("default ApplyTxs changed the caller's outer caches")
	}
	for i, inner := range inners {
		if !bytes.Equal(innerRaws[i], inner.GetRawBytes()) {
			t.Fatalf("default ApplyTxs changed inner %d raw bytes", i)
		}
		innerHash, err := tx.ComputeTransactionHash(inner)
		if err != nil {
			t.Fatalf("ComputeTransactionHash(inner %d): %v", i, err)
		}
		if inner.GetCommon().SignatureVerifiedWithRules(innerHash, rules) {
			t.Fatalf("default ApplyTxs populated inner %d signature cache", i)
		}
		if inner.GetCommon().PreflightVerified(rules, innerHash) {
			t.Fatalf("default ApplyTxs populated inner %d preflight cache", i)
		}
		if !inner.GetCommon().SignatureVerifiedWithRules(wrongID, rules) ||
			!inner.GetCommon().PreflightVerified(rules, wrongID) {
			t.Fatalf("default ApplyTxs changed inner %d caches", i)
		}
	}
}

func TestApplyOwnedTxsContext_MatchesDefaultRootsAndMetadata(t *testing.T) {
	for _, mutateInner := range []bool{false, true} {
		name := "unchanged"
		if mutateInner {
			name = "mutated inner"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newBatchAdmissionBenchmarkFixture(t)
			controlPending, err := openledger.ParsePendingTx(fixture.blob)
			if err != nil {
				t.Fatalf("ParsePendingTx control: %v", err)
			}
			ownedPending, err := openledger.ParsePendingTx(fixture.blob)
			if err != nil {
				t.Fatalf("ParsePendingTx owned: %v", err)
			}

			var mutatedInner tx.Transaction
			if mutateInner {
				outer := ownedPending.Parsed.(interface{ InnerTransactions() []tx.Transaction })
				mutatedInner = outer.InnerTransactions()[0]
				mutatedInner.GetCommon().Fee = "1"
			}

			control := newOwnedApplyView(t, fixture.parent)
			owned := newOwnedApplyView(t, fixture.parent)
			controlCfg := fixture.apply
			controlCfg.LedgerSequence = control.Sequence()
			ownedCfg := fixture.apply
			ownedCfg.LedgerSequence = owned.Sequence()
			var controlRetries, ownedRetries []openledger.PendingTx
			if err := openledger.ApplyTxs(control, []openledger.PendingTx{controlPending}, &controlRetries, controlCfg); err != nil {
				t.Fatalf("ApplyTxs: %v", err)
			}
			if err := openledger.ApplyOwnedTxsContext(context.Background(), owned, []openledger.PendingTx{ownedPending}, &ownedRetries, ownedCfg); err != nil {
				t.Fatalf("ApplyOwnedTxsContext: %v", err)
			}
			if len(controlRetries) != 0 || len(ownedRetries) != 0 {
				t.Fatalf("unexpected retries: control=%d owned=%d", len(controlRetries), len(ownedRetries))
			}
			assertOwnedApplyResultsMatch(t, control, owned, controlPending.Hash)
			if mutatedInner != nil && mutatedInner.GetCommon().Fee != "1" {
				t.Fatal("fallback changed the caller's mutated inner transaction")
			}
		})
	}
}

func TestApplyOwnedTxsContext_FallsBackForUntrustedParsed(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("owned-fallback-alice")
	bob := jtx.NewAccount("owned-fallback-bob")
	carol := jtx.NewAccount("owned-fallback-carol")
	env.Fund(alice, bob, carol)

	aliceSequence := env.Seq(alice)
	target := payment.Pay(alice, bob, 1_000_000).Sequence(aliceSequence).Build()
	targetBlob := buildSignedBlob(t, env, target, alice)
	other := payment.Pay(alice, carol, 1_000_000).Sequence(aliceSequence + 1).Build()
	otherBlob := buildSignedBlob(t, env, other, alice)
	env.Close()
	parent := env.LastClosedLedger()
	if parent == nil {
		t.Fatal("no closed parent")
	}

	cases := []struct {
		name   string
		modify func(openledger.PendingTx, tx.Transaction) openledger.PendingTx
	}{
		{name: "nil parsed", modify: func(pending openledger.PendingTx, _ tx.Transaction) openledger.PendingTx {
			pending.Parsed = nil
			return pending
		}},
		{name: "raw mismatch", modify: func(pending openledger.PendingTx, otherParsed tx.Transaction) openledger.PendingTx {
			pending.Parsed = otherParsed
			return pending
		}},
		{name: "hash mismatch", modify: func(pending openledger.PendingTx, _ tx.Transaction) openledger.PendingTx {
			pending.Hash = [32]byte{1}
			return pending
		}},
		{name: "field mutation", modify: func(pending openledger.PendingTx, _ tx.Transaction) openledger.PendingTx {
			pending.Parsed.GetCommon().Fee = "11"
			return pending
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controlPending, err := openledger.ParsePendingTx(targetBlob)
			if err != nil {
				t.Fatalf("ParsePendingTx control: %v", err)
			}
			candidate, err := openledger.ParsePendingTx(targetBlob)
			if err != nil {
				t.Fatalf("ParsePendingTx candidate: %v", err)
			}
			otherPending, err := openledger.ParsePendingTx(otherBlob)
			if err != nil {
				t.Fatalf("ParsePendingTx other: %v", err)
			}
			candidate = tc.modify(candidate, otherPending.Parsed)
			mutatedParsed := candidate.Parsed

			controlView := newOwnedApplyView(t, parent)
			ownedView := newOwnedApplyView(t, parent)
			controlCfg := ownedApplyConfig(controlView, openledger.OpenLedgerMode)
			ownedCfg := ownedApplyConfig(ownedView, openledger.OpenLedgerMode)
			var controlRetries, ownedRetries []openledger.PendingTx
			if err := openledger.ApplyTxs(controlView, []openledger.PendingTx{controlPending}, &controlRetries, controlCfg); err != nil {
				t.Fatalf("ApplyTxs: %v", err)
			}
			if err := openledger.ApplyOwnedTxsContext(context.Background(), ownedView, []openledger.PendingTx{candidate}, &ownedRetries, ownedCfg); err != nil {
				t.Fatalf("ApplyOwnedTxsContext: %v", err)
			}
			if len(controlRetries) != 0 || len(ownedRetries) != 0 {
				t.Fatalf("unexpected retries: control=%d owned=%d", len(controlRetries), len(ownedRetries))
			}
			assertOwnedApplyResultsMatch(t, controlView, ownedView, controlPending.Hash)
			if tc.name == "field mutation" {
				if mutatedParsed == nil || mutatedParsed.GetCommon().Fee != "11" {
					t.Fatal("fallback changed the caller's mutated parsed transaction")
				}
			}
		})
	}
}

func TestApplyOwnedTxsContext_RetryRetainsParsedOwnership(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("owned-retry-alice")
	bob := jtx.NewAccount("owned-retry-bob")
	carol := jtx.NewAccount("owned-retry-carol")
	env.Fund(alice, carol)
	view := freshView(t, env)

	paymentFromMissingAccount := payment.Pay(bob, carol, 5_000_000).
		Sequence(view.Sequence()).
		Build()
	blob := buildSignedBlob(t, env, paymentFromMissingAccount, bob)
	pending, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}
	originalParsed := pending.Parsed
	var retries []openledger.PendingTx
	if err := openledger.ApplyOwnedTxsContext(context.Background(), view, []openledger.PendingTx{pending}, &retries, ownedApplyConfig(view, openledger.OpenLedgerMode)); err != nil {
		t.Fatalf("ApplyOwnedTxsContext: %v", err)
	}
	if len(retries) != 1 {
		t.Fatalf("retries = %d, want 1", len(retries))
	}
	if retries[0].Parsed != originalParsed {
		t.Fatal("retry did not retain the transferred parsed transaction")
	}
	if !bytes.Equal(retries[0].Parsed.GetRawBytes(), pending.Blob) {
		t.Fatal("retry parsed transaction lost its raw bytes")
	}
}

func TestApplyOwnedTxsContext_DuplicateContinuesBuild(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	alice := jtx.NewAccount("owned-duplicate-alice")
	bob := jtx.NewAccount("owned-duplicate-bob")
	carol := jtx.NewAccount("owned-duplicate-carol")
	env.Fund(alice, bob, carol)
	view := freshView(t, env)

	aliceSequence := env.Seq(alice)
	firstBlob := buildSignedBlob(t, env, payment.Pay(alice, bob, 1_000_000).Sequence(aliceSequence).Build(), alice)
	secondBlob := buildSignedBlob(t, env, payment.Pay(alice, carol, 1_000_000).Sequence(aliceSequence+1).Build(), alice)
	first, err := openledger.ParsePendingTx(firstBlob)
	if err != nil {
		t.Fatalf("ParsePendingTx first: %v", err)
	}
	second, err := openledger.ParsePendingTx(secondBlob)
	if err != nil {
		t.Fatalf("ParsePendingTx second: %v", err)
	}

	var retries []openledger.PendingTx
	cfg := ownedApplyConfig(view, openledger.BuildLedgerMode)
	if err := openledger.ApplyOwnedTxsContext(context.Background(), view, []openledger.PendingTx{first, first, second}, &retries, cfg); err != nil {
		t.Fatalf("ApplyOwnedTxsContext: %v", err)
	}
	if len(retries) != 0 {
		t.Fatalf("retries = %d, want 0", len(retries))
	}
	if !ledgerTxExists(t, view, first.Hash) || !ledgerTxExists(t, view, second.Hash) {
		t.Fatal("duplicate interrupted the build")
	}
	if got := view.TxCount(); got != 2 {
		t.Fatalf("transaction count = %d, want 2", got)
	}
}
