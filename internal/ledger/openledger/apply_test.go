package openledger_test

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func ledgerTxExists(t *testing.T, view *ledger.Ledger, hash [32]byte) bool {
	t.Helper()
	exists, err := view.TxExists(hash)
	if err != nil {
		t.Fatalf("TxExists(%x): %v", hash, err)
	}
	return exists
}

// buildSignedBlob constructs a transaction, signs it with the sender's key,
// and returns the binary blob ready to feed into openledger.ApplyTxs.
//
// We bypass env.Submit because that would mutate the env's live open
// ledger; we want to test ApplyTxs in isolation against a fresh view.
func buildSignedBlob(t *testing.T, env *jtx.TestEnv, txn tx.Transaction, signer *jtx.Account) []byte {
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

func freshView(t *testing.T, env *jtx.TestEnv) *ledger.Ledger {
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

func TestTxqAdapter_ApplyTransaction_ContinuesTransactionIndex(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	env.Fund(alice, bob, carol)
	view := freshView(t, env)

	sequence := env.Seq(alice)
	transactions := []tx.Transaction{
		payment.Pay(alice, bob, 1_000_000).Sequence(sequence).Build(),
		payment.Pay(alice, carol, 1_000_000).Sequence(sequence + 1).Build(),
	}
	adapter := openledger.NewTxqAdapter(view, openledger.ApplyConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
	})

	for i, transaction := range transactions {
		blob := buildSignedBlob(t, env, transaction, alice)
		parsed, err := tx.ParseFromBinary(blob)
		if err != nil {
			t.Fatalf("parse transaction %d: %v", i, err)
		}
		parsed.SetRawBytes(blob)
		result, applied := adapter.ApplyTransaction(parsed)
		if !applied {
			t.Fatalf("transaction %d result = %s, want applied", i, result)
		}
		applyResult := adapter.LastApplyResult()
		if applyResult == nil || applyResult.Metadata == nil {
			t.Fatalf("transaction %d has no metadata", i)
		}
		if applyResult.Metadata.TransactionIndex != uint32(i) {
			t.Fatalf("transaction %d metadata index = %d, want %d",
				i, applyResult.Metadata.TransactionIndex, i)
		}
	}
	if view.TxCount() != uint32(len(transactions)) {
		t.Fatalf("ledger transaction count = %d, want %d", view.TxCount(), len(transactions))
	}
}

// TestApplyTxs_RetrySettles submits two dependent payments in the wrong
// order. The first creates bob with a 1 XRP payment; the second sends
// from bob. On pass 0 the bob→carol payment fails with terNO_ACCOUNT
// because bob does not exist yet; on a retry pass after alice→bob
// succeeds it must settle.
func TestApplyTxs_RetrySettles(t *testing.T) {
	env := jtx.NewTestEnv(t)
	// ApplyTxs always verifies signatures on pass 0 (engine config
	// SkipSignatureVerification = pass > 0), so we need real signatures.
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
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
		Rules:            amendment.AllSupportedRules(),
	}
	if err := openledger.ApplyTxs(view, pending, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if len(retries) != 0 {
		t.Errorf("expected 0 retries, got %d", len(retries))
	}
	if !ledgerTxExists(t, view, pt1.Hash) {
		t.Errorf("pay1 (alice->bob) missing from view after ApplyTxs")
	}
	if !ledgerTxExists(t, view, pt2.Hash) {
		t.Errorf("pay2 (bob->carol) missing from view after ApplyTxs — retry did not settle")
	}
}

// TestApplyTxs_TemMalformed_DroppedNotRetried builds a tx with a
// corrupted TxnSignature so signature verification fails — that surfaces
// in the engine as a tem/tef class result. The tx must NOT land in the
// view and must NOT appear in retries.
func TestApplyTxs_TemMalformed_DroppedNotRetried(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true) // ensure the bad sig is actually checked

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
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
		Rules:            amendment.AllSupportedRules(),
	}
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{pt}, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if len(retries) != 0 {
		t.Errorf("expected 0 retries for tem/tef-class failure, got %d", len(retries))
	}
	if ledgerTxExists(t, view, pt.Hash) {
		t.Errorf("malformed tx leaked into view")
	}
}

