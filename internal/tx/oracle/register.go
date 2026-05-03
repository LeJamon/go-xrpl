package oracle

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all Oracle-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeOracleSet, func() tx.Transaction {
		return &OracleSet{BaseTx: *tx.NewBaseTx(tx.TypeOracleSet, "")}
	})
	tx.Register(tx.TypeOracleDelete, func() tx.Transaction {
		return &OracleDelete{BaseTx: *tx.NewBaseTx(tx.TypeOracleDelete, "")}
	})
}
