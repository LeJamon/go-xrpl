package check

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all Check-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeCheckCreate, func() tx.Transaction {
		return &CheckCreate{BaseTx: *tx.NewBaseTx(tx.TypeCheckCreate, "")}
	})
	tx.Register(tx.TypeCheckCash, func() tx.Transaction {
		return &CheckCash{BaseTx: *tx.NewBaseTx(tx.TypeCheckCash, "")}
	})
	tx.Register(tx.TypeCheckCancel, func() tx.Transaction {
		return &CheckCancel{BaseTx: *tx.NewBaseTx(tx.TypeCheckCancel, "")}
	})
}
