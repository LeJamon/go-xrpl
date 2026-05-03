package trustset

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers the TrustSet transaction type with the tx registry.
func Register() {
	tx.Register(tx.TypeTrustSet, func() tx.Transaction {
		return &TrustSet{BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "")}
	})
}
