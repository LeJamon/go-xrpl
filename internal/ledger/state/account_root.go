package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// accountRootReader is the minimal read surface ReadAccountRoot needs:
// entry existence plus a raw read by keylet. Both *ledger.Ledger and the
// full LedgerView satisfy it.
type accountRootReader interface {
	Exists(k keylet.Keylet) (bool, error)
	Read(k keylet.Keylet) ([]byte, error)
}

// ReadAccountRoot reads and parses the AccountRoot for accountID from view.
// Returns (nil, false) when the account is absent or cannot be read or
// parsed — the "look up an account, skip if it isn't there" idiom shared by
// the held-tx sweep and the TxQ preclaim path.
func ReadAccountRoot(view accountRootReader, accountID [20]byte) (*AccountRoot, bool) {
	k := keylet.Account(accountID)
	exists, err := view.Exists(k)
	if err != nil || !exists {
		return nil, false
	}
	data, err := view.Read(k)
	if err != nil {
		return nil, false
	}
	ar, err := ParseAccountRoot(data)
	if err != nil || ar == nil {
		return nil, false
	}
	return ar, true
}

// AccountRoot represents an account in the ledger
type AccountRoot struct {
	Account              string
	Balance              uint64
	Sequence             uint32
	OwnerCount           uint32
	Flags                uint32
	RegularKey           string
	Domain               string
	EmailHash            string
	MessageKey           string
	TransferRate         uint32
	TickSize             uint8
	NFTokenMinter        string   // Account allowed to mint NFTokens on behalf of this account
	MintedNFTokens       uint32   // Number of NFTokens minted by this account (issuer tracking)
	BurnedNFTokens       uint32   // Number of NFTokens burned for this issuer
	FirstNFTokenSequence uint32   // First NFToken sequence (set by fixNFTokenRemint)
	HasFirstNFTSeq       bool     // Whether FirstNFTokenSequence is set (zero is a valid value)
	AccountTxnID         [32]byte // Hash of the last transaction this account submitted (when enabled)
	HasAccountTxnID      bool     // Whether sfAccountTxnID is present (zero is a valid value after asfAccountTxnID is enabled)
	WalletLocator        string   // Arbitrary hex data (deprecated)
	TicketCount          uint32   // Number of outstanding tickets owned by this account
	AMMID                [32]byte // Links AMM pseudo-account to its AMM ledger entry (sfAMMID, fieldCode 14)
	PreviousTxnID        [32]byte
	PreviousTxnLgrSeq    uint32
}

// HasAMMID reports whether the sfAMMID field is present, the faithful equivalent
// of rippled's sleAcct->isFieldPresent(sfAMMID). AMMID is a SHA-512Half hash that
// is never zero when set, and the serializer emits it only when non-zero, so a zero
// value is the canonical representation of an absent field.
func (a *AccountRoot) HasAMMID() bool {
	return a != nil && a.AMMID != [32]byte{}
}

// IsPseudoAccount reports whether this AccountRoot is a pseudo-account, mirroring
// rippled's isPseudoAccount (View.cpp:1138) which tests whether any of the
// pseudo-account owner fields (sfAMMID, sfVaultID) is present. go-xrpl currently
// surfaces only AMMID on AccountRoot; VaultID will land alongside featureSingleAssetVault.
func (a *AccountRoot) IsPseudoAccount() bool {
	return a.HasAMMID()
}

// Field type codes (exported for use by parent tx/ package)
const (
	FieldTypeUInt16    = 1
	FieldTypeUInt32    = 2
	FieldTypeUInt64    = 3
	FieldTypeHash128   = 4
	FieldTypeHash256   = 5
	FieldTypeAmount    = 6
	FieldTypeBlob      = 7
	FieldTypeAccount   = 8
	FieldTypeAccountID = 8 // Same as Account, used in serialization
	FieldTypeObject    = 14
	FieldTypeArray     = 15
)

// STArray/STObject delimiters in the canonical binary format.
const (
	objectEndMarker = 0xE1
	arrayEndMarker  = 0xF1
)

