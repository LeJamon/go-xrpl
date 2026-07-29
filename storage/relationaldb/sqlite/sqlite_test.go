package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/contracttest"
	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *RepositoryManager {
	t.Helper()
	rm, err := NewRepositoryManager(context.Background(), t.TempDir(), Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rm.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return rm
}

func TestCurrentSchemaRejectsMalformedRows(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	ledgerDB := rm.ledgerDB.Raw()
	txDB := rm.txDB.Raw()

	statements := []struct {
		name string
		db   *sql.DB
		sql  string
	}{
		{
			name: "ledger hash",
			db:   ledgerDB,
			sql: `INSERT INTO ledgers (
				ledger_hash, ledger_seq, prev_hash, total_coins, closing_time,
				prev_closing_time, close_time_res, close_flags, account_set_hash, trans_set_hash
			) VALUES (x'01', 1, zeroblob(32), 1, 1, 1, 1, 0, zeroblob(32), zeroblob(32))`,
		},
		{
			name: "transaction ID",
			db:   txDB,
			sql:  `INSERT INTO transactions VALUES (x'01', 1, 'tesSUCCESS', x'01', NULL)`,
		},
		{
			name: "validation node key",
			db:   ledgerDB,
			sql: `INSERT INTO validations (
				ledger_seq, initial_seq, ledger_hash, node_pubkey,
				sign_time, seen_time, flags, raw
			) VALUES (1, 1, zeroblob(32), x'01', 1, 1, 0, x'01')`,
		},
		{
			name: "amendment ID",
			db:   ledgerDB,
			sql:  `INSERT INTO feature_votes VALUES ('aa', 'Alpha', 0)`,
		},
	}
	for _, test := range statements {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.db.ExecContext(ctx, test.sql); err == nil {
				t.Fatal("malformed row passed database constraint")
			}
		})
	}
}

func TestMalformedStoredRowsRejectedByScanners(t *testing.T) {
	t.Run("ledger hash", func(t *testing.T) {
		rm := setupTestDB(t)
		ctx := context.Background()
		db := rm.ledgerDB.Raw()
		if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO ledgers (
			ledger_hash, ledger_seq, prev_hash, total_coins, closing_time,
			prev_closing_time, close_time_res, close_flags, account_set_hash, trans_set_hash
		) VALUES (x'01', 1, zeroblob(32), 1, 1, 1, 1, 0, zeroblob(32), zeroblob(32))`); err != nil {
			t.Fatal(err)
		}
		if _, err := rm.Ledger().GetLedgerInfoBySeq(ctx, 1); !errors.Is(err, relationaldb.ErrInvalidData) {
			t.Fatalf("GetLedgerInfoBySeq() error = %v, want ErrInvalidData", err)
		}
	})

	t.Run("transaction ID", func(t *testing.T) {
		rm := setupTestDB(t)
		ctx := context.Background()
		db := rm.txDB.Raw()
		if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO transactions VALUES (x'01', 1, 'tesSUCCESS', x'01', NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := rm.Transaction().GetTxHistory(ctx, 0, 1); !errors.Is(err, relationaldb.ErrInvalidData) {
			t.Fatalf("GetTxHistory() error = %v, want ErrInvalidData", err)
		}
	})

	for _, test := range []struct {
		name       string
		ledgerHash string
		nodeKey    string
	}{
		{name: "validation ledger hash", ledgerHash: "x'01'", nodeKey: "zeroblob(33)"},
		{name: "validation node key", ledgerHash: "zeroblob(32)", nodeKey: "x'01'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rm := setupTestDB(t)
			ctx := context.Background()
			db := rm.ledgerDB.Raw()
			if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
				t.Fatal(err)
			}
			query := fmt.Sprintf(`INSERT INTO validations (
				ledger_seq, initial_seq, ledger_hash, node_pubkey,
				sign_time, seen_time, flags, raw
			) VALUES (1, 1, %s, %s, 1, 1, 0, x'01')`, test.ledgerHash, test.nodeKey)
			if _, err := db.ExecContext(ctx, query); err != nil {
				t.Fatal(err)
			}
			if _, err := rm.Validation().GetValidationsForLedger(ctx, 1); !errors.Is(err, relationaldb.ErrInvalidData) {
				t.Fatalf("GetValidationsForLedger() error = %v, want ErrInvalidData", err)
			}
		})
	}

	t.Run("amendment ID", func(t *testing.T) {
		rm := setupTestDB(t)
		ctx := context.Background()
		db := rm.ledgerDB.Raw()
		if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO feature_votes VALUES ('aa', 'Alpha', 0)`); err != nil {
			t.Fatal(err)
		}
		if _, err := rm.Amendment().LoadAmendmentVotes(ctx); !errors.Is(err, relationaldb.ErrInvalidData) {
			t.Fatalf("LoadAmendmentVotes() error = %v, want ErrInvalidData", err)
		}
	})
}

