package sqlite

import (
	"context"
	"database/sql"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// transactionContext wraps a sql.Tx on the transaction database.
// The ledger repository operates outside the transaction since
// SQLite does not support cross-database transactions.
type transactionContext struct {
	tx *sql.Tx

	ledgerRepo             *ledgerRepository
	transactionRepo        *transactionRepository
	accountTransactionRepo *accountTransactionRepository
}

// newTransactionContext creates a SQLite transaction context. The transaction and
// account-transaction repositories run inside tx; the ledger repository runs on
// ledgerDB outside it, since SQLite has no cross-database transactions.
func newTransactionContext(tx *sql.Tx, ledgerDB *sql.DB) *transactionContext {
	return &transactionContext{
		tx:                     tx,
		ledgerRepo:             newLedgerRepository(ledgerDB), // non-transactional
		transactionRepo:        newTransactionRepositoryWithTx(tx),
		accountTransactionRepo: newAccountTransactionRepositoryWithTx(tx),
	}
}

// Commit commits the underlying transaction-database transaction.
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

// Rollback aborts the underlying transaction; it is a no-op if already committed
// or rolled back.
func (tc *transactionContext) Rollback(ctx context.Context) error {
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
