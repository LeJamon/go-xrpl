package signerlist

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the SignerListSet and SetRegularKey transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeSignerListSet, func() tx.Transaction {
		return &SignerListSet{BaseTx: *tx.NewBaseTx(tx.TypeSignerListSet, "")}
	})
	tx.Register(tx.TypeRegularKeySet, func() tx.Transaction {
		return &SetRegularKey{BaseTx: *tx.NewBaseTx(tx.TypeRegularKeySet, "")}
	})
}
