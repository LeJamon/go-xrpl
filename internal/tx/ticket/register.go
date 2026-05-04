package ticket

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the TicketCreate transaction type with the tx registry.
func Register() {
	tx.Register(tx.TypeTicketCreate, func() tx.Transaction {
		return &TicketCreate{BaseTx: *tx.NewBaseTx(tx.TypeTicketCreate, "")}
	})
}
