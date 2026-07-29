package sponsor

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers both Sponsor lifecycle transaction types.
func Register() {
	tx.Register(tx.TypeSponsorshipSet, func() tx.Transaction {
		return &SponsorshipSet{BaseTx: *tx.NewBaseTx(tx.TypeSponsorshipSet, "")}
	})
	tx.Register(tx.TypeSponsorshipTransfer, func() tx.Transaction {
		return &SponsorshipTransfer{BaseTx: *tx.NewBaseTx(tx.TypeSponsorshipTransfer, "")}
	})
}
