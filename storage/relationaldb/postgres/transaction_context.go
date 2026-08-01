package postgres

import (
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/LeJamon/go-xrpl/storage/relationaldb/internal/sqlutil"
)

type transactionRepositories struct {
	transaction        *transactionRepository
	accountTransaction *accountTransactionRepository
}

func newTransactionRepositories(tx *sqlutil.Tx) *transactionRepositories {
	return &transactionRepositories{
		transaction:        newTransactionRepository(tx),
		accountTransaction: newAccountTransactionRepository(tx),
	}
}

func (r *transactionRepositories) Transaction() relationaldb.TransactionRepository {
	return r.transaction
}

func (r *transactionRepositories) AccountTransaction() relationaldb.AccountTransactionRepository {
	return r.accountTransaction
}
