package paychan

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all PaymentChannel-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypePaymentChannelCreate,
		New: func() tx.Transaction {
			return &PaymentChannelCreate{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelCreate, "")}
		},
		Delegation:         tx.Delegable,
		Consequences:       tx.CustomConsequences,
		ReserveSponsorable: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypePaymentChannelFund,
		New: func() tx.Transaction {
			return &PaymentChannelFund{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelFund, "")}
		},
		Delegation:         tx.Delegable,
		Consequences:       tx.CustomConsequences,
		ReserveSponsorable: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypePaymentChannelClaim,
		New: func() tx.Transaction {
			return &PaymentChannelClaim{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelClaim, "")}
		},
		Delegation:         tx.Delegable,
		Consequences:       tx.NormalConsequences,
		ReserveSponsorable: true,
	})
}
