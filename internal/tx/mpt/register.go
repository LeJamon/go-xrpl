package mpt

import "github.com/LeJamon/go-xrpl/internal/tx"

// Register registers all MPT-related transaction types with the tx registry.
func Register() {
	tx.Register(tx.TypeMPTokenIssuanceCreate, func() tx.Transaction {
		return &MPTokenIssuanceCreate{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "")}
	})
	tx.Register(tx.TypeMPTokenIssuanceDestroy, func() tx.Transaction {
		return &MPTokenIssuanceDestroy{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "")}
	})
	tx.Register(tx.TypeMPTokenIssuanceSet, func() tx.Transaction {
		return &MPTokenIssuanceSet{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, "")}
	})
	tx.Register(tx.TypeMPTokenAuthorize, func() tx.Transaction {
		return &MPTokenAuthorize{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "")}
	})
	tx.Register(tx.TypeConfidentialMPTConvert, func() tx.Transaction {
		return &ConfidentialMPTConvert{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTConvert, "")}
	})
	tx.Register(tx.TypeConfidentialMPTMergeInbox, func() tx.Transaction {
		return &ConfidentialMPTMergeInbox{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTMergeInbox, "")}
	})
	tx.Register(tx.TypeConfidentialMPTConvertBack, func() tx.Transaction {
		return &ConfidentialMPTConvertBack{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTConvertBack, "")}
	})
	tx.Register(tx.TypeConfidentialMPTSend, func() tx.Transaction {
		return &ConfidentialMPTSend{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTSend, "")}
	})
	tx.Register(tx.TypeConfidentialMPTClawback, func() tx.Transaction {
		return &ConfidentialMPTClawback{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, "")}
	})
}
