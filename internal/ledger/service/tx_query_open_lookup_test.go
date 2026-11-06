package service

import (
	"encoding/hex"
	"errors"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

func TestGetTransactionOpenHashMismatchIsOperationalError(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	transaction := payment.Pay(jtx.MasterAccount(), jtx.NewAccount("open-lookup-mismatch"), 50_000_000).Sequence(1).Build()
	env.SignWith(transaction, jtx.MasterAccount())
	txMap, err := transaction.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	hexBlob, err := binarycodec.Encode(txMap)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	blob, err := hex.DecodeString(hexBlob)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	hash, err := tx.ComputeTransactionHash(transaction)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	if result, err := svc.SubmitOpenLedgerTx(blob, true); err != nil || result != openledger.ResultSuccess {
		t.Fatalf("SubmitOpenLedgerTx = (%v, %v), want success", result, err)
	}

	var mismatch [32]byte
	mismatch[0] = 0xA5
	svc.mu.Lock()
	data, found, err := svc.openLedgerView.Current().GetTransaction(hash)
	if err != nil || !found {
		svc.mu.Unlock()
		t.Fatalf("read submitted open leaf = (%v, %v, %v)", len(data), found, err)
	}
	var modifyErr error
	svc.openLedgerView.Modify(func(view *ledger.Ledger) bool {
		modifyErr = view.AddTransactionWithMeta(mismatch, data)
		return modifyErr == nil
	})
	svc.mu.Unlock()
	if modifyErr != nil {
		t.Fatalf("inject mismatched open leaf: %v", modifyErr)
	}

	_, err = svc.GetTransaction(mismatch)
	if err == nil {
		t.Fatal("hash-mismatched open leaf unexpectedly succeeded")
	}
	if errors.Is(err, svcerr.ErrTxnNotFound) {
		t.Fatalf("hash-mismatched open leaf collapsed to not found: %v", err)
	}
	if !errors.Is(err, svcerr.ErrTxnDataCorrupt) {
		t.Fatalf("hash-mismatched open leaf error = %v, want svcerr.ErrTxnDataCorrupt", err)
	}
}

func TestGetTransactionIgnoresRelayCachesButReturnsHeldPool(t *testing.T) {
	raw, hash := validRelationalTestTransaction(t, 1)
	for _, source := range []string{"relay", "corrupt relay", "held"} {
		t.Run(source, func(t *testing.T) {
			svc, err := New(DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Stop)
			switch source {
			case "relay":
				svc.rememberRelayTransaction(hash, raw, false)
			case "corrupt relay":
				svc.rememberRelayTransaction(hash, []byte{0x01, 0x02}, false)
			case "held":
				svc.localTxs.PushBack(1, openledger.PendingTx{Hash: hash, Blob: raw})
			}

			result, err := svc.GetTransaction(hash)
			if source == "held" {
				requireHeldTxOnly(t, result, err, raw)
				result, searched, err := svc.GetTransactionWithRange(t.Context(), hash, 1, 2)
				requireHeldTxOnly(t, result, err, raw)
				if searched != relationaldb.TxSearchAll {
					t.Fatalf("GetTransactionWithRange search = %v, want TxSearchAll", searched)
				}
				return
			}
			if result != nil || !errors.Is(err, svcerr.ErrTxnNotFound) {
				t.Fatalf("GetTransaction = (%+v, %v), want txnNotFound", result, err)
			}
			result, searched, err := svc.GetTransactionWithRange(t.Context(), hash, 1, 2)
			if result != nil || !errors.Is(err, svcerr.ErrTxnNotFound) || searched != relationaldb.TxSearchUnknown {
				t.Fatalf("GetTransactionWithRange = (%+v, %v, %v), want txnNotFound and unknown search coverage", result, searched, err)
			}
		})
	}
}

func requireHeldTxOnly(t *testing.T, result *TransactionResult, err error, raw []byte) {
	t.Helper()
	if err != nil {
		t.Fatalf("held transaction lookup: %v", err)
	}
	if result == nil {
		t.Fatal("held transaction lookup returned nil")
	}
	if result.LedgerIndex != 0 || result.Validated || result.TxIndex != invalidTransactionIndex {
		t.Fatalf("held transaction advertised closed-ledger state: %+v", result)
	}
	txBlob, metaBlob, err := tx.SplitTxWithMetaBlob(result.TxData)
	if err != nil {
		t.Fatalf("split held transaction: %v", err)
	}
	if string(txBlob) != string(raw) || metaBlob != nil {
		t.Fatalf("held transaction payload = (%x, %x), want tx-only input", txBlob, metaBlob)
	}
}

func TestGetTransactionHeldPoolExpiresAfterRetention(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := jtx.MasterAccount()
	transaction := payment.Pay(master, jtx.NewAccount("held-expiration"), 50_000_000).Sequence(2).Build()
	env.SignWith(transaction, master)
	blob, err := tx.SerializeTransaction(transaction)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	hash, err := tx.ComputeTransactionHash(transaction)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}
	anchor := svc.GetOpenLedger().Sequence()
	outcome, err := svc.SubmitOpenLedgerTxDetailed(blob, true)
	if err != nil {
		t.Fatalf("SubmitOpenLedgerTxDetailed: %v", err)
	}
	if outcome.Result != ter.TerPRE_SEQ || outcome.Applied || outcome.Queued {
		t.Fatalf("future-sequence submission = %+v, want terPRE_SEQ held outside open ledger/TxQ", outcome)
	}
	if found, err := svc.OpenLedgerHasTx(hash); err != nil || found {
		t.Fatalf("future transaction open-ledger membership = (%t, %v), want absent", found, err)
	}
	for _, candidate := range svc.QueueAllTxs() {
		if candidate.TxID == hash {
			t.Fatal("future transaction entered TxQ")
		}
	}
	result, err := svc.GetTransaction(hash)
	requireHeldTxOnly(t, result, err, blob)
	ranged, searched, err := svc.GetTransactionWithRange(t.Context(), hash, 1, anchor+10)
	requireHeldTxOnly(t, ranged, err, blob)
	if searched != relationaldb.TxSearchAll {
		t.Fatalf("held range search = %v, want TxSearchAll", searched)
	}

	for attempts := 0; attempts < 10; attempts++ {
		closedSeq, err := svc.AcceptLedger(t.Context())
		if err != nil {
			t.Fatalf("AcceptLedger: %v", err)
		}
		result, lookupErr := svc.GetTransaction(hash)
		if closedSeq <= anchor+5 {
			requireHeldTxOnly(t, result, lookupErr, blob)
			continue
		}
		if result != nil || !errors.Is(lookupErr, svcerr.ErrTxnNotFound) {
			t.Fatalf("expired held transaction lookup = (%+v, %v), want txnNotFound", result, lookupErr)
		}
		result, searched, lookupErr = svc.GetTransactionWithRange(t.Context(), hash, 1, closedSeq)
		if result != nil || !errors.Is(lookupErr, svcerr.ErrTxnNotFound) || searched != relationaldb.TxSearchUnknown {
			t.Fatalf("expired held range lookup = (%+v, %v, %v), want txnNotFound and unknown search coverage", result, lookupErr, searched)
		}
		return
	}
	t.Fatal("held transaction did not expire within the retention window")
}

