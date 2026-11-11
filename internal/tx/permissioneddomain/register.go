package permissioneddomain

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all PermissionedDomain-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypePermissionedDomainSet,
		New: func() tx.Transaction {
			return &PermissionedDomainSet{BaseTx: *tx.NewBaseTx(tx.TypePermissionedDomainSet, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypePermissionedDomainDelete,
		New: func() tx.Transaction {
			return &PermissionedDomainDelete{BaseTx: *tx.NewBaseTx(tx.TypePermissionedDomainDelete, "")}
		},
		Delegation: tx.Delegable,
	})
}
