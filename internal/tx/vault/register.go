package vault

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all Vault-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeVaultCreate,
		New: func() tx.Transaction {
			return &VaultCreate{BaseTx: *tx.NewBaseTx(tx.TypeVaultCreate, "")}
		},
		Delegation:        tx.NotDelegable,
		DisallowedInBatch: true,
		Privileges:        tx.PrivCreatePseudoAcct | tx.PrivCreateMPTIssuance | tx.PrivMustModifyVault,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeVaultSet,
		New: func() tx.Transaction {
			return &VaultSet{BaseTx: *tx.NewBaseTx(tx.TypeVaultSet, "")}
		},
		Delegation:        tx.NotDelegable,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMustModifyVault,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeVaultDelete,
		New: func() tx.Transaction {
			return &VaultDelete{BaseTx: *tx.NewBaseTx(tx.TypeVaultDelete, "")}
		},
		Delegation:        tx.NotDelegable,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMustDeleteAcct | tx.PrivDestroyMPTIssuance | tx.PrivMustModifyVault,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeVaultDeposit,
		New: func() tx.Transaction {
			return &VaultDeposit{BaseTx: *tx.NewBaseTx(tx.TypeVaultDeposit, "")}
		},
		Delegation:        tx.NotDelegable,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMayAuthorizeMPT | tx.PrivMustModifyVault,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeVaultWithdraw,
		New: func() tx.Transaction {
			return &VaultWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeVaultWithdraw, "")}
		},
		Delegation:        tx.NotDelegable,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMayDeleteMPT | tx.PrivMayAuthorizeMPT | tx.PrivMustModifyVault,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeVaultClawback,
		New: func() tx.Transaction {
			return &VaultClawback{BaseTx: *tx.NewBaseTx(tx.TypeVaultClawback, "")}
		},
		Delegation:        tx.NotDelegable,
		DisallowedInBatch: true,
		Privileges:        tx.PrivMayDeleteMPT | tx.PrivMustModifyVault,
	})
}
