package mpt

import "github.com/LeJamon/goXRPLd/internal/tx"

// MPTokenIssuanceCreate flags (transaction flags, tf prefix)
// Reference: rippled TxFlags.h
const (
	// tfMPTCanLock allows the issuer to lock tokens
	MPTokenIssuanceCreateFlagCanLock uint32 = 0x00000002
	// tfMPTRequireAuth requires holder authorization
	MPTokenIssuanceCreateFlagRequireAuth uint32 = 0x00000004
	// tfMPTCanEscrow allows escrow
	MPTokenIssuanceCreateFlagCanEscrow uint32 = 0x00000008
	// tfMPTCanTrade allows trading on DEX
	MPTokenIssuanceCreateFlagCanTrade uint32 = 0x00000010
	// tfMPTCanTransfer allows transfers
	MPTokenIssuanceCreateFlagCanTransfer uint32 = 0x00000020
	// tfMPTCanClawback allows issuer clawback
	MPTokenIssuanceCreateFlagCanClawback uint32 = 0x00000040
)

// MPTokenIssuanceCreate flag mask
const (
	tfMPTokenIssuanceCreateValidMask uint32 = tx.TfUniversal |
		MPTokenIssuanceCreateFlagCanLock |
		MPTokenIssuanceCreateFlagRequireAuth |
		MPTokenIssuanceCreateFlagCanEscrow |
		MPTokenIssuanceCreateFlagCanTrade |
		MPTokenIssuanceCreateFlagCanTransfer |
		MPTokenIssuanceCreateFlagCanClawback
)

// MPTokenIssuanceSet flags (transaction flags, tf prefix)
const (
	// tfMPTLock locks the token (sets lsfMPTLocked)
	MPTokenIssuanceSetFlagLock uint32 = 0x00000001
	// tfMPTUnlock unlocks the token (clears lsfMPTLocked)
	MPTokenIssuanceSetFlagUnlock uint32 = 0x00000002
)

// MPTokenIssuanceSet flag mask
const (
	tfMPTokenIssuanceSetValidMask uint32 = tx.TfUniversal |
		MPTokenIssuanceSetFlagLock |
		MPTokenIssuanceSetFlagUnlock
)

// MPTokenAuthorize flags (transaction flags, tf prefix)
const (
	// tfMPTUnauthorize - holder wants to delete MPToken, or issuer wants to unauthorize holder
	MPTokenAuthorizeFlagUnauthorize uint32 = 0x00000001
)

// MPTokenAuthorize flag mask
const (
	tfMPTokenAuthorizeValidMask uint32 = tx.TfUniversal | MPTokenAuthorizeFlagUnauthorize
)
