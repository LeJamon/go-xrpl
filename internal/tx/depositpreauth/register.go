package depositpreauth

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the DepositPreauth transaction type with the tx registry.
func Register() {
	tx.Register(tx.TypeDepositPreauth, func() tx.Transaction {
		return &DepositPreauth{BaseTx: *tx.NewBaseTx(tx.TypeDepositPreauth, "")}
	})
}
