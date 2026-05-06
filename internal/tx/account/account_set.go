package account

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
)

const qualityOne uint32 = 1000000000

func isZeroHash256(s string) bool {
	return strings.Trim(s, "0") == ""
}

// isValidPublicKey checks if the bytes represent a valid XRPL public key.
// Reference: rippled publicKeyType() — checks prefix byte and length.
func isValidPublicKey(key []byte) bool {
	if len(key) == 33 {
		// secp256k1 compressed (0x02 or 0x03) or ed25519 (0xED)
		return key[0] == 0x02 || key[0] == 0x03 || key[0] == 0xED
	}
	if len(key) == 65 {
		// secp256k1 uncompressed (0x04)
		return key[0] == 0x04
	}
	return false
}

// AccountSet modifies the properties of an account in the XRP Ledger.
type AccountSet struct {
	tx.BaseTx

	// ClearFlag is a flag to disable for this account
	ClearFlag *uint32 `json:"ClearFlag,omitempty" xrpl:"ClearFlag,omitempty"`

	// Domain is the domain associated with this account (hex-encoded)
	// Pointer to distinguish nil (not present) from "" (present but empty, for clearing)
	Domain *string `json:"Domain,omitempty" xrpl:"Domain,omitempty"`

	// EmailHash is MD5 hash of email for Gravatar (deprecated)
	EmailHash string `json:"EmailHash,omitempty" xrpl:"EmailHash,omitempty"`

	// MessageKey is a public key for sending encrypted messages
	// Pointer to distinguish nil (not present) from "" (present but empty, for clearing)
	MessageKey *string `json:"MessageKey,omitempty" xrpl:"MessageKey,omitempty"`

	// NFTokenMinter is the account allowed to mint NFTokens for this account
	NFTokenMinter string `json:"NFTokenMinter,omitempty" xrpl:"NFTokenMinter,omitempty"`

	// SetFlag is a flag to enable for this account
	SetFlag *uint32 `json:"SetFlag,omitempty" xrpl:"SetFlag,omitempty"`

	// TransferRate is the fee for transferring issued currencies (1e9 = 100%)
	TransferRate *uint32 `json:"TransferRate,omitempty" xrpl:"TransferRate,omitempty"`

	// TickSize is the tick size for offers involving this account's currencies
	TickSize *uint8 `json:"TickSize,omitempty" xrpl:"TickSize,omitempty"`

	// WalletLocator is arbitrary hex data (deprecated)
	WalletLocator string `json:"WalletLocator,omitempty" xrpl:"WalletLocator,omitempty"`

	// WalletSize is arbitrary data (deprecated)
	WalletSize *uint32 `json:"WalletSize,omitempty" xrpl:"WalletSize,omitempty"`
}

// Common transaction flags
const (
	// TxFlagFullyCanonicalSig indicates the signature is fully canonical
	TxFlagFullyCanonicalSig uint32 = 0x80000000
)

// AccountSet transaction flags (legacy)
// Reference: rippled SetAccount.cpp
const (
	// tfRequireDestTag requires destination tag
	AccountSetTxFlagRequireDestTag uint32 = 0x00010000
	// tfOptionalDestTag makes destination tag optional
	AccountSetTxFlagOptionalDestTag uint32 = 0x00020000
	// tfRequireAuth requires authorization
	AccountSetTxFlagRequireAuth uint32 = 0x00040000
	// tfOptionalAuth makes authorization optional
	AccountSetTxFlagOptionalAuth uint32 = 0x00080000
	// tfDisallowXRP disallows XRP payments
	AccountSetTxFlagDisallowXRP uint32 = 0x00100000
	// tfAllowXRP allows XRP payments
	AccountSetTxFlagAllowXRP uint32 = 0x00200000

	// tfAccountSetMask is the mask for valid AccountSet transaction flags
	AccountSetTxFlagMask uint32 = ^(AccountSetTxFlagRequireDestTag |
		AccountSetTxFlagOptionalDestTag |
		AccountSetTxFlagRequireAuth |
		AccountSetTxFlagOptionalAuth |
		AccountSetTxFlagDisallowXRP |
		AccountSetTxFlagAllowXRP |
		TxFlagFullyCanonicalSig)
)

