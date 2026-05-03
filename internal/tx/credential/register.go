package credential

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all Credential-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeCredentialAccept, func() tx.Transaction {
		return &CredentialAccept{BaseTx: *tx.NewBaseTx(tx.TypeCredentialAccept, "")}
	})
	tx.Register(tx.TypeCredentialCreate, func() tx.Transaction {
		return &CredentialCreate{BaseTx: *tx.NewBaseTx(tx.TypeCredentialCreate, "")}
	})
	tx.Register(tx.TypeCredentialDelete, func() tx.Transaction {
		return &CredentialDelete{BaseTx: *tx.NewBaseTx(tx.TypeCredentialDelete, "")}
	})
}