func TestGetTransactionOpenRawCanonicalValidatesHash(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	raw, hash := validRelationalTestTransaction(t, 1)
	var wrongHash [32]byte
	wrongHash[0] = 0xA5
	var malformedHash [32]byte
	malformedHash[0] = 0x5A
	svc.mu.Lock()
	var addErr error
	svc.openLedgerView.Modify(func(view *ledger.Ledger) bool {
		if addErr = view.AddTransaction(hash, raw); addErr != nil {
			return false
		}
		if addErr = view.AddTransaction(wrongHash, raw); addErr != nil {
			return false
		}
		addErr = view.AddTransaction(malformedHash, make([]byte, 32))
		return addErr == nil
	})
	svc.mu.Unlock()
	if addErr != nil {
		t.Fatalf("inject raw open leaves: %v", addErr)
	}

	result, err := svc.GetTransaction(hash)
	if err != nil {
		t.Fatalf("raw open transaction lookup: %v", err)
	}
	if result.LedgerIndex != 0 || result.Validated || result.TxIndex != invalidTransactionIndex {
		t.Fatalf("raw open transaction advertised closed-ledger state: %+v", result)
	}
	txBlob, metaBlob, err := tx.SplitTxWithMetaBlob(result.TxData)
	if err != nil {
		t.Fatalf("split raw open transaction: %v", err)
	}
	if string(txBlob) != string(raw) || metaBlob != nil {
		t.Fatalf("raw open transaction payload = (%x, %x), want tx-only input", txBlob, metaBlob)
	}

	for _, test := range []struct {
		name string
		hash [32]byte
	}{
		{name: "wrong hash", hash: wrongHash},
		{name: "malformed raw", hash: malformedHash},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.GetTransaction(test.hash)
			if !errors.Is(err, svcerr.ErrTxnDataCorrupt) {
				t.Fatalf("raw open transaction error = %v, want svcerr.ErrTxnDataCorrupt", err)
			}
		})
	}
}