func TestRepositoryContract(t *testing.T) {
	contracttest.Run(t, func(t *testing.T) relationaldb.RepositoryManager {
		return setupTestDB(t)
	})
}

func makeLedgerInfo(seq uint32) relationaldb.LedgerInfo {
	var info relationaldb.LedgerInfo
	info.Hash[0] = byte(seq)
	info.ParentHash[0] = byte(seq - 1)
	info.AccountHash[1] = byte(seq)
	info.TransactionHash[2] = byte(seq)
	info.Sequence = relationaldb.LedgerIndex(seq)
	info.TotalCoins = 100_000_000
	info.CloseTime = time.Unix(1_700_000_000+int64(seq), 0).UTC()
	info.ParentCloseTime = info.CloseTime.Add(-time.Second)
	info.CloseTimeRes = 10
	return info
}

func makePersistValue(seq uint32) relationaldb.ValidatedLedger {
	var tx relationaldb.TransactionInfo
	tx.Hash[0] = byte(seq)
	tx.LedgerSeq = relationaldb.LedgerIndex(seq)
	tx.TxnSeq = 1
	tx.Status = "validated"
	tx.RawTxn = []byte{1, 2, 3}
	var account relationaldb.AccountID
	account[0] = 1
	return relationaldb.ValidatedLedger{
		Ledger: makeLedgerInfo(seq),
		Transactions: []relationaldb.IndexedTransaction{{
			Transaction: tx,
			Accounts:    []relationaldb.AccountID{account},
		}},
	}
}

func TestReadyLifecycleAndRetainedRepository(t *testing.T) {
	ctx := context.Background()
	rm := setupTestDB(t)
	repo := rm.Ledger()
	if err := repo.SaveValidatedLedger(ctx, makeLedgerInfo(1)); err != nil {
		t.Fatal(err)
	}
	if err := rm.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rm.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := repo.GetMaxLedgerSeq(ctx)
	if !errors.Is(err, relationaldb.ErrDatabaseClosed) {
		t.Fatalf("retained repository error = %v, want ErrDatabaseClosed", err)
	}
	_, err = rm.Transaction().GetTxHistory(ctx, 0, 1)
	if !errors.Is(err, relationaldb.ErrDatabaseClosed) {
		t.Fatalf("manager repository error = %v, want ErrDatabaseClosed", err)
	}
}

func TestWithTransactionCommitRollbackAndPanic(t *testing.T) {
	ctx := context.Background()
	rm := setupTestDB(t)
	value := makePersistValue(1).Transactions[0].Transaction
	if err := rm.WithTransaction(ctx, func(repos relationaldb.TransactionRepositories) error {
		return repos.Transaction().SaveTransaction(ctx, value)
	}); err != nil {
		t.Fatal(err)
	}
	if err := rm.WithTransaction(ctx, func(repos relationaldb.TransactionRepositories) error {
		tx := value
		tx.Hash[0] = 2
		if err := repos.Transaction().SaveTransaction(ctx, tx); err != nil {
			return err
		}
		return errors.New("rollback")
	}); err == nil {
		t.Fatal("expected rollback error")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		_ = rm.WithTransaction(ctx, func(repos relationaldb.TransactionRepositories) error {
			tx := value
			tx.Hash[0] = 3
			if err := repos.Transaction().SaveTransaction(ctx, tx); err != nil {
				t.Fatal(err)
			}
			panic("rollback")
		})
	}()
	for _, first := range []byte{2, 3} {
		var hash relationaldb.Hash
		hash[0] = first
		got, _, err := rm.Transaction().GetTransaction(ctx, hash, nil)
		if err != nil || got != nil {
			t.Fatalf("transaction %d survived rollback: got=%v err=%v", first, got, err)
		}
	}
}

