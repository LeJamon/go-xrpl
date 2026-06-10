package sqlite

import (
	"context"
	"database/sql"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
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

// NewTransactionContext creates a SQLite transaction context. The transaction and
// account-transaction repositories run inside tx; the ledger repository runs on
// ledgerDB outside it, since SQLite has no cross-database transactions.
func NewTransactionContext(tx *sql.Tx, ledgerDB *sql.DB) *TransactionContext {
	return &TransactionContext{
		tx:                     tx,
		ledgerRepo:             NewLedgerRepository(ledgerDB), // non-transactional
		transactionRepo:        NewTransactionRepositoryWithTx(tx, ledgerDB),
		accountTransactionRepo: NewAccountTransactionRepositoryWithTx(tx),
	}
}

// Commit commits the underlying transaction-database transaction.
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

// Rollback aborts the underlying transaction; it is a no-op if already committed
// or rolled back.
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

// Ledger returns the (non-transactional) ledger repository.
func (tc *TransactionContext) Ledger() relationaldb.LedgerRepository {
	return tc.ledgerRepo
}

// Transaction returns the transaction-scoped transaction repository.
func (tc *TransactionContext) Transaction() relationaldb.TransactionRepository {
	return tc.transactionRepo
}

// AccountTransaction returns the transaction-scoped account-transaction repository.
func (tc *TransactionContext) AccountTransaction() relationaldb.AccountTransactionRepository {
	return tc.accountTransactionRepo
}
