package sponsor

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers both Sponsor lifecycle transaction types.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeSponsorshipSet,
		New: func() tx.Transaction {
			return &SponsorshipSet{BaseTx: *tx.NewBaseTx(tx.TypeSponsorshipSet, "")}
		},
		Delegation:   tx.Delegable,
		Consequences: tx.CustomConsequences,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeSponsorshipTransfer,
		New: func() tx.Transaction {
			return &SponsorshipTransfer{BaseTx: *tx.NewBaseTx(tx.TypeSponsorshipTransfer, "")}
		},
		Delegation:         tx.NotDelegable,
		Consequences:       tx.NormalConsequences,
		ReserveSponsorable: true,
	})
}
