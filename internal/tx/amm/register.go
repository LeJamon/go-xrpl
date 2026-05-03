package amm

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all AMM-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeAMMCreate, func() tx.Transaction {
		return &AMMCreate{BaseTx: *tx.NewBaseTx(tx.TypeAMMCreate, "")}
	})
	tx.Register(tx.TypeAMMDeposit, func() tx.Transaction {
		return &AMMDeposit{BaseTx: *tx.NewBaseTx(tx.TypeAMMDeposit, "")}
	})
	tx.Register(tx.TypeAMMWithdraw, func() tx.Transaction {
		return &AMMWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeAMMWithdraw, "")}
	})
	tx.Register(tx.TypeAMMVote, func() tx.Transaction {
		return &AMMVote{BaseTx: *tx.NewBaseTx(tx.TypeAMMVote, "")}
	})
	tx.Register(tx.TypeAMMBid, func() tx.Transaction {
		return &AMMBid{BaseTx: *tx.NewBaseTx(tx.TypeAMMBid, "")}
	})
	tx.Register(tx.TypeAMMDelete, func() tx.Transaction {
		return &AMMDelete{BaseTx: *tx.NewBaseTx(tx.TypeAMMDelete, "")}
	})
	tx.Register(tx.TypeAMMClawback, func() tx.Transaction {
		return &AMMClawback{BaseTx: *tx.NewBaseTx(tx.TypeAMMClawback, "")}
	})
}
