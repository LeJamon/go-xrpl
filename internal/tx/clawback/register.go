package clawback

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the Clawback transaction type with the tx registry.
func Register() {
	tx.Register(tx.TypeClawback, func() tx.Transaction {
		return &Clawback{BaseTx: *tx.NewBaseTx(tx.TypeClawback, "")}
	})
}
