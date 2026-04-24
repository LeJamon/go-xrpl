package postgres

import (
	"context"
	"database/sql"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
	_ "github.com/lib/pq" // PostgreSQL driver
)

// RepositoryManager implements the RepositoryManager interface for PostgreSQL
type RepositoryManager struct {
	db     *sql.DB
	config *relationaldb.Config

	// Repository instances
	ledgerRepo             *LedgerRepository
	transactionRepo        *TransactionRepository
	accountTransactionRepo *AccountTransactionRepository
	systemRepo             *SystemRepository
}

// NewRepositoryManager creates a new PostgreSQL repository manager
func NewRepositoryManager(config *relationaldb.Config) (*RepositoryManager, error) {
	if err := config.Validate(); err != nil {
		return nil, relationaldb.NewConfigurationError("new_repository_manager", "invalid configuration", err)
	}

	return &RepositoryManager{
		config: config,
	}, nil
}

func (rm *RepositoryManager) Open(ctx context.Context) error {
	connStr, err := rm.config.BuildConnectionString()
	if err != nil {
		return relationaldb.NewConfigurationError("open", "failed to build connection string", err)
	}

	sqlDB, err := sql.Open(rm.config.Driver, connStr)
	if err != nil {
		return relationaldb.NewConnectionError("open", "failed to open database connection", err)
	}

	// Configure connection pool
	sqlDB.SetMaxOpenConns(rm.config.MaxOpenConns)
	sqlDB.SetMaxIdleConns(rm.config.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(rm.config.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(rm.config.ConnMaxIdleTime)

	// Test connection
	ctxTimeout, cancel := context.WithTimeout(ctx, rm.config.DefaultTimeout)
	defer cancel()

	if err := sqlDB.PingContext(ctxTimeout); err != nil {
		sqlDB.Close()
		return relationaldb.NewConnectionError("open", "failed to ping database", err)
	}

	rm.db = sqlDB

	// Initialize schema (matches rippled's table structure)
	if err := rm.initSchema(ctx); err != nil {
		rm.db.Close()
		rm.db = nil
		return relationaldb.NewSchemaError("open", "failed to initialize schema", err)
	}

	rm.ledgerRepo = NewLedgerRepository(rm.db)
	rm.transactionRepo = NewTransactionRepository(rm.db)
	rm.accountTransactionRepo = NewAccountTransactionRepository(rm.db)
	rm.systemRepo = NewSystemRepository(rm.db)

	return nil
}

func (rm *RepositoryManager) Close(ctx context.Context) error {
	if rm.db == nil {
		return nil
	}

	err := rm.db.Close()
	rm.db = nil

	// Clear repository instances
	rm.ledgerRepo = nil
	rm.transactionRepo = nil
	rm.accountTransactionRepo = nil
	rm.systemRepo = nil

	if err != nil {
		return relationaldb.NewConnectionError("close", "failed to close database connection", err)
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

// Validation returns the validation archive repository. Stubbed nil until
// the PostgreSQL impl lands — callers gating on a non-nil value treat the
// archive as disabled.
func (rm *RepositoryManager) Validation() relationaldb.ValidationRepository {
	return nil
}

func (rm *RepositoryManager) WithTransaction(ctx context.Context, fn func(relationaldb.TransactionContext) error) error {
	tx, err := rm.systemRepo.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			tx.Rollback(ctx)
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			// Log the rollback error but return the original error
			return err
		}
		return err
	}

	return tx.Commit(ctx)
}

// initSchema initializes the database schema matching rippled's structure
func (rm *RepositoryManager) initSchema(ctx context.Context) error {
	// Schema based on rippled's SQLite tables but adapted for PostgreSQL
	queries := []string{
		// Ledgers table - matches rippled's Ledgers table structure
		`CREATE TABLE IF NOT EXISTS ledgers (
			ledger_hash BYTEA PRIMARY KEY,
			ledger_seq BIGINT UNIQUE NOT NULL,
			prev_hash BYTEA NOT NULL,
			total_coins DECIMAL(20,0) NOT NULL,
			closing_time BIGINT NOT NULL,
			prev_closing_time BIGINT NOT NULL,
			close_time_res INTEGER NOT NULL,
			close_flags INTEGER NOT NULL,
			account_set_hash BYTEA NOT NULL,
			trans_set_hash BYTEA NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
		)`,

		// Transactions table - matches rippled's Transactions table
		`CREATE TABLE IF NOT EXISTS transactions (
			trans_id BYTEA PRIMARY KEY,
			ledger_seq BIGINT NOT NULL,
			status VARCHAR(50) NOT NULL,
			raw_txn BYTEA NOT NULL,
			txn_meta BYTEA,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
		)`,

		// AccountTransactions table - matches rippled's AccountTransactions table
		`CREATE TABLE IF NOT EXISTS account_transactions (
			trans_id BYTEA NOT NULL,
			account VARCHAR(34) NOT NULL,
			ledger_seq BIGINT NOT NULL,
			txn_seq INTEGER NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
			PRIMARY KEY (trans_id, account)
		)`,

		// Indexes matching rippled's performance optimizations
		`CREATE INDEX IF NOT EXISTS idx_ledgers_seq ON ledgers(ledger_seq)`,
		`CREATE INDEX IF NOT EXISTS idx_ledgers_closing_time ON ledgers(closing_time)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_ledger_seq ON transactions(ledger_seq)`,
		`CREATE INDEX IF NOT EXISTS idx_account_transactions_account ON account_transactions(account)`,
		`CREATE INDEX IF NOT EXISTS idx_account_transactions_ledger_seq ON account_transactions(ledger_seq)`,
		`CREATE INDEX IF NOT EXISTS idx_account_transactions_account_ledger_txn ON account_transactions(account, ledger_seq, txn_seq)`,
	}

	for _, query := range queries {
		if _, err := rm.db.ExecContext(ctx, query); err != nil {
			return relationaldb.NewSchemaError("init_schema", "failed to execute schema query", err)
		}
	}

	return nil
}
