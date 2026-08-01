package handlers

import "github.com/LeJamon/go-xrpl/ledger/entry"

// Canonical flag tables for the server_definitions RPC method's 3.2.0
// TRANSACTION_FLAGS / LEDGER_ENTRY_FLAGS / ACCOUNT_SET_FLAGS sections.
//
// These are protocol constants (identical across implementations), so they are
// transcribed directly from rippled 3.2.0's TxFlags.h (getAllTxFlags /
// getUniversalFlags / getAsfFlagMap) and LedgerFormats.h (getAllLedgerFlags)
// rather than assembled from go-xrpl's per-package Go-named flag constants. The
// section is a static protocol description and, like rippled, is emitted
// unconditionally regardless of amendment activation.

// txFlagsTable maps a transaction-flag group name to its flag name->value map.
// The "universal" key holds the flags valid on every transaction; every other
// key is a transaction type carrying type-specific tf* flags. Mirrors rippled
// getAllTxFlags().
var txFlagsTable = map[string]map[string]uint32{
	"universal": {
		"tfFullyCanonicalSig": 0x80000000,
		"tfInnerBatchTxn":     0x40000000,
	},
	"AccountSet": {
		"tfRequireDestTag":  0x00010000,
		"tfOptionalDestTag": 0x00020000,
		"tfRequireAuth":     0x00040000,
		"tfOptionalAuth":    0x00080000,
		"tfDisallowXRP":     0x00100000,
		"tfAllowXRP":        0x00200000,
	},
	"OfferCreate": {
		"tfPassive":           0x00010000,
		"tfImmediateOrCancel": 0x00020000,
		"tfFillOrKill":        0x00040000,
		"tfSell":              0x00080000,
		"tfHybrid":            0x00100000,
	},
	"Payment": {
		"tfNoRippleDirect": 0x00010000,
		"tfPartialPayment": 0x00020000,
		"tfLimitQuality":   0x00040000,
	},
	"TrustSet": {
		"tfSetfAuth":        0x00010000,
		"tfSetNoRipple":     0x00020000,
		"tfClearNoRipple":   0x00040000,
		"tfSetFreeze":       0x00100000,
		"tfClearFreeze":     0x00200000,
		"tfSetDeepFreeze":   0x00400000,
		"tfClearDeepFreeze": 0x00800000,
	},
	"EnableAmendment": {
		"tfGotMajority":  0x00010000,
		"tfLostMajority": 0x00020000,
	},
	"PaymentChannelClaim": {
		"tfRenew": 0x00010000,
		"tfClose": 0x00020000,
	},
	"NFTokenMint": {
		"tfBurnable":     0x00000001,
		"tfOnlyXRP":      0x00000002,
		"tfTransferable": 0x00000008,
		"tfMutable":      0x00000010,
	},
	"MPTokenIssuanceCreate": {
		"tfMPTCanLock":     0x00000002,
		"tfMPTRequireAuth": 0x00000004,
		"tfMPTCanEscrow":   0x00000008,
		"tfMPTCanTrade":    0x00000010,
		"tfMPTCanTransfer": 0x00000020,
		"tfMPTCanClawback": 0x00000040,
	},
	"MPTokenAuthorize": {
		"tfMPTUnauthorize": 0x00000001,
	},
	"MPTokenIssuanceSet": {
		"tfMPTLock":   0x00000001,
		"tfMPTUnlock": 0x00000002,
	},
	"NFTokenCreateOffer": {
		"tfSellNFToken": 0x00000001,
	},
	"AMMDeposit": {
		"tfLPToken":         0x00010000,
		"tfSingleAsset":     0x00080000,
		"tfTwoAsset":        0x00100000,
		"tfOneAssetLPToken": 0x00200000,
		"tfLimitLPToken":    0x00400000,
		"tfTwoAssetIfEmpty": 0x00800000,
	},
	"AMMWithdraw": {
		"tfLPToken":             0x00010000,
		"tfWithdrawAll":         0x00020000,
		"tfOneAssetWithdrawAll": 0x00040000,
		"tfSingleAsset":         0x00080000,
		"tfTwoAsset":            0x00100000,
		"tfOneAssetLPToken":     0x00200000,
		"tfLimitLPToken":        0x00400000,
	},
	"AMMClawback": {
		"tfClawTwoAssets": 0x00000001,
	},
	"XChainModifyBridge": {
		"tfClearAccountCreateAmount": 0x00010000,
	},
	"VaultCreate": {
		"tfVaultPrivate":              0x00010000,
		"tfVaultShareNonTransferable": 0x00020000,
	},
	"Batch": {
		"tfAllOrNothing": 0x00010000,
		"tfOnlyOne":      0x00020000,
		"tfUntilFailure": 0x00040000,
		"tfIndependent":  0x00080000,
	},
	"LoanSet": {
		"tfLoanOverpayment": 0x00010000,
	},
	"LoanPay": {
		"tfLoanOverpayment": 0x00010000,
		"tfLoanFullPayment": 0x00020000,
		"tfLoanLatePayment": 0x00040000,
	},
	"LoanManage": {
		"tfLoanDefault":  0x00010000,
		"tfLoanImpair":   0x00020000,
		"tfLoanUnimpair": 0x00040000,
	},
}

