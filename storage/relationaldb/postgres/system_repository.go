package postgres

import (
	"context"
	"database/sql"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// SystemRepository implements the SystemRepository interface for PostgreSQL
type SystemRepository struct {
	db *sql.DB
}

// NewSystemRepository creates a new PostgreSQL system repository
func NewSystemRepository(db *sql.DB) *SystemRepository {
	return &SystemRepository{db: db}
}

func (r *SystemRepository) GetKBUsedAll(ctx context.Context) (uint32, error) {
	if r.db == nil {
		return 0, relationaldb.ErrDatabaseClosed
	}

	var size int64
	err := r.db.QueryRowContext(ctx,
		"SELECT pg_database_size(current_database())").Scan(&size)

	if err != nil {
		return 0, relationaldb.NewQueryError("get_kb_used_all", "failed to get database size", err)
	}

	return uint32(size / 1024), nil
}

func (r *SystemRepository) Ping(ctx context.Context) error {
	if r.db == nil {
		return relationaldb.ErrDatabaseClosed
	}

	if err := r.db.PingContext(ctx); err != nil {
		return relationaldb.NewConnectionError("ping", "database ping failed", err)
	}

	return nil
}

func (r *SystemRepository) Begin(ctx context.Context) (relationaldb.TransactionContext, error) {
	if r.db == nil {
		return nil, relationaldb.ErrDatabaseClosed
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, relationaldb.NewTransactionError("begin", "failed to begin transaction", err)
	}

	return NewTransactionContext(tx), nil
}

// CloseLedgerDB closes the ledger database connection.
// In PostgreSQL, this is a no-op since we use a single connection pool.
func (r *SystemRepository) CloseLedgerDB(ctx context.Context) error {
	// PostgreSQL uses a single connection pool, so this is a no-op
	return nil
}

// CloseTransactionDB closes the transaction database connection.
// In PostgreSQL, this is a no-op since we use a single connection pool.
func (r *SystemRepository) CloseTransactionDB(ctx context.Context) error {
	// PostgreSQL uses a single connection pool, so this is a no-op
	return nil
}
