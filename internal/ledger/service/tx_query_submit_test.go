package service_test

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

// signedPaymentWithFee builds a signed Payment blob carrying an explicit
// fee (drops). Used to drive the TxQ fee-escalation decision in
// Service.SubmitTransaction: a fee below the open-ledger fee level should
// be queued (terQUEUED) rather than applied.
func signedPaymentWithFee(t *testing.T, env *jtx.TestEnv, sender, receiver *jtx.Account, dropsAmount, fee uint64, senderSeq uint32) ([]byte, [32]byte) {
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

func innerBatchPaymentBlob(t *testing.T, sender, receiver *jtx.Account) ([]byte, [32]byte) {
	t.Helper()
	txn := payment.Pay(sender, receiver, 1).Fee(10).Sequence(1).Build()
	txn.GetCommon().SetFlags(tx.TfInnerBatchTxn)
	txn.GetCommon().SigningPubKey = ""

	txMap, err := txn.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	txMap["SigningPubKey"] = ""
	hexStr, err := binarycodec.Encode(txMap)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	hash, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	return blob, hash
}

// memoPaymentBlob builds an (unsigned) Payment blob carrying a memo whose
// MemoData is the given hex string. It encodes the field map directly rather
// than via the typed Payment.Flatten, whose reflective memo conversion is a
// separate concern; the local memo check reads the decoded Memos regardless.
func memoPaymentBlob(t *testing.T, from, to string, memoDataHex string) []byte {
	t.Helper()
	txMap := map[string]any{
		"TransactionType": "Payment",
		"Account":         from,
		"Destination":     to,
		"Amount":          "100000000",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"Memos": []map[string]any{
			{"Memo": map[string]any{"MemoData": memoDataHex}},
		},
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

// TestService_SubmitTransaction_RejectsOversizedMemo verifies the local
// submission ingress enforces rippled's passesLocalChecks memo rule: a memo
// whose serialized size exceeds 1024 bytes is refused temMALFORMED at
// Service.SubmitTransaction, before the engine ever runs. This is the local-only
// counterpart to the engine's now memo-agnostic preflight (see the engine
// AcceptsOversizedMemo test) — a relayed or consensus-applied oversized-memo tx
// still applies, so the two behaviours cannot fork.
func TestService_SubmitTransaction_RejectsOversizedMemo(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")

	// 1020 decoded bytes → 1025 serialized (> 1024).
	blob := memoPaymentBlob(t, master.Address, alice.Address, strings.Repeat("AA", 1020))
	res := submitBlob(t, svc, blob, false)
	if res.Result != ter.TemMALFORMED {
		t.Fatalf("Result = %s, want temMALFORMED", res.Result)
	}
	if res.Applied {
		t.Errorf("Applied = true, want false for a memo-rejected tx")
	}
	if res.CurrentLedgerState != nil {
		t.Errorf("local rejection must omit current-ledger state, got %+v", res.CurrentLedgerState)
	}
}

func TestService_SubmitTransaction_RejectsInnerBatchTransaction(t *testing.T) {
	tests := []struct {
		name       string
		amendments [][32]byte
		want       ter.Result
	}{
		{
			name:       "BatchV1_1 disabled",
			amendments: nil,
			want:       ter.TemINVALID_FLAG,
		},
		{
			name:       "BatchV1_1 enabled",
			amendments: [][32]byte{amendment.FeatureBatchV1_1},
			want:       ter.TemINVALID_INNER_BATCH,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultServiceConfig()
			cfg.Startup.Mode = service.StartupFresh
			cfg.GenesisConfig.Amendments = append(cfg.GenesisConfig.Amendments, test.amendments...)
			svc, err := service.New(cfg)
			if err != nil {
				t.Fatalf("service.New: %v", err)
			}
			if err := svc.Start(); err != nil {
				t.Fatalf("service.Start: %v", err)
			}
			t.Cleanup(svc.Stop)
			for _, feature := range test.amendments {
				if !svc.TransactionRules().Enabled(feature) {
					t.Fatalf("configured amendment %X is not enabled", feature)
				}
			}

			blob, hash := innerBatchPaymentBlob(t, jtx.MasterAccount(), jtx.NewAccount("alice"))
			result := submitBlob(t, svc, blob, false)
			if result.Result != test.want {
				t.Fatalf("Result = %s, want %s", result.Result, test.want)
			}
			if result.Applied {
				t.Fatal("directly submitted inner Batch transaction applied")
			}
			if openLedgerHasTx(t, svc, hash) {
				t.Fatal("directly submitted inner Batch transaction entered the open ledger")
			}
			if _, err := svc.GetTransaction(hash); !errors.Is(err, svcerr.ErrTxnNotFound) {
				t.Fatalf("directly submitted inner Batch transaction was retained: %v", err)
			}
		})
	}
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

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")

	blob, hash := signedPaymentWithFee(t, env, master, alice, 100_000_000, 10, 1)

	res := submitBlob(t, svc, blob, false)

	if !res.Applied {
		t.Fatalf("Applied = false, want true (result=%s)", res.Result)
	}
	if res.Result != ter.TesSUCCESS {
		t.Fatalf("Result = %s, want tesSUCCESS", res.Result)
	}
	if !openLedgerHasTx(t, svc, hash) {
		t.Errorf("applied tx not present in open view")
	}
	if res.CurrentLedgerState == nil {
		t.Fatal("applied submit must include current-ledger state")
	}
	if got := res.CurrentLedgerState.AccountSequenceNext; got != 2 {
		t.Errorf("account_sequence_next = %d, want post-apply sequence 2", got)
	}
	if got := res.CurrentLedgerState.AccountSequenceAvailable; got != 2 {
		t.Errorf("account_sequence_available = %d, want 2", got)
	}
	if res.CurrentLedgerState.OpenLedgerCost == 0 || res.CurrentLedgerState.ValidatedLedgerIndex == 0 {
		t.Errorf("submit state missing fee/validated index: %+v", res.CurrentLedgerState)
	}
}

func TestService_SubmitTransaction_BadSignatureIsNotQueryable(t *testing.T) {
	cfg := defaultServiceConfig()
	cfg.Standalone = false
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	blob, _ := signedPaymentWithFee(t, env, jtx.MasterAccount(), jtx.NewAccount("alice"), 100_000_000, 10, 1)
	wire, err := binarycodec.DecodeBytes(blob)
	if err != nil {
		t.Fatalf("DecodeBytes: %v", err)
	}
	wire["TxnSignature"] = strings.Repeat("00", 64)
	badHex, err := binarycodec.Encode(wire)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	badBlob, err := hex.DecodeString(badHex)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	parsed, err := tx.ParseFromBinary(badBlob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	hash, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}

	result, err := svc.SubmitTransaction(parsed, badBlob, false)
	if err != nil {
		t.Fatalf("SubmitTransaction: %v", err)
	}
	if result.Result != ter.TemINVALID {
		t.Fatalf("Result = %s, want temINVALID", result.Result)
	}
	if _, err := svc.GetTransaction(hash); !errors.Is(err, svcerr.ErrTxnNotFound) {
		t.Fatalf("GetTransaction(bad signature) = %v, want svcerr.ErrTxnNotFound", err)
	}
}

func TestService_SubmitTransaction_BatchSignerFailureIsNotHeld(t *testing.T) {
	cfg := defaultServiceConfig()
	cfg.Standalone = false
	cfg.Startup.Mode = service.StartupFresh
	cfg.GenesisConfig.Amendments = append(cfg.GenesisConfig.Amendments, amendment.FeatureBatchV1_1)
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	t.Cleanup(svc.Stop)
	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("GetClosedLedger returned nil")
	}
	svc.SetValidatedLedger(parent.Sequence(), parent.Hash())
	if _, err := svc.AcceptConsensusResult(t.Context(), parent, nil, nil, parent.CloseTime().Add(time.Second), true); err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}
	if !svc.TransactionRules().Enabled(amendment.FeatureBatchV1_1) {
		t.Fatal("Batch amendment not enabled in service rules")
	}

	env := jtx.NewTestEnv(t)
	env.EnableFeatureNow("BatchV1_1")
	env.SetVerifySignatures(true)
	outer := jtx.MasterAccount()
	innerSigner := jtx.NewAccount("batch-signer")
	batch := batchtx.NewBatch(outer.Address)
	batch.Fee = "50"
	batch.SetSequence(1)
	batch.SetFlags(batchtx.BatchFlagAllOrNothing)
	for _, inner := range []tx.Transaction{
		payment.Pay(outer, innerSigner, uint64(jtx.XRP(1))).Fee(0).Sequence(2).Build(),
		payment.Pay(innerSigner, outer, uint64(jtx.XRP(1))).Fee(0).Sequence(1).Build(),
	} {
		inner.GetCommon().SigningPubKey = ""
		inner.GetCommon().SetFlags(tx.TfInnerBatchTxn)
		batch.AddInnerTransaction(inner)
	}
	batch.BatchSigners = []batchtx.BatchSigner{{
		BatchSigner: batchtx.BatchSignerData{
			Account:           innerSigner.Address,
			SigningPubKey:     innerSigner.PublicKeyHex(),
			BatchTxnSignature: "DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF",
		},
	}}
	env.SignWith(batch, outer)

	txMap, err := batch.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	blobHex, err := binarycodec.Encode(txMap)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	blob, err := hex.DecodeString(blobHex)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	hash, err := tx.ComputeTransactionHash(parsed)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}

	result, err := svc.SubmitTransaction(parsed, blob, false)
	if err != nil {
		t.Fatalf("SubmitTransaction: %v", err)
	}
	if result.Result != ter.TemINVALID {
		t.Fatalf("Result = %s, want temINVALID", result.Result)
	}
	if result.Applied {
		t.Fatal("invalid BatchSigner signature must not apply")
	}
	if _, err := svc.GetTransaction(hash); !errors.Is(err, svcerr.ErrTxnNotFound) {
		t.Fatalf("GetTransaction(bad BatchSigner) = %v, want svcerr.ErrTxnNotFound", err)
	}
}