func TestPersistValidatedLedgerFailureRecovery(t *testing.T) {
	ctx := context.Background()
	t.Run("mid-index", func(t *testing.T) {
		rm := setupTestDB(t)
		rm.persistHook = func(stage string, index int) error {
			if stage == "index" && index == 1 {
				return errors.New("injected")
			}
			return nil
		}
		value := makePersistValue(10)
		if err := rm.PersistValidatedLedger(ctx, value); err == nil {
			t.Fatal("expected injected error")
		}
		if info, err := rm.Ledger().GetLedgerInfoBySeq(ctx, value.Ledger.Sequence); info != nil || !errors.Is(err, relationaldb.ErrLedgerNotFound) {
			t.Fatalf("published partial ledger: info=%v err=%v", info, err)
		}
		rm.persistHook = nil
		if err := rm.PersistValidatedLedger(ctx, value); err != nil {
			t.Fatal(err)
		}
		assertPersisted(t, rm, value)
	})
	t.Run("after-ledger", func(t *testing.T) {
		rm := setupTestDB(t)
		rm.persistHook = func(stage string, _ int) error {
			if stage == "ledger" {
				return errors.New("injected")
			}
			return nil
		}
		value := makePersistValue(11)
		if err := rm.PersistValidatedLedger(ctx, value); err == nil {
			t.Fatal("expected injected error")
		}
		if info, err := rm.Ledger().GetLedgerInfoBySeq(ctx, value.Ledger.Sequence); info != nil || !errors.Is(err, relationaldb.ErrLedgerNotFound) {
			t.Fatalf("published partial ledger: info=%v err=%v", info, err)
		}
		rm.persistHook = nil
		if err := rm.PersistValidatedLedger(ctx, value); err != nil {
			t.Fatal(err)
		}
		assertPersisted(t, rm, value)
	})
}

