package trustset

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers the TrustSet transaction type with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeTrustSet,
		New: func() tx.Transaction {
			return &TrustSet{BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "")}
		},
		Delegation:         tx.Delegable,
		Consequences:       tx.NormalConsequences,
		ReserveSponsorable: true,
	})
}
