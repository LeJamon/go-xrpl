package signerlist

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the SignerListSet and SetRegularKey transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeSignerListSet,
		New: func() tx.Transaction {
			return &SignerListSet{BaseTx: *tx.NewBaseTx(tx.TypeSignerListSet, "")}
		},
		Delegation:         tx.NotDelegable,
		ReserveSponsorable: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeRegularKeySet,
		New: func() tx.Transaction {
			return &SetRegularKey{BaseTx: *tx.NewBaseTx(tx.TypeRegularKeySet, "")}
		},
		Delegation:         tx.NotDelegable,
		ReserveSponsorable: true,
	})
}
