package mpt

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all MPT-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeMPTokenIssuanceCreate, func() tx.Transaction {
		return &MPTokenIssuanceCreate{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "")}
	})
	tx.Register(tx.TypeMPTokenIssuanceDestroy, func() tx.Transaction {
		return &MPTokenIssuanceDestroy{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "")}
	})
	tx.Register(tx.TypeMPTokenIssuanceSet, func() tx.Transaction {
		return &MPTokenIssuanceSet{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, "")}
	})
	tx.Register(tx.TypeMPTokenAuthorize, func() tx.Transaction {
		return &MPTokenAuthorize{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "")}
	})
}
