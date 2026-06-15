package service_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// signedPaymentWithFee builds a signed Payment blob carrying an explicit
// fee (drops). Used to drive the TxQ fee-escalation decision in
// Service.SubmitTransaction: a fee below the open-ledger fee level should
// be queued (terQUEUED) rather than applied.
func signedPaymentWithFee(t *testing.T, env *testenv.TestEnv, sender, receiver *testenv.Account, dropsAmount, fee uint64, senderSeq uint32) ([]byte, [32]byte) {
	t.Helper()
	env.SetVerifySignatures(true)

	txn := payment.Pay(sender, receiver, dropsAmount).Fee(fee).Sequence(senderSeq).Build()
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

func submitBlob(t *testing.T, svc *service.Service, blob []byte, failHard bool) *service.SubmitResult {
	t.Helper()
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	res, err := svc.SubmitTransaction(parsed, blob, failHard)
	if err != nil {
		t.Fatalf("SubmitTransaction: %v", err)
	}
	return res
}

// TestService_SubmitTransaction_AppliesAtOrAboveFeeLevel verifies the RPC
// ingress path now routes through TxQ.Apply and still applies a tx that
// meets the open-ledger fee level: it lands in the persistent open view
// with tesSUCCESS. This is the regression guard for the convergence onto
// the TxQ path (previously SubmitTransaction called engine.Apply directly).
func TestService_SubmitTransaction_AppliesAtOrAboveFeeLevel(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := testenv.NewTestEnv(t)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")

	// Fee 10 == base fee → fee level == base level == required level at an
	// empty open ledger, so the tx applies directly.
	blob, hash := signedPaymentWithFee(t, env, master, alice, 100_000_000, 10, 1)

	res := submitBlob(t, svc, blob, false)

	if !res.Applied {
		t.Fatalf("Applied = false, want true (result=%s)", res.Result)
	}
	if res.Result != ter.TesSUCCESS {
		t.Fatalf("Result = %s, want tesSUCCESS", res.Result)
	}
	if !svc.OpenLedgerHasTx(hash) {
		t.Errorf("applied tx not present in open view")
	}
}

// TestService_SubmitTransaction_QueuesBelowFeeLevel verifies that a tx
// paying below the open-ledger fee level is held by TxQ and surfaces
// terQUEUED through SubmitTransaction (Applied=false) instead of applying.
// Before the convergence the RPC path bypassed TxQ entirely and could
// never produce terQUEUED. The queued tx must NOT appear in the open view.
func TestService_SubmitTransaction_QueuesBelowFeeLevel(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := testenv.NewTestEnv(t)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")

	// Fee 1 < base fee 10 → fee level 25 < required base level 256 at an
	// empty open ledger, so TxQ holds the tx rather than applying it.
	blob, hash := signedPaymentWithFee(t, env, master, alice, 100_000_000, 1, 1)

	res := submitBlob(t, svc, blob, false)

	if res.Result != ter.TerQUEUED {
		t.Fatalf("Result = %s, want terQUEUED", res.Result)
	}
	if res.Applied {
		t.Errorf("Applied = true, want false for a queued tx")
	}
	if svc.OpenLedgerHasTx(hash) {
		t.Errorf("queued tx must not be present in the open view")
	}
}

// TestService_SubmitTransaction_FailHardNotQueued verifies tapFAIL_HARD
// blocks queue admission: a below-fee-level tx that would otherwise be
// queued is rejected (telCAN_NOT_QUEUE) when fail_hard is set, mirroring
// rippled TxQ::canBeHeld (TxQ.cpp:393-399).
func TestService_SubmitTransaction_FailHardNotQueued(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := testenv.NewTestEnv(t)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")

	blob, hash := signedPaymentWithFee(t, env, master, alice, 100_000_000, 1, 1)

	res := submitBlob(t, svc, blob, true)

	if res.Applied {
		t.Errorf("Applied = true, want false")
	}
	if res.Result == ter.TerQUEUED {
		t.Errorf("Result = terQUEUED, want a rejection under fail_hard")
	}
	if svc.OpenLedgerHasTx(hash) {
		t.Errorf("fail_hard rejected tx must not be in the open view")
	}
}