func TestPersistValidatedLedgerReplacementFailureUnpublishesHeader(t *testing.T) {
	ctx := context.Background()
	rm := setupTestDB(t)
	neighbor := makePersistValue(19)
	if err := rm.PersistValidatedLedger(ctx, neighbor); err != nil {
		t.Fatal(err)
	}
	original := makePersistValue(20)
	original.Transactions[0].Accounts[0][0] = 3
	if err := rm.PersistValidatedLedger(ctx, original); err != nil {
		t.Fatal(err)
	}

	replacement := makePersistValue(20)
	replacement.Ledger.Hash[0] = 0xee
	replacement.Ledger.TotalCoins = 200_000_000
	replacement.Transactions[0].Transaction.Hash[0] = 0xdd
	replacement.Transactions[0].Transaction.RawTxn = []byte{4, 5, 6}
	replacement.Transactions[0].Accounts[0][0] = 2
	rm.persistHook = func(stage string, index int) error {
		if stage == "index" && index == 1 {
			return errors.New("injected")
		}
		return nil
	}
	if err := rm.PersistValidatedLedger(ctx, replacement); err == nil {
		t.Fatal("expected injected replacement error")
	}
	if info, err := rm.Ledger().GetLedgerInfoBySeq(ctx, replacement.Ledger.Sequence); info != nil || !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("replacement left old header published: info=%v err=%v", info, err)
	}
	if _, err := rm.Ledger().GetLedgerInfoBySeq(ctx, neighbor.Ledger.Sequence); err != nil {
		t.Fatalf("unpublished neighboring ledger: %v", err)
	}

	rm.persistHook = nil
	if err := rm.PersistValidatedLedger(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	gotLedger, err := rm.Ledger().GetLedgerInfoBySeq(ctx, replacement.Ledger.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if gotLedger.Hash != replacement.Ledger.Hash || gotLedger.TotalCoins != replacement.Ledger.TotalCoins {
		t.Fatalf("ledger = %+v, want replacement %+v", gotLedger, replacement.Ledger)
	}
	oldTx, _, err := rm.Transaction().GetTransaction(ctx, original.Transactions[0].Transaction.Hash, nil)
	if err != nil || oldTx != nil {
		t.Fatalf("old transaction remains after retry: tx=%v err=%v", oldTx, err)
	}
	assertPersisted(t, rm, replacement)
	oldPage, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: original.Transactions[0].Accounts[0],
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(oldPage.Transactions) != 0 {
		t.Fatalf("old account index remains after retry: %+v", oldPage.Transactions)
	}
}

func TestConcurrentSameSequencePersistenceRemainsConsistent(t *testing.T) {
	ctx := context.Background()
	rm := setupTestDB(t)
	first := makePersistValue(21)
	first.Ledger.Hash[0] = 0xa1
	first.Transactions[0].Transaction.Hash[0] = 0xa2
	first.Transactions[0].Accounts[0][0] = 0xa3
	second := makePersistValue(21)
	second.Ledger.Hash[0] = 0xb1
	second.Transactions[0].Transaction.Hash[0] = 0xb2
	second.Transactions[0].Accounts[0][0] = 0xb3

	blocked := make(chan struct{})
	release := make(chan struct{})
	var blockOnce sync.Once
	rm.persistHook = func(stage string, index int) error {
		if stage == "index" && index == 1 {
			blockOnce.Do(func() {
				close(blocked)
				<-release
			})
		}
		return nil
	}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- rm.PersistValidatedLedger(ctx, first)
	}()
	<-blocked
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- rm.PersistValidatedLedger(ctx, second)
	}()
	<-secondStarted
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	rm.persistHook = nil

	gotLedger, err := rm.Ledger().GetLedgerInfoBySeq(ctx, second.Ledger.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if gotLedger.Hash != second.Ledger.Hash {
		t.Fatalf("final ledger hash = %x, want %x", gotLedger.Hash, second.Ledger.Hash)
	}
	firstTx, _, err := rm.Transaction().GetTransaction(ctx, first.Transactions[0].Transaction.Hash, nil)
	if err != nil || firstTx != nil {
		t.Fatalf("first transaction remains: tx=%v err=%v", firstTx, err)
	}
	assertPersisted(t, rm, second)
	firstPage, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: first.Transactions[0].Accounts[0],
		Limit:   1,
	})
	if err != nil || len(firstPage.Transactions) != 0 {
		t.Fatalf("first account index remains: page=%v err=%v", firstPage, err)
	}
}

func TestCloseWaitsForLedgerPublication(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rm, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	value := makePersistValue(22)
	blocked := make(chan struct{})
	release := make(chan struct{})
	rm.persistHook = func(stage string, _ int) error {
		if stage == "ledger" {
			close(blocked)
			<-release
		}
		return nil
	}
	persistDone := make(chan error, 1)
	go func() {
		persistDone <- rm.PersistValidatedLedger(ctx, value)
	}()
	<-blocked
	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- rm.Close()
	}()
	<-closeStarted
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before publication completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-persistDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := rm.PersistValidatedLedger(ctx, value); !errors.Is(err, relationaldb.ErrDatabaseClosed) {
		t.Fatalf("post-close persistence error = %v, want ErrDatabaseClosed", err)
	}

	reopened, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertPersisted(t, reopened, value)
}

