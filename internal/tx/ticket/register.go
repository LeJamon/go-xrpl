package ticket

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the TicketCreate transaction type with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeTicketCreate,
		New: func() tx.Transaction {
			return &TicketCreate{BaseTx: *tx.NewBaseTx(tx.TypeTicketCreate, "")}
		},
		Delegation:   tx.Delegable,
		Consequences: tx.CustomConsequences,
	})
}
