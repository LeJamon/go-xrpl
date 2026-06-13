package tx

import (
	"encoding/hex"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// Threading types conditional on fixPreviousTxnID amendment
// These types only support threading if the amendment is enabled
var conditionalThreadingTypes = map[string]bool{
	"DirectoryNode": true,
	"Amendments":    true,
	"FeeSettings":   true,
	"NegativeUNL":   true,
	"AMM":           true,
}

// Types that do NOT support threading (no PreviousTxnID field)
var nonThreadedTypes = map[string]bool{
	"LedgerHashes": true,
}

// isThreadedType determines if an entry type supports transaction threading
// An entry is threaded if it has PreviousTxnID/PreviousTxnLgrSeq fields
// Some types are conditional on the fixPreviousTxnID amendment
func isThreadedType(entryType string, fixPreviousTxnIDEnabled bool) bool {
	// Non-threaded types never support threading
	if nonThreadedTypes[entryType] {
		return false
	}

	// Conditional types require amendment
	if conditionalThreadingTypes[entryType] {
		return fixPreviousTxnIDEnabled
	}

	// All other types with PreviousTxnID field are threaded
	return true
}

// threadItem updates PreviousTxnID and PreviousTxnLgrSeq on the entry
// Returns the previous values for metadata inclusion
// The entry data is modified in place
func threadItem(data []byte, txHash [32]byte, ledgerSeq uint32) (prevTxnID [32]byte, prevLgrSeq uint32, newData []byte, changed bool) {
	// Decode the current entry to get existing values
	hexStr := hex.EncodeToString(data)
	fields, err := binarycodec.Decode(hexStr)
	if err != nil {
		return prevTxnID, prevLgrSeq, data, false
	}

	// Get current PreviousTxnID and PreviousTxnLgrSeq
	if v, ok := fields["PreviousTxnID"].(string); ok {
		decoded, _ := hex.DecodeString(v)
		if len(decoded) == 32 {
			copy(prevTxnID[:], decoded)
		}
	}
	if v, ok := fields["PreviousTxnLgrSeq"].(uint32); ok {
		prevLgrSeq = v
	} else if v, ok := fields["PreviousTxnLgrSeq"].(float64); ok {
		prevLgrSeq = uint32(v)
	} else if v, ok := fields["PreviousTxnLgrSeq"].(int); ok {
		prevLgrSeq = uint32(v)
	}

	// Check if already threaded to this transaction
	if prevTxnID == txHash {
		return prevTxnID, prevLgrSeq, data, false
	}

	// Update with new transaction info
	fields["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(txHash[:]))
	fields["PreviousTxnLgrSeq"] = ledgerSeq

	// Re-encode the entry
	newHex, err := binarycodec.Encode(fields)
	if err != nil {
		return prevTxnID, prevLgrSeq, data, false
	}

	newData, err = hex.DecodeString(newHex)
	if err != nil {
		return prevTxnID, prevLgrSeq, data, false
	}

	return prevTxnID, prevLgrSeq, newData, true
}

// getOwnerAccounts returns the account IDs that own this ledger entry.
// These accounts should have their PreviousTxnID/PreviousTxnLgrSeq updated.
// fixCheckThreading gates whether Check entries thread to their Destination.
// Reference: rippled ApplyStateTable.cpp threadOwners() lines 659-695.
func getOwnerAccounts(data []byte, entryType string, fixCheckThreading bool) [][20]byte {
	var owners [][20]byte

	// Decode the entry
	hexStr := hex.EncodeToString(data)
	fields, err := binarycodec.Decode(hexStr)
	if err != nil {
		return owners
	}

	switch entryType {
	case "AccountRoot":
		// AccountRoot is the owner itself, no additional owners to thread
		return owners

	case "RippleState":
		// Thread to both accounts in the trust line
		// LowLimit and HighLimit contain issuer (account) info
		if lowLimit, ok := fields["LowLimit"].(map[string]any); ok {
			if issuer, ok := lowLimit["issuer"].(string); ok {
				if id := decodeAccountAddress(issuer); id != nil {
					owners = append(owners, *id)
				}
			}
		}
		if highLimit, ok := fields["HighLimit"].(map[string]any); ok {
			if issuer, ok := highLimit["issuer"].(string); ok {
				if id := decodeAccountAddress(issuer); id != nil {
					owners = append(owners, *id)
				}
			}
		}
		return owners

	default:
		// For most types: Account field (primary owner)
		if account, ok := fields["Account"].(string); ok {
			if id := decodeAccountAddress(account); id != nil {
				owners = append(owners, *id)
			}
		}

		// Don't thread a Check's Destination unless fixCheckThreading is enabled.
		// Reference: rippled ApplyStateTable.cpp lines 686-689
		if entryType == "Check" && !fixCheckThreading {
			return owners
		}

		// Destination field (secondary owner) for types that have it
		// Check (with amendment), Escrow, PayChannel, etc.
		if dest, ok := fields["Destination"].(string); ok {
			if id := decodeAccountAddress(dest); id != nil {
				owners = append(owners, *id)
			}
		}

		return owners
	}
}

// decodeAccountAddress decodes an XRPL classic address to a 20-byte account ID,
// returning nil when the address is malformed (the callers treat a nil result
// as "no owner to thread").
func decodeAccountAddress(address string) *[20]byte {
	id, err := state.DecodeAccountID(address)
	if err != nil {
		return nil
	}
	return &id
}
