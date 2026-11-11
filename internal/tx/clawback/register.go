package clawback

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the Clawback transaction type with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeClawback,
		New: func() tx.Transaction {
			return &Clawback{BaseTx: *tx.NewBaseTx(tx.TypeClawback, "")}
		},
		Delegation:         tx.Delegable,
		ReserveSponsorable: true,
	})
}
