package did

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all DID-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeDIDSet, func() tx.Transaction {
		return &DIDSet{BaseTx: *tx.NewBaseTx(tx.TypeDIDSet, "")}
	})
	tx.Register(tx.TypeDIDDelete, func() tx.Transaction {
		return &DIDDelete{BaseTx: *tx.NewBaseTx(tx.TypeDIDDelete, "")}
	})
}