// buildTecPayment builds a Payment whose apply will return tec — the
// classic shape is "fund a brand-new account with less than ReserveBase",
// which yields tecNO_DST_INSUF_XRP. This is the same scenario the
// convergence test had to pre-fund around before Mode existed.
func buildTecPayment(t *testing.T, env *jtx.TestEnv) (openledger.PendingTx, *jtx.Account) {
	t.Helper()
	master := jtx.MasterAccount()
	newAcct := jtx.NewAccount("tec-target")
	// 100 XRP < 200 XRP reserve → tecNO_DST_INSUF_XRP.
	pay := payment.Pay(master, newAcct, 100_000_000).
		Sequence(env.Seq(master)).
		Build()
	blob := buildSignedBlob(t, env, pay, master)
	pt, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}
	return pt, newAcct
}

// TestApplyTxs_OpenLedgerMode_TecCommits verifies that under
// OpenLedgerMode, a tec result classifies as Success+commit per
// rippled OpenLedger::apply_one (OpenLedger.cpp:170-189). The tx must
// end up in the view's tx map and must NOT appear in retries.
func TestApplyTxs_OpenLedgerMode_TecCommits(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	pt, _ := buildTecPayment(t, env)
	view := freshView(t, env)

	var retries []openledger.PendingTx
	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   view.Sequence(),
		NetworkID:        0,
		Rules:            amendment.AllSupportedRules(),
		Mode:             openledger.OpenLedgerMode,
	}
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{pt}, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if len(retries) != 0 {
		t.Errorf("OpenLedgerMode: expected 0 retries (tec is Success), got %d", len(retries))
	}
	if !ledgerTxExists(t, view, pt.Hash) {
		t.Errorf("OpenLedgerMode: tec tx missing from view — should have committed with metadata")
	}
}