// Domain length limits
const (
	// MaxDomainLength is the maximum length of a domain in bytes
	MaxDomainLength = 256
)

// AccountSet account flags (for SetFlag/ClearFlag)
const (
	// asfRequireDest requires a destination tag
	AccountSetFlagRequireDest uint32 = 1
	// asfRequireAuth requires authorization for trust lines
	AccountSetFlagRequireAuth uint32 = 2
	// asfDisallowXRP disallows XRP payments to this account
	AccountSetFlagDisallowXRP uint32 = 3
	// asfDisableMaster disables the master key
	AccountSetFlagDisableMaster uint32 = 4
	// asfAccountTxnID enables AccountTxnID tracking
	AccountSetFlagAccountTxnID uint32 = 5
	// asfNoFreeze prevents freezing trust lines
	AccountSetFlagNoFreeze uint32 = 6
	// asfGlobalFreeze freezes all trust lines
	AccountSetFlagGlobalFreeze uint32 = 7
	// asfDefaultRipple enables rippling by default
	AccountSetFlagDefaultRipple uint32 = 8
	// asfDepositAuth requires deposit authorization
	AccountSetFlagDepositAuth uint32 = 9
	// asfAuthorizedNFTokenMinter enables NFToken minting
	AccountSetFlagAuthorizedNFTokenMinter uint32 = 10
	// asfDisallowIncomingNFTokenOffer disallows incoming NFT offers
	AccountSetFlagDisallowIncomingNFTokenOffer uint32 = 12
	// asfDisallowIncomingCheck disallows incoming checks
	AccountSetFlagDisallowIncomingCheck uint32 = 13
	// asfDisallowIncomingPayChan disallows incoming payment channels
	AccountSetFlagDisallowIncomingPayChan uint32 = 14
	// asfDisallowIncomingTrustline disallows incoming trust lines
	AccountSetFlagDisallowIncomingTrustline uint32 = 15
	// asfAllowTrustLineClawback enables clawback on trust lines
	AccountSetFlagAllowTrustLineClawback uint32 = 16
	// asfAllowTrustLineLocking enables trust-line locking for token escrow
	AccountSetFlagAllowTrustLineLocking uint32 = 17
)

// NewAccountSet creates a new AccountSet transaction
func NewAccountSet(account string) *AccountSet {
	return &AccountSet{
		BaseTx: *tx.NewBaseTx(tx.TypeAccountSet, account),
	}
}

func (a *AccountSet) TxType() tx.Type {
	return tx.TypeAccountSet
}

