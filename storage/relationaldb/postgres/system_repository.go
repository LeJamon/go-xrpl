package postgres

import (
	"context"
	"database/sql"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// systemRepository implements the systemRepository interface for PostgreSQL
type systemRepository struct {
	db *sql.DB
}

// newSystemRepository creates a new PostgreSQL system repository
func newSystemRepository(db *sql.DB) *systemRepository {
	return &systemRepository{db: db}
}

// GetKBUsedAll returns the total on-disk size of all public tables in KB.
func (r *systemRepository) GetKBUsedAll(ctx context.Context) (uint32, error) {
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

// Ping verifies connectivity to the database.
func (r *systemRepository) Ping(ctx context.Context) error {
	if r.db == nil {
		return relationaldb.ErrDatabaseClosed
	}

	if err := r.db.PingContext(ctx); err != nil {
		return relationaldb.NewConnectionError("ping", "database ping failed", err)
	}

	return nil
}

// Begin starts a database transaction and returns a transactionContext bound to it.
func (r *systemRepository) Begin(ctx context.Context) (relationaldb.TransactionContext, error) {
	if r.db == nil {
		return nil, relationaldb.ErrDatabaseClosed
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, relationaldb.NewTransactionError("begin", "failed to begin transaction", err)
	}

	return newTransactionContext(tx), nil
}
