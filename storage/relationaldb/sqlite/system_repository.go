package sqlite

import (
	"context"
	"database/sql"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// systemRepository is the SQLite-backed system repository, spanning the separate
// ledger and transaction databases.
type systemRepository struct {
	ledgerDB *sql.DB
	txDB     *sql.DB
}

// newSystemRepository creates a SQLite system repository over the ledger and
// transaction databases.
func newSystemRepository(ledgerDB, txDB *sql.DB) *systemRepository {
	return &systemRepository{ledgerDB: ledgerDB, txDB: txDB}
}

// GetKBUsedAll returns the combined on-disk size of both databases in KB.
func (r *systemRepository) GetKBUsedAll(ctx context.Context) (uint32, error) {
	var total int64
	for _, db := range []*sql.DB{r.ledgerDB, r.txDB} {
		if db == nil {
			continue
		}
		var pageCount, pageSize int64
		if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
			return 0, relationaldb.NewQueryError("get_kb_used_all", "failed to get page count", err)
		}
		if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
			return 0, relationaldb.NewQueryError("get_kb_used_all", "failed to get page size", err)
		}
		total += pageCount * pageSize
	}
	return uint32(total / 1024), nil
}

// Ping verifies connectivity to both databases.
func (r *systemRepository) Ping(ctx context.Context) error {
	if r.ledgerDB != nil {
		if err := r.ledgerDB.PingContext(ctx); err != nil {
			return relationaldb.NewConnectionError("ping", "ledger database ping failed", err)
		}
	}
	if r.txDB != nil {
		if err := r.txDB.PingContext(ctx); err != nil {
			return relationaldb.NewConnectionError("ping", "transaction database ping failed", err)
		}
	}
	return nil
}

// Begin starts a transaction on the transaction database and returns a
// transactionContext bound to it.
func (r *systemRepository) Begin(ctx context.Context) (relationaldb.TransactionContext, error) {
	if r.txDB == nil {
		return nil, relationaldb.ErrDatabaseClosed
	}
	tx, err := r.txDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, relationaldb.NewTransactionError("begin", "failed to begin transaction", err)
	}
	return newTransactionContext(tx, r.ledgerDB), nil
}
