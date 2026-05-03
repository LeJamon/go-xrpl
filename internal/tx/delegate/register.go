package delegate

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the DelegateSet transaction type with the tx registry.
func Register() {
	tx.Register(tx.TypeDelegateSet, func() tx.Transaction {
		return &DelegateSet{BaseTx: *tx.NewBaseTx(tx.TypeDelegateSet, "")}
	})
}
