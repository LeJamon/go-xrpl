package delegate

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the DelegateSet transaction type with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeDelegateSet,
		New: func() tx.Transaction {
			return &DelegateSet{BaseTx: *tx.NewBaseTx(tx.TypeDelegateSet, "")}
		},
		Delegation: tx.NotDelegable,
	})
}
