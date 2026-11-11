package mpt

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all MPT-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.Descriptor{
		Type: tx.TypeMPTokenIssuanceCreate,
		New: func() tx.Transaction {
			return &MPTokenIssuanceCreate{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "")}
		},
		Delegation: tx.Delegable,
		Privileges: tx.PrivCreateMPTIssuance,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeMPTokenIssuanceDestroy,
		New: func() tx.Transaction {
			return &MPTokenIssuanceDestroy{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "")}
		},
		Delegation: tx.Delegable,
		Privileges: tx.PrivDestroyMPTIssuance,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeMPTokenIssuanceSet,
		New: func() tx.Transaction {
			return &MPTokenIssuanceSet{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeMPTokenAuthorize,
		New: func() tx.Transaction {
			return &MPTokenAuthorize{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "")}
		},
		Delegation: tx.Delegable,
		Privileges: tx.PrivMustAuthorizeMPT,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeConfidentialMPTConvert,
		New: func() tx.Transaction {
			return &ConfidentialMPTConvert{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTConvert, "")}
		},
		Delegation: tx.NotDelegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeConfidentialMPTMergeInbox,
		New: func() tx.Transaction {
			return &ConfidentialMPTMergeInbox{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTMergeInbox, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeConfidentialMPTConvertBack,
		New: func() tx.Transaction {
			return &ConfidentialMPTConvertBack{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTConvertBack, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeConfidentialMPTSend,
		New: func() tx.Transaction {
			return &ConfidentialMPTSend{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTSend, "")}
		},
		Delegation: tx.Delegable,
	})
	tx.Register(tx.Descriptor{
		Type: tx.TypeConfidentialMPTClawback,
		New: func() tx.Transaction {
			return &ConfidentialMPTClawback{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, "")}
		},
		Delegation: tx.Delegable,
	})
}
