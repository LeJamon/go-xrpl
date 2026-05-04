package xchain

import "github.com/LeJamon/goXRPLd/internal/tx"

// Register registers all XChain (cross-chain bridge) transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeXChainCreateBridge, func() tx.Transaction {
		return &XChainCreateBridge{BaseTx: *tx.NewBaseTx(tx.TypeXChainCreateBridge, "")}
	})
	tx.Register(tx.TypeXChainModifyBridge, func() tx.Transaction {
		return &XChainModifyBridge{BaseTx: *tx.NewBaseTx(tx.TypeXChainModifyBridge, "")}
	})
	tx.Register(tx.TypeXChainCreateClaimID, func() tx.Transaction {
		return &XChainCreateClaimID{BaseTx: *tx.NewBaseTx(tx.TypeXChainCreateClaimID, "")}
	})
	tx.Register(tx.TypeXChainCommit, func() tx.Transaction {
		return &XChainCommit{BaseTx: *tx.NewBaseTx(tx.TypeXChainCommit, "")}
	})
	tx.Register(tx.TypeXChainClaim, func() tx.Transaction {
		return &XChainClaim{BaseTx: *tx.NewBaseTx(tx.TypeXChainClaim, "")}
	})
	tx.Register(tx.TypeXChainAccountCreateCommit, func() tx.Transaction {
		return &XChainAccountCreateCommit{BaseTx: *tx.NewBaseTx(tx.TypeXChainAccountCreateCommit, "")}
	})
	tx.Register(tx.TypeXChainAddClaimAttestation, func() tx.Transaction {
		return &XChainAddClaimAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddClaimAttestation, "")}
	})
	tx.Register(tx.TypeXChainAddAccountCreateAttest, func() tx.Transaction {
		return &XChainAddAccountCreateAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddAccountCreateAttest, "")}
	})
}
