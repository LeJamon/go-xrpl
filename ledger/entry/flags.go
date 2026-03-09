package entry

import (
	"errors"
)

// Flags for different entry types
const (
	// AccountRoot flags
	AccountRootDefaultRipple uint32 = 0x00800000
	AccountRootDepositAuth   uint32 = 0x01000000
	AccountRootDisableMaster uint32 = 0x00100000
	AccountRootDisallowXRP   uint32 = 0x00080000
	AccountRootGlobalFreeze  uint32 = 0x00400000
	AccountRootNoFreeze      uint32 = 0x00200000
	AccountRootPasswordSpent uint32 = 0x00010000
	AccountRootRequireAuth   uint32 = 0x00040000
	AccountRootRequireDest   uint32 = 0x00020000

	// Offer flags
	OfferPassive uint32 = 0x00010000
	OfferSell    uint32 = 0x00020000

	// MPTokenIssuance flags (ledger entry flags, lsf prefix in rippled)
	// Reference: rippled LedgerFormats.h
	LsfMPTLocked      uint32 = 0x00000001 // Token is locked (frozen) - also used in MPToken
	LsfMPTCanLock     uint32 = 0x00000002 // Issuer can lock tokens
	LsfMPTRequireAuth uint32 = 0x00000004 // Holders require authorization
	LsfMPTCanEscrow   uint32 = 0x00000008 // Tokens can be escrowed
	LsfMPTCanTrade    uint32 = 0x00000010 // Tokens can be traded on DEX
	LsfMPTCanTransfer uint32 = 0x00000020 // Tokens can be transferred
	LsfMPTCanClawback uint32 = 0x00000040 // Issuer can clawback tokens

	// MPToken flags (holder's entry)
	// Reference: rippled LedgerFormats.h
	LsfMPTAuthorized uint32 = 0x00000002 // Holder is authorized by issuer
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