func assertPersisted(t *testing.T, rm *RepositoryManager, value relationaldb.ValidatedLedger) {
	t.Helper()
	ctx := context.Background()
	if _, err := rm.Ledger().GetLedgerInfoBySeq(ctx, value.Ledger.Sequence); err != nil {
		t.Fatal(err)
	}
	got, _, err := rm.Transaction().GetTransaction(ctx, value.Transactions[0].Transaction.Hash, nil)
	if err != nil || got == nil {
		t.Fatalf("transaction missing: got=%v err=%v", got, err)
	}
	page, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: value.Transactions[0].Accounts[0],
		Limit:   1,
	})
	if err != nil || len(page.Transactions) != 1 {
		t.Fatalf("account index missing: page=%v err=%v", page, err)
	}
}

func TestLegacySignatureSchemaMigrates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE validations (
		ledger_seq INTEGER NOT NULL, initial_seq INTEGER NOT NULL,
		ledger_hash BLOB NOT NULL, node_pubkey BLOB NOT NULL,
		signature BLOB NOT NULL, sign_time INTEGER NOT NULL,
		seen_time INTEGER NOT NULL, flags INTEGER NOT NULL, raw BLOB NOT NULL,
		PRIMARY KEY (ledger_hash, node_pubkey))`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rm, err := NewRepositoryManager(context.Background(), dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	rows, err := rm.ledgerDB.QueryContext(context.Background(), "PRAGMA table_info(validations)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "signature" {
			t.Fatal("legacy signature column was not removed")
		}
	}
}

func TestMalformedLegacyWidthAbortsMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE ledgers (
		ledger_hash BLOB PRIMARY KEY, ledger_seq INTEGER UNIQUE NOT NULL,
		prev_hash BLOB NOT NULL, total_coins INTEGER NOT NULL,
		closing_time INTEGER NOT NULL, prev_closing_time INTEGER NOT NULL,
		close_time_res INTEGER NOT NULL, close_flags INTEGER NOT NULL,
		account_set_hash BLOB NOT NULL, trans_set_hash BLOB NOT NULL);
		INSERT INTO ledgers VALUES (x'01', 1, zeroblob(32), 1, 1, 1, 1, 0, zeroblob(32), zeroblob(32))`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if rm, err := NewRepositoryManager(context.Background(), dir, Settings{}); err == nil {
		_ = rm.Close()
		t.Fatal("malformed legacy hash unexpectedly migrated")
	}
}

func TestFutureSchemaVersionRejected(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 5"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if rm, err := NewRepositoryManager(context.Background(), dir, Settings{}); !errors.Is(err, relationaldb.ErrInvalidSchema) {
		if rm != nil {
			_ = rm.Close()
		}
		t.Fatalf("future schema error = %v, want ErrInvalidSchema", err)
	}
}

func TestEveryRecordedSchemaVersionUpgrades(t *testing.T) {
	for version := 0; version <= len(ledgerMigrations); version++ {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			dir := t.TempDir()
			createHistoricalSQLiteDatabases(t, dir, version)
			upgraded, err := NewRepositoryManager(context.Background(), dir, Settings{})
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.Close()
			for _, db := range []executor{upgraded.ledgerDB, upgraded.txDB} {
				var got int
				if err := db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != len(ledgerMigrations) {
					t.Fatalf("schema version = %d, want %d", got, len(ledgerMigrations))
				}
			}
			if version == 0 {
				return
			}
			assertHistoricalSQLiteData(t, upgraded, version)
		})
	}
}

