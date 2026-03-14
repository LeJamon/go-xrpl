package state

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
)

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
	WalletLocator        string   // Arbitrary hex data (deprecated)
	TicketCount          uint32   // Number of outstanding tickets owned by this account
	AMMID                [32]byte // Links AMM pseudo-account to its AMM ledger entry (sfAMMID, fieldCode 14)
	PreviousTxnID        [32]byte
	PreviousTxnLgrSeq    uint32
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
	fieldCodeBalance              = 1  // Amount
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
const ledgerEntryTypeAccountRoot = 0x0061

// AccountRoot ledger entry flags (exported for external use)
const (
	// LsfPasswordSpent indicates the account has spent the free transaction
	LsfPasswordSpent uint32 = 0x00010000

	// LsfRequireDestTag indicates the account requires a destination tag
	LsfRequireDestTag uint32 = 0x00020000

	// LsfRequireAuth indicates the account requires authorization for trust lines
	LsfRequireAuth uint32 = 0x00040000

	// LsfDisallowXRP indicates the account does not want to receive XRP
	// Per rippled, this flag means non-direct XRP payments are rejected
	LsfDisallowXRP uint32 = 0x00080000

	// LsfDisableMaster indicates the master key is disabled
	LsfDisableMaster uint32 = 0x00100000

	// LsfNoFreeze indicates the account has permanently given up the ability to freeze trust lines
	// Once set, cannot be cleared
	LsfNoFreeze uint32 = 0x00200000

	// LsfGlobalFreeze indicates this account has globally frozen all trust lines
	LsfGlobalFreeze uint32 = 0x00400000

	// LsfDefaultRipple indicates rippling is enabled by default on trust lines
	LsfDefaultRipple uint32 = 0x00800000

	// LsfDepositAuth indicates the account requires deposit authorization
	// Payments to this account require preauthorization unless both the
	// destination balance and payment amount are at or below the base reserve
	LsfDepositAuth uint32 = 0x01000000

	// LsfAMM indicates the account is an AMM (pseudo-account)
	// AMM accounts cannot receive direct payments
	LsfAMM uint32 = 0x02000000

	// LsfDisallowIncomingNFTokenOffer disallows incoming NFToken offers
	LsfDisallowIncomingNFTokenOffer uint32 = 0x04000000

	// LsfDisallowIncomingCheck disallows incoming checks
	LsfDisallowIncomingCheck uint32 = 0x08000000

	// LsfDisallowIncomingPayChan disallows incoming payment channels
	LsfDisallowIncomingPayChan uint32 = 0x10000000

	// LsfDisallowIncomingTrustline disallows incoming trust lines
	LsfDisallowIncomingTrustline uint32 = 0x20000000

	// LsfAllowTrustLineClawback allows clawback on issued currencies
	// Once set, this flag CANNOT be cleared
	LsfAllowTrustLineClawback uint32 = 0x80000000
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
	ledgerEntryTypeVerified := false // Track if we've verified the LedgerEntryType

	// Parse the binary format
	// XRPL uses a TLV-like format with field headers
	offset := 0

	for offset < len(data) {
		if offset+1 > len(data) {
			break
		}

		// Read field header
		header := data[offset]
		offset++

		// Decode type and field from header
		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		// Handle extended type codes
		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		// Handle extended field codes
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		// Parse field based on type
		switch typeCode {
		case FieldTypeUInt16:
			if offset+2 > len(data) {
				return account, nil
			}
			value := binary.BigEndian.Uint16(data[offset : offset+2])
			offset += 2
			// Only check LedgerEntryType once (first UInt16 with fieldCode 1)
			if fieldCode == fieldCodeLedgerEntryType && !ledgerEntryTypeVerified {
				// LedgerEntryType - verify it's AccountRoot
				if value != ledgerEntryTypeAccountRoot {
					return nil, errors.New("not an AccountRoot entry")
				}
				ledgerEntryTypeVerified = true
			}

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return account, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case fieldCodeFlags:
				account.Flags = value
			case fieldCodeSequence:
				account.Sequence = value
			case 5: // PreviousTxnLgrSeq
				account.PreviousTxnLgrSeq = value
			case fieldCodeOwnerCount:
				account.OwnerCount = value
			case fieldCodeTransferRate:
				account.TransferRate = value
			case fieldCodeMintedNFTokens:
				account.MintedNFTokens = value
			case fieldCodeBurnedNFTokens:
				account.BurnedNFTokens = value
			case fieldCodeFirstNFTokenSequence:
				account.FirstNFTokenSequence = value
				account.HasFirstNFTSeq = true
			case fieldCodeTicketCount:
				account.TicketCount = value
			}

		case FieldTypeAmount:
			// XRP amounts are 8 bytes, IOU amounts are 48 bytes
			if offset+8 > len(data) {
				return account, nil
			}
			// Check if it's XRP (first bit is 0) or IOU (first bit is 1)
			if data[offset]&0x80 == 0 {
				// XRP amount - 8 bytes
				// The format is: top bit = 0 for XRP, next bit = positive, remaining 62 bits = drops
				rawAmount := binary.BigEndian.Uint64(data[offset : offset+8])
				// Clear the top bit and extract drops
				account.Balance = rawAmount & 0x3FFFFFFFFFFFFFFF
				offset += 8
			} else {
				// IOU amount - skip 48 bytes (we don't handle this in AccountRoot)
				offset += 48
			}

		case FieldTypeAccount:
			// Account IDs are variable length encoded
			if offset >= len(data) {
				return account, nil
			}
			length := int(data[offset])
			offset++
			if offset+length > len(data) {
				return account, nil
			}
			if length == 20 {
				var accID [20]byte
				copy(accID[:], data[offset:offset+length])
				addr, err := encodeAccountID(accID)
				if err == nil {
					if fieldCode == 1 {
						account.Account = addr
					} else if fieldCode == fieldCodeRegularKey {
						account.RegularKey = addr
					} else if fieldCode == fieldCodeNFTokenMinter {
						account.NFTokenMinter = addr
					}
				}
			}
			offset += length

		case FieldTypeBlob:
			// Variable length blob — XRPL VL encoding
			if offset >= len(data) {
				return account, nil
			}
			byte1 := int(data[offset])
			offset++
			var length int
			if byte1 <= 192 {
				// Single-byte encoding: length = byte1
				length = byte1
			} else if byte1 <= 240 {
				// Two-byte encoding: length = 193 + ((byte1 - 193) * 256) + byte2
				if offset >= len(data) {
					return account, nil
				}
				byte2 := int(data[offset])
				offset++
				length = 193 + (byte1-193)*256 + byte2
			} else {
				// Three-byte encoding: length = 12481 + ((byte1 - 241) * 65536) + (byte2 * 256) + byte3
				if offset+2 > len(data) {
					return account, nil
				}
				byte2 := int(data[offset])
				byte3 := int(data[offset+1])
				offset += 2
				length = 12481 + (byte1-241)*65536 + byte2*256 + byte3
			}
			if offset+length > len(data) {
				return account, nil
			}
			switch fieldCode {
			case 2: // MessageKey field
				account.MessageKey = hex.EncodeToString(data[offset : offset+length])
			case 7: // Domain field
				account.Domain = string(data[offset : offset+length])
			}
			offset += length

		case FieldTypeHash128:
			if offset+16 > len(data) {
				return account, nil
			}
			if fieldCode == fieldCodeEmailHash {
				account.EmailHash = hex.EncodeToString(data[offset : offset+16])
			}
			offset += 16

		case FieldTypeHash256:
			// Hash256 fields (e.g., PreviousTxnID, AccountTxnID, WalletLocator, AMMID) are 32 bytes
			if offset+32 > len(data) {
				return account, nil
			}
			switch fieldCode {
			case 5: // PreviousTxnID
				copy(account.PreviousTxnID[:], data[offset:offset+32])
			case fieldCodeAccountTxnID: // AccountTxnID
				copy(account.AccountTxnID[:], data[offset:offset+32])
			case fieldCodeWalletLocator: // WalletLocator
				account.WalletLocator = hex.EncodeToString(data[offset : offset+32])
			case fieldCodeAMMID: // AMMID - links AMM pseudo-account to AMM entry
				copy(account.AMMID[:], data[offset:offset+32])
			}
			offset += 32

		case 16: // UInt8
			if offset+1 > len(data) {
				return account, nil
			}
			value := data[offset]
			offset++
			if fieldCode == fieldCodeTickSize {
				account.TickSize = value
			}

		default:
			// Unknown type - can't determine size, must stop parsing
			// This shouldn't happen for valid AccountRoot entries
			return account, nil
		}
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

	// Add AccountTxnID if set (non-zero)
	var zeroHash [32]byte
	if account.AccountTxnID != zeroHash {
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