// TestService_SubmitTransaction_QueuesBelowFeeLevel verifies that a tx
// paying below the open-ledger fee level is held by TxQ and surfaces
// terQUEUED through SubmitTransaction (Applied=false) instead of applying.
// Before the convergence the RPC path bypassed TxQ entirely and could
// never produce terQUEUED. The queued tx must NOT appear in the open view.
func TestService_SubmitTransaction_QueuesBelowFeeLevel(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")

	blob, hash := signedPaymentWithFee(t, env, master, alice, 100_000_000, 1, 1)

	res := submitBlob(t, svc, blob, false)

	if res.Result != ter.TerQUEUED {
		t.Fatalf("Result = %s, want terQUEUED", res.Result)
	}
	if res.Applied {
		t.Errorf("Applied = true, want false for a queued tx")
	}
	if openLedgerHasTx(t, svc, hash) {
		t.Errorf("queued tx must not be present in the open view")
	}
	queuedResult, err := svc.GetTransaction(hash)
	if err != nil {
		t.Fatalf("queued tx lookup: %v", err)
	}
	if queuedResult.LedgerIndex != 0 || queuedResult.Validated || queuedResult.TxIndex != ^uint32(0) {
		t.Fatalf("queued tx advertised closed-ledger state: %+v", queuedResult)
	}
	queuedBlob, metaBlob, err := tx.SplitTxWithMetaBlob(queuedResult.TxData)
	if err != nil {
		t.Fatalf("split queued tx: %v", err)
	}
	if string(queuedBlob) != string(blob) || metaBlob != nil {
		t.Fatalf("queued tx payload = (%x, %x), want tx-only input", queuedBlob, metaBlob)
	}
	if res.CurrentLedgerState == nil {
		t.Fatal("queued submit must include current-ledger state")
	}
	if got := res.CurrentLedgerState.AccountSequenceNext; got != 1 {
		t.Errorf("queued account_sequence_next = %d, want unchanged sequence 1", got)
	}
	if got := res.CurrentLedgerState.AccountSequenceAvailable; got != 2 {
		t.Errorf("queued account_sequence_available = %d, want just-admitted sequence 2", got)
	}
}