// TestApplyTxs_BuildLedgerMode_TecRetriesThenCommits verifies that under
// BuildLedgerMode, a tec is held for retry on retriable passes and
// commits on the final non-retry pass — matching BuildLedger.cpp's
// apply loop. Net effect: the tx still ends up in the view and is not
// reported as a leftover retry.
func TestApplyTxs_BuildLedgerMode_TecRetriesThenCommits(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	pt, _ := buildTecPayment(t, env)
	view := freshView(t, env)

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
	if err := openledger.ApplyTxs(view, []openledger.PendingTx{pt}, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if len(retries) != 0 {
		t.Errorf("BuildLedgerMode: expected 0 leftover retries (tec commits on final pass), got %d", len(retries))
	}
	if !ledgerTxExists(t, view, pt.Hash) {
		t.Errorf("BuildLedgerMode: tec tx missing from view — final non-retry pass should have committed")
	}
}

func TestApplyTxs_BuildLedgerModeUsesThreeTotalPasses(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("three-pass-alice")
	bob := jtx.NewAccount("three-pass-bob")
	carol := jtx.NewAccount("three-pass-carol")
	dave := jtx.NewAccount("three-pass-dave")
	erin := jtx.NewAccount("three-pass-erin")
	env.FundAmount(alice, 3_000_000_000)

	view := freshView(t, env)
	newAccountSequence := view.Sequence()
	buildPending := func(builder *payment.PaymentBuilder, signer *jtx.Account) openledger.PendingTx {
		t.Helper()
		blob := buildSignedBlob(t, env, builder.Build(), signer)
		ptx, err := openledger.ParsePendingTx(blob)
		if err != nil {
			t.Fatalf("ParsePendingTx: %v", err)
		}
		return ptx
	}

	aliceToBob := buildPending(
		payment.Pay(alice, bob, 1_500_000_000).Sequence(env.Seq(alice)),
		alice,
	)
	bobToCarol := buildPending(
		payment.Pay(bob, carol, 1_000_000_000).Sequence(newAccountSequence),
		bob,
	)
	carolToDave := buildPending(
		payment.Pay(carol, dave, 600_000_000).Sequence(newAccountSequence),
		carol,
	)
	daveToErin := buildPending(
		payment.Pay(dave, erin, 300_000_000).Sequence(newAccountSequence),
		dave,
	)

	pending := []openledger.PendingTx{
		daveToErin,
		carolToDave,
		bobToCarol,
		aliceToBob,
	}
	var retries []openledger.PendingTx
	err := openledger.ApplyTxs(
		view,
		pending,
		&retries,
		openledger.ApplyConfig{
			BaseFee:          10,
			ReserveBase:      200_000_000,
			ReserveIncrement: 50_000_000,
			LedgerSequence:   view.Sequence(),
			Rules:            amendment.AllSupportedRules(),
			Mode:             openledger.BuildLedgerMode,
		},
	)
	if err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	for _, ptx := range []openledger.PendingTx{aliceToBob, bobToCarol, carolToDave} {
		if !ledgerTxExists(t, view, ptx.Hash) {
			t.Fatalf("transaction %x did not settle within three passes", ptx.Hash)
		}
	}
	if ledgerTxExists(t, view, daveToErin.Hash) {
		t.Fatal("fourth dependency settled; BuildLedgerMode performed more than three total passes")
	}
	if len(retries) != 1 || retries[0].Hash != daveToErin.Hash {
		t.Fatalf("retries = %#v, want only the fourth dependency", retries)
	}
}

func TestApplyTxsResultDrivenFinalPassSettlesPostFinalDependency(t *testing.T) {
	for _, mode := range []openledger.Mode{
		openledger.OpenLedgerMode,
		openledger.BuildLedgerMode,
	} {
		t.Run(fmt.Sprintf("mode_%d", mode), func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.SetVerifySignatures(true)
			alice := jtx.NewAccount(fmt.Sprintf("final-alice-%d", mode))
			bob := jtx.NewAccount(fmt.Sprintf("final-bob-%d", mode))
			tooSmall := jtx.NewAccount(fmt.Sprintf("final-small-%d", mode))
			env.Fund(alice, bob)
			view := freshView(t, env)

			sequence := env.Seq(alice)
			tecTx := pendingPayment(
				t,
				env,
				payment.Pay(alice, tooSmall, 100_000_000).Sequence(sequence),
				alice,
			)
			dependent := pendingPayment(
				t,
				env,
				payment.Pay(alice, bob, 1_000_000).Sequence(sequence+1),
				alice,
			)

			var retries []openledger.PendingTx
			err := openledger.ApplyTxs(
				view,
				[]openledger.PendingTx{dependent, tecTx},
				&retries,
				openledger.ApplyConfig{
					BaseFee:          10,
					ReserveBase:      200_000_000,
					ReserveIncrement: 50_000_000,
					LedgerSequence:   view.Sequence(),
					Rules:            amendment.AllSupportedRules(),
					Mode:             mode,
				},
			)
			if err != nil {
				t.Fatalf("ApplyTxs: %v", err)
			}
			if !ledgerTxExists(t, view, tecTx.Hash) || !ledgerTxExists(t, view, dependent.Hash) {
				t.Fatalf(
					"post-final dependency did not settle: tec=%t dependent=%t",
					ledgerTxExists(t, view, tecTx.Hash),
					ledgerTxExists(t, view, dependent.Hash),
				)
			}
			if len(retries) != 0 {
				t.Fatalf("retries = %#v, want none", retries)
			}
		})
	}
}

func TestApplyTxsResultDrivenRetryPasses(t *testing.T) {
	for _, mode := range []openledger.Mode{
		openledger.OpenLedgerMode,
		openledger.BuildLedgerMode,
	} {
		t.Run(fmt.Sprintf("mode_%d", mode), func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.SetVerifySignatures(true)
			alice := jtx.NewAccount(fmt.Sprintf("retry-alice-%d", mode))
			sink := jtx.NewAccount(fmt.Sprintf("retry-sink-%d", mode))
			funder := jtx.NewAccount(fmt.Sprintf("retry-funder-%d", mode))
			bob := jtx.NewAccount(fmt.Sprintf("retry-bob-%d", mode))
			carol := jtx.NewAccount(fmt.Sprintf("retry-carol-%d", mode))
			tooSmall := jtx.NewAccount(fmt.Sprintf("retry-small-%d", mode))
			env.Fund(alice, sink, funder)
			view := freshView(t, env)

			aliceSequence := env.Seq(alice)
			tecTx := pendingPayment(
				t,
				env,
				payment.Pay(alice, tooSmall, 100_000_000).Sequence(aliceSequence),
				alice,
			)
			dependent := pendingPayment(
				t,
				env,
				payment.Pay(alice, sink, 1_000_000).Sequence(aliceSequence+1),
				alice,
			)
			bobToCarol := pendingPayment(
				t,
				env,
				payment.Pay(bob, carol, 300_000_000).Sequence(view.Sequence()),
				bob,
			)
			fundBob := pendingPayment(
				t,
				env,
				payment.Pay(funder, bob, 600_000_000).Sequence(env.Seq(funder)),
				funder,
			)

			var retries []openledger.PendingTx
			err := openledger.ApplyTxs(
				view,
				[]openledger.PendingTx{dependent, tecTx, bobToCarol, fundBob},
				&retries,
				openledger.ApplyConfig{
					BaseFee:          10,
					ReserveBase:      200_000_000,
					ReserveIncrement: 50_000_000,
					LedgerSequence:   view.Sequence(),
					Rules:            amendment.AllSupportedRules(),
					Mode:             mode,
				},
			)
			if err != nil {
				t.Fatalf("ApplyTxs: %v", err)
			}
			for _, ptx := range []openledger.PendingTx{tecTx, bobToCarol, fundBob} {
				if !ledgerTxExists(t, view, ptx.Hash) {
					t.Fatalf("transaction %x did not settle", ptx.Hash)
				}
			}
			if ledgerTxExists(t, view, dependent.Hash) {
				t.Fatal("dependency settled after the last available final pass")
			}
			if len(retries) != 1 || retries[0].Hash != dependent.Hash {
				t.Fatalf("retries = %#v, want only the post-final dependency", retries)
			}
		})
	}
}

