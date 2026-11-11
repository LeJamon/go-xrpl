package payment

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the Payment transaction type with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypePayment,
		New: func() tx.Transaction {
			return &Payment{BaseTx: *tx.NewBaseTx(tx.TypePayment, "")}
		},
		Delegation:         tx.Delegable,
		Consequences:       tx.CustomConsequences,
		ReserveSponsorable: true,
		Privileges:         tx.PrivCreateAcct | tx.PrivMayCreateMPT,
	})
}