// Field codes for AccountRoot (unexported, only used locally)
const (
	fieldCodeLedgerEntryType      = 1  // UInt16
	fieldCodeFlags                = 2  // UInt32
	fieldCodeSequence             = 4  // UInt32
	fieldCodeOwnerCount           = 13 // UInt32 (per rippled sfields.macro)
	fieldCodeTransferRate         = 11 // UInt32
	fieldCodeMintedNFTokens       = 43 // UInt32 - number of NFTokens minted
	fieldCodeBurnedNFTokens       = 44 // UInt32 - number of NFTokens burned
	fieldCodeFirstNFTokenSequence = 50 // UInt32 - first NFToken sequence (fixNFTokenRemint)
	fieldCodeRegularKey           = 8  // Account
	fieldCodeAccount              = 1  // Account (different context)
	fieldCodeNFTokenMinter        = 9  // Account - authorized NFT minter
	fieldCodeEmailHash            = 1  // Hash128
	fieldCodeDomain               = 7  // Blob
	fieldCodeTickSize             = 16 // UInt8 (type code 16)
	fieldCodeTicketCount          = 40 // UInt32 - number of outstanding tickets
	fieldCodeAccountTxnID         = 9  // Hash256 - last transaction ID
	fieldCodeWalletLocator        = 7  // Hash256 - wallet locator (deprecated)
	fieldCodeAMMID                = 14 // Hash256 - links AMM pseudo-account to AMM entry (sfAMMID)
)

// Ledger entry type code for AccountRoot (unexported)
const ledgerEntryTypeAccountRoot = uint16(entry.TypeAccountRoot)

// AccountRoot ledger entry flags.
const (
	LsfPasswordSpent                = entry.LsfPasswordSpent
	LsfRequireDestTag               = entry.LsfRequireDestTag
	LsfRequireAuth                  = entry.LsfRequireAuth
	LsfDisallowXRP                  = entry.LsfDisallowXRP
	LsfDisableMaster                = entry.LsfDisableMaster
	LsfNoFreeze                     = entry.LsfNoFreeze
	LsfGlobalFreeze                 = entry.LsfGlobalFreeze
	LsfDefaultRipple                = entry.LsfDefaultRipple
	LsfDepositAuth                  = entry.LsfDepositAuth
	LsfDisallowIncomingNFTokenOffer = entry.LsfDisallowIncomingNFTokenOffer
	LsfDisallowIncomingCheck        = entry.LsfDisallowIncomingCheck
	LsfDisallowIncomingPayChan      = entry.LsfDisallowIncomingPayChan
	LsfDisallowIncomingTrustline    = entry.LsfDisallowIncomingTrustline
	LsfAllowTrustLineLocking        = entry.LsfAllowTrustLineLocking
	LsfAllowTrustLineClawback       = entry.LsfAllowTrustLineClawback
)

// encodeAccountID encodes a 20-byte account ID to an XRPL address
func encodeAccountID(accountID [20]byte) (string, error) {
	return addresscodec.EncodeAccountIDToClassicAddress(accountID[:])
}

