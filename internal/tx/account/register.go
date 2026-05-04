package account

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all account-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeAccountSet, func() tx.Transaction {
		return &AccountSet{BaseTx: *tx.NewBaseTx(tx.TypeAccountSet, "")}
	})
	tx.Register(tx.TypeAccountDelete, func() tx.Transaction {
		return &AccountDelete{BaseTx: *tx.NewBaseTx(tx.TypeAccountDelete, "")}
	})
}
