//go:build postgres

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

func postgresPublicationValue(seq uint32, ledgerByte, txByte, accountByte byte) relationaldb.ValidatedLedger {
	value := makePersistValue(seq)
	value.Ledger.Hash = relationaldb.Hash{ledgerByte}
	value.Ledger.ParentHash = relationaldb.Hash{ledgerByte - 1}
	value.Ledger.AccountHash = relationaldb.Hash{accountByte}
	value.Ledger.TransactionHash = relationaldb.Hash{txByte}
	value.Transactions[0] = postgresIndexedTransaction(value.Transactions[0], txByte, accountByte, 0)
	return value
}

func postgresIndexedTransaction(
	value relationaldb.IndexedTransaction,
	txByte, accountByte byte,
	sequence uint32,
) relationaldb.IndexedTransaction {
	value.Transaction.Hash = relationaldb.Hash{txByte}
	value.Transaction.TxnSeq = sequence
	value.Transaction.RawTxn = []byte{txByte, 1}
	value.Transaction.TxnMeta = []byte{2, txByte}
	account := relationaldb.AccountID{}
	account[0] = accountByte
	value.Accounts = []relationaldb.AccountID{account}
	return value
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

func TestPersistLedgerStreamReplayIsIdempotentAndConflictsOnDescriptorChange(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	value := postgresPublicationValue(90, 0x90, 0x91, 0x10)
	var commitID [32]byte
	commitID[0] = 1

	if err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(value.Transactions)); err != nil {
		t.Fatal(err)
	}
	var transactions, receipts int
	if err := rm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions").Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if err := rm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ledger_publication_receipts").Scan(&receipts); err != nil {
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

	conflictingLedger := value.Ledger
	conflictingLedger.Hash[0] ^= 0xff
	err := rm.PersistLedgerStream(ctx, conflictingLedger, commitID, "sql-publication", streamFor(value.Transactions))
	if !errors.Is(err, relationaldb.ErrCommitConflict) {
		t.Fatalf("descriptor conflict error = %v, want ErrCommitConflict", err)
	}

	changed := value.Transactions[0]
	changed.Transaction.RawTxn = append(append([]byte(nil), changed.Transaction.RawTxn...), 0xff)
	err = rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor([]relationaldb.IndexedTransaction{changed}))
	if !errors.Is(err, relationaldb.ErrCommitConflict) {
		t.Fatalf("semantic stream conflict error = %v, want ErrCommitConflict", err)
	}
	if err := rm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions").Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if transactions != 1 {
		t.Fatalf("replay transaction rows = %d, want 1", transactions)
	}
}

func TestPersistLedgerStreamReplayCanonicalizesHeaderTimes(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	value := postgresPublicationValue(97, 0x97, 0x98, 0x16)
	value.Ledger.CloseTime = time.Unix(1_700_000_097, 750_000_000).UTC()
	value.Ledger.ParentCloseTime = time.Unix(1_700_000_096, 250_000_000).UTC()
	var commitID [32]byte
	commitID[0] = 2

	if err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(value.Transactions)); err != nil {
		t.Fatal(err)
	}
	replayed := value.Ledger
	replayed.CloseTime = time.Unix(replayed.CloseTime.Unix(), 0).UTC()
	replayed.ParentCloseTime = time.Unix(replayed.ParentCloseTime.Unix(), 0).UTC()
	if err := rm.PersistLedgerStream(ctx, replayed, commitID, "sql-publication", streamFor(value.Transactions)); err != nil {
		t.Fatalf("canonical-time replay: %v", err)
	}
}

