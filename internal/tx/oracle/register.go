package oracle

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all Oracle-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeOracleSet,
		New: func() tx.Transaction {
			return &OracleSet{BaseTx: *tx.NewBaseTx(tx.TypeOracleSet, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeOracleDelete,
		New: func() tx.Transaction {
			return &OracleDelete{BaseTx: *tx.NewBaseTx(tx.TypeOracleDelete, "")}
		},
		Delegation: tx.Delegable,
	})
}
