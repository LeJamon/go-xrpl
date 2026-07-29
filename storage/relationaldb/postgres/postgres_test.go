//go:build postgres

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/contracttest"
	"github.com/lib/pq"
)

const postgresDSNEnv = "XRPLD_TEST_POSTGRES_DSN"

func setupTestDB(t *testing.T) *RepositoryManager {
	t.Helper()
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", postgresDSNEnv)
	}
	rm, err := NewRepositoryManager(context.Background(), relationaldb.NewConfig().WithConnectionString(dsn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rm.db.ExecContext(context.Background(),
		`TRUNCATE account_transactions, transactions, ledgers, validations, feature_votes`); err != nil {
		_ = rm.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rm.Close(); err != nil {
			t.Fatal(err)
		}
	})
	return rm
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
}

func TestWithTransactionCommitRollbackAndPanic(t *testing.T) {
	ctx := context.Background()
	rm := setupTestDB(t)
	value := makePersistValue(1).Transactions[0].Transaction
	var retained relationaldb.TransactionRepository
	if err := rm.WithTransaction(ctx, func(repos relationaldb.TransactionRepositories) error {
		retained = repos.Transaction()
		return retained.SaveTransaction(ctx, value)
	}); err != nil {
		t.Fatal(err)
	}
	if err := retained.SaveTransaction(ctx, value); !errors.Is(err, relationaldb.ErrTransactionClosed) {
		t.Fatalf("retained transaction repository error = %v", err)
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
	for _, tc := range []struct {
		name  string
		stage string
	}{
		{name: "mid-index", stage: "index"},
		{name: "after-ledger", stage: "ledger"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := setupTestDB(t)
			rm.persistHook = func(stage string, _ int) error {
				if stage == tc.stage {
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
	}
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
	rm := setupTestDB(t)
	ctx := context.Background()
	if _, err := rm.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 4`); err != nil {
		t.Fatal(err)
	}
	if _, err := rm.db.ExecContext(ctx, `ALTER TABLE validations ADD COLUMN signature BYTEA NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, rm.db.Raw()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := rm.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'validations' AND column_name = 'signature'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("legacy signature column was not removed")
	}
}

func TestExactWidthConstraints(t *testing.T) {
	rm := setupTestDB(t)
	ctx := context.Background()
	for _, test := range []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "ledger hash",
			sql: `INSERT INTO ledgers (
				ledger_hash, ledger_seq, prev_hash, total_coins, closing_time,
				prev_closing_time, close_time_res, close_flags, account_set_hash, trans_set_hash
			) VALUES ($1, 1, $2, 1, 1, 1, 1, 0, $2, $2)`,
			args: []any{[]byte{1}, make([]byte, 32)},
		},
		{
			name: "transaction ID",
			sql:  `INSERT INTO transactions VALUES ($1, 1, 'tesSUCCESS', $2, NULL)`,
			args: []any{[]byte{1}, []byte{1}},
		},
		{
			name: "validation node key",
			sql: `INSERT INTO validations (
				ledger_seq, initial_seq, ledger_hash, node_pubkey,
				sign_time, seen_time, flags, raw
			) VALUES (1, 1, $1, $2, 1, 1, 0, $3)`,
			args: []any{make([]byte, 32), []byte{1}, []byte{1}},
		},
		{
			name: "amendment ID",
			sql:  `INSERT INTO feature_votes VALUES ('aa', 'Alpha', FALSE)`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := rm.db.ExecContext(ctx, test.sql, test.args...); err == nil {
				t.Fatal("malformed row passed database constraint")
			}
		})
	}
}

func TestNilConfigurationRejected(t *testing.T) {
	if _, err := NewRepositoryManager(context.Background(), nil); err == nil {
		t.Fatal("nil configuration accepted")
	}
}

