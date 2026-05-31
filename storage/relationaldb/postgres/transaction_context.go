package postgres

import (
	"context"
	"database/sql"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// TransactionContext implements the TransactionContext interface for PostgreSQL
type TransactionContext struct {
	tx *sql.Tx

	// Repository instances for this transaction
	ledgerRepo             *LedgerRepository
	transactionRepo        *TransactionRepository
	accountTransactionRepo *AccountTransactionRepository
}

// NewTransactionContext creates a new PostgreSQL transaction context
func NewTransactionContext(tx *sql.Tx) *TransactionContext {
	return &TransactionContext{
		tx:                     tx,
		ledgerRepo:             NewLedgerRepositoryWithTx(tx),
		transactionRepo:        NewTransactionRepositoryWithTx(tx),
		accountTransactionRepo: NewAccountTransactionRepositoryWithTx(tx),
	}
}

// Commit commits the underlying database transaction.
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

// Rollback aborts the underlying database transaction; it is a no-op if already
// committed or rolled back.
func (tc *TransactionContext) Rollback(ctx context.Context) error {
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
