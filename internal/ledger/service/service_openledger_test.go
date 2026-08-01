package service_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	consensuscommon "github.com/LeJamon/go-xrpl/internal/consensus/common"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
)

func openLedgerHasTx(t *testing.T, svc *service.Service, hash [32]byte) bool {
	t.Helper()
	exists, err := svc.OpenLedgerHasTx(hash)
	if err != nil {
		t.Fatalf("OpenLedgerHasTx(%x): %v", hash, err)
	}
	return exists
}

// buildSignedPaymentBlob constructs a signed Payment binary blob from sender
// to receiver for the given drops amount. The sender's sequence is fixed at
// 1, matching the master-account sequence on a freshly-started service. The
// signature is real (secp256k1) because the service's open-ledger Submit
// path verifies signatures by default.
func buildSignedPaymentBlob(t *testing.T, env *jtx.TestEnv, sender, receiver *jtx.Account, dropsAmount uint64, senderSeq uint32) ([]byte, [32]byte) {
	t.Helper()
	env.SetVerifySignatures(true)

	txn := payment.Pay(sender, receiver, dropsAmount).Sequence(senderSeq).Build()
	env.SignWith(txn, sender)

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

	hash, err := tx.ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	return blob, hash
}

// newServiceForOpenLedgerTest spins up a service and runs Start. Mirrors
// the service_test.go New/Start pattern used by the existing
// TestService_* cases.
func newServiceForOpenLedgerTest(t *testing.T) *service.Service {
	t.Helper()
	cfg := defaultServiceConfig()
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	return svc
}

// TestService_OpenLedgerSubmit_Roundtrip verifies that a tx submitted via
// SubmitOpenLedgerTx lands in the persistent open view and is observable
// through OpenLedgerTxs / OpenLedgerHasTx / OpenLedgerGetTx.
func TestService_OpenLedgerSubmit_Roundtrip(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	dest := jtx.NewAccount("alice")

	blob, hash := buildSignedPaymentBlob(t, env, master, dest, 100_000_000, 1)

	res, err := svc.SubmitOpenLedgerTx(blob, true)
	if err != nil {
		t.Fatalf("SubmitOpenLedgerTx: %v", err)
	}
	if res != openledger.ResultSuccess {
		t.Fatalf("SubmitOpenLedgerTx result = %v, want ResultSuccess", res)
	}

	gotBlobs := svc.OpenLedgerTxs()
	if len(gotBlobs) != 1 {
		t.Fatalf("OpenLedgerTxs len = %d, want 1", len(gotBlobs))
	}
	if !bytes.Equal(gotBlobs[0], blob) {
		t.Errorf("OpenLedgerTxs[0] != original blob")
	}

	if !openLedgerHasTx(t, svc, hash) {
		t.Errorf("OpenLedgerHasTx(hash) = false, want true")
	}

	got, ok := svc.OpenLedgerGetTx(hash)
	if !ok {
		t.Fatal("OpenLedgerGetTx returned ok=false for known hash")
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("OpenLedgerGetTx returned mismatched blob")
	}
}

// TestService_AcceptConsensusResult_RebuildsOpenView verifies that on an
// LCL transition with an empty agreed-set, the persistent open view
// replays the prior current view's txs onto the new closed ledger.
// This is the key invariant proving the OpenLedger.Accept wiring works:
// txs that didn't land in the closed ledger get carried forward.
func TestService_AcceptConsensusResult_RebuildsOpenView(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	// Two independent txs from the master account at consecutive sequences.
	blob1, hash1 := buildSignedPaymentBlob(t, env, master, alice, 50_000_000, 1)
	blob2, hash2 := buildSignedPaymentBlob(t, env, master, bob, 60_000_000, 2)

	for i, blob := range [][]byte{blob1, blob2} {
		res, err := svc.SubmitOpenLedgerTx(blob, true)
		if err != nil {
			t.Fatalf("SubmitOpenLedgerTx[%d]: %v", i, err)
		}
		if res != openledger.ResultSuccess {
			t.Fatalf("SubmitOpenLedgerTx[%d] result = %v, want ResultSuccess", i, res)
		}
	}

	// Close with an empty agreed-set so neither tx lands in the closed
	// ledger. Accept must replay both from the prior view.
	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("GetClosedLedger nil before AcceptConsensusResult")
	}
	if _, err := svc.AcceptConsensusResult(context.TODO(), parent, nil, nil, time.Now(), true); err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}

	if !openLedgerHasTx(t, svc, hash1) {
		t.Errorf("post-Accept OpenLedgerHasTx(hash1) = false; replay dropped tx1")
	}
	if !openLedgerHasTx(t, svc, hash2) {
		t.Errorf("post-Accept OpenLedgerHasTx(hash2) = false; replay dropped tx2")
	}
}

