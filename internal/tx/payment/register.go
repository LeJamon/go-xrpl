package payment

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the Payment transaction type with the tx registry.
func Register() {
	tx.Register(tx.TypePayment, func() tx.Transaction {
		return &Payment{BaseTx: *tx.NewBaseTx(tx.TypePayment, "")}
	})
}
