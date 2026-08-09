package handlers

// Canonical flag tables for the server_definitions RPC method's protocol
// TRANSACTION_FLAGS / LEDGER_ENTRY_FLAGS / ACCOUNT_SET_FLAGS sections.
//
// These are protocol constants (identical across implementations), so they are
// transcribed directly from rippled's TxFlags.h (getAllTxFlags /
// getUniversalFlags / getAsfFlagMap) and LedgerFormats.h (getAllLedgerFlags).
// The baseline is rippled 3.2.0 plus the Sponsor additions pinned to 3.3.0-rc1
// commit 18e311e1. The section is a static protocol description and, like
// rippled, is emitted unconditionally regardless of amendment activation.

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
		"tfNoRippleDirect":        0x00010000,
		"tfPartialPayment":        0x00020000,
		"tfLimitQuality":          0x00040000,
		"tfSponsorCreatedAccount": 0x00080000,
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
		"tfMPTCanLock":                    0x00000002,
		"tfMPTRequireAuth":                0x00000004,
		"tfMPTCanEscrow":                  0x00000008,
		"tfMPTCanTrade":                   0x00000010,
		"tfMPTCanTransfer":                0x00000020,
		"tfMPTCanClawback":                0x00000040,
		"tfMPTCanHoldConfidentialBalance": 0x00000080,
	},
	"MPTokenAuthorize": {
		"tfMPTUnauthorize": 0x00000001,
	},
	"MPTokenIssuanceSet": {
		"tfMPTLock":                          0x00000001,
		"tfMPTUnlock":                        0x00000002,
		"tfMPTSetCanLock":                    0x00000004,
		"tfMPTSetRequireAuth":                0x00000008,
		"tfMPTSetCanEscrow":                  0x00000010,
		"tfMPTSetCanTrade":                   0x00000020,
		"tfMPTSetCanTransfer":                0x00000040,
		"tfMPTSetCanClawback":                0x00000080,
		"tfMPTSetCanHoldConfidentialBalance": 0x00000100,
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
	"SponsorshipSet": {
		"tfSponsorshipSetRequireSignForFee":       0x00010000,
		"tfSponsorshipClearRequireSignForFee":     0x00020000,
		"tfSponsorshipSetRequireSignForReserve":   0x00040000,
		"tfSponsorshipClearRequireSignForReserve": 0x00080000,
		"tfDeleteObject":                          0x00100000,
	},
	"SponsorshipTransfer": {
		"tfSponsorshipEnd":      0x00010000,
		"tfSponsorshipCreate":   0x00020000,
		"tfSponsorshipReassign": 0x00040000,
	},
}

// ledgerFlagsTable maps a ledger-object group name to its lsf* flag map.
// Keys are the LEDGER_OBJECT macro names from rippled getAllLedgerFlags()
// (note "DirNode").
var ledgerFlagsTable = map[string]map[string]uint32{
	"AccountRoot": {
		"lsfPasswordSpent":                0x00010000,
		"lsfRequireDestTag":               0x00020000,
		"lsfRequireAuth":                  0x00040000,
		"lsfDisallowXRP":                  0x00080000,
		"lsfDisableMaster":                0x00100000,
		"lsfNoFreeze":                     0x00200000,
		"lsfGlobalFreeze":                 0x00400000,
		"lsfDefaultRipple":                0x00800000,
		"lsfDepositAuth":                  0x01000000,
		"lsfDisallowIncomingNFTokenOffer": 0x04000000,
		"lsfDisallowIncomingCheck":        0x08000000,
		"lsfDisallowIncomingPayChan":      0x10000000,
		"lsfDisallowIncomingTrustline":    0x20000000,
		"lsfAllowTrustLineLocking":        0x40000000,
		"lsfAllowTrustLineClawback":       0x80000000,
	},
	"Offer": {
		"lsfPassive": 0x00010000,
		"lsfSell":    0x00020000,
		"lsfHybrid":  0x00040000,
	},
	"RippleState": {
		"lsfLowReserve":     0x00010000,
		"lsfHighReserve":    0x00020000,
		"lsfLowAuth":        0x00040000,
		"lsfHighAuth":       0x00080000,
		"lsfLowNoRipple":    0x00100000,
		"lsfHighNoRipple":   0x00200000,
		"lsfLowFreeze":      0x00400000,
		"lsfHighFreeze":     0x00800000,
		"lsfAMMNode":        0x01000000,
		"lsfLowDeepFreeze":  0x02000000,
		"lsfHighDeepFreeze": 0x04000000,
	},
	"SignerList": {
		"lsfOneOwnerCount": 0x00010000,
	},
	"DirNode": {
		"lsfNFTokenBuyOffers":  0x00000001,
		"lsfNFTokenSellOffers": 0x00000002,
	},
	"NFTokenOffer": {
		"lsfSellNFToken": 0x00000001,
	},
	"MPTokenIssuance": {
		"lsfMPTLocked":                     0x00000001,
		"lsfMPTCanLock":                    0x00000002,
		"lsfMPTRequireAuth":                0x00000004,
		"lsfMPTCanEscrow":                  0x00000008,
		"lsfMPTCanTrade":                   0x00000010,
		"lsfMPTCanTransfer":                0x00000020,
		"lsfMPTCanClawback":                0x00000040,
		"lsfMPTCanHoldConfidentialBalance": 0x00000080,
	},
	"MPToken": {
		"lsfMPTLocked":     0x00000001,
		"lsfMPTAuthorized": 0x00000002,
		"lsfMPTAMM":        0x00000004,
	},
	"Credential": {
		"lsfAccepted": 0x00010000,
	},
	"Vault": {
		"lsfVaultPrivate": 0x00010000,
	},
	"Loan": {
		"lsfLoanDefault":     0x00010000,
		"lsfLoanImpaired":    0x00020000,
		"lsfLoanOverpayment": 0x00040000,
	},
	"Sponsorship": {
		"lsfSponsorshipRequireSignForFee":     0x00010000,
		"lsfSponsorshipRequireSignForReserve": 0x00020000,
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