// ParseAccountRoot parses account data from binary format
func ParseAccountRoot(data []byte) (*AccountRoot, error) {
	if len(data) < 20 {
		return nil, errors.New("account data too short")
	}

	account := &AccountRoot{}
	verified := false

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt16:
			if f.FieldCode == fieldCodeLedgerEntryType && !verified {
				if f.UInt16() != ledgerEntryTypeAccountRoot {
					return errors.New("not an AccountRoot entry")
				}
				verified = true
			}

		case stUInt32:
			switch f.FieldCode {
			case fieldCodeFlags:
				account.Flags = f.UInt32()
			case fieldCodeSequence:
				account.Sequence = f.UInt32()
			case 5: // PreviousTxnLgrSeq
				account.PreviousTxnLgrSeq = f.UInt32()
			case fieldCodeOwnerCount:
				account.OwnerCount = f.UInt32()
			case fieldCodeTransferRate:
				account.TransferRate = f.UInt32()
			case fieldCodeMintedNFTokens:
				account.MintedNFTokens = f.UInt32()
			case fieldCodeBurnedNFTokens:
				account.BurnedNFTokens = f.UInt32()
			case fieldCodeFirstNFTokenSequence:
				account.FirstNFTokenSequence = f.UInt32()
				account.HasFirstNFTSeq = true
			case fieldCodeTicketCount:
				account.TicketCount = f.UInt32()
			}

		case stAmount:
			// AccountRoot's only Amount is the native XRP Balance (8 bytes); a
			// foreign IOU/MPT value is ignored.
			if len(f.Value) == 8 {
				account.Balance = xrpDrops(f.Value)
			}

		case stAccountID:
			if id, ok := f.AccountID(); ok {
				if addr, err := encodeAccountID(id); err == nil {
					switch f.FieldCode {
					case fieldCodeAccount:
						account.Account = addr
					case fieldCodeRegularKey:
						account.RegularKey = addr
					case fieldCodeNFTokenMinter:
						account.NFTokenMinter = addr
					}
				}
			}

		case stBlob:
			switch f.FieldCode {
			case 2: // MessageKey
				account.MessageKey = hex.EncodeToString(f.VLBytes())
			case fieldCodeDomain:
				account.Domain = string(f.VLBytes())
			}

		case stHash128:
			if f.FieldCode == fieldCodeEmailHash {
				account.EmailHash = hex.EncodeToString(f.Value)
			}

		case stHash256:
			switch f.FieldCode {
			case 5: // PreviousTxnID
				account.PreviousTxnID = f.Hash256()
			case fieldCodeAccountTxnID:
				account.AccountTxnID = f.Hash256()
				account.HasAccountTxnID = true
			case fieldCodeWalletLocator:
				account.WalletLocator = hex.EncodeToString(f.Value)
			case fieldCodeAMMID:
				account.AMMID = f.Hash256()
			}

		case stUInt8:
			if f.FieldCode == fieldCodeTickSize {
				account.TickSize = f.UInt8()
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return account, nil
}

// ParseAccountRootFromBytes parses account data from binary format (delegates to ParseAccountRoot)
func ParseAccountRootFromBytes(data []byte) (*AccountRoot, error) {
	return ParseAccountRoot(data)
}

// SerializeAccountRoot serializes an AccountRoot to binary format
func SerializeAccountRoot(account *AccountRoot) ([]byte, error) {
	// Build the JSON representation for the binary codec
	jsonObj := map[string]any{
		"LedgerEntryType": "AccountRoot",
		"Balance":         fmt.Sprintf("%d", account.Balance), // XRP balance as drops string
		"Sequence":        account.Sequence,
		"OwnerCount":      account.OwnerCount,
		"Flags":           account.Flags,
	}

	// Add Account if set
	if account.Account != "" {
		jsonObj["Account"] = account.Account
	}

	// Add TransferRate if set
	if account.TransferRate > 0 {
		jsonObj["TransferRate"] = account.TransferRate
	}

	// Add RegularKey if set
	if account.RegularKey != "" {
		jsonObj["RegularKey"] = account.RegularKey
	}

	// Add Domain if set (as hex string)
	if account.Domain != "" {
		jsonObj["Domain"] = strings.ToUpper(hex.EncodeToString([]byte(account.Domain)))
	}

	// Add EmailHash if set
	if account.EmailHash != "" {
		jsonObj["EmailHash"] = strings.ToUpper(account.EmailHash)
	}

	// Add MessageKey if set
	if account.MessageKey != "" {
		jsonObj["MessageKey"] = strings.ToUpper(account.MessageKey)
	}

	// Add NFTokenMinter if set
	if account.NFTokenMinter != "" {
		jsonObj["NFTokenMinter"] = account.NFTokenMinter
	}

	// Add MintedNFTokens if set (for NFToken issuer tracking)
	if account.MintedNFTokens > 0 {
		jsonObj["MintedNFTokens"] = account.MintedNFTokens
	}

	// Add BurnedNFTokens if set (for NFToken issuer tracking)
	if account.BurnedNFTokens > 0 {
		jsonObj["BurnedNFTokens"] = account.BurnedNFTokens
	}

	// Add FirstNFTokenSequence if set (fixNFTokenRemint amendment)
	if account.HasFirstNFTSeq {
		jsonObj["FirstNFTokenSequence"] = account.FirstNFTokenSequence
	}

	// Add TicketCount if set (number of outstanding tickets)
	if account.TicketCount > 0 {
		jsonObj["TicketCount"] = account.TicketCount
	}

	// Add AccountTxnID if present. Once asfAccountTxnID is enabled the field is
	// present even while still zero (before the account's next transaction),
	// mirroring rippled's makeFieldPresent(sfAccountTxnID). Keyed on presence,
	// not non-zero, so a present-zero value round-trips.
	var zeroHash [32]byte
	if account.HasAccountTxnID {
		jsonObj["AccountTxnID"] = strings.ToUpper(hex.EncodeToString(account.AccountTxnID[:]))
	}

	// Add WalletLocator if set
	if account.WalletLocator != "" {
		jsonObj["WalletLocator"] = strings.ToUpper(account.WalletLocator)
	}

	// Add AMMID if set (non-zero) — links AMM pseudo-account to AMM entry
	if account.AMMID != zeroHash {
		jsonObj["AMMID"] = strings.ToUpper(hex.EncodeToString(account.AMMID[:]))
	}

	// Add PreviousTxnID if set (non-zero)
	if account.PreviousTxnID != zeroHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(account.PreviousTxnID[:]))
	}

	// Add PreviousTxnLgrSeq if set
	if account.PreviousTxnLgrSeq > 0 {
		jsonObj["PreviousTxnLgrSeq"] = account.PreviousTxnLgrSeq
	}

	// Add TickSize if set (non-zero)
	if account.TickSize > 0 {
		jsonObj["TickSize"] = account.TickSize
	}

	// Encode using the binary codec
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode AccountRoot: %w", err)
	}

	// Convert hex string to bytes
	return hex.DecodeString(hexStr)
}
