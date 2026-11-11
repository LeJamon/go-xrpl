package account

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all account-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeAccountSet,
		New: func() tx.Transaction {
			return &AccountSet{BaseTx: *tx.NewBaseTx(tx.TypeAccountSet, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeAccountDelete,
		New: func() tx.Transaction {
			return &AccountDelete{BaseTx: *tx.NewBaseTx(tx.TypeAccountDelete, "")}
		},
		Delegation: tx.NotDelegable,
		Privileges: tx.PrivMustDeleteAcct,
	})
}