func TestService_SwitchToPreferredLedgerReplaysOpenTransactions(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := jtx.NewTestEnv(t)
	blob, hash := buildSignedPaymentBlob(
		t,
		env,
		jtx.MasterAccount(),
		jtx.NewAccount("preferred-switch-destination"),
		50_000_000,
		1,
	)
	res, err := svc.SubmitOpenLedgerTx(blob, true)
	if err != nil {
		t.Fatalf("SubmitOpenLedgerTx: %v", err)
	}
	if res != openledger.ResultSuccess {
		t.Fatalf("SubmitOpenLedgerTx result = %v, want ResultSuccess", res)
	}

	preferred, err := ledger.NewOpen(svc.GetClosedLedger(), time.Now())
	if err != nil {
		t.Fatalf("NewOpen: %v", err)
	}
	if err := preferred.Close(time.Now(), 0); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.SwitchToPreferredLedger(preferred); err != nil {
		t.Fatalf("SwitchToPreferredLedger: %v", err)
	}

	if got := svc.GetClosedLedger(); got.Hash() != preferred.Hash() {
		t.Fatalf("closed ledger = %x, want %x", got.Hash(), preferred.Hash())
	}
	if !openLedgerHasTx(t, svc, hash) {
		t.Fatal("preferred-ledger switch dropped a transaction from the open view")
	}
}

// TestService_AcceptConsensusResult_IncludedTxsNotDuplicated verifies that
// when a tx makes it into the agreed-set (and thus into the closed
// ledger), the post-Accept open view does NOT contain a duplicate entry.
// The replay's per-tx TxExists guard against the new closed parent must
// drop already-committed txs.
func TestService_AcceptConsensusResult_IncludedTxsNotDuplicated(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")

	blob1, hash1 := buildSignedPaymentBlob(t, env, master, alice, 50_000_000, 1)

	res, err := svc.SubmitOpenLedgerTx(blob1, true)
	if err != nil {
		t.Fatalf("SubmitOpenLedgerTx: %v", err)
	}
	if res != openledger.ResultSuccess {
		t.Fatalf("SubmitOpenLedgerTx result = %v, want ResultSuccess", res)
	}

	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("GetClosedLedger nil before AcceptConsensusResult")
	}
	if _, err := svc.AcceptConsensusResult(context.TODO(), parent, [][]byte{blob1}, nil, time.Now(), true); err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}

	closed := svc.GetClosedLedger()
	if closed == nil {
		t.Fatal("GetClosedLedger nil after AcceptConsensusResult")
	}
	closedHasHash1, err := closed.TxExists(hash1)
	if err != nil {
		t.Fatalf("closed.TxExists(hash1): %v", err)
	}
	if !closedHasHash1 {
		t.Errorf("closed ledger missing tx1 after consensus close")
	}
	if openLedgerHasTx(t, svc, hash1) {
		t.Errorf("post-Accept open view still contains tx1 (already in closed ledger)")
	}
	if got := svc.OpenLedgerTxs(); len(got) != 0 {
		t.Errorf("post-Accept OpenLedgerTxs len = %d, want 0", len(got))
	}
}

// TestService_AcceptConsensusResult_DisputedTxsFirstCrack verifies that a
// disputed tx we voted NO on — peer-proposed, never in our open ledger —
// is replayed into the fresh open view on the LCL transition, not into the
// closed ledger.
func TestService_AcceptConsensusResult_DisputedTxsFirstCrack(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")

	disputedBlob, disputedHash := buildSignedPaymentBlob(t, env, master, alice, 50_000_000, 1)

	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("GetClosedLedger nil before AcceptConsensusResult")
	}
	if _, err := svc.AcceptConsensusResult(context.TODO(), parent, nil, [][]byte{disputedBlob}, time.Now(), true); err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}

	closed := svc.GetClosedLedger()
	if closed == nil {
		t.Fatal("GetClosedLedger nil after AcceptConsensusResult")
	}
	closedHasDisputed, err := closed.TxExists(disputedHash)
	if err != nil {
		t.Fatalf("closed.TxExists(disputedHash): %v", err)
	}
	if closedHasDisputed {
		t.Errorf("disputed tx landed in the closed ledger; must only reach the open view")
	}
	if !openLedgerHasTx(t, svc, disputedHash) {
		t.Errorf("post-Accept open view missing the disputed we-voted-NO tx")
	}
}

// TestService_AcceptConsensusResult_DisputedPseudoTxSkipped verifies that a
// disputed pseudo-tx is dropped from the replay: a pseudo-tx rejected by
// consensus cannot be successfully applied in a later ledger.
func TestService_AcceptConsensusResult_DisputedPseudoTxSkipped(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	ledgerSeq := uint32(2)
	pseudoBlob, err := consensuscommon.BuildPseudoTx(tx.TypeAmendment, func(base tx.BaseTx) tx.Transaction {
		return &pseudo.EnableAmendment{
			BaseTx:         base,
			Amendment:      strings.Repeat("AB", 32),
			LedgerSequence: &ledgerSeq,
		}
	})
	if err != nil {
		t.Fatalf("BuildPseudoTx: %v", err)
	}
	pseudoTx, err := openledger.ParsePendingTx(pseudoBlob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("GetClosedLedger nil before AcceptConsensusResult")
	}
	if _, err := svc.AcceptConsensusResult(context.TODO(), parent, nil, [][]byte{pseudoBlob}, time.Now(), true); err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}

	if openLedgerHasTx(t, svc, pseudoTx.Hash) {
		t.Errorf("post-Accept open view contains a disputed pseudo-tx; pseudo-txs must be skipped")
	}
	if got := svc.OpenLedgerTxs(); len(got) != 0 {
		t.Errorf("post-Accept OpenLedgerTxs len = %d, want 0", len(got))
	}
}
