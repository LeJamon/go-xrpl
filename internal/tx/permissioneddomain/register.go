package permissioneddomain

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all PermissionedDomain-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypePermissionedDomainSet, func() tx.Transaction {
		return &PermissionedDomainSet{BaseTx: *tx.NewBaseTx(tx.TypePermissionedDomainSet, "")}
	})
	tx.Register(tx.TypePermissionedDomainDelete, func() tx.Transaction {
		return &PermissionedDomainDelete{BaseTx: *tx.NewBaseTx(tx.TypePermissionedDomainDelete, "")}
	})
}
