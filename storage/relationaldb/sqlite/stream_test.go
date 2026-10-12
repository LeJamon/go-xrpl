package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

func TestPersistLedgerStreamReplayIsIdempotentAndConflictsOnDescriptorChange(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	value := publicationValue(90, 0x90, 0x91, publicationAccount(0x10, 0x20))
	var commitID [32]byte
	commitID[0] = 1

	if err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(value.Transactions)); err != nil {
		t.Fatal(err)
	}
	var transactions, receipts int
	if err := rm.txDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions").Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if err := rm.txDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM ledger_publication_receipts").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if transactions != 1 || receipts != 1 {
		t.Fatalf("initial publication rows = transactions %d receipts %d, want 1 and 1", transactions, receipts)
	}

	rm.persistHook = func(string, int) error {
		return errors.New("duplicate replay wrote rows")
	}
	if err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(value.Transactions)); err != nil {
		t.Fatalf("duplicate replay: %v", err)
	}
	rm.persistHook = nil
	if err := rm.txDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions").Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if transactions != 1 {
		t.Fatalf("duplicate replay transaction rows = %d, want 1", transactions)
	}

	conflictingLedger := value.Ledger
	conflictingLedger.Hash[0] ^= 0xff
	err := rm.PersistLedgerStream(ctx, conflictingLedger, commitID, "sql-publication", streamFor(value.Transactions))
	if !errors.Is(err, relationaldb.ErrCommitConflict) {
		t.Fatalf("descriptor conflict error = %v, want ErrCommitConflict", err)
	}
}

func TestPersistLedgerStreamRollbackAfterIndexedRow(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	value := publicationValue(91, 0x91, 0x92, publicationAccount(0x11, 0x21))
	second := value.Transactions[0]
	second.Transaction.Hash[0] = 0x93
	second.Transaction.TxnSeq = 1
	second.Transaction.RawTxn = []byte{0x93, 1}
	second.Transaction.TxnMeta = []byte{2, 0x93}
	value.Ledger.TransactionHash[3] = 0x94
	value.Transactions = append(value.Transactions, second)
	var commitID [32]byte
	commitID[0] = 2

	rm.persistHook = func(stage string, index int) error {
		if stage == "index" && index == 1 {
			return errors.New("injected stream failure")
		}
		return nil
	}
	err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(value.Transactions))
	if err == nil || !strings.Contains(err.Error(), "injected stream failure") {
		t.Fatalf("failed stream error = %v, want injected error", err)
	}
	rm.persistHook = nil
	var count int
	for _, query := range []string{
		"SELECT COUNT(*) FROM ledgers",
		"SELECT COUNT(*) FROM transactions",
		"SELECT COUNT(*) FROM account_transactions",
		"SELECT COUNT(*) FROM ledger_publication_receipts",
	} {
		if err := rm.txDB.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s after rollback = %d, want 0", query, count)
		}
	}

	if err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(value.Transactions)); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestPersistLedgerStreamAcceptsCanonicalOrderAndRejectsDuplicateIndexes(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	value := publicationValue(92, 0x92, 0x95, publicationAccount(0x12, 0x22))
	second := value.Transactions[0]
	second.Transaction.Hash[0] = 0x96
	second.Transaction.TxnSeq = 1
	second.Transaction.RawTxn = []byte{0x96, 1}
	second.Transaction.TxnMeta = []byte{2, 0x96}
	value.Ledger.TransactionHash[3] = 0x97
	value.Transactions = append(value.Transactions, second)
	var commitID [32]byte
	commitID[0] = 3
	canonicalOrder := []relationaldb.IndexedTransaction{value.Transactions[1], value.Transactions[0]}
	if err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(canonicalOrder)); err != nil {
		t.Fatalf("canonical order: %v", err)
	}

	bad := publicationValue(93, 0x93, 0x98, publicationAccount(0x13, 0x23))
	badSecond := bad.Transactions[0]
	badSecond.Transaction.Hash[0] = 0x99
	badSecond.Transaction.TxnSeq = 0
	badSecond.Transaction.RawTxn = []byte{0x99, 1}
	badSecond.Transaction.TxnMeta = []byte{2, 0x99}
	bad.Ledger.TransactionHash[3] = 0x9a
	bad.Transactions = append(bad.Transactions, badSecond)
	commitID[0] = 4
	err := rm.PersistLedgerStream(ctx, bad.Ledger, commitID, "sql-publication", streamFor(bad.Transactions))
	if !errors.Is(err, relationaldb.ErrInvalidData) {
		t.Fatalf("duplicate index error = %v, want ErrInvalidData", err)
	}
	if _, err := rm.Ledger().GetLedgerInfoBySeq(ctx, bad.Ledger.Sequence); !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("duplicate index published ledger: %v", err)
	}
}

func TestPersistLedgerStreamCancellationRollsBack(t *testing.T) {
	rm := setupTestDB(t)
	value := publicationValue(94, 0x94, 0x9b, publicationAccount(0x14, 0x24))
	var commitID [32]byte
	commitID[0] = 5
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", func(context.Context) (relationaldb.IndexedTransaction, bool, error) {
		if called {
			return relationaldb.IndexedTransaction{}, false, nil
		}
		called = true
		cancel()
		return value.Transactions[0], true, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stream error = %v, want context.Canceled", err)
	}
	var count int
	if err := rm.txDB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("canceled stream transaction rows = %d, want 0", count)
	}
}

func TestPersistLedgerStreamGeneratesLargeInputWithoutSlicePublication(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	const total = 1024
	ledger := makeLedgerInfo(95)
	ledger.TransactionHash[0] = 0xa1
	var commitID [32]byte
	commitID[0] = 6
	nextIndex := 0
	next := func(context.Context) (relationaldb.IndexedTransaction, bool, error) {
		if nextIndex == total {
			return relationaldb.IndexedTransaction{}, false, nil
		}
		index := nextIndex
		nextIndex++
		var tx relationaldb.TransactionInfo
		tx.Hash[0] = byte(index >> 8)
		tx.Hash[1] = byte(index)
		tx.Hash[2] = 0xd1
		tx.LedgerSeq = ledger.Sequence
		tx.TxnSeq = uint32(index)
		tx.Status = "validated"
		tx.RawTxn = []byte{byte(index), 0x01}
		tx.TxnMeta = []byte{0x02, byte(index)}
		return relationaldb.IndexedTransaction{Transaction: tx}, true, nil
	}
	if err := rm.PersistLedgerStream(ctx, ledger, commitID, "sql-publication", next); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := rm.txDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions WHERE ledger_seq = ?", ledger.Sequence).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != total {
		t.Fatalf("large stream transaction rows = %d, want %d", count, total)
	}
}

func streamFor(values []relationaldb.IndexedTransaction) relationaldb.IndexedTransactionStream {
	index := 0
	return func(context.Context) (relationaldb.IndexedTransaction, bool, error) {
		if index == len(values) {
			return relationaldb.IndexedTransaction{}, false, nil
		}
		value := values[index]
		index++
		return value, true, nil
	}
}
