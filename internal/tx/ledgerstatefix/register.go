package ledgerstatefix

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the LedgerStateFix transaction type with the tx registry.
func Register() {
	tx.Register(tx.TypeLedgerStateFix, func() tx.Transaction {
		return &LedgerStateFix{BaseTx: *tx.NewBaseTx(tx.TypeLedgerStateFix, "")}
	})
}
