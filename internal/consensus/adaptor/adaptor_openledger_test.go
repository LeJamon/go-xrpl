package adaptor_test

import (
	"bytes"
	"encoding/hex"
	"sort"
	"testing"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/adaptor"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	testenv "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/protocol"
)

// newOpenLedgerTestService spins up a service for the open-ledger
// adaptor tests. Mirrors the helper in service_openledger_test.go.
func newOpenLedgerTestService(t *testing.T) *service.Service {
	t.Helper()
	cfg := service.Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
	}
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	return svc
}

// newAdaptorWithService wraps svc in an Adaptor with a known validator
// identity. The exact identity does not matter for tx-ingress tests —
// we only care about the open-ledger plumbing.
func newAdaptorWithService(t *testing.T, svc *service.Service) *adaptor.Adaptor {
	t.Helper()
	identity, err := adaptor.NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	if err != nil {
		t.Fatalf("NewValidatorIdentity: %v", err)
	}
	return adaptor.New(adaptor.Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
}

// buildSignedPaymentBlob constructs a signed Payment binary blob from
// sender to receiver for the given drops amount. Same shape as the
// service-test helper of the same name.
func buildSignedPaymentBlob(t *testing.T, env *testenv.TestEnv, sender, receiver *testenv.Account, dropsAmount uint64, senderSeq uint32) ([]byte, [32]byte) {
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

// TestAdaptor_AddPendingTx_RoutesToOpenLedger verifies that AddPendingTx
// funnels the blob into the persistent OpenLedger view. HasTx then
// resolves through that same view. Pins the wiring in
// adaptor.AddPendingTx → service.SubmitOpenLedgerTx and
// adaptor.HasTx → service.OpenLedgerHasTx.
func TestAdaptor_AddPendingTx_RoutesToOpenLedger(t *testing.T) {
	svc := newOpenLedgerTestService(t)
	a := newAdaptorWithService(t, svc)

	env := testenv.NewTestEnv(t)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")
	blob, hash := buildSignedPaymentBlob(t, env, master, alice, 100_000_000, 1)

	a.AddPendingTx(blob, true)

	if !svc.OpenLedgerHasTx(hash) {
		t.Errorf("service.OpenLedgerHasTx(hash) = false; AddPendingTx did not land blob in open view")
	}
	if !a.HasTx(consensus.TxID(hash)) {
		t.Errorf("adaptor.HasTx = false; HasTx must resolve through the persistent open view")
	}
}

// TestAdaptor_GetProposableTxs_FromOpenLedger verifies that propose-time
// reads go through service.OpenLedgerTxs (which reads
// openLedger.Current().Txs()) and surface every successfully submitted
// tx. This is the core #407 fix: the propose-time read is a pointer-
// deref, not a per-call multi-pass filter.
func TestAdaptor_GetProposableTxs_FromOpenLedger(t *testing.T) {
	svc := newOpenLedgerTestService(t)
	a := newAdaptorWithService(t, svc)

	env := testenv.NewTestEnv(t)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	blob1, _ := buildSignedPaymentBlob(t, env, master, alice, 50_000_000, 1)
	blob2, _ := buildSignedPaymentBlob(t, env, master, bob, 60_000_000, 2)

	a.AddPendingTx(blob1, true)
	a.AddPendingTx(blob2, true)

	closed := svc.GetClosedLedger()
	if closed == nil {
		t.Fatal("GetClosedLedger nil after Start")
	}

	got := a.GetProposableTxs(adaptor.WrapLedger(closed))
	if len(got) != 2 {
		t.Fatalf("GetProposableTxs len = %d, want 2", len(got))
	}

	// Order is implementation-defined — sort lexicographically and compare.
	want := [][]byte{blob1, blob2}
	sortBlobs := func(blobs [][]byte) {
		sort.Slice(blobs, func(i, j int) bool {
			return bytes.Compare(blobs[i], blobs[j]) < 0
		})
	}
	sortBlobs(got)
	sortBlobs(want)
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("GetProposableTxs[%d] mismatch", i)
		}
	}
}

// TestAdaptor_AddPendingTx_FailureNotInPool verifies that a tef/tem-class
// failure on the open-view Submit drops the blob — the persistent view
// must not retain a failed tx, otherwise peer HasTx replies would claim
// we still hold a known-bad blob.
func TestAdaptor_AddPendingTx_FailureNotInPool(t *testing.T) {
	svc := newOpenLedgerTestService(t)
	a := newAdaptorWithService(t, svc)

	env := testenv.NewTestEnv(t)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")
	goodBlob, _ := buildSignedPaymentBlob(t, env, master, alice, 50_000_000, 1)

	// Corrupt the signature by XORing a byte near the end, where the
	// TxnSignature trailer lives. Matches the trick in
	// internal/ledger/openledger/apply_test.go for guaranteeing a
	// non-retry tef/tem class result.
	if len(goodBlob) < 10 {
		t.Fatalf("blob too short to corrupt: %d bytes", len(goodBlob))
	}
	corrupted := make([]byte, len(goodBlob))
	copy(corrupted, goodBlob)
	mid := len(corrupted) - 8
	corrupted[mid] ^= 0xFF

	corruptedHash := computeTxIDForTest(corrupted)

	a.AddPendingTx(corrupted, true)

	if svc.OpenLedgerHasTx(corruptedHash) {
		t.Errorf("service.OpenLedgerHasTx(corrupted) = true; failed tx leaked into open view")
	}
	if a.HasTx(consensus.TxID(corruptedHash)) {
		t.Errorf("adaptor.HasTx(corrupted) = true; failed tx leaked into open view (spec says drop on ResultFailure)")
	}
}

// computeTxIDForTest mirrors the unexported computeTxID inside the
// adaptor package: sha512Half(HashPrefixTransactionID, blob). This is
// the canonical XRPL tx hash. Re-implemented here so the test file can
// stay in the _test package.
func computeTxIDForTest(blob []byte) [32]byte {
	return common.Sha512Half(protocol.HashPrefixTransactionID[:], blob)
}
