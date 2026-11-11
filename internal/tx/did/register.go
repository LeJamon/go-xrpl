package did

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all DID-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeDIDSet,
		New: func() tx.Transaction {
			return &DIDSet{BaseTx: *tx.NewBaseTx(tx.TypeDIDSet, "")}
		},
		Delegation:   tx.Delegable,
		Consequences: tx.NormalConsequences,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeDIDDelete,
		New: func() tx.Transaction {
			return &DIDDelete{BaseTx: *tx.NewBaseTx(tx.TypeDIDDelete, "")}
		},
		Delegation:   tx.Delegable,
		Consequences: tx.NormalConsequences,
	})
}
