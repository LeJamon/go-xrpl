package batch

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the Batch transaction type with the tx registry.
func Register() {
	tx.Register(tx.TypeBatch, func() tx.Transaction {
		return &Batch{BaseTx: *tx.NewBaseTx(tx.TypeBatch, "")}
	})
}
