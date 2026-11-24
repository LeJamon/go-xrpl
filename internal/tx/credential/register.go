package credential

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all Credential-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeCredentialAccept,
		New: func() tx.Transaction {
			return &CredentialAccept{BaseTx: *tx.NewBaseTx(tx.TypeCredentialAccept, "")}
		},
		Delegation:         tx.Delegable,
		Consequences:       tx.NormalConsequences,
		ReserveSponsorable: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeCredentialCreate,
		New: func() tx.Transaction {
			return &CredentialCreate{BaseTx: *tx.NewBaseTx(tx.TypeCredentialCreate, "")}
		},
		Delegation:         tx.Delegable,
		Consequences:       tx.NormalConsequences,
		ReserveSponsorable: true,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeCredentialDelete,
		New: func() tx.Transaction {
			return &CredentialDelete{BaseTx: *tx.NewBaseTx(tx.TypeCredentialDelete, "")}
		},
		Delegation:         tx.Delegable,
		Consequences:       tx.NormalConsequences,
		ReserveSponsorable: true,
	})
}
