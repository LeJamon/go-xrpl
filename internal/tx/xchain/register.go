package xchain

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all XChain (cross-chain bridge) transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeXChainCreateBridge,
		New: func() tx.Transaction {
			return &XChainCreateBridge{BaseTx: *tx.NewBaseTx(tx.TypeXChainCreateBridge, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeXChainModifyBridge,
		New: func() tx.Transaction {
			return &XChainModifyBridge{BaseTx: *tx.NewBaseTx(tx.TypeXChainModifyBridge, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeXChainCreateClaimID,
		New: func() tx.Transaction {
			return &XChainCreateClaimID{BaseTx: *tx.NewBaseTx(tx.TypeXChainCreateClaimID, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeXChainCommit,
		New: func() tx.Transaction {
			return &XChainCommit{BaseTx: *tx.NewBaseTx(tx.TypeXChainCommit, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeXChainClaim,
		New: func() tx.Transaction {
			return &XChainClaim{BaseTx: *tx.NewBaseTx(tx.TypeXChainClaim, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeXChainAccountCreateCommit,
		New: func() tx.Transaction {
			return &XChainAccountCreateCommit{BaseTx: *tx.NewBaseTx(tx.TypeXChainAccountCreateCommit, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeXChainAddClaimAttestation,
		New: func() tx.Transaction {
			return &XChainAddClaimAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddClaimAttestation, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeXChainAddAccountCreateAttest,
		New: func() tx.Transaction {
			return &XChainAddAccountCreateAttestation{BaseTx: *tx.NewBaseTx(tx.TypeXChainAddAccountCreateAttest, "")}
		},
		Delegation: tx.Delegable,
	})
}
