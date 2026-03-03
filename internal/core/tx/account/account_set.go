package account

import (
	"encoding/hex"
	"errors"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
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

func init() {
	tx.Register(tx.TypeAccountSet, func() tx.Transaction {
		return &AccountSet{BaseTx: *tx.NewBaseTx(tx.TypeAccountSet, "")}
	})
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
)

// NewAccountSet creates a new AccountSet transaction
func NewAccountSet(account string) *AccountSet {
	return &AccountSet{
		BaseTx: *tx.NewBaseTx(tx.TypeAccountSet, account),
	}
}

// TxType returns the transaction type
func (a *AccountSet) TxType() tx.Type {
	return tx.TypeAccountSet
}

// Validate validates the AccountSet transaction
// Reference: rippled SetAccount.cpp preflight()
func (a *AccountSet) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	txFlags := a.GetFlags()

	// Check for invalid transaction flags
	// Reference: rippled SetAccount.cpp:71-75
	if txFlags&AccountSetTxFlagMask != 0 {
		return errors.New("temINVALID_FLAG: invalid transaction flags")
	}

	// Cannot set and clear the same flag
	// Reference: rippled SetAccount.cpp:80-84
	if a.SetFlag != nil && a.ClearFlag != nil && *a.SetFlag == *a.ClearFlag {
		return errors.New("temINVALID_FLAG: cannot set and clear the same flag")
	}

	// Check for contradictory RequireAuth flags
	// Reference: rippled SetAccount.cpp:89-98
	setRequireAuth := (txFlags&AccountSetTxFlagRequireAuth != 0) ||
		(a.SetFlag != nil && *a.SetFlag == AccountSetFlagRequireAuth)
	clearRequireAuth := (txFlags&AccountSetTxFlagOptionalAuth != 0) ||
		(a.ClearFlag != nil && *a.ClearFlag == AccountSetFlagRequireAuth)
	if setRequireAuth && clearRequireAuth {
		return errors.New("temINVALID_FLAG: contradictory RequireAuth flags")
	}

	// Check for contradictory RequireDest flags
	// Reference: rippled SetAccount.cpp:103-112
	setRequireDest := (txFlags&AccountSetTxFlagRequireDestTag != 0) ||
		(a.SetFlag != nil && *a.SetFlag == AccountSetFlagRequireDest)
	clearRequireDest := (txFlags&AccountSetTxFlagOptionalDestTag != 0) ||
		(a.ClearFlag != nil && *a.ClearFlag == AccountSetFlagRequireDest)
	if setRequireDest && clearRequireDest {
		return errors.New("temINVALID_FLAG: contradictory RequireDest flags")
	}

	// Check for contradictory DisallowXRP flags
	// Reference: rippled SetAccount.cpp:117-126
	setDisallowXRP := (txFlags&AccountSetTxFlagDisallowXRP != 0) ||
		(a.SetFlag != nil && *a.SetFlag == AccountSetFlagDisallowXRP)
	clearDisallowXRP := (txFlags&AccountSetTxFlagAllowXRP != 0) ||
		(a.ClearFlag != nil && *a.ClearFlag == AccountSetFlagDisallowXRP)
	if setDisallowXRP && clearDisallowXRP {
		return errors.New("temINVALID_FLAG: contradictory DisallowXRP flags")
	}

	// TransferRate validation
	// Reference: rippled SetAccount.cpp:129-146
	if a.TransferRate != nil {
		tr := *a.TransferRate
		if tr != 0 && tr < 1000000000 {
			return errors.New("temBAD_TRANSFER_RATE: transfer rate too small")
		}
		if tr > 2000000000 {
			return errors.New("temBAD_TRANSFER_RATE: transfer rate too large")
		}
	}

	// TickSize validation
	// Reference: rippled SetAccount.cpp:149-159
	if a.TickSize != nil {
		ts := *a.TickSize
		if ts != 0 && (ts < 3 || ts > 15) {
			return errors.New("temBAD_TICK_SIZE: tick size must be 0 or 3-15")
		}
	}

	// MessageKey validation
	// Reference: rippled SetAccount.cpp lines 161-168
	// If present and non-empty, must be a valid public key (ed25519 or secp256k1).
	if a.MessageKey != nil && *a.MessageKey != "" {
		mkBytes, err := hex.DecodeString(*a.MessageKey)
		if err != nil || !isValidPublicKey(mkBytes) {
			return errors.New("telBAD_PUBLIC_KEY: invalid message key specified")
		}
	}

	// Domain length validation
	// Reference: rippled SetAccount.cpp:170-175
	// Domain is stored as hex, so max hex length is 2*256 = 512
	if a.Domain != nil && len(*a.Domain) > MaxDomainLength*2 {
		return errors.New("telBAD_DOMAIN: domain too long")
	}

	// NFTokenMinter validation
	// Reference: rippled SetAccount.cpp:177-187
	if a.SetFlag != nil && *a.SetFlag == AccountSetFlagAuthorizedNFTokenMinter {
		if a.NFTokenMinter == "" {
			return errors.New("temMALFORMED: NFTokenMinter required when setting asfAuthorizedNFTokenMinter")
		}
	}
	if a.ClearFlag != nil && *a.ClearFlag == AccountSetFlagAuthorizedNFTokenMinter {
		if a.NFTokenMinter != "" {
			return errors.New("temMALFORMED: NFTokenMinter must be empty when clearing asfAuthorizedNFTokenMinter")
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
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

// Apply applies the AccountSet transaction to ledger state.
func (a *AccountSet) Apply(ctx *tx.ApplyContext) tx.Result {
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
			// Cannot set clawback if NoFreeze is already set
			if (uFlagsIn & sle.LsfNoFreeze) != 0 {
				return tx.TecNO_PERMISSION
			}
			// Owner directory must be empty
			ownerDirKey := keylet.OwnerDir(ctx.AccountID)
			dirExists, dirErr := ctx.View.Exists(ownerDirKey)
			if dirErr == nil && dirExists {
				dirData, readErr := ctx.View.Read(ownerDirKey)
				if readErr == nil {
					dirNode, parseErr := sle.ParseDirectoryNode(dirData)
					if parseErr == nil && len(dirNode.Indexes) > 0 {
						return tx.TecOWNERS
					}
				}
			}
		}
		if uSetFlag == AccountSetFlagNoFreeze {
			// Cannot set NoFreeze if clawback is already set
			if (uFlagsIn & sle.LsfAllowTrustLineClawback) != 0 {
				return tx.TecNO_PERMISSION
			}
		}
	}

	// RequireAuth
	// Reference: rippled SetAccount.cpp preclaim() lines 269-276
	// dirIsEmpty() checks whether the owner directory has any entries.
	bSetRequireAuth := (a.GetFlags()&AccountSetTxFlagRequireAuth != 0) ||
		uSetFlag == AccountSetFlagRequireAuth
	if bSetRequireAuth && (uFlagsIn&sle.LsfRequireAuth) == 0 {
		// Owner directory must be empty to set RequireAuth
		ownerDirKey := keylet.OwnerDir(ctx.AccountID)
		dirExists, dirErr := ctx.View.Exists(ownerDirKey)
		if dirErr == nil && dirExists {
			dirData, readErr := ctx.View.Read(ownerDirKey)
			if readErr == nil {
				dirNode, parseErr := sle.ParseDirectoryNode(dirData)
				if parseErr == nil && len(dirNode.Indexes) > 0 {
					return tx.TecOWNERS
				}
			}
		}
		uFlagsOut |= sle.LsfRequireAuth
	}
	if uClearFlag == AccountSetFlagRequireAuth && (uFlagsIn&sle.LsfRequireAuth) != 0 {
		uFlagsOut &^= sle.LsfRequireAuth
	}

	// RequireDestTag
	if uSetFlag == AccountSetFlagRequireDest && (uFlagsIn&sle.LsfRequireDestTag) == 0 {
		uFlagsOut |= sle.LsfRequireDestTag
	}
	if uClearFlag == AccountSetFlagRequireDest && (uFlagsIn&sle.LsfRequireDestTag) != 0 {
		uFlagsOut &^= sle.LsfRequireDestTag
	}

	// DisallowXRP
	if uSetFlag == AccountSetFlagDisallowXRP && (uFlagsIn&sle.LsfDisallowXRP) == 0 {
		uFlagsOut |= sle.LsfDisallowXRP
	}
	if uClearFlag == AccountSetFlagDisallowXRP && (uFlagsIn&sle.LsfDisallowXRP) != 0 {
		uFlagsOut &^= sle.LsfDisallowXRP
	}

	// DisableMaster
	// Reference: rippled SetAccount.cpp:402-418
	if uSetFlag == AccountSetFlagDisableMaster && (uFlagsIn&sle.LsfDisableMaster) == 0 {
		// Account has no regular key or multi-signer signer list.
		// Reference: rippled SetAccount.cpp:410-415
		hasRegularKey := account.RegularKey != ""
		hasSignerList, _ := ctx.View.Exists(keylet.SignerList(ctx.AccountID))
		if !hasRegularKey && !hasSignerList {
			return tx.TecNO_ALTERNATIVE_KEY
		}
		uFlagsOut |= sle.LsfDisableMaster
	}
	if uClearFlag == AccountSetFlagDisableMaster && (uFlagsIn&sle.LsfDisableMaster) != 0 {
		uFlagsOut &^= sle.LsfDisableMaster
	}

	// DefaultRipple
	if uSetFlag == AccountSetFlagDefaultRipple {
		uFlagsOut |= sle.LsfDefaultRipple
	} else if uClearFlag == AccountSetFlagDefaultRipple {
		uFlagsOut &^= sle.LsfDefaultRipple
	}

	// NoFreeze (cannot be cleared once set)
	// Reference: rippled SetAccount.cpp lines 444-454
	// Must be signed with master key (unless master is already disabled)
	if uSetFlag == AccountSetFlagNoFreeze {
		if !ctx.SignedWithMaster && (uFlagsIn&sle.LsfDisableMaster) == 0 {
			return tx.TecNEED_MASTER_KEY
		}
		uFlagsOut |= sle.LsfNoFreeze
	}

	// GlobalFreeze
	if uSetFlag == AccountSetFlagGlobalFreeze {
		uFlagsOut |= sle.LsfGlobalFreeze
	}
	if uSetFlag != AccountSetFlagGlobalFreeze && uClearFlag == AccountSetFlagGlobalFreeze {
		if (uFlagsOut & sle.LsfNoFreeze) == 0 {
			uFlagsOut &^= sle.LsfGlobalFreeze
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
			uFlagsOut |= sle.LsfDepositAuth
		} else if uClearFlag == AccountSetFlagDepositAuth {
			uFlagsOut &^= sle.LsfDepositAuth
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
			uFlagsOut |= sle.LsfDisallowIncomingNFTokenOffer
		} else if uClearFlag == AccountSetFlagDisallowIncomingNFTokenOffer {
			uFlagsOut &^= sle.LsfDisallowIncomingNFTokenOffer
		}

		if uSetFlag == AccountSetFlagDisallowIncomingCheck {
			uFlagsOut |= sle.LsfDisallowIncomingCheck
		} else if uClearFlag == AccountSetFlagDisallowIncomingCheck {
			uFlagsOut &^= sle.LsfDisallowIncomingCheck
		}

		if uSetFlag == AccountSetFlagDisallowIncomingPayChan {
			uFlagsOut |= sle.LsfDisallowIncomingPayChan
		} else if uClearFlag == AccountSetFlagDisallowIncomingPayChan {
			uFlagsOut &^= sle.LsfDisallowIncomingPayChan
		}

		if uSetFlag == AccountSetFlagDisallowIncomingTrustline {
			uFlagsOut |= sle.LsfDisallowIncomingTrustline
		} else if uClearFlag == AccountSetFlagDisallowIncomingTrustline {
			uFlagsOut &^= sle.LsfDisallowIncomingTrustline
		}
	}

	// AllowTrustLineClawback (cannot be cleared once set, gated by amendment)
	// Reference: rippled SetAccount.cpp doApply() lines 663-668
	if ctx.Rules().Enabled(amendment.FeatureClawback) && uSetFlag == AccountSetFlagAllowTrustLineClawback {
		uFlagsOut |= sle.LsfAllowTrustLineClawback
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
		if tickSize == 0 || tickSize == 15 {
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