// ledgerFlagsTable maps a ledger-object group name to its lsf*/lsmf* flag map.
// Keys are the LEDGER_OBJECT macro names from rippled getAllLedgerFlags()
// (note "DirNode", and the synthetic "MPTokenIssuanceMutable" group for the
// MPTokenIssuance mutable-flag set).
var ledgerFlagsTable = map[string]map[string]uint32{
	"AccountRoot": {
		"lsfPasswordSpent":                entry.LsfPasswordSpent,
		"lsfRequireDestTag":               entry.LsfRequireDestTag,
		"lsfRequireAuth":                  entry.LsfRequireAuth,
		"lsfDisallowXRP":                  entry.LsfDisallowXRP,
		"lsfDisableMaster":                entry.LsfDisableMaster,
		"lsfNoFreeze":                     entry.LsfNoFreeze,
		"lsfGlobalFreeze":                 entry.LsfGlobalFreeze,
		"lsfDefaultRipple":                entry.LsfDefaultRipple,
		"lsfDepositAuth":                  entry.LsfDepositAuth,
		"lsfDisallowIncomingNFTokenOffer": entry.LsfDisallowIncomingNFTokenOffer,
		"lsfDisallowIncomingCheck":        entry.LsfDisallowIncomingCheck,
		"lsfDisallowIncomingPayChan":      entry.LsfDisallowIncomingPayChan,
		"lsfDisallowIncomingTrustline":    entry.LsfDisallowIncomingTrustline,
		"lsfAllowTrustLineLocking":        entry.LsfAllowTrustLineLocking,
		"lsfAllowTrustLineClawback":       entry.LsfAllowTrustLineClawback,
	},
	"Offer": {
		"lsfPassive": entry.LsfPassive,
		"lsfSell":    entry.LsfSell,
		"lsfHybrid":  entry.LsfHybrid,
	},
	"RippleState": {
		"lsfLowReserve":     entry.LsfLowReserve,
		"lsfHighReserve":    entry.LsfHighReserve,
		"lsfLowAuth":        entry.LsfLowAuth,
		"lsfHighAuth":       entry.LsfHighAuth,
		"lsfLowNoRipple":    entry.LsfLowNoRipple,
		"lsfHighNoRipple":   entry.LsfHighNoRipple,
		"lsfLowFreeze":      entry.LsfLowFreeze,
		"lsfHighFreeze":     entry.LsfHighFreeze,
		"lsfAMMNode":        entry.LsfAMMNode,
		"lsfLowDeepFreeze":  entry.LsfLowDeepFreeze,
		"lsfHighDeepFreeze": entry.LsfHighDeepFreeze,
	},
	"SignerList": {
		"lsfOneOwnerCount": entry.LsfOneOwnerCount,
	},
	"DirNode": {
		"lsfNFTokenBuyOffers":  entry.LsfNFTokenBuyOffers,
		"lsfNFTokenSellOffers": entry.LsfNFTokenSellOffers,
	},
	"NFTokenOffer": {
		"lsfSellNFToken": entry.LsfSellNFToken,
	},
	"MPTokenIssuance": {
		"lsfMPTLocked":      entry.LsfMPTLocked,
		"lsfMPTCanLock":     entry.LsfMPTCanLock,
		"lsfMPTRequireAuth": entry.LsfMPTRequireAuth,
		"lsfMPTCanEscrow":   entry.LsfMPTCanEscrow,
		"lsfMPTCanTrade":    entry.LsfMPTCanTrade,
		"lsfMPTCanTransfer": entry.LsfMPTCanTransfer,
		"lsfMPTCanClawback": entry.LsfMPTCanClawback,
	},
	"MPTokenIssuanceMutable": {
		"lsmfMPTCanMutateCanLock":     entry.LsmfMPTCanMutateCanLock,
		"lsmfMPTCanMutateRequireAuth": entry.LsmfMPTCanMutateRequireAuth,
		"lsmfMPTCanMutateCanEscrow":   entry.LsmfMPTCanMutateCanEscrow,
		"lsmfMPTCanMutateCanTrade":    entry.LsmfMPTCanMutateCanTrade,
		"lsmfMPTCanMutateCanTransfer": entry.LsmfMPTCanMutateCanTransfer,
		"lsmfMPTCanMutateCanClawback": entry.LsmfMPTCanMutateCanClawback,
		"lsmfMPTCanMutateMetadata":    entry.LsmfMPTCanMutateMetadata,
		"lsmfMPTCanMutateTransferFee": entry.LsmfMPTCanMutateTransferFee,
	},
	"MPToken": {
		"lsfMPTLocked":     entry.LsfMPTLocked,
		"lsfMPTAuthorized": entry.LsfMPTAuthorized,
		"lsfMPTAMM":        entry.LsfMPTAMM,
	},
	"Credential": {
		"lsfAccepted": entry.LsfAccepted,
	},
	"Vault": {
		"lsfVaultPrivate": entry.LsfVaultPrivate,
	},
	"Loan": {
		"lsfLoanDefault":     entry.LsfLoanDefault,
		"lsfLoanImpaired":    entry.LsfLoanImpaired,
		"lsfLoanOverpayment": entry.LsfLoanOverpayment,
	},
}

// accountSetFlagsTable maps asf* flag names to their AccountSet SetFlag/ClearFlag
// values. Mirrors rippled getAsfFlagMap(); asfTshCollect (11) is intentionally
// absent, matching rippled.
var accountSetFlagsTable = map[string]uint32{
	"asfRequireDest":                  1,
	"asfRequireAuth":                  2,
	"asfDisallowXRP":                  3,
	"asfDisableMaster":                4,
	"asfAccountTxnID":                 5,
	"asfNoFreeze":                     6,
	"asfGlobalFreeze":                 7,
	"asfDefaultRipple":                8,
	"asfDepositAuth":                  9,
	"asfAuthorizedNFTokenMinter":      10,
	"asfDisallowIncomingNFTokenOffer": 12,
	"asfDisallowIncomingCheck":        13,
	"asfDisallowIncomingPayChan":      14,
	"asfDisallowIncomingTrustline":    15,
	"asfAllowTrustLineClawback":       16,
	"asfAllowTrustLineLocking":        17,
}
