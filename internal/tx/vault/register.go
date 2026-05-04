package vault

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all Vault-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeVaultCreate, func() tx.Transaction {
		return &VaultCreate{BaseTx: *tx.NewBaseTx(tx.TypeVaultCreate, "")}
	})
	tx.Register(tx.TypeVaultSet, func() tx.Transaction {
		return &VaultSet{BaseTx: *tx.NewBaseTx(tx.TypeVaultSet, "")}
	})
	tx.Register(tx.TypeVaultDelete, func() tx.Transaction {
		return &VaultDelete{BaseTx: *tx.NewBaseTx(tx.TypeVaultDelete, "")}
	})
	tx.Register(tx.TypeVaultDeposit, func() tx.Transaction {
		return &VaultDeposit{BaseTx: *tx.NewBaseTx(tx.TypeVaultDeposit, "")}
	})
	tx.Register(tx.TypeVaultWithdraw, func() tx.Transaction {
		return &VaultWithdraw{BaseTx: *tx.NewBaseTx(tx.TypeVaultWithdraw, "")}
	})
	tx.Register(tx.TypeVaultClawback, func() tx.Transaction {
		return &VaultClawback{BaseTx: *tx.NewBaseTx(tx.TypeVaultClawback, "")}
	})
}
