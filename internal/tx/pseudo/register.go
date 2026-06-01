package pseudo

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Register registers all pseudo-transaction types (EnableAmendment, SetFee, UNLModify) with the tx registry.
func Register() {
	tx.Register(tx.TypeAmendment, func() tx.Transaction {
		return &EnableAmendment{BaseTx: *tx.NewBaseTx(tx.TypeAmendment, protocol.ZeroAccount)}
	})
	tx.Register(tx.TypeFee, func() tx.Transaction {
		return &SetFee{BaseTx: *tx.NewBaseTx(tx.TypeFee, protocol.ZeroAccount)}
	})
	tx.Register(tx.TypeUNLModify, func() tx.Transaction {
		return &UNLModify{BaseTx: *tx.NewBaseTx(tx.TypeUNLModify, protocol.ZeroAccount)}
	})
}
