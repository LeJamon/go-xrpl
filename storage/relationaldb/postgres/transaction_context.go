package postgres

import (
	"context"
	"database/sql"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// transactionContext implements the transactionContext interface for PostgreSQL
type transactionContext struct {
	tx *sql.Tx

	// Repository instances for this transaction
	ledgerRepo             *ledgerRepository
	transactionRepo        *transactionRepository
	accountTransactionRepo *accountTransactionRepository
}

// newTransactionContext creates a new PostgreSQL transaction context
func newTransactionContext(tx *sql.Tx) *transactionContext {
	return &transactionContext{
		tx:                     tx,
		ledgerRepo:             newLedgerRepositoryWithTx(tx),
		transactionRepo:        newTransactionRepositoryWithTx(tx),
		accountTransactionRepo: newAccountTransactionRepositoryWithTx(tx),
	}
}

// Commit commits the underlying database transaction.
func (tc *transactionContext) Commit(ctx context.Context) error {
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

// Rollback aborts the underlying database transaction; it is a no-op if already
// committed or rolled back.
func (tc *transactionContext) Rollback(ctx context.Context) error {
	if tc.tx == nil {
		return nil // Already rolled back or committed
	}

	err := tc.tx.Rollback()
	tc.tx = nil

	if err != nil {
		return relationaldb.NewTransactionError("rollback", "failed to rollback transaction", err)
	}

	return nil
}

// Ledger returns the transaction-scoped ledger repository.
func (tc *transactionContext) Ledger() relationaldb.LedgerRepository {
	return tc.ledgerRepo
}

// Transaction returns the transaction-scoped transaction repository.
func (tc *transactionContext) Transaction() relationaldb.TransactionRepository {
	return tc.transactionRepo
}

// AccountTransaction returns the transaction-scoped account-transaction repository.
func (tc *transactionContext) AccountTransaction() relationaldb.AccountTransactionRepository {
	return tc.accountTransactionRepo
}
