package service

import (
	"context"
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
)

func TestGetTransactionWithRangeRelationalFallback(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repositories.Close() })

	cfg := DefaultConfig()
	cfg.RelationalDB = repositories
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	metaHex, err := binarycodec.Encode(map[string]any{
		"TransactionIndex":  uint32(7),
		"TransactionResult": "tesSUCCESS",
		"AffectedNodes":     []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	metaBlob, err := hex.DecodeString(metaHex)
	if err != nil {
		t.Fatal(err)
	}

	var ledgerHash relationaldb.Hash
	ledgerHash[0] = 0xA5
	if err := repositories.Ledger().SaveValidatedLedger(ctx, relationaldb.LedgerInfo{
		Hash:     ledgerHash,
		Sequence: 5,
	}); err != nil {
		t.Fatal(err)
	}

	for sequence := relationaldb.LedgerIndex(1); sequence <= 3; sequence++ {
		var hash relationaldb.Hash
		hash[0] = byte(sequence)
		if err := repositories.Transaction().SaveTransaction(ctx, relationaldb.TransactionInfo{
			Hash:      hash,
			LedgerSeq: sequence,
			Status:    "validated",
			RawTxn:    []byte{byte(sequence)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var foundHash relationaldb.Hash
	foundHash[0] = 0xF0
	rawTxn := []byte{0x12, 0x34}
	if err := repositories.Transaction().SaveTransaction(ctx, relationaldb.TransactionInfo{
		Hash:      foundHash,
		LedgerSeq: 5,
		Status:    "validated",
		RawTxn:    rawTxn,
		TxnMeta:   metaBlob,
	}); err != nil {
		t.Fatal(err)
	}

	result, searched, err := svc.GetTransactionWithRange(ctx, [32]byte(foundHash), 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if searched != relationaldb.TxSearchAll {
		t.Fatalf("found search result = %d, want TxSearchAll", searched)
	}
	if result.LedgerIndex != 5 || result.LedgerHash != [32]byte(ledgerHash) || !result.Validated || result.TxIndex != 7 {
		t.Fatalf("unexpected DB transaction result: %+v", result)
	}
	gotTxn, gotMeta, err := tx.SplitTxWithMetaBlob(result.TxData)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotTxn) != string(rawTxn) || string(gotMeta) != string(metaBlob) {
		t.Fatal("DB transaction and metadata were not reframed into the ledger result shape")
	}

	var missingHash [32]byte
	missingHash[0] = 0xEE
	_, searched, err = svc.GetTransactionWithRange(ctx, missingHash, 1, 3)
	if err == nil || searched != relationaldb.TxSearchAll {
		t.Fatalf("complete miss = (%v, %d), want error and TxSearchAll", err, searched)
	}
	_, searched, err = svc.GetTransactionWithRange(ctx, missingHash, 1, 4)
	if err == nil || searched != relationaldb.TxSearchSome {
		t.Fatalf("partial miss = (%v, %d), want error and TxSearchSome", err, searched)
	}
}

func TestTransactionSearchReturnsUnvalidatedHistoryBeforeDatabase(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlitedb.NewRepositoryManager(ctx, t.TempDir(), sqlitedb.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repositories.Close() })

	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.relationalDB = repositories
	svc.mu.Unlock()

	rawTxn, hash := validRelationalTestTransaction(t, 1)
	combined, err := tx.CreateTxWithMetaBlob(rawTxn, &tx.Metadata{
		TransactionResult: ter.TesSUCCESS,
		TransactionIndex:  7,
	})
	if err != nil {
		t.Fatal(err)
	}
	historyLedger := relationalTestLedger(t, hash, combined)
	svc.historyComponent.mu.Lock()
	svc.putHistoryLocked(historyLedger)
	svc.txIndex[hash] = historyLedger.Sequence()
	svc.historyComponent.mu.Unlock()

	ranged, searched, err := svc.GetTransactionWithRange(ctx, hash, 999, 1000)
	if err != nil {
		t.Fatalf("GetTransactionWithRange: %v", err)
	}
	if ranged == nil || ranged.LedgerIndex != historyLedger.Sequence() || ranged.Validated {
		t.Fatalf("range lookup = %+v, want unvalidated history result", ranged)
	}
	if searched != relationaldb.TxSearchAll {
		t.Fatalf("range search = %d, want TxSearchAll", searched)
	}

	if err := historyLedger.SetValidated(); err != nil {
		t.Fatalf("SetValidated: %v", err)
	}
	dbCombined, err := tx.CreateTxWithMetaBlob(rawTxn, &tx.Metadata{
		TransactionResult: ter.TesSUCCESS,
		TransactionIndex:  9,
	})
	if err != nil {
		t.Fatal(err)
	}
	dbRaw, dbMeta, err := tx.SplitTxWithMetaBlobStrict(dbCombined)
	if err != nil {
		t.Fatal(err)
	}
	ledgerHash := relationaldb.Hash(historyLedger.Hash())
	if err := repositories.Ledger().SaveValidatedLedger(ctx, relationaldb.LedgerInfo{
		Hash:     ledgerHash,
		Sequence: relationaldb.LedgerIndex(historyLedger.Sequence()),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Transaction().SaveTransaction(ctx, relationaldb.TransactionInfo{
		Hash:      relationaldb.Hash(hash),
		LedgerSeq: relationaldb.LedgerIndex(historyLedger.Sequence()),
		Status:    "validated",
		RawTxn:    dbRaw,
		TxnMeta:   dbMeta,
	}); err != nil {
		t.Fatal(err)
	}

	validatedRange, _, err := svc.GetTransactionWithRange(ctx, hash, 999, 1000)
	if err != nil {
		t.Fatalf("GetTransactionWithRange(validated): %v", err)
	}
	if validatedRange == nil || !validatedRange.Validated || validatedRange.TxIndex != 9 {
		t.Fatalf("validated range lookup = %+v, want DB result with index 9", validatedRange)
	}
}