func TestService_SubmitTransaction_QueuedSnapshotUsesEscalatedFee(t *testing.T) {
	cfg := service.DefaultConfig()
	cfg.Standalone = true
	queueCfg := txq.StandaloneConfig()
	queueCfg.MinimumTxnInLedgerStandalone = 1
	queueCfg.TargetTxnInLedger = 1
	cfg.TxQ = &queueCfg
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	firstBlob, _ := signedPaymentWithFee(t, env, master, alice, 100_000_000, 10, 1)
	if first := submitBlob(t, svc, firstBlob, false); first.Result != ter.TesSUCCESS {
		t.Fatalf("first payment result = %s, want tesSUCCESS", first.Result)
	}
	secondBlob, _ := signedPaymentWithFee(t, env, master, bob, 100_000_000, 10, 2)
	if second := submitBlob(t, svc, secondBlob, false); second.Result != ter.TesSUCCESS {
		t.Fatalf("second payment result = %s, want tesSUCCESS", second.Result)
	}
	queuedBlob, _ := signedPaymentWithFee(t, env, master, jtx.NewAccount("carol"), 100_000_000, 1, 3)
	queued := submitBlob(t, svc, queuedBlob, false)
	if queued.Result != ter.TerQUEUED {
		t.Fatalf("queued payment result = %s, want terQUEUED", queued.Result)
	}
	if queued.CurrentLedgerState == nil {
		t.Fatal("queued submit must include current-ledger state")
	}
	if got := queued.CurrentLedgerState.AccountSequenceNext; got != 3 {
		t.Errorf("account_sequence_next = %d, want unchanged sequence 3", got)
	}
	if got := queued.CurrentLedgerState.AccountSequenceAvailable; got != 4 {
		t.Errorf("account_sequence_available = %d, want just-admitted sequence 4", got)
	}
	if got := queued.CurrentLedgerState.OpenLedgerCost; got <= 10 {
		t.Errorf("open_ledger_cost = %d, want escalated fee above base 10", got)
	}
}

// TestService_SubmitTransaction_FailHardNotQueued verifies tapFAIL_HARD
// blocks queue admission: a below-fee-level tx that would otherwise be
// queued is rejected (telCAN_NOT_QUEUE) when fail_hard is set, mirroring
// rippled TxQ::canBeHeld (TxQ.cpp:393-399).
func TestService_SubmitTransaction_FailHardNotQueued(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	alice := jtx.NewAccount("alice")

	blob, hash := signedPaymentWithFee(t, env, master, alice, 100_000_000, 1, 1)

	res := submitBlob(t, svc, blob, true)

	if res.Applied {
		t.Errorf("Applied = true, want false")
	}
	if res.Result == ter.TerQUEUED {
		t.Errorf("Result = terQUEUED, want a rejection under fail_hard")
	}
	if openLedgerHasTx(t, svc, hash) {
		t.Errorf("fail_hard rejected tx must not be in the open view")
	}
}
