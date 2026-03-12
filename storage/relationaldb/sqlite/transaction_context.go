package sqlite

import (
	"context"
	"database/sql"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// TransactionContext wraps a sql.Tx on the transaction database.
// The ledger repository operates outside the transaction since
// SQLite does not support cross-database transactions.
type TransactionContext struct {
	tx *sql.Tx

	ledgerRepo             *LedgerRepository
	transactionRepo        *TransactionRepository
	accountTransactionRepo *AccountTransactionRepository
}

func NewTransactionContext(tx *sql.Tx, ledgerDB *sql.DB) *TransactionContext {
	return &TransactionContext{
		tx:                     tx,
		ledgerRepo:             NewLedgerRepository(ledgerDB), // non-transactional
		transactionRepo:        NewTransactionRepositoryWithTx(tx),
		accountTransactionRepo: NewAccountTransactionRepositoryWithTx(tx),
	}
}

func (tc *TransactionContext) Commit(ctx context.Context) error {
	if tc.tx == nil {
		return relationaldb.ErrTransactionClosed
	}
	err := tc.tx.Commit()
	tc.tx = nil
	if err != nil {
		return relationaldb.NewTransactionError("commit", "failed to commit transaction", err)
	}
	return nil
}

func (tc *TransactionContext) Rollback(ctx context.Context) error {
	if tc.tx == nil {
		return nil
	}
	err := tc.tx.Rollback()
	tc.tx = nil
	if err != nil {
		return relationaldb.NewTransactionError("rollback", "failed to rollback transaction", err)
	}
	return nil
}

func (tc *TransactionContext) Ledger() relationaldb.LedgerRepository {
	return tc.ledgerRepo
}

func (tc *TransactionContext) Transaction() relationaldb.TransactionRepository {
	return tc.transactionRepo
}

func (tc *TransactionContext) AccountTransaction() relationaldb.AccountTransactionRepository {
	return tc.accountTransactionRepo
}