func TestCloseWaitsForPersistValidatedLedger(t *testing.T) {
	ctx := context.Background()
	rm := setupTestDB(t)
	value := makePersistValue(30)
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
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- rm.Close()
	}()
	contracttest.WaitForTransactionRejection(t, rm)
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before persistence completed: %v", err)
	default:
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

	reopened, err := NewRepositoryManager(ctx, relationaldb.NewConfig().WithConnectionString(os.Getenv(postgresDSNEnv)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertPersisted(t, reopened, value)
}

func restoreMigrationHistory(t *testing.T, rm *RepositoryManager) {
	t.Helper()
	if _, err := rm.db.ExecContext(context.Background(), `DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= len(postgresMigrations); version++ {
		if _, err := rm.db.ExecContext(context.Background(),
			`INSERT INTO schema_migrations(version) VALUES ($1)`, version); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFutureSchemaVersionRejected(t *testing.T) {
	rm := setupTestDB(t)
	defer restoreMigrationHistory(t, rm)
	future := len(postgresMigrations) + 1
	if _, err := rm.db.ExecContext(context.Background(),
		`INSERT INTO schema_migrations(version) VALUES ($1)`, future); err != nil {
		t.Fatal(err)
	}
	other, err := NewRepositoryManager(
		context.Background(),
		relationaldb.NewConfig().WithConnectionString(os.Getenv(postgresDSNEnv)),
	)
	if other != nil {
		_ = other.Close()
	}
	if !errors.Is(err, relationaldb.ErrInvalidSchema) {
		t.Fatalf("future schema error = %v, want ErrInvalidSchema", err)
	}
}

func TestGappedSchemaHistoryRejected(t *testing.T) {
	rm := setupTestDB(t)
	defer restoreMigrationHistory(t, rm)
	if _, err := rm.db.ExecContext(context.Background(), `DELETE FROM schema_migrations WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	other, err := NewRepositoryManager(
		context.Background(),
		relationaldb.NewConfig().WithConnectionString(os.Getenv(postgresDSNEnv)),
	)
	if other != nil {
		_ = other.Close()
	}
	if !errors.Is(err, relationaldb.ErrInvalidSchema) {
		t.Fatalf("gapped schema error = %v, want ErrInvalidSchema", err)
	}
}

func TestEveryRecordedSchemaVersionUpgrades(t *testing.T) {
	admin := setupTestDB(t)
	for version := 0; version <= len(postgresMigrations); version++ {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			schema := fmt.Sprintf("relational_migration_v%d", version)
			schemaID := pq.QuoteIdentifier(schema)
			if _, err := admin.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schemaID+` CASCADE`); err != nil {
				t.Fatal(err)
			}
			if _, err := admin.db.ExecContext(context.Background(), `CREATE SCHEMA `+schemaID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = admin.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schemaID+` CASCADE`)
			})
			dsn := postgresDSNWithSchema(t, os.Getenv(postgresDSNEnv), schema)
			createHistoricalPostgresDatabase(t, dsn, version)
			upgraded, err := NewRepositoryManager(
				context.Background(),
				relationaldb.NewConfig().WithConnectionString(dsn),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.Close()
			var count int
			if err := upgraded.db.QueryRowContext(context.Background(),
				`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != len(postgresMigrations) {
				t.Fatalf("migration count = %d, want %d", count, len(postgresMigrations))
			}
			if version > 0 {
				assertHistoricalPostgresData(t, upgraded, version)
			}
		})
	}
}

func postgresDSNWithSchema(t *testing.T, dsn, schema string) string {
	t.Helper()
	if !strings.Contains(dsn, "://") {
		return dsn + " search_path=" + schema
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func createHistoricalPostgresDatabase(t *testing.T, dsn string, version int) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateTo(ctx, db, postgresMigrations[:version]); err != nil {
		t.Fatal(err)
	}
	if version == 0 {
		return
	}
	hash := make([]byte, 32)
	hash[0] = 40
	parent := make([]byte, 32)
	parent[0] = 39
	if _, err := db.ExecContext(ctx, `INSERT INTO ledgers (
			ledger_hash, ledger_seq, prev_hash, total_coins, closing_time,
			prev_closing_time, close_time_res, close_flags, account_set_hash, trans_set_hash
		) VALUES ($1, 40, $2, 100, 10, 9, 1, 0, $1, $2)`,
		hash, parent); err != nil {
		t.Fatal(err)
	}
	txHash := make([]byte, 32)
	txHash[0] = 41
	if _, err := db.ExecContext(ctx, `INSERT INTO transactions
			(trans_id, ledger_seq, status, raw_txn, txn_meta)
			VALUES ($1, 40, 'validated', $2, $3)`,
		txHash, []byte{1, 2}, []byte{3}); err != nil {
		t.Fatal(err)
	}
	account := relationaldb.AccountID{9}
	if _, err := db.ExecContext(ctx, `INSERT INTO account_transactions
			(trans_id, account, ledger_seq, txn_seq) VALUES ($1, $2, 40, 1)`,
		txHash, account.String()); err != nil {
		t.Fatal(err)
	}
	if version >= 2 {
		nodeKey := make([]byte, 33)
		nodeKey[0] = 2
		if version < 4 {
			_, err = db.ExecContext(ctx, `INSERT INTO validations
				(ledger_seq, initial_seq, ledger_hash, node_pubkey, signature, sign_time, seen_time, flags, raw)
				VALUES (40, 39, $1, $2, $3, 10, 11, 1, $4)`,
				hash, nodeKey, []byte{4}, []byte{5})
		} else {
			_, err = db.ExecContext(ctx, `INSERT INTO validations
				(ledger_seq, initial_seq, ledger_hash, node_pubkey, sign_time, seen_time, flags, raw)
				VALUES (40, 39, $1, $2, 10, 11, 1, $3)`,
				hash, nodeKey, []byte{5})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if version >= 3 {
		if _, err := db.ExecContext(ctx, `INSERT INTO feature_votes(amendment, name, vetoed)
			VALUES ($1, 'Alpha', TRUE)`, strings.Repeat("A", 64)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertHistoricalPostgresData(t *testing.T, rm *RepositoryManager, version int) {
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
		if err := rm.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'validations' AND column_name = 'signature'
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
