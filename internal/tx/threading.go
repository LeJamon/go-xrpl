package tx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
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

// decodeAccountAddress decodes an XRPL address to a 20-byte account ID
func decodeAccountAddress(address string) *[20]byte {
	// Use base58 decoding for XRPL addresses
	decoded, err := decodeBase58Check(address)
	if err != nil || len(decoded) != 21 {
		return nil
	}

	var id [20]byte
	copy(id[:], decoded[1:21]) // Skip the version byte
	return &id
}

// decodeBase58Check decodes a base58check encoded string
func decodeBase58Check(input string) ([]byte, error) {
	const alphabet = "rpshnaf39wBUDNEGHJKLM4PQRST7VWXYZ2bcdeCg65jkm8oFqi1tuvAxyz"

	result := make([]byte, 0, len(input))

	for i := 0; i < len(input); i++ {
		c := input[i]
		digit := int64(-1)
		for j := 0; j < len(alphabet); j++ {
			if alphabet[j] == c {
				digit = int64(j)
				break
			}
		}
		if digit < 0 {
			return nil, nil // Invalid character
		}

		// Multiply result by 58 and add digit
		carry := digit
		for j := len(result) - 1; j >= 0; j-- {
			carry += int64(result[j]) * 58
			result[j] = byte(carry & 0xff)
			carry >>= 8
		}
		for carry > 0 {
			result = append([]byte{byte(carry & 0xff)}, result...)
			carry >>= 8
		}
	}

	// Add leading zeros
	for i := 0; i < len(input) && input[i] == alphabet[0]; i++ {
		result = append([]byte{0}, result...)
	}

	// Verify checksum (last 4 bytes)
	if len(result) < 5 {
		return nil, nil
	}

	payload := result[:len(result)-4]
	checksum := result[len(result)-4:]

	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	if h2[0] != checksum[0] || h2[1] != checksum[1] || h2[2] != checksum[2] || h2[3] != checksum[3] {
		return nil, fmt.Errorf("invalid base58check checksum")
	}

	return payload, nil
}
