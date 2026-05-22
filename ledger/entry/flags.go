package entry

import (
	"errors"
)

// LedgerSpecificFlags mirrors rippled's LedgerSpecificFlags enum.
// Reference: rippled/include/xrpl/protocol/LedgerFormats.h (LedgerSpecificFlags).
// Values are scoped per ledger entry type; identical numeric values are
// reused across types and disambiguated by name.
const (
	// ltACCOUNT_ROOT
	LsfPasswordSpent                uint32 = 0x00010000
	LsfRequireDestTag               uint32 = 0x00020000
	LsfRequireAuth                  uint32 = 0x00040000
	LsfDisallowXRP                  uint32 = 0x00080000
	LsfDisableMaster                uint32 = 0x00100000
	LsfNoFreeze                     uint32 = 0x00200000
	LsfGlobalFreeze                 uint32 = 0x00400000
	LsfDefaultRipple                uint32 = 0x00800000
	LsfDepositAuth                  uint32 = 0x01000000
	LsfDisallowIncomingNFTokenOffer uint32 = 0x04000000
	LsfDisallowIncomingCheck        uint32 = 0x08000000
	LsfDisallowIncomingPayChan      uint32 = 0x10000000
	LsfDisallowIncomingTrustline    uint32 = 0x20000000
	LsfAllowTrustLineLocking        uint32 = 0x40000000
	LsfAllowTrustLineClawback       uint32 = 0x80000000

	// ltOFFER
	LsfPassive uint32 = 0x00010000
	LsfSell    uint32 = 0x00020000
	LsfHybrid  uint32 = 0x00040000

	// ltRIPPLE_STATE
	LsfLowReserve     uint32 = 0x00010000
	LsfHighReserve    uint32 = 0x00020000
	LsfLowAuth        uint32 = 0x00040000
	LsfHighAuth       uint32 = 0x00080000
	LsfLowNoRipple    uint32 = 0x00100000
	LsfHighNoRipple   uint32 = 0x00200000
	LsfLowFreeze      uint32 = 0x00400000
	LsfHighFreeze     uint32 = 0x00800000
	LsfAMMNode        uint32 = 0x01000000
	LsfLowDeepFreeze  uint32 = 0x02000000
	LsfHighDeepFreeze uint32 = 0x04000000

	// ltSIGNER_LIST
	LsfOneOwnerCount uint32 = 0x00010000

	// ltDIR_NODE
	LsfNFTokenBuyOffers  uint32 = 0x00000001
	LsfNFTokenSellOffers uint32 = 0x00000002

	// ltNFTOKEN_OFFER
	LsfSellNFToken uint32 = 0x00000001

	// ltMPTOKEN_ISSUANCE (also LsfMPTLocked applies to ltMPTOKEN)
	LsfMPTLocked      uint32 = 0x00000001
	LsfMPTCanLock     uint32 = 0x00000002
	LsfMPTRequireAuth uint32 = 0x00000004
	LsfMPTCanEscrow   uint32 = 0x00000008
	LsfMPTCanTrade    uint32 = 0x00000010
	LsfMPTCanTransfer uint32 = 0x00000020
	LsfMPTCanClawback uint32 = 0x00000040

	// ltMPTOKEN
	LsfMPTAuthorized uint32 = 0x00000002

	// ltCREDENTIAL
	LsfAccepted uint32 = 0x00010000

	// ltVAULT
	LsfVaultPrivate uint32 = 0x00010000
)

// Transaction flags for MPToken transactions (tf prefix in rippled)
// Reference: rippled TxFlags.h
const (
	// MPTokenIssuanceCreate flags
	// These map directly to the ledger entry flags (tfMPT* = lsfMPT*)
	TfMPTCanLock     uint32 = LsfMPTCanLock
	TfMPTRequireAuth uint32 = LsfMPTRequireAuth
	TfMPTCanEscrow   uint32 = LsfMPTCanEscrow
	TfMPTCanTrade    uint32 = LsfMPTCanTrade
	TfMPTCanTransfer uint32 = LsfMPTCanTransfer
	TfMPTCanClawback uint32 = LsfMPTCanClawback

	// MPTokenAuthorize flags
	TfMPTUnauthorize uint32 = 0x00000001

	// MPTokenIssuanceSet flags
	TfMPTLock   uint32 = 0x00000001
	TfMPTUnlock uint32 = 0x00000002
)

// Flag masks for transaction validation
const (
	// tfUniversal is the set of flags valid for all transactions
	TfUniversal uint32 = 0x80000000

	// MPTokenIssuanceCreate valid flags
	TfMPTokenIssuanceCreateMask uint32 = ^(TfUniversal | TfMPTCanLock | TfMPTRequireAuth |
		TfMPTCanEscrow | TfMPTCanTrade | TfMPTCanTransfer | TfMPTCanClawback)

	// MPTokenAuthorize valid flags
	TfMPTokenAuthorizeMask uint32 = ^(TfUniversal | TfMPTUnauthorize)

	// MPTokenIssuanceSet valid flags
	TfMPTokenIssuanceSetMask uint32 = ^(TfUniversal | TfMPTLock | TfMPTUnlock)

	// MPTokenIssuanceDestroy valid flags (only universal flags allowed)
	TfMPTokenIssuanceDestroyMask uint32 = ^TfUniversal
)

// MPToken constants
const (
	// MaxMPTokenMetadataLength is the maximum length of MPToken metadata
	// Reference: rippled Protocol.h
	MaxMPTokenMetadataLength = 1024

	// MaxTransferFee is the maximum transfer fee in basis points (50000 = 50%)
	// Reference: rippled Protocol.h
	MaxTransferFee uint16 = 50000

	// MaxMPTokenAmount is the maximum amount for MPTokens (63-bit unsigned)
	// Reference: rippled Protocol.h
	MaxMPTokenAmount uint64 = 0x7FFFFFFFFFFFFFFF
)

// Errors returned by entry operations
var (
	ErrInvalidEntry = errors.New("invalid entry")
	ErrInvalidFlags = errors.New("invalid flags")
	ErrInvalidHash  = errors.New("invalid hash")
)
