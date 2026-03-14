package sqlite

import (
	"context"
	"database/sql"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

type SystemRepository struct {
	ledgerDB *sql.DB
	txDB     *sql.DB
}

func NewSystemRepository(ledgerDB, txDB *sql.DB) *SystemRepository {
	return &SystemRepository{ledgerDB: ledgerDB, txDB: txDB}
}

func (r *SystemRepository) GetKBUsedAll(ctx context.Context) (uint32, error) {
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

func (r *SystemRepository) Ping(ctx context.Context) error {
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

func (r *SystemRepository) Begin(ctx context.Context) (relationaldb.TransactionContext, error) {
	if r.txDB == nil {
		return nil, relationaldb.ErrDatabaseClosed
	}
	tx, err := r.txDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, relationaldb.NewTransactionError("begin", "failed to begin transaction", err)
	}
	return NewTransactionContext(tx, r.ledgerDB), nil
}

func (r *SystemRepository) CloseLedgerDB(ctx context.Context) error {
	if r.ledgerDB == nil {
		return nil
	}
	err := r.ledgerDB.Close()
	r.ledgerDB = nil
	if err != nil {
		return relationaldb.NewConnectionError("close_ledger_db", "failed to close ledger database", err)
	}
	return nil
}

func (r *SystemRepository) CloseTransactionDB(ctx context.Context) error {
	if r.txDB == nil {
		return nil
	}
	err := r.txDB.Close()
	r.txDB = nil
	if err != nil {
		return relationaldb.NewConnectionError("close_transaction_db", "failed to close transaction database", err)
	}
	return nil
}
