package batch

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the Batch transaction type with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeBatch,
		New: func() tx.Transaction {
			return &Batch{BaseTx: *tx.NewBaseTx(tx.TypeBatch, "")}
		},
		Delegation:   tx.NotDelegable,
		Consequences: tx.NormalConsequences,
	})
}