func TestPersistLedgerStreamRollbackAfterIndexedRow(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	value := postgresPublicationValue(91, 0x91, 0x92, 0x11)
	second := postgresIndexedTransaction(value.Transactions[0], 0x93, 0x21, 1)
	value.Ledger.TransactionHash[3] = 0x94
	value.Transactions = append(value.Transactions, second)
	var commitID [32]byte
	commitID[0] = 3

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
	for _, query := range []string{
		"SELECT COUNT(*) FROM ledgers",
		"SELECT COUNT(*) FROM transactions",
		"SELECT COUNT(*) FROM account_transactions",
		"SELECT COUNT(*) FROM ledger_publication_receipts",
		"SELECT COUNT(*) FROM xrpl_publication_stream_indexes",
	} {
		var count int
		if err := rm.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
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
	value := postgresPublicationValue(92, 0x92, 0x95, 0x12)
	second := postgresIndexedTransaction(value.Transactions[0], 0x96, 0x22, 1)
	value.Ledger.TransactionHash[3] = 0x97
	value.Transactions = append(value.Transactions, second)
	var commitID [32]byte
	commitID[0] = 4
	canonicalOrder := []relationaldb.IndexedTransaction{value.Transactions[1], value.Transactions[0]}
	if err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(canonicalOrder)); err != nil {
		t.Fatalf("canonical order: %v", err)
	}

	bad := postgresPublicationValue(93, 0x93, 0x98, 0x13)
	badSecond := postgresIndexedTransaction(bad.Transactions[0], 0x99, 0x23, 0)
	bad.Ledger.TransactionHash[3] = 0x9a
	bad.Transactions = append(bad.Transactions, badSecond)
	commitID[0] = 5
	err := rm.PersistLedgerStream(ctx, bad.Ledger, commitID, "sql-publication", streamFor(bad.Transactions))
	if !errors.Is(err, relationaldb.ErrInvalidData) {
		t.Fatalf("duplicate index error = %v, want ErrInvalidData", err)
	}
	if _, err := rm.Ledger().GetLedgerInfoBySeq(ctx, bad.Ledger.Sequence); !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("duplicate index published ledger: %v", err)
	}

	gapped := postgresPublicationValue(94, 0x94, 0x9b, 0x14)
	gappedSecond := postgresIndexedTransaction(gapped.Transactions[0], 0x9c, 0x24, 2)
	gapped.Ledger.TransactionHash[3] = 0x9d
	gapped.Transactions = append(gapped.Transactions, gappedSecond)
	commitID[0] = 6
	err = rm.PersistLedgerStream(ctx, gapped.Ledger, commitID, "sql-publication", streamFor(gapped.Transactions))
	if !errors.Is(err, relationaldb.ErrInvalidData) {
		t.Fatalf("noncontiguous index error = %v, want ErrInvalidData", err)
	}
	if _, err := rm.Ledger().GetLedgerInfoBySeq(ctx, gapped.Ledger.Sequence); !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("noncontiguous index published ledger: %v", err)
	}
}

func TestPersistLedgerStreamCancellationRollsBack(t *testing.T) {
	rm := setupTestDB(t)
	value := postgresPublicationValue(95, 0x95, 0x9e, 0x15)
	var commitID [32]byte
	commitID[0] = 7
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
	for _, query := range []string{
		"SELECT COUNT(*) FROM ledgers",
		"SELECT COUNT(*) FROM transactions",
		"SELECT COUNT(*) FROM account_transactions",
		"SELECT COUNT(*) FROM ledger_publication_receipts",
	} {
		var count int
		if err := rm.db.QueryRowContext(context.Background(), query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s after cancellation = %d, want 0", query, count)
		}
	}
}

func TestPublicationReceiptsPruneWithLedgers(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	value := postgresPublicationValue(96, 0x96, 0x9f, 0x16)
	var commitID [32]byte
	commitID[0] = 8
	if err := rm.PersistLedgerStream(ctx, value.Ledger, commitID, "sql-publication", streamFor(value.Transactions)); err != nil {
		t.Fatal(err)
	}
	if err := rm.Ledger().DeleteLedgersBySeq(ctx, value.Ledger.Sequence); err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err := rm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ledger_publication_receipts").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatalf("publication receipts after ledger prune = %d, want 0", receipts)
	}
}
