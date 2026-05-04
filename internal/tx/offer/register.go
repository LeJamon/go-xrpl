package offer

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all Offer-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeOfferCreate, func() tx.Transaction {
		return &OfferCreate{BaseTx: *tx.NewBaseTx(tx.TypeOfferCreate, "")}
	})
	tx.Register(tx.TypeOfferCancel, func() tx.Transaction {
		return &OfferCancel{BaseTx: *tx.NewBaseTx(tx.TypeOfferCancel, "")}
	})
}
