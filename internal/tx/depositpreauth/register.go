package depositpreauth

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the DepositPreauth transaction type with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeDepositPreauth,
		New: func() tx.Transaction {
			return &DepositPreauth{BaseTx: *tx.NewBaseTx(tx.TypeDepositPreauth, "")}
		},
		Delegation: tx.Delegable,
	})
}
