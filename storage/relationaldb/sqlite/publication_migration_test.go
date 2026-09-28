package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
)

func TestLedgerPublicationMigrationRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	createHistoricalSQLiteDatabases(t, dir, 5)
	raw, err := sql.Open("sqlite", filepath.Join(dir, "transaction.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db := sqlutil.NewDB(raw)
	orphan := makePersistValue(41)
	orphan.Transactions[0].Transaction.Hash[0] = 42
	if err := newTransactionRepository(db).SaveTransaction(ctx, orphan.Transactions[0].Transaction); err != nil {
		t.Fatal(err)
	}
	if err := newAccountTransactionRepository(db).SaveAccountTransaction(ctx, orphan.Transactions[0].Accounts[0], orphan.Transactions[0].Transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER fail_publication_migration BEFORE DELETE ON transactions
		BEGIN SELECT RAISE(ABORT, 'injected migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if rm, err := NewRepositoryManager(ctx, dir, Settings{}); err == nil {
		_ = rm.Close()
		t.Fatal("expected migration failure")
	}
	var version, tables, count int
	if err := raw.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ledgers'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if version != 5 || tables != 0 || count != 2 {
		t.Fatalf("failed migration changed database: version=%d header tables=%d transactions=%d", version, tables, count)
	}
	legacy, err := sql.Open("sqlite", filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if err := legacy.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatalf("legacy schema after interrupted upgrade: version=%d err=%v", version, err)
	}
	if err := legacy.QueryRowContext(ctx, "SELECT COUNT(*) FROM ledgers").Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy headers after interrupted upgrade: count=%d err=%v", count, err)
	}
	if _, err := raw.ExecContext(ctx, "DROP TRIGGER fail_publication_migration"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	rm, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rm.Close() })
	assertHistoricalSQLiteData(t, rm, 5)
	for _, db := range []*sql.DB{rm.ledgerDB.Raw(), rm.txDB.Raw()} {
		if err := migrate(ctx, db, ledgerMigrations[:5]); !errors.Is(err, relationaldb.ErrInvalidSchema) {
			t.Fatalf("older binary accepted migrated schema: %v", err)
		}
	}
	if info, err := rm.Ledger().GetLedgerInfoBySeq(ctx, 41); info != nil || !errors.Is(err, relationaldb.ErrLedgerNotFound) {
		t.Fatalf("orphan header: info=%v err=%v", info, err)
	}
	if tx, _, err := rm.Transaction().GetTransaction(ctx, orphan.Transactions[0].Transaction.Hash, nil); err != nil || tx != nil {
		t.Fatalf("orphan transaction: tx=%v err=%v", tx, err)
	}
	if err := rm.txDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_transactions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("account indexes after recovery: count=%d err=%v", count, err)
	}

	replacement := makePersistValue(40)
	if err := rm.PersistValidatedLedger(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err := rm.PersistValidatedLedger(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	if err := rm.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewRepositoryManager(ctx, dir, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertPersisted(t, reopened, replacement)
	assertPersisted(t, reopened, orphan)
	if info, err := reopened.Ledger().GetLedgerInfoBySeq(ctx, 40); err != nil || info.TotalCoins != replacement.Ledger.TotalCoins {
		t.Fatalf("reopening restored retired header: info=%v err=%v", info, err)
	}
	if err := reopened.txDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_transactions").Scan(&count); err != nil || count != 2 {
		t.Fatalf("account indexes after retry: count=%d err=%v", count, err)
	}
}