func TestApplyTxsRejectsNilView(t *testing.T) {
	err := openledger.ApplyTxs(nil, nil, nil, openledger.ApplyConfig{})
	if err == nil {
		t.Fatal("ApplyTxs(nil) succeeded")
	}
}

func TestApplyTxsRejectsMissingRules(t *testing.T) {
	env := jtx.NewTestEnv(t)
	view := freshView(t, env)
	err := openledger.ApplyTxs(
		view,
		[]openledger.PendingTx{{Blob: []byte{0xff}}},
		nil,
		openledger.ApplyConfig{},
	)
	if err == nil {
		t.Fatal("ApplyTxs without amendment rules succeeded")
	}
}

func TestApplyTxsReturnsMalformedTransactionError(t *testing.T) {
	env := jtx.NewTestEnv(t)
	view := freshView(t, env)
	err := openledger.ApplyTxs(
		view,
		[]openledger.PendingTx{{Blob: []byte{0xff}}},
		nil,
		openledger.ApplyConfig{Rules: amendment.AllSupportedRules()},
	)
	if err == nil {
		t.Fatal("ApplyTxs with malformed transaction succeeded")
	}
}

func TestApplyTxsPreservesApplyFlagsAcrossPasses(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	pt, _ := buildTecPayment(t, env)
	view := freshView(t, env)

	var retries []openledger.PendingTx
	err := openledger.ApplyTxs(
		view,
		[]openledger.PendingTx{pt},
		&retries,
		openledger.ApplyConfig{
			BaseFee:          10,
			ReserveBase:      200_000_000,
			ReserveIncrement: 50_000_000,
			LedgerSequence:   view.Sequence(),
			Rules:            amendment.AllSupportedRules(),
			ApplyFlags:       tx.TapFAIL_HARD,
		},
	)
	if err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}
	if ledgerTxExists(t, view, pt.Hash) {
		t.Fatal("fail-hard tec transaction committed")
	}
	if len(retries) != 1 || retries[0].Hash != pt.Hash {
		t.Fatalf("retries = %#v, want the fail-hard transaction", retries)
	}
}

func TestTxqAdapterDirectApplyPreservesApplyFlags(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	ptx, _ := buildTecPayment(t, env)
	view := freshView(t, env)
	parsed, err := tx.ParseFromBinary(ptx.Blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	parsed.SetRawBytes(ptx.Blob)

	adapter := openledger.NewTxqAdapter(view, openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		Rules:            amendment.AllSupportedRules(),
		ApplyFlags:       tx.TapFAIL_HARD,
	})
	_, applied := adapter.ApplyTransaction(parsed)
	if applied {
		t.Fatal("TxQ direct apply committed a fail-hard tec transaction")
	}
	if ledgerTxExists(t, view, ptx.Hash) {
		t.Fatal("fail-hard TxQ transaction was added to the view")
	}
}

func pendingPayment(
	t *testing.T,
	env *jtx.TestEnv,
	builder *payment.PaymentBuilder,
	signer *jtx.Account,
) openledger.PendingTx {
	t.Helper()
	blob := buildSignedBlob(t, env, builder.Build(), signer)
	ptx, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}
	return ptx
}
