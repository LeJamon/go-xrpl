package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

type migration struct {
	version int
	apply   func(context.Context, *sql.Tx) error
}

func migrate(ctx context.Context, db *sql.DB, migrations []migration) error {
	latest, err := validateMigrationDefinitions(migrations)
	if err != nil {
		return err
	}
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current > latest {
		return fmt.Errorf("%w: database version %d is newer than supported version %d", relationaldb.ErrInvalidSchema, current, latest)
	}
	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := migration.apply(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", migration.version, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", migration.version)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", migration.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", migration.version, err)
		}
		current = migration.version
	}
	return nil
}

func validateMigrationDefinitions(migrations []migration) (int, error) {
	for i, migration := range migrations {
		expected := i + 1
		if migration.version != expected {
			return 0, fmt.Errorf("%w: migration version %d, want %d", relationaldb.ErrInvalidSchema, migration.version, expected)
		}
	}
	return len(migrations), nil
}

func execAll(ctx context.Context, tx *sql.Tx, statements ...string) error {
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

var ledgerMigrations = []migration{
	{version: 1, apply: func(ctx context.Context, tx *sql.Tx) error {
		return execAll(ctx, tx,
			`CREATE TABLE IF NOT EXISTS ledgers (
				ledger_hash BLOB PRIMARY KEY,
				ledger_seq INTEGER UNIQUE NOT NULL,
				prev_hash BLOB NOT NULL,
				total_coins INTEGER NOT NULL,
				closing_time INTEGER NOT NULL,
				prev_closing_time INTEGER NOT NULL,
				close_time_res INTEGER NOT NULL,
				close_flags INTEGER NOT NULL,
				account_set_hash BLOB NOT NULL,
				trans_set_hash BLOB NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_ledgers_seq ON ledgers(ledger_seq)`,
		)
	}},
	{version: 2, apply: func(ctx context.Context, tx *sql.Tx) error {
		return execAll(ctx, tx,
			`CREATE TABLE IF NOT EXISTS validations (
				ledger_seq INTEGER NOT NULL,
				initial_seq INTEGER NOT NULL,
				ledger_hash BLOB NOT NULL,
				node_pubkey BLOB NOT NULL,
				signature BLOB NOT NULL,
				sign_time INTEGER NOT NULL,
				seen_time INTEGER NOT NULL,
				flags INTEGER NOT NULL,
				raw BLOB NOT NULL,
				PRIMARY KEY (ledger_hash, node_pubkey)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_validations_seq ON validations(ledger_seq)`,
			`CREATE INDEX IF NOT EXISTS idx_validations_node ON validations(node_pubkey, ledger_seq)`,
			`CREATE INDEX IF NOT EXISTS idx_validations_sign_time ON validations(sign_time)`,
			`CREATE INDEX IF NOT EXISTS idx_validations_initial ON validations(initial_seq, ledger_seq)`,
		)
	}},
	{version: 3, apply: func(ctx context.Context, tx *sql.Tx) error {
		return execAll(ctx, tx, `CREATE TABLE IF NOT EXISTS feature_votes (
			amendment TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			vetoed INTEGER NOT NULL
		)`)
	}},
	{version: 4, apply: migrateLedgerWidths},
}

func migrateLedgerWidths(ctx context.Context, tx *sql.Tx) error {
	return execAll(ctx, tx,
		`CREATE TABLE ledgers_new (
			ledger_hash BLOB PRIMARY KEY CHECK(length(ledger_hash) = 32),
			ledger_seq INTEGER UNIQUE NOT NULL CHECK(ledger_seq BETWEEN 0 AND 4294967295),
			prev_hash BLOB NOT NULL CHECK(length(prev_hash) = 32),
			total_coins INTEGER NOT NULL,
			closing_time INTEGER NOT NULL,
			prev_closing_time INTEGER NOT NULL,
			close_time_res INTEGER NOT NULL,
			close_flags INTEGER NOT NULL CHECK(close_flags BETWEEN 0 AND 4294967295),
			account_set_hash BLOB NOT NULL CHECK(length(account_set_hash) = 32),
			trans_set_hash BLOB NOT NULL CHECK(length(trans_set_hash) = 32)
		)`,
		`INSERT INTO ledgers_new SELECT ledger_hash, ledger_seq, prev_hash, total_coins,
			closing_time, prev_closing_time, close_time_res, close_flags,
			account_set_hash, trans_set_hash FROM ledgers`,
		`DROP TABLE ledgers`,
		`ALTER TABLE ledgers_new RENAME TO ledgers`,
		`CREATE INDEX idx_ledgers_seq ON ledgers(ledger_seq)`,
		`CREATE TABLE validations_new (
			ledger_seq INTEGER NOT NULL CHECK(ledger_seq BETWEEN 0 AND 4294967295),
			initial_seq INTEGER NOT NULL CHECK(initial_seq BETWEEN 0 AND 4294967295),
			ledger_hash BLOB NOT NULL CHECK(length(ledger_hash) = 32),
			node_pubkey BLOB NOT NULL CHECK(length(node_pubkey) = 33),
			sign_time INTEGER NOT NULL,
			seen_time INTEGER NOT NULL,
			flags INTEGER NOT NULL CHECK(flags BETWEEN 0 AND 4294967295),
			raw BLOB NOT NULL,
			PRIMARY KEY (ledger_hash, node_pubkey)
		)`,
		`INSERT INTO validations_new (ledger_seq, initial_seq, ledger_hash, node_pubkey,
			sign_time, seen_time, flags, raw)
			SELECT ledger_seq, initial_seq, ledger_hash, node_pubkey,
				sign_time, seen_time, flags, raw FROM validations`,
		`DROP TABLE validations`,
		`ALTER TABLE validations_new RENAME TO validations`,
		`CREATE INDEX idx_validations_seq ON validations(ledger_seq)`,
		`CREATE INDEX idx_validations_node ON validations(node_pubkey, ledger_seq)`,
		`CREATE INDEX idx_validations_sign_time ON validations(sign_time)`,
		`CREATE INDEX idx_validations_initial ON validations(initial_seq, ledger_seq)`,
		`CREATE TABLE feature_votes_new (
			amendment TEXT PRIMARY KEY CHECK(length(amendment) = 64 AND amendment NOT GLOB '*[^0-9A-F]*'),
			name TEXT NOT NULL,
			vetoed INTEGER NOT NULL
		)`,
		`INSERT INTO feature_votes_new SELECT amendment, name, vetoed FROM feature_votes`,
		`DROP TABLE feature_votes`,
		`ALTER TABLE feature_votes_new RENAME TO feature_votes`,
	)
}

var transactionMigrations = []migration{
	{version: 1, apply: func(ctx context.Context, tx *sql.Tx) error {
		return execAll(ctx, tx,
			`CREATE TABLE IF NOT EXISTS transactions (
				trans_id BLOB PRIMARY KEY,
				ledger_seq INTEGER NOT NULL,
				status TEXT NOT NULL,
				raw_txn BLOB NOT NULL,
				txn_meta BLOB
			)`,
			`CREATE INDEX IF NOT EXISTS idx_transactions_ledger_seq ON transactions(ledger_seq)`,
			`CREATE TABLE IF NOT EXISTS account_transactions (
				trans_id BLOB NOT NULL,
				account TEXT NOT NULL,
				ledger_seq INTEGER NOT NULL,
				txn_seq INTEGER NOT NULL,
				PRIMARY KEY (trans_id, account)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_acct_tx_id ON account_transactions(trans_id)`,
			`CREATE INDEX IF NOT EXISTS idx_acct_tx ON account_transactions(account, ledger_seq, txn_seq, trans_id)`,
			`CREATE INDEX IF NOT EXISTS idx_acct_lgr ON account_transactions(ledger_seq, account, trans_id)`,
		)
	}},
	{version: 2, apply: func(context.Context, *sql.Tx) error { return nil }},
	{version: 3, apply: func(context.Context, *sql.Tx) error { return nil }},
	{version: 4, apply: migrateTransactionWidths},
}

func migrateTransactionWidths(ctx context.Context, tx *sql.Tx) error {
	return execAll(ctx, tx,
		`CREATE TABLE transactions_new (
			trans_id BLOB PRIMARY KEY CHECK(length(trans_id) = 32),
			ledger_seq INTEGER NOT NULL CHECK(ledger_seq BETWEEN 0 AND 4294967295),
			status TEXT NOT NULL,
			raw_txn BLOB NOT NULL,
			txn_meta BLOB
		)`,
		`INSERT INTO transactions_new SELECT trans_id, ledger_seq, status, raw_txn, txn_meta FROM transactions`,
		`DROP TABLE transactions`,
		`ALTER TABLE transactions_new RENAME TO transactions`,
		`CREATE INDEX idx_transactions_ledger_seq ON transactions(ledger_seq)`,
		`CREATE TABLE account_transactions_new (
			trans_id BLOB NOT NULL CHECK(length(trans_id) = 32),
			account TEXT NOT NULL CHECK(length(account) = 40 AND account NOT GLOB '*[^0-9a-f]*'),
			ledger_seq INTEGER NOT NULL CHECK(ledger_seq BETWEEN 0 AND 4294967295),
			txn_seq INTEGER NOT NULL CHECK(txn_seq BETWEEN 0 AND 4294967295),
			PRIMARY KEY (trans_id, account)
		)`,
		`INSERT INTO account_transactions_new SELECT trans_id, account, ledger_seq, txn_seq FROM account_transactions`,
		`DROP TABLE account_transactions`,
		`ALTER TABLE account_transactions_new RENAME TO account_transactions`,
		`CREATE INDEX idx_acct_tx_id ON account_transactions(trans_id)`,
		`CREATE INDEX idx_acct_tx ON account_transactions(account, ledger_seq, txn_seq, trans_id)`,
		`CREATE INDEX idx_acct_lgr ON account_transactions(ledger_seq, account, trans_id)`,
	)
}
