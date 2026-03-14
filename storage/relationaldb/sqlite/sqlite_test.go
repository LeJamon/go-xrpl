package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// Compile-time interface checks
var (
	_ relationaldb.RepositoryManager            = (*RepositoryManager)(nil)
	_ relationaldb.LedgerRepository             = (*LedgerRepository)(nil)
	_ relationaldb.TransactionRepository        = (*TransactionRepository)(nil)
	_ relationaldb.AccountTransactionRepository = (*AccountTransactionRepository)(nil)
	_ relationaldb.SystemRepository             = (*SystemRepository)(nil)
	_ relationaldb.TransactionContext           = (*TransactionContext)(nil)
)

func setupTestDB(t *testing.T) *RepositoryManager {
	t.Helper()
	dir := t.TempDir()
	rm, err := NewRepositoryManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rm.Close(context.Background()) })
	return rm
}

func makeLedgerInfo(seq uint32) *relationaldb.LedgerInfo {
	var hash, parentHash, accountHash, txHash relationaldb.Hash
	hash[0] = byte(seq)
	parentHash[0] = byte(seq - 1)
	accountHash[1] = byte(seq)
	txHash[2] = byte(seq)
	return &relationaldb.LedgerInfo{
		Hash:            hash,
		Sequence:        relationaldb.LedgerIndex(seq),
		ParentHash:      parentHash,
		AccountHash:     accountHash,
		TransactionHash: txHash,
		TotalCoins:      100000000000,
		CloseTime:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		ParentCloseTime: time.Date(2024, 12, 31, 23, 59, 56, 0, time.UTC),
		CloseTimeRes:    10,
		CloseFlags:      0,
	}
}

