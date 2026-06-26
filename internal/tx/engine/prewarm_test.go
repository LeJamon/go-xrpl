package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// prewarmTestAccount is a real, decodable classic address used as the source
// account. Signature verification checks only the cryptographic signature over
// the signing payload — binding the signing key to the account is a preclaim
// concern — so the account need not match the signing key.
const prewarmTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

// newSignedSingleSignTx builds a single-signed AccountSet-shaped BaseTx signed
// with a fresh ed25519 key. ed25519 signatures are always canonical, so the
// verdict is independent of the RequireFullyCanonicalSig rule.
func newSignedSingleSignTx(t *testing.T) *txcore.BaseTx {
	t.Helper()
	priv, pub, err := ed25519.ED25519().DeriveKeypair([]byte("prewarm-issue-1105-seed"), false)
	if err != nil {
		t.Fatalf("DeriveKeypair: %v", err)
	}
	txn := txcore.NewBaseTx(txcore.TypeAccountSet, prewarmTestAccount)
	txn.Common.Fee = "10"
	seq := uint32(1)
	txn.Common.Sequence = &seq
	txn.Common.SigningPubKey = pub
	sig, err := sign.SignTransaction(txn, priv)
	if err != nil {
		t.Fatalf("SignTransaction: %v", err)
	}
	txn.Common.TxnSignature = sig
	return txn
}

func verifyingEngine(rules *amendment.Rules) *Engine {
	return NewEngine(newMockBaseView(), txcore.EngineConfig{
		Rules:                     rules,
		SkipSignatureVerification: false,
	})
}

// TestPrewarmSignature_SkipsRepeatVerify proves the off-strand verdict is
// honoured: once PrewarmSignature has cached a positive verdict, the in-strand
// signature check trusts it and does not re-verify — demonstrated by corrupting
// the signature after prewarming and observing the check still passes.
func TestPrewarmSignature_SkipsRepeatVerify(t *testing.T) {
	rules := amendment.AllSupportedRules()
	txn := newSignedSingleSignTx(t)

	if txn.GetCommon().SignatureVerified() {
		t.Fatal("a freshly built tx must not be marked verified")
	}
	PrewarmSignature(txn, rules)
	if !txn.GetCommon().SignatureVerified() {
		t.Fatal("PrewarmSignature must cache a positive verdict for a valid single-signed tx")
	}

	// Corrupt the signature; the cached verdict must short-circuit the verify.
	txn.GetCommon().TxnSignature = "00"
	if res := verifyingEngine(rules).verifySignatures(txn); res != ter.TesSUCCESS {
		t.Fatalf("cached-good signature must skip re-verify; got %s", res)
	}
}

// TestPrewarmSignature_ColdCacheStillVerifies is the control for the skip test:
// the same corrupted signature WITHOUT a prewarmed verdict must be rejected by
// the in-strand check, confirming the prior test's pass came from the cache.
func TestPrewarmSignature_ColdCacheStillVerifies(t *testing.T) {
	rules := amendment.AllSupportedRules()
	txn := newSignedSingleSignTx(t)
	txn.GetCommon().TxnSignature = "00"

	if res := verifyingEngine(rules).verifySignatures(txn); res != ter.TemINVALID {
		t.Fatalf("a cold cache must verify and reject a bad signature with temINVALID; got %s", res)
	}
}

// TestPrewarmSignature_NoNegativeCache confirms a bad signature leaves the cache
// cold, so the in-strand path reports the canonical, ordered result rather than
// a spuriously cached pass.
func TestPrewarmSignature_NoNegativeCache(t *testing.T) {
	txn := newSignedSingleSignTx(t)
	txn.GetCommon().TxnSignature = "00" // invalidate before prewarming

	PrewarmSignature(txn, amendment.AllSupportedRules())
	if txn.GetCommon().SignatureVerified() {
		t.Fatal("PrewarmSignature must not cache a verdict for a bad signature")
	}
}

// TestPrewarmSignature_SkipsMultiSignAndUnsigned confirms transactions with an
// empty SigningPubKey (multi-signed or unsigned) are left for the in-strand
// path, which interleaves the multi-sign crypto check with ledger-state
// signer-list authorization.
func TestPrewarmSignature_SkipsMultiSignAndUnsigned(t *testing.T) {
	txn := txcore.NewBaseTx(txcore.TypeAccountSet, prewarmTestAccount)
	txn.Common.Fee = "10"
	seq := uint32(1)
	txn.Common.Sequence = &seq
	txn.Common.SigningPubKey = "" // multi-sign / unsigned marker

	PrewarmSignature(txn, amendment.AllSupportedRules())
	if txn.GetCommon().SignatureVerified() {
		t.Fatal("PrewarmSignature must not pre-verify a transaction with no single-sign key")
	}
}

// TestPrewarmSignature_NilTxn is a defensive guard: a nil transaction must be a
// no-op rather than a panic.
func TestPrewarmSignature_NilTxn(t *testing.T) {
	PrewarmSignature(nil, amendment.AllSupportedRules())
}
