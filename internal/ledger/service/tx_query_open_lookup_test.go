package service

import (
	"encoding/hex"
	"errors"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
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

func TestGetTransactionRelayCacheRejectsCorruptData(t *testing.T) {
	svc := &Service{relayTxCache: make(map[[32]byte]relayTxRecord)}
	var hash [32]byte
	hash[0] = 0xC1
	svc.rememberRelayTransaction(hash, []byte{0x01, 0x02}, false)

	_, err := svc.GetTransaction(hash)
	if !errors.Is(err, svcerr.ErrTxnDataCorrupt) {
		t.Fatalf("corrupt relay-cache error = %v, want svcerr.ErrTxnDataCorrupt", err)
	}
}

func TestGetTransactionHeldPoolReturnsTxOnly(t *testing.T) {
	raw, hash := validRelationalTestTransaction(t, 1)
	svc := &Service{localTxs: localtxs.New()}
	svc.localTxs.PushBack(1, openledger.PendingTx{Hash: hash, Blob: raw})

	result, err := svc.GetTransaction(hash)
	if err != nil {
		t.Fatalf("held transaction lookup: %v", err)
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
