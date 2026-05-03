package paychan

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all PaymentChannel-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypePaymentChannelCreate, func() tx.Transaction {
		return &PaymentChannelCreate{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelCreate, "")}
	})
	tx.Register(tx.TypePaymentChannelFund, func() tx.Transaction {
		return &PaymentChannelFund{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelFund, "")}
	})
	tx.Register(tx.TypePaymentChannelClaim, func() tx.Transaction {
		return &PaymentChannelClaim{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelClaim, "")}
	})
}
