package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
	_ "modernc.org/sqlite" // Pure-Go SQLite driver
)

// RepositoryManager implements the RepositoryManager interface for SQLite.
// It uses two separate database files matching rippled's layout:
//   - ledger.db: Ledgers table
//   - transaction.db: Transactions + AccountTransactions tables
type RepositoryManager struct {
	dbDir    string
	ledgerDB *sql.DB
	txDB     *sql.DB

	ledgerRepo             *LedgerRepository
	transactionRepo        *TransactionRepository
	accountTransactionRepo *AccountTransactionRepository
	systemRepo             *SystemRepository
	validationRepo         *ValidationRepository
}

// Compile-time interface check
var _ relationaldb.RepositoryManager = (*RepositoryManager)(nil)

// NewRepositoryManager creates a new SQLite repository manager.
// dbDir is the directory where ledger.db and transaction.db will be created.
func NewRepositoryManager(dbDir string) (*RepositoryManager, error) {
	if dbDir == "" {
		return nil, relationaldb.NewConfigurationError("new_repository_manager", "database directory is required", nil)
	}
	return &RepositoryManager{dbDir: dbDir}, nil
}

func (rm *RepositoryManager) Open(ctx context.Context) error {
	if err := os.MkdirAll(rm.dbDir, 0700); err != nil {
		return relationaldb.NewConnectionError("open", "failed to create database directory", err)
	}

	var err error

	// Open ledger database
	ledgerPath := filepath.Join(rm.dbDir, "ledger.db")
	rm.ledgerDB, err = sql.Open("sqlite", ledgerPath)
	if err != nil {
		return relationaldb.NewConnectionError("open", "failed to open ledger database", err)
	}

	// Open transaction database
	txPath := filepath.Join(rm.dbDir, "transaction.db")
	rm.txDB, err = sql.Open("sqlite", txPath)
	if err != nil {
		rm.ledgerDB.Close()
		rm.ledgerDB = nil
		return relationaldb.NewConnectionError("open", "failed to open transaction database", err)
	}

	// Configure both connections for SQLite
	for _, db := range []*sql.DB{rm.ledgerDB, rm.txDB} {
		db.SetMaxOpenConns(1) // SQLite write concurrency limitation
		db.SetMaxIdleConns(1)
	}

	// Apply PRAGMAs
	if err := rm.applyPragmas(ctx, rm.ledgerDB); err != nil {
		rm.close()
		return relationaldb.NewConnectionError("open", "failed to apply ledger DB pragmas", err)
	}
	if err := rm.applyPragmas(ctx, rm.txDB); err != nil {
		rm.close()
		return relationaldb.NewConnectionError("open", "failed to apply transaction DB pragmas", err)
	}

	if err := rm.initLedgerSchema(ctx); err != nil {
		rm.close()
		return relationaldb.NewSchemaError("open", "failed to initialize ledger schema", err)
	}
	if err := rm.initValidationSchema(ctx); err != nil {
		rm.close()
		return relationaldb.NewSchemaError("open", "failed to initialize validation schema", err)
	}
	if err := rm.initTxSchema(ctx); err != nil {
		rm.close()
		return relationaldb.NewSchemaError("open", "failed to initialize transaction schema", err)
	}

	rm.ledgerRepo = NewLedgerRepository(rm.ledgerDB)
	rm.transactionRepo = NewTransactionRepository(rm.txDB)
	rm.accountTransactionRepo = NewAccountTransactionRepository(rm.txDB)
	rm.systemRepo = NewSystemRepository(rm.ledgerDB, rm.txDB)
	rm.validationRepo = NewValidationRepository(rm.ledgerDB)

	return nil
}

func (rm *RepositoryManager) Close(ctx context.Context) error {
	return rm.close()
}

func (rm *RepositoryManager) close() error {
	var firstErr error
	if rm.ledgerDB != nil {
		if err := rm.ledgerDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		rm.ledgerDB = nil
	}
	if rm.txDB != nil {
		if err := rm.txDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		rm.txDB = nil
	}
	rm.ledgerRepo = nil
	rm.transactionRepo = nil
	rm.accountTransactionRepo = nil
	rm.systemRepo = nil
	rm.validationRepo = nil

	if firstErr != nil {
		return relationaldb.NewConnectionError("close", "failed to close database", firstErr)
	}
	return nil
}

func (rm *RepositoryManager) Ledger() relationaldb.LedgerRepository {
	return rm.ledgerRepo
}

func (rm *RepositoryManager) Transaction() relationaldb.TransactionRepository {
	return rm.transactionRepo
}

func (rm *RepositoryManager) AccountTransaction() relationaldb.AccountTransactionRepository {
	return rm.accountTransactionRepo
}

func (rm *RepositoryManager) System() relationaldb.SystemRepository {
	return rm.systemRepo
}

func (rm *RepositoryManager) Validation() relationaldb.ValidationRepository {
	return rm.validationRepo
}

func (rm *RepositoryManager) WithTransaction(ctx context.Context, fn func(relationaldb.TransactionContext) error) error {
	tx, err := rm.txDB.BeginTx(ctx, nil)
	if err != nil {
		return relationaldb.NewTransactionError("begin", "failed to begin transaction", err)
	}

	tc := NewTransactionContext(tx, rm.ledgerDB)

	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tc); err != nil {
		if rbErr := tc.Rollback(ctx); rbErr != nil {
			return err
		}
		return err
	}

	return tc.Commit(ctx)
}

func (rm *RepositoryManager) applyPragmas(ctx context.Context, db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = -64000", // 64MB
		"PRAGMA temp_store = MEMORY",
		"PRAGMA foreign_keys = ON",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (rm *RepositoryManager) initLedgerSchema(ctx context.Context) error {
	queries := []string{
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
	}
	for _, q := range queries {
		if _, err := rm.ledgerDB.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// initValidationSchema installs the on-disk validation archive table.
// Cohabits ledger.db — see ValidationRepository for the rationale.
// Columns mirror rippled's historical Validations DDL (DBInit.h,
// pre-May-2019) with SeenTime + Flags added for receive-side forensics.
func (rm *RepositoryManager) initValidationSchema(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS validations (
			ledger_seq   INTEGER NOT NULL,
			initial_seq  INTEGER NOT NULL,
			ledger_hash  BLOB NOT NULL,
			node_pubkey  BLOB NOT NULL,
			sign_time    INTEGER NOT NULL,
			seen_time    INTEGER NOT NULL,
			flags        INTEGER NOT NULL,
			raw          BLOB NOT NULL,
			PRIMARY KEY (ledger_hash, node_pubkey)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_validations_seq       ON validations(ledger_seq)`,
		`CREATE INDEX IF NOT EXISTS idx_validations_node      ON validations(node_pubkey, ledger_seq)`,
		`CREATE INDEX IF NOT EXISTS idx_validations_sign_time ON validations(sign_time)`,
		`CREATE INDEX IF NOT EXISTS idx_validations_initial   ON validations(initial_seq, ledger_seq)`,
	}
	for _, q := range queries {
		if _, err := rm.ledgerDB.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (rm *RepositoryManager) initTxSchema(ctx context.Context) error {
	queries := []string{
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
	}
	for _, q := range queries {
		if _, err := rm.txDB.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