func createHistoricalSQLiteDatabases(t *testing.T, dir string, version int) {
	t.Helper()
	ctx := context.Background()
	ledgerDB, err := sql.Open("sqlite", filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	txDB, err := sql.Open("sqlite", filepath.Join(dir, "transaction.db"))
	if err != nil {
		_ = ledgerDB.Close()
		t.Fatal(err)
	}
	defer ledgerDB.Close()
	defer txDB.Close()
	if err := migrate(ctx, ledgerDB, ledgerMigrations[:version]); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, txDB, transactionMigrations[:version]); err != nil {
		t.Fatal(err)
	}
	if version == 0 {
		return
	}
	hash := make([]byte, 32)
	hash[0] = 40
	parent := make([]byte, 32)
	parent[0] = 39
	if _, err := ledgerDB.ExecContext(ctx, `INSERT INTO ledgers VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hash, 40, parent, 100, 10, 9, 1, 0, hash, parent); err != nil {
		t.Fatal(err)
	}
	txHash := make([]byte, 32)
	txHash[0] = 41
	if _, err := txDB.ExecContext(ctx, `INSERT INTO transactions VALUES (?, ?, ?, ?, ?)`,
		txHash, 40, "validated", []byte{1, 2}, []byte{3}); err != nil {
		t.Fatal(err)
	}
	account := relationaldb.AccountID{9}
	if _, err := txDB.ExecContext(ctx, `INSERT INTO account_transactions VALUES (?, ?, ?, ?)`,
		txHash, account.String(), 40, 1); err != nil {
		t.Fatal(err)
	}
	if version >= 2 {
		nodeKey := make([]byte, 33)
		nodeKey[0] = 2
		if version < 4 {
			_, err = ledgerDB.ExecContext(ctx, `INSERT INTO validations
				(ledger_seq, initial_seq, ledger_hash, node_pubkey, signature, sign_time, seen_time, flags, raw)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				40, 39, hash, nodeKey, []byte{4}, 10, 11, 1, []byte{5})
		} else {
			_, err = ledgerDB.ExecContext(ctx, `INSERT INTO validations
				(ledger_seq, initial_seq, ledger_hash, node_pubkey, sign_time, seen_time, flags, raw)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				40, 39, hash, nodeKey, 10, 11, 1, []byte{5})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if version >= 3 {
		if _, err := ledgerDB.ExecContext(ctx,
			`INSERT INTO feature_votes(amendment, name, vetoed) VALUES (?, ?, ?)`,
			strings.Repeat("A", 64), "Alpha", true); err != nil {
			t.Fatal(err)
		}
	}
}

func assertHistoricalSQLiteData(t *testing.T, rm *RepositoryManager, version int) {
	t.Helper()
	ctx := context.Background()
	info, err := rm.Ledger().GetLedgerInfoBySeq(ctx, 40)
	if err != nil || info.TotalCoins != 100 {
		t.Fatalf("ledger not preserved: info=%v err=%v", info, err)
	}
	var txHash relationaldb.Hash
	txHash[0] = 41
	transaction, _, err := rm.Transaction().GetTransaction(ctx, txHash, nil)
	if err != nil || transaction == nil || string(transaction.RawTxn) != string([]byte{1, 2}) {
		t.Fatalf("transaction not preserved: tx=%v err=%v", transaction, err)
	}
	account := relationaldb.AccountID{9}
	page, err := rm.AccountTransaction().GetOldestAccountTxsPage(ctx, relationaldb.AccountTxPageOptions{
		Account: account,
		Limit:   1,
	})
	if err != nil || len(page.Transactions) != 1 {
		t.Fatalf("account index not preserved: page=%v err=%v", page, err)
	}
	if version >= 2 {
		validations, err := rm.Validation().GetValidationsForLedger(ctx, 40)
		if err != nil || len(validations) != 1 || string(validations[0].Raw) != string([]byte{5}) {
			t.Fatalf("validation not preserved: validations=%v err=%v", validations, err)
		}
		var signatureColumns int
		if err := rm.ledgerDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM pragma_table_info('validations') WHERE name = 'signature'
		`).Scan(&signatureColumns); err != nil {
			t.Fatal(err)
		}
		if signatureColumns != 0 {
			t.Fatal("legacy signature column remains")
		}
	}
	if version >= 3 {
		votes, err := rm.Amendment().LoadAmendmentVotes(ctx)
		if err != nil || len(votes) != 1 || votes[0].Amendment != strings.Repeat("A", 64) || !votes[0].Vetoed {
			t.Fatalf("amendment vote not preserved: votes=%v err=%v", votes, err)
		}
	}
}
