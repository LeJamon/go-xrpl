package postgres

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

func migrate(ctx context.Context, db *sql.DB) error {
	return migrateTo(ctx, db, postgresMigrations)
}

func migrateTo(ctx context.Context, db *sql.DB, migrations []migration) error {
	latest, err := validateMigrationDefinitions(migrations)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended(current_database() || ':' || current_schema(), 0))`); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return err
	}

	rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	current := 0
	expected := 1
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		if version > latest {
			rows.Close()
			return fmt.Errorf("%w: database version %d is newer than supported version %d", relationaldb.ErrInvalidSchema, version, latest)
		}
		if version != expected {
			rows.Close()
			return fmt.Errorf("%w: migration history has version %d, want %d", relationaldb.ErrInvalidSchema, version, expected)
		}
		current = version
		expected++
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}
		if err := migration.apply(ctx, tx); err != nil {
			return fmt.Errorf("migration %d: %w", migration.version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, migration.version); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.version, err)
		}
		current = migration.version
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
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

var postgresMigrations = []migration{
	{version: 1, apply: func(ctx context.Context, tx *sql.Tx) error {
		return execAll(ctx, tx,
			`CREATE TABLE IF NOT EXISTS ledgers (
				ledger_hash BYTEA PRIMARY KEY,
				ledger_seq BIGINT UNIQUE NOT NULL,
				prev_hash BYTEA NOT NULL,
				total_coins DECIMAL(20,0) NOT NULL,
				closing_time BIGINT NOT NULL,
				prev_closing_time BIGINT NOT NULL,
				close_time_res INTEGER NOT NULL,
				close_flags BIGINT NOT NULL,
				account_set_hash BYTEA NOT NULL,
				trans_set_hash BYTEA NOT NULL,
				created_at TIMESTAMPTZ DEFAULT NOW()
			)`,
			`CREATE TABLE IF NOT EXISTS transactions (
				trans_id BYTEA PRIMARY KEY,
				ledger_seq BIGINT NOT NULL,
				status VARCHAR(50) NOT NULL,
				raw_txn BYTEA NOT NULL,
				txn_meta BYTEA,
				created_at TIMESTAMPTZ DEFAULT NOW()
			)`,
			`CREATE TABLE IF NOT EXISTS account_transactions (
				trans_id BYTEA NOT NULL,
				account VARCHAR(40) NOT NULL,
				ledger_seq BIGINT NOT NULL,
				txn_seq BIGINT NOT NULL,
				created_at TIMESTAMPTZ DEFAULT NOW(),
				PRIMARY KEY (trans_id, account)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_ledgers_seq ON ledgers(ledger_seq)`,
			`CREATE INDEX IF NOT EXISTS idx_ledgers_closing_time ON ledgers(closing_time)`,
			`CREATE INDEX IF NOT EXISTS idx_transactions_ledger_seq ON transactions(ledger_seq)`,
			`CREATE INDEX IF NOT EXISTS idx_account_transactions_account ON account_transactions(account)`,
			`CREATE INDEX IF NOT EXISTS idx_account_transactions_ledger_seq ON account_transactions(ledger_seq)`,
			`CREATE INDEX IF NOT EXISTS idx_account_transactions_account_ledger_txn ON account_transactions(account, ledger_seq, txn_seq)`,
		)
	}},
	{version: 2, apply: func(ctx context.Context, tx *sql.Tx) error {
		return execAll(ctx, tx,
			`CREATE TABLE IF NOT EXISTS validations (
				ledger_seq BIGINT NOT NULL,
				initial_seq BIGINT NOT NULL,
				ledger_hash BYTEA NOT NULL,
				node_pubkey BYTEA NOT NULL,
				signature BYTEA NOT NULL,
				sign_time BIGINT NOT NULL,
				seen_time BIGINT NOT NULL,
				flags BIGINT NOT NULL,
				raw BYTEA NOT NULL,
				created_at TIMESTAMPTZ DEFAULT NOW(),
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
			amendment VARCHAR(64) PRIMARY KEY,
			name TEXT NOT NULL,
			vetoed BOOLEAN NOT NULL
		)`)
	}},
	{version: 4, apply: func(ctx context.Context, tx *sql.Tx) error {
		return execAll(ctx, tx,
			`ALTER TABLE validations DROP COLUMN IF EXISTS signature`,
			`ALTER TABLE ledgers ALTER COLUMN close_flags TYPE BIGINT`,
			`ALTER TABLE account_transactions ALTER COLUMN txn_seq TYPE BIGINT`,
			`ALTER TABLE ledgers DROP CONSTRAINT IF EXISTS ledgers_hash_width`,
			`ALTER TABLE ledgers ADD CONSTRAINT ledgers_hash_width CHECK (
				octet_length(ledger_hash) = 32 AND octet_length(prev_hash) = 32 AND
				octet_length(account_set_hash) = 32 AND octet_length(trans_set_hash) = 32)`,
			`ALTER TABLE ledgers DROP CONSTRAINT IF EXISTS ledgers_uint32`,
			`ALTER TABLE ledgers ADD CONSTRAINT ledgers_uint32 CHECK (
				ledger_seq BETWEEN 0 AND 4294967295 AND close_flags BETWEEN 0 AND 4294967295)`,
			`ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_width`,
			`ALTER TABLE transactions ADD CONSTRAINT transactions_width CHECK (
				octet_length(trans_id) = 32 AND ledger_seq BETWEEN 0 AND 4294967295)`,
			`ALTER TABLE account_transactions DROP CONSTRAINT IF EXISTS account_transactions_width`,
			`ALTER TABLE account_transactions ADD CONSTRAINT account_transactions_width CHECK (
				octet_length(trans_id) = 32 AND char_length(account) = 40 AND
				account ~ '^[0-9a-f]{40}$' AND ledger_seq BETWEEN 0 AND 4294967295 AND
				txn_seq BETWEEN 0 AND 4294967295)`,
			`ALTER TABLE validations DROP CONSTRAINT IF EXISTS validations_width`,
			`ALTER TABLE validations ADD CONSTRAINT validations_width CHECK (
				octet_length(ledger_hash) = 32 AND octet_length(node_pubkey) = 33 AND
				ledger_seq BETWEEN 0 AND 4294967295 AND initial_seq BETWEEN 0 AND 4294967295 AND
				flags BETWEEN 0 AND 4294967295)`,
			`ALTER TABLE feature_votes DROP CONSTRAINT IF EXISTS feature_votes_amendment`,
			`ALTER TABLE feature_votes ADD CONSTRAINT feature_votes_amendment CHECK (
				char_length(amendment) = 64 AND amendment ~ '^[0-9A-F]{64}$')`,
		)
	}},
}
