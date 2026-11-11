package escrow

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all Escrow-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeEscrowCreate,
		New: func() tx.Transaction {
			return &EscrowCreate{BaseTx: *tx.NewBaseTx(tx.TypeEscrowCreate, "")}
		},
		Delegation:         tx.Delegable,
		ReserveSponsorable: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeEscrowFinish,
		New: func() tx.Transaction {
			return &EscrowFinish{BaseTx: *tx.NewBaseTx(tx.TypeEscrowFinish, "")}
		},
		Delegation:         tx.Delegable,
		ReserveSponsorable: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeEscrowCancel,
		New: func() tx.Transaction {
			return &EscrowCancel{BaseTx: *tx.NewBaseTx(tx.TypeEscrowCancel, "")}
		},
		Delegation:         tx.Delegable,
		ReserveSponsorable: true,
	})
}
