package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// A successful in-strand verify records the tx ID in the verified-good cache.
func TestSigCache_PopulatesOnVerifySuccess(t *testing.T) {
	sigcache.Reset()
	rules := amendment.AllSupportedRules()
	txn := newSignedSingleSignTx(t)

	id, err := txcore.ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	if sigcache.Verified(id) {
		t.Fatal("cache must start cold for a never-verified tx")
	}
	if res := verifyingEngine(rules).verifyOuterSignature(txn); res != ter.TesSUCCESS {
		t.Fatalf("a good signature must verify; got %s", res)
	}
	if !sigcache.Verified(id) {
		t.Fatal("a successful verify must publish the tx ID to the verified-good cache")
	}
}

// Security invariant: an uncached bad-signature tx is fully verified and
// rejected, and a failed verify must never populate the cache.
func TestSigCache_MissRejectsBadSignature(t *testing.T) {
	sigcache.Reset()
	rules := amendment.AllSupportedRules()
	txn := newSignedSingleSignTx(t)
	txn.GetCommon().TxnSignature = "00" // corrupt: distinct blob → distinct ID

	id, err := txcore.ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	if sigcache.Verified(id) {
		t.Fatal("a forged/never-verified tx must not be pre-cached")
	}
	if res := verifyingEngine(rules).verifyOuterSignature(txn); res != ter.TemINVALID {
		t.Fatalf("a cache miss must run the full verify and reject the bad signature; got %s", res)
	}
	if sigcache.Verified(id) {
		t.Fatal("a failed verify must NOT populate the cache (positive-cache invariant)")
	}
}

// A tx-ID cache hit short-circuits the crypto verify: the tx carries a BAD
// signature and a cold object flag, so a pass can only come from the seeded
// cache entry. Paired with TestSigCache_MissRejectsBadSignature (same tx, no
// seed → rejected), this proves the skip is gated solely on the cache.
func TestSigCache_HitSkipsVerify(t *testing.T) {
	sigcache.Reset()
	rules := amendment.AllSupportedRules()
	txn := newSignedSingleSignTx(t)
	txn.GetCommon().TxnSignature = "00" // a real verify of this blob would fail

	id, err := txcore.ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	sigcache.MarkVerified(id) // as if this exact blob verified good elsewhere

	if txn.GetCommon().SignatureVerified() {
		t.Fatal("object-level flag must be cold so the pass can only come from the tx-ID cache")
	}
	if res := verifyingEngine(rules).verifyOuterSignature(txn); res != ter.TesSUCCESS {
		t.Fatalf("a tx-ID cache hit must skip the crypto verify and pass; got %s", res)
	}
	sigcache.Reset()
}

// Full preflight with verification on rejects a structurally-valid tx that has
// a bad signature and no cache entry — no unverified tx slips through the build.
func TestSigCache_BuildPathRejectsUnverifiedBadSig(t *testing.T) {
	sigcache.Reset()
	rules := amendment.AllSupportedRules()
	txn := newSignedSingleSignTx(t)
	txn.GetCommon().TxnSignature = "00"

	if res := verifyingEngine(rules).preflight(txn); res != ter.TemINVALID {
		t.Fatalf("build-path preflight must reject an unverified bad signature; got %s", res)
	}
}
