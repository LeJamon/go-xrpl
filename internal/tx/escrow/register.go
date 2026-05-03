package escrow

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all Escrow-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeEscrowCreate, func() tx.Transaction {
		return &EscrowCreate{BaseTx: *tx.NewBaseTx(tx.TypeEscrowCreate, "")}
	})
	tx.Register(tx.TypeEscrowFinish, func() tx.Transaction {
		return &EscrowFinish{BaseTx: *tx.NewBaseTx(tx.TypeEscrowFinish, "")}
	})
	tx.Register(tx.TypeEscrowCancel, func() tx.Transaction {
		return &EscrowCancel{BaseTx: *tx.NewBaseTx(tx.TypeEscrowCancel, "")}
	})
}
