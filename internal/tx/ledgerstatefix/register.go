package ledgerstatefix

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the LedgerStateFix transaction type with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeLedgerStateFix,
		New: func() tx.Transaction {
			return &LedgerStateFix{BaseTx: *tx.NewBaseTx(tx.TypeLedgerStateFix, "")}
		},
		Delegation: tx.Delegable,
	})
}
