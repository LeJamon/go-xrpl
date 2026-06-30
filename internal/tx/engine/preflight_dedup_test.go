package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// newDedupTx builds a structurally valid, unsigned AccountSet-shaped BaseTx.
// The dedup tests run with SkipSignatureVerification so the signature step is a
// no-op and the structural-verdict cache is exercised in isolation.
func newDedupTx() *txcore.BaseTx {
	txn := txcore.NewBaseTx(txcore.TypeAccountSet, prewarmTestAccount)
	txn.Common.Fee = "10"
	seq := uint32(1)
	txn.Common.Sequence = &seq
	return txn
}

func dedupEngine(rules *amendment.Rules) *Engine {
	return NewEngine(newMockBaseView(), txcore.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            100,
		Rules:                     rules,
		SkipSignatureVerification: true,
	})
}

// TestPreflight_SkipsRepeatStructural proves the structural verdict is memoised:
// once preflight succeeds under a set of rules, a second preflight of the same
// parsed transaction under those same rules skips the structural pass — shown by
// corrupting a structural field afterwards and observing the check still passes.
// This is the open-ledger apply strand's double preflight (TxQ.Apply then
// Engine.Apply) collapsing to a single structural pass (#1153).
func TestPreflight_SkipsRepeatStructural(t *testing.T) {
	rules := amendment.AllSupportedRules()
	txn := newDedupTx()
	eng := dedupEngine(rules)

	if txn.GetCommon().PreflightVerified(rules) {
		t.Fatal("a freshly built tx must not be preflight-verified")
	}
	if res := eng.preflight(txn); res != ter.TesSUCCESS {
		t.Fatalf("first preflight must pass; got %s", res)
	}
	if !txn.GetCommon().PreflightVerified(rules) {
		t.Fatal("a successful preflight must cache the structural verdict")
	}

	// Corrupt a structural field; the cached verdict must short-circuit the
	// structural pass so the second preflight still passes.
	txn.GetCommon().Sequence = nil
	txn.GetCommon().TicketSequence = nil
	if res := eng.preflight(txn); res != ter.TesSUCCESS {
		t.Fatalf("cached structural verdict must skip the repeat; got %s", res)
	}
}

// TestPreflight_ColdCacheRunsStructural is the control: the same corruption with
// no cached verdict must be rejected, confirming the skip above came from the
// cache and not from the field being ignored.
func TestPreflight_ColdCacheRunsStructural(t *testing.T) {
	rules := amendment.AllSupportedRules()
	txn := newDedupTx()
	txn.Common.Sequence = nil
	txn.Common.TicketSequence = nil

	if res := dedupEngine(rules).preflight(txn); res != ter.TemBAD_SEQUENCE {
		t.Fatalf("a cold cache must run structural and reject the bad sequence; got %s", res)
	}
}

// TestPreflight_DifferentRulesRecomputes proves the verdict is keyed on the
// rules pointer: a re-preflight under a distinct rules pointer (a later ledger
// rebuilds its rules) must not trust a verdict cached under the prior pointer,
// even when the two rule sets are semantically identical. This keeps a queued
// transaction re-applied on a later ledger from skipping a structural check that
// an amendment change could have altered.
func TestPreflight_DifferentRulesRecomputes(t *testing.T) {
	rulesA := amendment.AllSupportedRules()
	txn := newDedupTx()
	if res := dedupEngine(rulesA).preflight(txn); res != ter.TesSUCCESS {
		t.Fatalf("first preflight must pass; got %s", res)
	}

	txn.GetCommon().Sequence = nil
	txn.GetCommon().TicketSequence = nil

	rulesB := amendment.AllSupportedRules() // same rule set, distinct pointer
	if txn.GetCommon().PreflightVerified(rulesB) {
		t.Fatal("a verdict cached under rulesA must not be honoured for rulesB")
	}
	if res := dedupEngine(rulesB).preflight(txn); res != ter.TemBAD_SEQUENCE {
		t.Fatalf("a new ledger (rebuilt rules) must recompute structural; got %s", res)
	}
}

// TestPreflight_DoesNotCacheSignature is the correctness guard that separates
// this change from a naive whole-verdict cache: signature verification is left
// out of the memo and always runs, so a cached structural verdict never masks a
// corrupted signature (and a multi-signed tx's view-dependent signer-list check
// is never skipped).
func TestPreflight_DoesNotCacheSignature(t *testing.T) {
	rules := amendment.AllSupportedRules()
	txn := newSignedSingleSignTx(t)
	eng := NewEngine(newMockBaseView(), txcore.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            100,
		Rules:                     rules,
		SkipSignatureVerification: false,
	})

	if res := eng.preflight(txn); res != ter.TesSUCCESS {
		t.Fatalf("first preflight of a validly signed tx must pass; got %s", res)
	}
	if !txn.GetCommon().PreflightVerified(rules) {
		t.Fatal("structural verdict must be cached after a successful preflight")
	}

	// Corrupt only the signature. Structural is cached, but the signature is not,
	// so the second preflight must re-verify and reject.
	txn.GetCommon().TxnSignature = "00"
	if res := eng.preflight(txn); res != ter.TemINVALID {
		t.Fatalf("signature must never be cached; a corrupt sig must be re-rejected, got %s", res)
	}
}
