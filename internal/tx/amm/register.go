package amm

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all AMM-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeAMMCreate,
		New: func() tx.Transaction {
			return &AMMCreate{BaseTx: *tx.NewBaseTx(tx.TypeAMMCreate, "")}
		},
		Delegation: tx.Delegable,
		Privileges: tx.PrivCreatePseudoAcct | tx.PrivMayCreateMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeAMMDeposit,
		New: func() tx.Transaction {
			return &AMMDeposit{BaseTx: *tx.NewBaseTx(tx.TypeAMMDeposit, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeAMMWithdraw,
		New: func() tx.Transaction {
			return &AMMWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeAMMWithdraw, "")}
		},
		Delegation: tx.Delegable,
		Privileges: tx.PrivMayDeleteAcct | tx.PrivMayAuthorizeMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeAMMVote,
		New: func() tx.Transaction {
			return &AMMVote{BaseTx: *tx.NewBaseTx(tx.TypeAMMVote, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeAMMBid,
		New: func() tx.Transaction {
			return &AMMBid{BaseTx: *tx.NewBaseTx(tx.TypeAMMBid, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeAMMDelete,
		New: func() tx.Transaction {
			return &AMMDelete{BaseTx: *tx.NewBaseTx(tx.TypeAMMDelete, "")}
		},
		Delegation: tx.Delegable,
		Privileges: tx.PrivMustDeleteAcct | tx.PrivMayDeleteMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeAMMClawback,
		New: func() tx.Transaction {
			return &AMMClawback{BaseTx: *tx.NewBaseTx(tx.TypeAMMClawback, "")}
		},
		Delegation: tx.Delegable,
		Privileges: tx.PrivMayDeleteAcct | tx.PrivOverrideFreeze | tx.PrivMayAuthorizeMPT,
	})
}
