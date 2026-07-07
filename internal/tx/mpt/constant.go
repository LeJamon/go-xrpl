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

// MPTokenIssuanceCreate MutableFlags (tmf prefix). Present only under
// DynamicMPT; each bit marks a capability the issuer may later mutate. The
// values mirror the lsmfMPTCanMutate* ledger flags stored on the issuance.
const (
	TmfMPTCanMutateCanLock     uint32 = 0x00000002
	TmfMPTCanMutateRequireAuth uint32 = 0x00000004
	TmfMPTCanMutateCanEscrow   uint32 = 0x00000008
	TmfMPTCanMutateCanTrade    uint32 = 0x00000010
	TmfMPTCanMutateCanTransfer uint32 = 0x00000020
	TmfMPTCanMutateCanClawback uint32 = 0x00000040
	TmfMPTCanMutateMetadata    uint32 = 0x00010000
	TmfMPTCanMutateTransferFee uint32 = 0x00020000
)

// tmfMPTokenIssuanceCreateMutableMask holds every bit NOT valid in a create
// MutableFlags value.
const tmfMPTokenIssuanceCreateMutableMask uint32 = ^(TmfMPTCanMutateCanLock |
	TmfMPTCanMutateRequireAuth | TmfMPTCanMutateCanEscrow |
	TmfMPTCanMutateCanTrade | TmfMPTCanMutateCanTransfer |
	TmfMPTCanMutateCanClawback | TmfMPTCanMutateMetadata |
	TmfMPTCanMutateTransferFee)

// MPTokenIssuanceSet MutableFlags (tmf set/clear pairs). Each pair flips the
// matching lsf* flag on the issuance, gated by the CanMutate permission.
const (
	TmfMPTSetCanLock       uint32 = 0x00000001
	TmfMPTClearCanLock     uint32 = 0x00000002
	TmfMPTSetRequireAuth   uint32 = 0x00000004
	TmfMPTClearRequireAuth uint32 = 0x00000008
	TmfMPTSetCanEscrow     uint32 = 0x00000010
	TmfMPTClearCanEscrow   uint32 = 0x00000020
	TmfMPTSetCanTrade      uint32 = 0x00000040
	TmfMPTClearCanTrade    uint32 = 0x00000080
	TmfMPTSetCanTransfer   uint32 = 0x00000100
	TmfMPTClearCanTransfer uint32 = 0x00000200
	TmfMPTSetCanClawback   uint32 = 0x00000400
	TmfMPTClearCanClawback uint32 = 0x00000800
)

// tmfMPTokenIssuanceSetMutableMask holds every bit NOT valid in a set
// MutableFlags value.
const tmfMPTokenIssuanceSetMutableMask uint32 = ^(TmfMPTSetCanLock |
	TmfMPTClearCanLock | TmfMPTSetRequireAuth | TmfMPTClearRequireAuth |
	TmfMPTSetCanEscrow | TmfMPTClearCanEscrow | TmfMPTSetCanTrade |
	TmfMPTClearCanTrade | TmfMPTSetCanTransfer | TmfMPTClearCanTransfer |
	TmfMPTSetCanClawback | TmfMPTClearCanClawback)

// mptMutability maps a set/clear MutableFlags pair to the CanMutate permission
// bit that authorises the change. Since canMutate numerically equals the lsf*
// flag it governs, MPTokenIssuanceSet::doApply toggles the real flag with it.
type mptMutability struct {
	set       uint32
	clear     uint32
	canMutate uint32
}

var mptMutabilityFlags = [...]mptMutability{
	{TmfMPTSetCanLock, TmfMPTClearCanLock, entry.LsmfMPTCanMutateCanLock},
	{TmfMPTSetRequireAuth, TmfMPTClearRequireAuth, entry.LsmfMPTCanMutateRequireAuth},
	{TmfMPTSetCanEscrow, TmfMPTClearCanEscrow, entry.LsmfMPTCanMutateCanEscrow},
	{TmfMPTSetCanTrade, TmfMPTClearCanTrade, entry.LsmfMPTCanMutateCanTrade},
	{TmfMPTSetCanTransfer, TmfMPTClearCanTransfer, entry.LsmfMPTCanMutateCanTransfer},
	{TmfMPTSetCanClawback, TmfMPTClearCanClawback, entry.LsmfMPTCanMutateCanClawback},
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
