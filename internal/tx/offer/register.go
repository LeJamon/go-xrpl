package offer

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all Offer-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeOfferCreate,
		New: func() tx.Transaction {
			return &OfferCreate{BaseTx: *tx.NewBaseTx(tx.TypeOfferCreate, "")}
		},
		Delegation: tx.Delegable,
		Privileges: tx.PrivMayCreateMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeOfferCancel,
		New: func() tx.Transaction {
			return &OfferCancel{BaseTx: *tx.NewBaseTx(tx.TypeOfferCancel, "")}
		},
		Delegation: tx.Delegable,
	})
}