func TestOpenClose(t *testing.T) {
	dir := t.TempDir()
	rm, err := NewRepositoryManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rm.System().Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rm.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerCRUD(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	// Empty state
	minSeq, err := rm.Ledger().GetMinLedgerSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if minSeq != nil {
		t.Fatal("expected nil min seq on empty DB")
	}

	// Save ledger
	info := makeLedgerInfo(10)
	if err := rm.Ledger().SaveValidatedLedger(ctx, info, true); err != nil {
		t.Fatal(err)
	}

	// Read back by seq
	got, err := rm.Ledger().GetLedgerInfoBySeq(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sequence != 10 {
		t.Fatalf("expected seq 10, got %d", got.Sequence)
	}
	if got.TotalCoins != 100000000000 {
		t.Fatalf("expected total_coins 100000000000, got %d", got.TotalCoins)
	}
	if got.Hash != info.Hash {
		t.Fatalf("hash mismatch")
	}

	// Read by hash
	got2, err := rm.Ledger().GetLedgerInfoByHash(ctx, info.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Sequence != 10 {
		t.Fatalf("expected seq 10 from hash lookup, got %d", got2.Sequence)
	}

	// Newest
	newest, err := rm.Ledger().GetNewestLedgerInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newest.Sequence != 10 {
		t.Fatalf("expected newest seq 10, got %d", newest.Sequence)
	}

	// Min/Max
	minSeq, err = rm.Ledger().GetMinLedgerSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if *minSeq != 10 {
		t.Fatalf("expected min 10, got %d", *minSeq)
	}

	maxSeq, err := rm.Ledger().GetMaxLedgerSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if *maxSeq != 10 {
		t.Fatalf("expected max 10, got %d", *maxSeq)
	}

	// CountMinMax
	stats, err := rm.Ledger().GetLedgerCountMinMax(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 1 {
		t.Fatalf("expected count 1, got %d", stats.Count)
	}

	// Upsert: save again with updated total
	info.TotalCoins = 200000000000
	if err := rm.Ledger().SaveValidatedLedger(ctx, info, true); err != nil {
		t.Fatal(err)
	}
	got3, err := rm.Ledger().GetLedgerInfoBySeq(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got3.TotalCoins != 200000000000 {
		t.Fatalf("expected upserted total_coins 200000000000, got %d", got3.TotalCoins)
	}
}

func TestLedgerHashQueries(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	for i := uint32(1); i <= 5; i++ {
		if err := rm.Ledger().SaveValidatedLedger(ctx, makeLedgerInfo(i), false); err != nil {
			t.Fatal(err)
		}
	}

	hash, err := rm.Ledger().GetHashByIndex(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if hash[0] != 3 {
		t.Fatalf("expected hash[0]=3, got %d", hash[0])
	}

	pair, err := rm.Ledger().GetHashesByIndex(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if pair.LedgerHash[0] != 3 || pair.ParentHash[0] != 2 {
		t.Fatalf("unexpected hash pair")
	}

	rangeResult, err := rm.Ledger().GetHashesByRange(ctx, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(rangeResult) != 3 {
		t.Fatalf("expected 3 results, got %d", len(rangeResult))
	}
}

func TestLedgerDelete(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	for i := uint32(1); i <= 5; i++ {
		if err := rm.Ledger().SaveValidatedLedger(ctx, makeLedgerInfo(i), false); err != nil {
			t.Fatal(err)
		}
	}

	if err := rm.Ledger().DeleteLedgersBySeq(ctx, 3); err != nil {
		t.Fatal(err)
	}
	stats, err := rm.Ledger().GetLedgerCountMinMax(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 2 {
		t.Fatalf("expected 2 remaining, got %d", stats.Count)
	}
}

func TestLedgerLimited(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	for i := uint32(5); i <= 10; i++ {
		if err := rm.Ledger().SaveValidatedLedger(ctx, makeLedgerInfo(i), false); err != nil {
			t.Fatal(err)
		}
	}

	oldest, err := rm.Ledger().GetLimitedOldestLedgerInfo(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if oldest.Sequence != 7 {
		t.Fatalf("expected 7, got %d", oldest.Sequence)
	}

	newest, err := rm.Ledger().GetLimitedNewestLedgerInfo(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if newest.Sequence != 10 {
		t.Fatalf("expected 10, got %d", newest.Sequence)
	}
}

func TestTransactionCRUD(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	txInfo := &relationaldb.TransactionInfo{
		LedgerSeq: 10,
		Status:    "validated",
		RawTxn:    []byte("raw-data"),
		TxnMeta:   []byte("meta-data"),
	}
	txInfo.Hash[0] = 0xAB

	if err := rm.Transaction().SaveTransaction(ctx, txInfo); err != nil {
		t.Fatal(err)
	}

	count, err := rm.Transaction().GetTransactionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}

	got, searchResult, err := rm.Transaction().GetTransaction(ctx, txInfo.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if searchResult != relationaldb.TxSearchAll {
		t.Fatalf("expected TxSearchAll, got %d", searchResult)
	}
	if got.LedgerSeq != 10 {
		t.Fatalf("expected ledger_seq 10, got %d", got.LedgerSeq)
	}
	if string(got.RawTxn) != "raw-data" {
		t.Fatalf("raw_txn mismatch")
	}
	if string(got.TxnMeta) != "meta-data" {
		t.Fatalf("txn_meta mismatch")
	}

	// Not found
	var missingHash relationaldb.Hash
	missingHash[0] = 0xFF
	_, sr, err := rm.Transaction().GetTransaction(ctx, missingHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sr != relationaldb.TxSearchUnknown {
		t.Fatalf("expected TxSearchUnknown, got %d", sr)
	}
}

func TestTransactionHistory(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	for i := uint32(1); i <= 5; i++ {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: relationaldb.LedgerIndex(i),
			Status:    "validated",
			RawTxn:    []byte("data"),
		}
		tx.Hash[0] = byte(i)
		if err := rm.Transaction().SaveTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}

	history, err := rm.Transaction().GetTxHistory(ctx, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 results, got %d", len(history))
	}
	// Should be descending order
	if history[0].LedgerSeq != 5 || history[2].LedgerSeq != 3 {
		t.Fatal("unexpected order")
	}
}

func TestTransactionDelete(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	for i := uint32(1); i <= 5; i++ {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: relationaldb.LedgerIndex(i),
			Status:    "validated",
			RawTxn:    []byte("data"),
		}
		tx.Hash[0] = byte(i)
		if err := rm.Transaction().SaveTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}

	if err := rm.Transaction().DeleteTransactionsBeforeLedgerSeq(ctx, 3); err != nil {
		t.Fatal(err)
	}
	count, err := rm.Transaction().GetTransactionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 remaining, got %d", count)
	}
}

func TestAccountTransactionCRUD(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	var accountID relationaldb.AccountID
	accountID[0] = 0x01

	txInfo := &relationaldb.TransactionInfo{
		LedgerSeq: 10,
		TxnSeq:    1,
		Status:    "validated",
		RawTxn:    []byte("raw"),
	}
	txInfo.Hash[0] = 0xAA

	// Save the transaction first (needed for JOIN)
	if err := rm.Transaction().SaveTransaction(ctx, txInfo); err != nil {
		t.Fatal(err)
	}

	if err := rm.AccountTransaction().SaveAccountTransaction(ctx, accountID, txInfo); err != nil {
		t.Fatal(err)
	}

	count, err := rm.AccountTransaction().GetAccountTransactionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}

	// Query oldest
	results, err := rm.AccountTransaction().GetOldestAccountTxs(ctx, relationaldb.AccountTxOptions{
		Account: accountID,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].LedgerSeq != 10 {
		t.Fatalf("expected ledger_seq 10, got %d", results[0].LedgerSeq)
	}
}

func TestAccountTransactionPagination(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	var accountID relationaldb.AccountID
	accountID[0] = 0x01

	// Save 5 transactions
	for i := uint32(1); i <= 5; i++ {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: relationaldb.LedgerIndex(i),
			TxnSeq:    i,
			Status:    "validated",
			RawTxn:    []byte("raw"),
		}
		tx.Hash[0] = byte(i)
		if err := rm.Transaction().SaveTransaction(ctx, tx); err != nil {
			t.Fatal(err)
		}
		if err := rm.AccountTransaction().SaveAccountTransaction(ctx, accountID, tx); err != nil {
			t.Fatal(err)
		}
	}

	// First page
	page1, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: accountID,
		Limit:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1.Transactions) != 2 {
		t.Fatalf("expected 2 txs, got %d", len(page1.Transactions))
	}
	if page1.Marker == nil {
		t.Fatal("expected marker for more results")
	}

	// Second page
	page2, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: accountID,
		Limit:   2,
		Marker:  page1.Marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Transactions) != 2 {
		t.Fatalf("expected 2 txs, got %d", len(page2.Transactions))
	}

	// Third page (last)
	page3, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: accountID,
		Limit:   2,
		Marker:  page2.Marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Transactions) != 1 {
		t.Fatalf("expected 1 tx, got %d", len(page3.Transactions))
	}
	if page3.Marker != nil {
		t.Fatal("expected no marker on last page")
	}
}

func TestWithTransaction(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	// Commit path
	err := rm.WithTransaction(ctx, func(tc relationaldb.TransactionContext) error {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: 1,
			Status:    "validated",
			RawTxn:    []byte("data"),
		}
		tx.Hash[0] = 0x01
		return tc.Transaction().SaveTransaction(ctx, tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	count, _ := rm.Transaction().GetTransactionCount(ctx)
	if count != 1 {
		t.Fatalf("expected 1 after commit, got %d", count)
	}

	// Rollback path
	err = rm.WithTransaction(ctx, func(tc relationaldb.TransactionContext) error {
		tx := &relationaldb.TransactionInfo{
			LedgerSeq: 2,
			Status:    "validated",
			RawTxn:    []byte("data"),
		}
		tx.Hash[0] = 0x02
		if err := tc.Transaction().SaveTransaction(ctx, tx); err != nil {
			return err
		}
		return context.Canceled // force rollback
	})
	if err == nil {
		t.Fatal("expected error")
	}
	count, _ = rm.Transaction().GetTransactionCount(ctx)
	if count != 1 {
		t.Fatalf("expected 1 after rollback, got %d", count)
	}
}

func TestSystemSize(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()

	kb, err := rm.System().GetKBUsedAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Empty DB should have some minimum size from schema
	if kb == 0 {
		t.Fatal("expected non-zero size")
	}
}

func TestNewRepositoryManagerEmptyDir(t *testing.T) {
	_, err := NewRepositoryManager("")
	if err == nil {
		t.Fatal("expected error for empty dir")
	}
}
