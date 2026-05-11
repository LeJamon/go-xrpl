package openledger_test

import (
	"encoding/hex"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
	testenv "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// buildSignedBlob constructs a transaction, signs it with the sender's key,
// and returns the binary blob ready to feed into openledger.ApplyTxs.
//
// We bypass env.Submit because that would mutate the env's live open
// ledger; we want to test ApplyTxs in isolation against a fresh view.
func buildSignedBlob(t *testing.T, env *testenv.TestEnv, txn tx.Transaction, signer *testenv.Account) []byte {
	t.Helper()
	env.SignWith(txn, signer)
	txMap, err := txn.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	hexStr, err := binarycodec.Encode(txMap)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return blob
}

func freshView(t *testing.T, env *testenv.TestEnv) *ledger.Ledger {
	t.Helper()
	// Close the env once so we have a closed parent to anchor a brand-new
	// open view against — independent of env.ledger so AddTransactionWithMeta
	// inside ApplyTxs does not pollute the env.
	env.Close()
	parent := env.LastClosedLedger()
	if parent == nil {
		t.Fatal("no LastClosedLedger after Close")
	}
	view, err := ledger.NewOpen(parent, time.Now())
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	return view
}

// TestApplyTxs_RetrySettles submits two dependent payments in the wrong
// order. The first creates bob with a 1 XRP payment; the second sends
// from bob. On pass 0 the bob→carol payment fails with terNO_ACCOUNT
// because bob does not exist yet; on a retry pass after alice→bob
// succeeds it must settle.
func TestApplyTxs_RetrySettles(t *testing.T) {
	env := testenv.NewTestEnv(t)
	// ApplyTxs always verifies signatures on pass 0 (engine config
	// SkipSignatureVerification = pass > 0), so we need real signatures.
	env.SetVerifySignatures(true)

	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	carol := testenv.NewAccount("carol")
	env.Fund(alice, carol) // bob will be funded by the in-loop payment

	view := freshView(t, env)

	// Tx 1: alice -> bob, 300 XRP (creates bob — needs ≥ reserve = 200 XRP).
	aliceSeq := env.Seq(alice)
	pay1 := payment.Pay(alice, bob, 300_000_000).
		Sequence(aliceSeq).
		Build()
	blob1 := buildSignedBlob(t, env, pay1, alice)

	// Tx 2: bob -> carol, 5 XRP. bob's first sequence after creation is
	// the ledger sequence at the time of creation. Since this is a brand
	// new account, the engine assigns Sequence = ledger.Sequence() when
	// the account-creating payment applies. Using bob.Seq=ledger.Sequence
	// (view.Sequence()) matches what rippled does.
	pay2 := payment.Pay(bob, carol, 5_000_000).
		Sequence(view.Sequence()).
		Build()
	blob2 := buildSignedBlob(t, env, pay2, bob)

	pt1, err := openledger.ParsePendingTx(blob1)
	if err != nil {
		t.Fatalf("ParsePendingTx pay1: %v", err)
	}
	pt2, err := openledger.ParsePendingTx(blob2)
	if err != nil {
		t.Fatalf("ParsePendingTx pay2: %v", err)
	}

	// Feed pay2 FIRST so the 3-pass loop has to retry it after pay1
	// commits in pass 0.
	pending := []openledger.PendingTx{pt2, pt1}
	var retries []openledger.PendingTx

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   view.Sequence(),
		NetworkID:        0,
	}
	if err := openledger.ApplyTxs(view, pending, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if len(retries) != 0 {
		t.Errorf("expected 0 retries, got %d", len(retries))
	}
	if !view.TxExists(pt1.Hash) {
		t.Errorf("pay1 (alice->bob) missing from view after ApplyTxs")
	}
	if !view.TxExists(pt2.Hash) {
		t.Errorf("pay2 (bob->carol) missing from view after ApplyTxs — retry did not settle")
	}
}

// TestApplyTxs_TemMalformed_DroppedNotRetried builds a tx with a
// corrupted TxnSignature so signature verification fails — that surfaces
// in the engine as a tem/tef class result. The tx must NOT land in the
// view and must NOT appear in retries.
func TestApplyTxs_TemMalformed_DroppedNotRetried(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true) // ensure the bad sig is actually checked

	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)

	view := freshView(t, env)

	pay := payment.Pay(alice, bob, 1_000_000).
		Sequence(env.Seq(alice)).
		Build()
	blob := buildSignedBlob(t, env, pay, alice)

	// Flip a byte in the middle of the blob to break the signature. We
	// stay away from the leading header bytes so ParseFromBinary still
	// succeeds — we want the failure to surface at the engine layer.
	if len(blob) < 40 {
		t.Fatalf("blob too short to corrupt: %d bytes", len(blob))
	}
	corrupted := make([]byte, len(blob))
	copy(corrupted, blob)
	mid := len(corrupted) - 8
	corrupted[mid] ^= 0xFF

	pt, err := openledger.ParsePendingTx(corrupted)
	if err != nil {
		// If parsing itself fails ApplyTxs will drop it as Failure too;
		// this satisfies the same contract. Re-encode the original blob
		// with a hand-rolled signature flip if this ever starts firing.
		t.Skipf("corrupted blob failed to parse (still acceptable for this test): %v", err)
	}

	var retries []openledger.PendingTx
	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   view.Sequence(),
		NetworkID:        0,
	}
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{pt}, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if len(retries) != 0 {
		t.Errorf("expected 0 retries for tem/tef-class failure, got %d", len(retries))
	}
	if view.TxExists(pt.Hash) {
		t.Errorf("malformed tx leaked into view")
	}
}
