package pseudo

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all pseudo-transaction types (EnableAmendment, SetFee, UNLModify) with the tx registry.
func Register() {
	tx.Register(tx.TypeAmendment, func() tx.Transaction {
		return &EnableAmendment{BaseTx: *tx.NewBaseTx(tx.TypeAmendment, "")}
	})
	tx.Register(tx.TypeFee, func() tx.Transaction {
		return &SetFee{BaseTx: *tx.NewBaseTx(tx.TypeFee, "")}
	})
	tx.Register(tx.TypeUNLModify, func() tx.Transaction {
		return &UNLModify{BaseTx: *tx.NewBaseTx(tx.TypeUNLModify, "")}
	})
}