// Reference: rippled SetAccount.cpp preflight()
func (a *AccountSet) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	txFlags := a.GetFlags()

	// Check for invalid transaction flags
	// Reference: rippled SetAccount.cpp:71-75
	if txFlags&AccountSetTxFlagMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid transaction flags")
	}

	// Cannot set and clear the same flag
	// Reference: rippled SetAccount.cpp:80-84
	if a.SetFlag != nil && a.ClearFlag != nil && *a.SetFlag == *a.ClearFlag {
		return tx.Errorf(tx.TemINVALID_FLAG, "cannot set and clear the same flag")
	}

	// Check for contradictory RequireAuth flags
	// Reference: rippled SetAccount.cpp:89-98
	setRequireAuth := (txFlags&AccountSetTxFlagRequireAuth != 0) ||
		(a.SetFlag != nil && *a.SetFlag == AccountSetFlagRequireAuth)
	clearRequireAuth := (txFlags&AccountSetTxFlagOptionalAuth != 0) ||
		(a.ClearFlag != nil && *a.ClearFlag == AccountSetFlagRequireAuth)
	if setRequireAuth && clearRequireAuth {
		return tx.Errorf(tx.TemINVALID_FLAG, "contradictory RequireAuth flags")
	}

	// Check for contradictory RequireDest flags
	// Reference: rippled SetAccount.cpp:103-112
	setRequireDest := (txFlags&AccountSetTxFlagRequireDestTag != 0) ||
		(a.SetFlag != nil && *a.SetFlag == AccountSetFlagRequireDest)
	clearRequireDest := (txFlags&AccountSetTxFlagOptionalDestTag != 0) ||
		(a.ClearFlag != nil && *a.ClearFlag == AccountSetFlagRequireDest)
	if setRequireDest && clearRequireDest {
		return tx.Errorf(tx.TemINVALID_FLAG, "contradictory RequireDest flags")
	}

	// Check for contradictory DisallowXRP flags
	// Reference: rippled SetAccount.cpp:117-126
	setDisallowXRP := (txFlags&AccountSetTxFlagDisallowXRP != 0) ||
		(a.SetFlag != nil && *a.SetFlag == AccountSetFlagDisallowXRP)
	clearDisallowXRP := (txFlags&AccountSetTxFlagAllowXRP != 0) ||
		(a.ClearFlag != nil && *a.ClearFlag == AccountSetFlagDisallowXRP)
	if setDisallowXRP && clearDisallowXRP {
		return tx.Errorf(tx.TemINVALID_FLAG, "contradictory DisallowXRP flags")
	}

	// TransferRate validation
	// Reference: rippled SetAccount.cpp:129-146
	if a.TransferRate != nil {
		tr := *a.TransferRate
		if tr != 0 && tr < protocol.TransferRateMin {
			return tx.Errorf(tx.TemBAD_TRANSFER_RATE, "transfer rate too small")
		}
		if tr > protocol.TransferRateMax {
			return tx.Errorf(tx.TemBAD_TRANSFER_RATE, "transfer rate too large")
		}
	}

	// TickSize validation
	// Reference: rippled SetAccount.cpp:149-159
	if a.TickSize != nil {
		ts := *a.TickSize
		if ts != 0 && (ts < protocol.TickSizeMin || ts > protocol.TickSizeMax) {
			return tx.Errorf(tx.TemBAD_TICK_SIZE, "tick size must be 0 or 3-16")
		}
	}

	// MessageKey validation
	// Reference: rippled SetAccount.cpp lines 161-168
	// If present and non-empty, must be a valid public key (ed25519 or secp256k1).
	if a.MessageKey != nil && *a.MessageKey != "" {
		mkBytes, err := hex.DecodeString(*a.MessageKey)
		if err != nil || !isValidPublicKey(mkBytes) {
			return tx.Errorf(tx.TelBAD_PUBLIC_KEY, "invalid message key specified")
		}
	}

	// Domain length validation
	// Reference: rippled SetAccount.cpp:170-175
	// Domain is stored as hex, so max hex length is 2*256 = 512
	if a.Domain != nil && len(*a.Domain) > MaxDomainLength*2 {
		return tx.Errorf(tx.TelBAD_DOMAIN, "domain too long")
	}

	// NFTokenMinter validation
	// Reference: rippled SetAccount.cpp:177-187
	if a.SetFlag != nil && *a.SetFlag == AccountSetFlagAuthorizedNFTokenMinter {
		if a.NFTokenMinter == "" {
			return tx.Errorf(tx.TemMALFORMED, "NFTokenMinter required when setting asfAuthorizedNFTokenMinter")
		}
	}
	if a.ClearFlag != nil && *a.ClearFlag == AccountSetFlagAuthorizedNFTokenMinter {
		if a.NFTokenMinter != "" {
			return tx.Errorf(tx.TemMALFORMED, "NFTokenMinter must be empty when clearing asfAuthorizedNFTokenMinter")
		}
	}

	return nil
}

