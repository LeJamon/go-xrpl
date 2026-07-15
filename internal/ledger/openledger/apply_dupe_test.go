package openledger_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
)

// TestApplyTxs_DuplicateTxId_DroppedThenBuildContinues drives the open-ledger
// build loop with a duplicate transaction id: [pay1, pay1, pay2]. The second
// pay1 must be dropped for that entry only (its id is already in the view), and
// the build must still complete with pay1 committed exactly once and pay2
// committed. This is the consensus-build side of rippled 3.1.2's guarantee that a
// single offending transaction never aborts the ledger build. Reference: rippled
// BuildLedger.cpp skips txs already present (built->txExists) and its per-tx
// catch drops-and-continues; OpenView::rawTxInsert's duplicate-id LogicError was
// converted to a catchable exception in PR #6540.
func TestApplyTxs_DuplicateTxId_DroppedThenBuildContinues(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, carol)

	view := freshView(t, env)

	aliceSeq := env.Seq(alice)
	pay1 := payment.Pay(alice, bob, 1_000_000).Sequence(aliceSeq).Build()
	blob1 := buildSignedBlob(t, env, pay1, alice)
	pay2 := payment.Pay(alice, carol, 1_000_000).Sequence(aliceSeq + 1).Build()
	blob2 := buildSignedBlob(t, env, pay2, alice)

	pt1, err := openledger.ParsePendingTx(blob1)
	if err != nil {
		t.Fatalf("ParsePendingTx pay1: %v", err)
	}
	pt2, err := openledger.ParsePendingTx(blob2)
	if err != nil {
		t.Fatalf("ParsePendingTx pay2: %v", err)
	}

	// pt1 twice: the duplicate id must be skipped without disturbing the build.
	pending := []openledger.PendingTx{pt1, pt1, pt2}
	var retries []openledger.PendingTx

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   view.Sequence(),
		NetworkID:        0,
		Rules:            amendment.AllSupportedRules(),
		Mode:             openledger.BuildLedgerMode,
	}
	if err := openledger.ApplyTxs(view, pending, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if len(retries) != 0 {
		t.Errorf("expected 0 retries, got %d", len(retries))
	}
	if !view.TxExists(pt1.Hash) {
		t.Errorf("pay1 missing from view — build did not commit it")
	}
	if !view.TxExists(pt2.Hash) {
		t.Errorf("pay2 missing from view — build did not continue past the duplicate")
	}
	if got := view.TxCount(); got != 2 {
		t.Errorf("view tx count = %d, want 2 (duplicate committed once)", got)
	}
}
