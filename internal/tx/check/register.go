package check

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all Check-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeCheckCreate,
		New: func() tx.Transaction {
			return &CheckCreate{BaseTx: *tx.NewBaseTx(tx.TypeCheckCreate, "")}
		},
		Delegation:         tx.Delegable,
		ReserveSponsorable: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeCheckCash,
		New: func() tx.Transaction {
			return &CheckCash{BaseTx: *tx.NewBaseTx(tx.TypeCheckCash, "")}
		},
		Delegation:         tx.Delegable,
		ReserveSponsorable: true,
		Privileges:         tx.PrivMayCreateMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeCheckCancel,
		New: func() tx.Transaction {
			return &CheckCancel{BaseTx: *tx.NewBaseTx(tx.TypeCheckCancel, "")}
		},
		Delegation:         tx.Delegable,
		ReserveSponsorable: true,
	})
}