func (a *AccountSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// EnableRequireDest enables the require destination tag flag
func (a *AccountSet) EnableRequireDest() {
	flag := AccountSetFlagRequireDest
	a.SetFlag = &flag
}

// EnableDepositAuth enables deposit authorization
func (a *AccountSet) EnableDepositAuth() {
	flag := AccountSetFlagDepositAuth
	a.SetFlag = &flag
}

// EnableDefaultRipple enables default rippling
func (a *AccountSet) EnableDefaultRipple() {
	flag := AccountSetFlagDefaultRipple
	a.SetFlag = &flag
}

func (a *AccountSet) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("account set apply",
		"account", a.Account,
		"setFlag", a.SetFlag,
		"clearFlag", a.ClearFlag,
	)

	account := ctx.Account
	uFlagsIn := account.Flags
	uFlagsOut := uFlagsIn

	var uSetFlag, uClearFlag uint32
	if a.SetFlag != nil {
		uSetFlag = *a.SetFlag
	}
	if a.ClearFlag != nil {
		uClearFlag = *a.ClearFlag
	}

	// Clawback / NoFreeze mutual exclusion preclaim checks
	// Reference: rippled SetAccount.cpp preclaim() lines 281-307
	if ctx.Rules().Enabled(amendment.FeatureClawback) {
		if uSetFlag == AccountSetFlagAllowTrustLineClawback {
			if uFlagsIn&state.LsfNoFreeze != 0 {
				return tx.TecNO_PERMISSION
			}
			if !ownerDirIsEmpty(ctx.View, ctx.AccountID) {
				return tx.TecOWNERS
			}
		}
		if uSetFlag == AccountSetFlagNoFreeze && uFlagsIn&state.LsfAllowTrustLineClawback != 0 {
			return tx.TecNO_PERMISSION
		}
	}

	// RequireAuth
	// Reference: rippled SetAccount.cpp preclaim() lines 269-276
	// dirIsEmpty() checks whether the owner directory has any entries.
	//
	// In rippled, this returns terOWNERS (retry) when tapRETRY is set (open ledger)
	// and tecOWNERS (claim fee) otherwise. goXRPL has no open-ledger retry mechanism
	// (tapRETRY), so we always return tecOWNERS — equivalent to the closed-ledger
	// (consensus) path in rippled.
	bSetRequireAuth := (a.GetFlags()&AccountSetTxFlagRequireAuth != 0) ||
		uSetFlag == AccountSetFlagRequireAuth
	if bSetRequireAuth && (uFlagsIn&state.LsfRequireAuth) == 0 {
		if !ownerDirIsEmpty(ctx.View, ctx.AccountID) {
			return tx.TecOWNERS
		}
		uFlagsOut |= state.LsfRequireAuth
	}
	if uClearFlag == AccountSetFlagRequireAuth && (uFlagsIn&state.LsfRequireAuth) != 0 {
		uFlagsOut &^= state.LsfRequireAuth
	}

	// RequireDestTag
	if uSetFlag == AccountSetFlagRequireDest && (uFlagsIn&state.LsfRequireDestTag) == 0 {
		uFlagsOut |= state.LsfRequireDestTag
	}
	if uClearFlag == AccountSetFlagRequireDest && (uFlagsIn&state.LsfRequireDestTag) != 0 {
		uFlagsOut &^= state.LsfRequireDestTag
	}

	// DisallowXRP
	if uSetFlag == AccountSetFlagDisallowXRP && (uFlagsIn&state.LsfDisallowXRP) == 0 {
		uFlagsOut |= state.LsfDisallowXRP
	}
	if uClearFlag == AccountSetFlagDisallowXRP && (uFlagsIn&state.LsfDisallowXRP) != 0 {
		uFlagsOut &^= state.LsfDisallowXRP
	}

	// DisableMaster
	// Reference: rippled SetAccount.cpp:402-418
	if uSetFlag == AccountSetFlagDisableMaster && (uFlagsIn&state.LsfDisableMaster) == 0 {
		// Must use master key to disable master key.
		// Reference: rippled SetAccount.cpp:404-408
		if !ctx.SignedWithMaster {
			return tx.TecNEED_MASTER_KEY
		}
		// Account has no regular key or multi-signer signer list.
		// Reference: rippled SetAccount.cpp:410-415
		hasRegularKey := account.RegularKey != ""
		hasSignerList, _ := ctx.View.Exists(keylet.SignerList(ctx.AccountID))
		if !hasRegularKey && !hasSignerList {
			return tx.TecNO_ALTERNATIVE_KEY
		}
		uFlagsOut |= state.LsfDisableMaster
	}
	if uClearFlag == AccountSetFlagDisableMaster && (uFlagsIn&state.LsfDisableMaster) != 0 {
		uFlagsOut &^= state.LsfDisableMaster
	}

	// DefaultRipple
	if uSetFlag == AccountSetFlagDefaultRipple {
		uFlagsOut |= state.LsfDefaultRipple
	} else if uClearFlag == AccountSetFlagDefaultRipple {
		uFlagsOut &^= state.LsfDefaultRipple
	}

	// NoFreeze (cannot be cleared once set)
	// Reference: rippled SetAccount.cpp lines 444-454
	// Must be signed with master key (unless master is already disabled)
	if uSetFlag == AccountSetFlagNoFreeze {
		if !ctx.SignedWithMaster && (uFlagsIn&state.LsfDisableMaster) == 0 {
			return tx.TecNEED_MASTER_KEY
		}
		uFlagsOut |= state.LsfNoFreeze
	}

	// GlobalFreeze
	if uSetFlag == AccountSetFlagGlobalFreeze {
		uFlagsOut |= state.LsfGlobalFreeze
	}
	if uSetFlag != AccountSetFlagGlobalFreeze && uClearFlag == AccountSetFlagGlobalFreeze {
		if (uFlagsOut & state.LsfNoFreeze) == 0 {
			uFlagsOut &^= state.LsfGlobalFreeze
		}
	}

	// AccountTxnID
	if uSetFlag == AccountSetFlagAccountTxnID {
		var zeroHash [32]byte
		if account.AccountTxnID == zeroHash {
			account.AccountTxnID = ctx.TxHash
		}
	}
	if uClearFlag == AccountSetFlagAccountTxnID {
		account.AccountTxnID = [32]byte{}
	}

	// DepositAuth
	// Reference: rippled SetAccount.cpp:488-503 — gated behind featureDepositAuth
	if ctx.Rules().Enabled(amendment.FeatureDepositAuth) {
		if uSetFlag == AccountSetFlagDepositAuth {
			uFlagsOut |= state.LsfDepositAuth
		} else if uClearFlag == AccountSetFlagDepositAuth {
			uFlagsOut &^= state.LsfDepositAuth
		}
	}

	// AuthorizedNFTokenMinter
	if uSetFlag == AccountSetFlagAuthorizedNFTokenMinter {
		if a.NFTokenMinter != "" {
			account.NFTokenMinter = a.NFTokenMinter
		}
	}
	if uClearFlag == AccountSetFlagAuthorizedNFTokenMinter {
		account.NFTokenMinter = ""
	}

	// Disallow Incoming flags - gated by featureDisallowIncoming amendment
	// Reference: rippled SetAccount.cpp L630-651
	if ctx.Rules().Enabled(amendment.FeatureDisallowIncoming) {
		if uSetFlag == AccountSetFlagDisallowIncomingNFTokenOffer {
			uFlagsOut |= state.LsfDisallowIncomingNFTokenOffer
		} else if uClearFlag == AccountSetFlagDisallowIncomingNFTokenOffer {
			uFlagsOut &^= state.LsfDisallowIncomingNFTokenOffer
		}

		if uSetFlag == AccountSetFlagDisallowIncomingCheck {
			uFlagsOut |= state.LsfDisallowIncomingCheck
		} else if uClearFlag == AccountSetFlagDisallowIncomingCheck {
			uFlagsOut &^= state.LsfDisallowIncomingCheck
		}

		if uSetFlag == AccountSetFlagDisallowIncomingPayChan {
			uFlagsOut |= state.LsfDisallowIncomingPayChan
		} else if uClearFlag == AccountSetFlagDisallowIncomingPayChan {
			uFlagsOut &^= state.LsfDisallowIncomingPayChan
		}

		if uSetFlag == AccountSetFlagDisallowIncomingTrustline {
			uFlagsOut |= state.LsfDisallowIncomingTrustline
		} else if uClearFlag == AccountSetFlagDisallowIncomingTrustline {
			uFlagsOut &^= state.LsfDisallowIncomingTrustline
		}
	}

	// AllowTrustLineClawback (cannot be cleared once set, gated by amendment)
	// Reference: rippled SetAccount.cpp doApply() lines 663-668
	if ctx.Rules().Enabled(amendment.FeatureClawback) && uSetFlag == AccountSetFlagAllowTrustLineClawback {
		uFlagsOut |= state.LsfAllowTrustLineClawback
	}

	// AllowTrustLineLocking (gated by featureTokenEscrow)
	if ctx.Rules().Enabled(amendment.FeatureTokenEscrow) {
		if uSetFlag == AccountSetFlagAllowTrustLineLocking && (uFlagsIn&state.LsfAllowTrustLineLocking) == 0 {
			uFlagsOut |= state.LsfAllowTrustLineLocking
		}
		if uClearFlag == AccountSetFlagAllowTrustLineLocking && (uFlagsIn&state.LsfAllowTrustLineLocking) != 0 {
			uFlagsOut &^= state.LsfAllowTrustLineLocking
		}
	}

	// Domain
	// Reference: rippled SetAccount.cpp lines 565-579 — isFieldPresent(sfDomain)
	// If field present and empty → makeFieldAbsent; else → setFieldVL
	if a.Domain != nil {
		if *a.Domain == "" {
			account.Domain = ""
		} else {
			// Domain is stored as hex in the transaction; decode to plain text
			decoded, err := hex.DecodeString(*a.Domain)
			if err == nil {
				account.Domain = string(decoded)
			}
		}
	}

	// EmailHash
	// Reference: rippled SetAccount.cpp lines 508-522 — zero hash → makeFieldAbsent
	if a.EmailHash != "" {
		if a.EmailHash == "00000000000000000000000000000000" {
			account.EmailHash = ""
		} else {
			account.EmailHash = a.EmailHash
		}
	}

	// MessageKey
	// Reference: rippled SetAccount.cpp lines 546-560 — empty blob → makeFieldAbsent
	if a.MessageKey != nil {
		if *a.MessageKey == "" {
			account.MessageKey = ""
		} else {
			account.MessageKey = *a.MessageKey
		}
	}

	// WalletLocator
	// Reference: rippled SetAccount.cpp lines 527-541 — zero hash → makeFieldAbsent
	if a.WalletLocator != "" {
		if isZeroHash256(a.WalletLocator) {
			account.WalletLocator = ""
		} else {
			account.WalletLocator = a.WalletLocator
		}
	}

	// TransferRate
	if a.TransferRate != nil {
		rate := *a.TransferRate
		if rate != 0 && rate < qualityOne {
			return tx.TemBAD_TRANSFER_RATE
		}
		if rate > 2*qualityOne {
			return tx.TemBAD_TRANSFER_RATE
		}
		if rate == 0 || rate == qualityOne {
			account.TransferRate = 0
		} else {
			account.TransferRate = rate
		}
	}

	// TickSize
	if a.TickSize != nil {
		tickSize := *a.TickSize
		if tickSize == 0 || tickSize == protocol.TickSizeMax {
			account.TickSize = 0
		} else {
			account.TickSize = tickSize
		}
	}

	// Update flags if changed
	if uFlagsIn != uFlagsOut {
		account.Flags = uFlagsOut
	}

	return tx.TesSUCCESS
}

// ownerDirIsEmpty reports whether the account's owner directory has no
// entries. Missing, unreadable, or unparseable directories are treated as
// empty — matching rippled's dirIsEmpty semantics.
//
// An anchor (root) page may be empty of indexes while subsequent pages
// still hold entries; in that case IndexNext is non-zero and the directory
// is not empty.
//
// Reference: rippled View.cpp dirIsEmpty (lines 905-911).
func ownerDirIsEmpty(view tx.LedgerView, accountID [20]byte) bool {
	key := keylet.OwnerDir(accountID)
	exists, err := view.Exists(key)
	if err != nil || !exists {
		return true
	}
	data, err := view.Read(key)
	if err != nil {
		return true
	}
	dirNode, err := state.ParseDirectoryNode(data)
	if err != nil {
		return true
	}
	if len(dirNode.Indexes) > 0 {
		return false
	}
	return dirNode.IndexNext == 0
}
