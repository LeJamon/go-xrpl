package mpt

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

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
	// tfMPTCanHoldConfidentialBalance allows confidential balances
	MPTokenIssuanceCreateFlagCanHoldConfidentialBalance uint32 = 0x00000080
)

// MPTokenIssuanceCreate flag mask
const (
	tfMPTokenIssuanceCreateValidMask uint32 = tx.TfUniversal |
		MPTokenIssuanceCreateFlagCanLock |
		MPTokenIssuanceCreateFlagRequireAuth |
		MPTokenIssuanceCreateFlagCanEscrow |
		MPTokenIssuanceCreateFlagCanTrade |
		MPTokenIssuanceCreateFlagCanTransfer |
		MPTokenIssuanceCreateFlagCanClawback |
		MPTokenIssuanceCreateFlagCanHoldConfidentialBalance
)

// MPTokenIssuanceSet flags (transaction flags, tf prefix)
const (
	// tfMPTLock locks the token (sets lsfMPTLocked)
	MPTokenIssuanceSetFlagLock uint32 = 0x00000001
	// tfMPTUnlock unlocks the token (clears lsfMPTLocked)
	MPTokenIssuanceSetFlagUnlock                        uint32 = 0x00000002
	MPTokenIssuanceSetFlagSetCanLock                    uint32 = 0x00000004
	MPTokenIssuanceSetFlagSetRequireAuth                uint32 = 0x00000008
	MPTokenIssuanceSetFlagSetCanEscrow                  uint32 = 0x00000010
	MPTokenIssuanceSetFlagSetCanTrade                   uint32 = 0x00000020
	MPTokenIssuanceSetFlagSetCanTransfer                uint32 = 0x00000040
	MPTokenIssuanceSetFlagSetCanClawback                uint32 = 0x00000080
	MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance uint32 = 0x00000100
)

const tfMPTokenIssuanceSetEnableFlagMask uint32 = MPTokenIssuanceSetFlagSetCanLock |
	MPTokenIssuanceSetFlagSetRequireAuth |
	MPTokenIssuanceSetFlagSetCanEscrow |
	MPTokenIssuanceSetFlagSetCanTrade |
	MPTokenIssuanceSetFlagSetCanTransfer |
	MPTokenIssuanceSetFlagSetCanClawback |
	MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance

// MPTokenIssuanceSet flag mask
const (
	tfMPTokenIssuanceSetValidMask uint32 = tx.TfUniversal |
		MPTokenIssuanceSetFlagLock |
		MPTokenIssuanceSetFlagUnlock |
		tfMPTokenIssuanceSetEnableFlagMask
)

// MPTokenIssuanceCreate and MPTokenIssuanceSet ImmutableFlags.
const (
	TifMPTCanLock                    uint32 = entry.LsifMPTCanLock
	TifMPTRequireAuth                uint32 = entry.LsifMPTRequireAuth
	TifMPTCanEscrow                  uint32 = entry.LsifMPTCanEscrow
	TifMPTCanTrade                   uint32 = entry.LsifMPTCanTrade
	TifMPTCanTransfer                uint32 = entry.LsifMPTCanTransfer
	TifMPTCanClawback                uint32 = entry.LsifMPTCanClawback
	TifMPTCanHoldConfidentialBalance uint32 = entry.LsifMPTCanHoldConfidentialBalance
	TifMPTMetadata                   uint32 = entry.LsifMPTMetadata
	TifMPTTransferFee                uint32 = entry.LsifMPTTransferFee
)

const tifMPTokenIssuanceImmutableMask uint32 = ^(TifMPTCanLock |
	TifMPTRequireAuth | TifMPTCanEscrow | TifMPTCanTrade |
	TifMPTCanTransfer | TifMPTCanClawback |
	TifMPTCanHoldConfidentialBalance | TifMPTMetadata | TifMPTTransferFee)

type mptCapability struct {
	setFlag       uint32
	ledgerFlag    uint32
	immutableFlag uint32
}

var mptCapabilities = [...]mptCapability{
	{MPTokenIssuanceSetFlagSetCanLock, entry.LsfMPTCanLock, TifMPTCanLock},
	{MPTokenIssuanceSetFlagSetRequireAuth, entry.LsfMPTRequireAuth, TifMPTRequireAuth},
	{MPTokenIssuanceSetFlagSetCanEscrow, entry.LsfMPTCanEscrow, TifMPTCanEscrow},
	{MPTokenIssuanceSetFlagSetCanTrade, entry.LsfMPTCanTrade, TifMPTCanTrade},
	{MPTokenIssuanceSetFlagSetCanTransfer, entry.LsfMPTCanTransfer, TifMPTCanTransfer},
	{MPTokenIssuanceSetFlagSetCanClawback, entry.LsfMPTCanClawback, TifMPTCanClawback},
	{MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance, entry.LsfMPTCanHoldConfidentialBalance, TifMPTCanHoldConfidentialBalance},
}

// MPTokenAuthorize flags (transaction flags, tf prefix)
const (
	// tfMPTUnauthorize - holder wants to delete MPToken, or issuer wants to unauthorize holder
	MPTokenAuthorizeFlagUnauthorize uint32 = 0x00000001
)

// MPTokenAuthorize flag mask
const (
	tfMPTokenAuthorizeValidMask uint32 = tx.TfUniversal | MPTokenAuthorizeFlagUnauthorize
)
