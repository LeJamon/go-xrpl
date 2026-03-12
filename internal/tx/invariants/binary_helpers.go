package invariants

import (
	"encoding/binary"
	"fmt"
)

// skipFieldBytes returns the number of bytes to skip for a field given typeCode, fieldCode, and remaining data.
func skipFieldBytes(typeCode, fieldCode int, data []byte, offset int) (int, bool) {
	switch typeCode {
	case 1: // UInt16
		return 2, offset+2 <= len(data)
	case 2: // UInt32
		return 4, offset+4 <= len(data)
	case 3: // UInt64
		return 8, offset+8 <= len(data)
	case 4: // Hash128
		return 16, offset+16 <= len(data)
	case 5: // Hash256
		return 32, offset+32 <= len(data)
	case 6: // Amount (handled above, shouldn't reach here)
		return 0, false
	case 7: // Blob (variable length)
		if offset >= len(data) {
			return 0, false
		}
		length := int(data[offset])
		extra := 1
		if length > 192 {
			if offset+1 >= len(data) {
				return 0, false
			}
			length = 193 + ((length-193)<<8 | int(data[offset+1]))
			extra = 2
		}
		return extra + length, offset+extra+length <= len(data)
	case 8: // AccountID
		return 20, offset+20 <= len(data)
	case 14: // STObject end marker
		return 0, true
	case 15: // STArray end marker
		return 0, true
	default:
		return 0, false
	}
}

// isXRPCurrency returns true if the given currency bytes represent XRP.
// XRP currency is either all-zeros or the ASCII bytes "XRP" at position 12.
func isXRPCurrency(curr string) bool {
	if len(curr) == 0 || curr == "XRP" {
		return true
	}
	// Hex-encoded currency: 40 hex chars = 20 bytes
	if len(curr) == 40 {
		b, err := hexDecode20(curr)
		if err != nil {
			return false
		}
		// All zeros = XRP
		allZero := true
		for _, bb := range b {
			if bb != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return true
		}
		// Check "XRP" at bytes 12-14
		if b[12] == 'X' && b[13] == 'R' && b[14] == 'P' {
			return true
		}
	}
	return false
}

func hexDecode20(s string) ([20]byte, error) {
	var b [20]byte
	if len(s) != 40 {
		return b, fmt.Errorf("expected 40 hex chars, got %d", len(s))
	}
	for i := 0; i < 20; i++ {
		hi := hexVal(s[i*2])
		lo := hexVal(s[i*2+1])
		if hi < 0 || lo < 0 {
			return b, fmt.Errorf("invalid hex char")
		}
		b[i] = byte(hi<<4 | lo)
	}
	return b, nil
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// getLedgerEntryType extracts the ledger entry type from binary data.
// This is a copy of the function in apply_state_table.go, needed here
// to avoid a circular import between invariants and tx packages.
func getLedgerEntryType(data []byte) string {
	if len(data) < 4 {
		return "Unknown"
	}

	// Parse the binary to find LedgerEntryType field
	// LedgerEntryType is a UInt16 with type code 1 and field code 1
	// Header byte: 0x11 (type 1, field 1)
	offset := 0
	for offset < len(data)-2 {
		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		// Check for LedgerEntryType (type 1 = UInt16, field 1)
		if typeCode == 1 && fieldCode == 1 {
			if offset+2 > len(data) {
				break
			}
			entryType := binary.BigEndian.Uint16(data[offset : offset+2])
			return ledgerEntryTypeName(entryType)
		}

		// Skip field value based on type
		switch typeCode {
		case 1: // UInt16
			offset += 2
		case 2: // UInt32
			offset += 4
		case 3: // UInt64
			offset += 8
		case 4: // Hash128
			offset += 16
		case 5: // Hash256
			offset += 32
		case 6: // Amount
			if offset < len(data) && (data[offset]&0x80) == 0 {
				offset += 8 // XRP
			} else {
				offset += 48 // IOU
			}
		case 7: // Blob (variable length)
			if offset >= len(data) {
				return "Unknown"
			}
			length := int(data[offset])
			offset++
			if length > 192 {
				if offset >= len(data) {
					return "Unknown"
				}
				length = 193 + ((length-193)<<8 | int(data[offset]))
				offset++
			}
			offset += length
		case 8: // AccountID (variable-length encoded with length prefix)
			// Reference: rippled STAccount.cpp uses addVL() — 1-byte length prefix + data
			if offset >= len(data) {
				return "Unknown"
			}
			length := int(data[offset])
			offset++
			if length > 192 {
				if offset >= len(data) {
					return "Unknown"
				}
				length = 193 + ((length-193)<<8 | int(data[offset]))
				offset++
			}
			offset += length
		default:
			// Unknown type, can't continue
			return "Unknown"
		}
	}

	return "Unknown"
}

// ledgerEntryTypeName converts entry type code to name.
// Based on rippled's ledger_entries.macro.
// This is a copy of the function in apply_state_table.go, needed here
// to avoid a circular import between invariants and tx packages.
func ledgerEntryTypeName(code uint16) string {
	switch code {
	// Active ledger entry types (from rippled ledger_entries.macro)
	case 0x0037:
		return "NFTokenOffer"
	case 0x0043:
		return "Check"
	case 0x0049:
		return "DID"
	case 0x004e:
		return "NegativeUNL"
	case 0x0050:
		return "NFTokenPage"
	case 0x0053:
		return "SignerList"
	case 0x0054:
		return "Ticket"
	case 0x0061:
		return "AccountRoot"
	case 0x0063:
		return "Contract" // deprecated
	case 0x0064:
		return "DirectoryNode"
	case 0x0066:
		return "Amendments"
	case 0x0068:
		return "LedgerHashes"
	case 0x0069:
		return "Bridge"
	case 0x006e:
		return "Nickname" // deprecated
	case 0x006f:
		return "Offer"
	case 0x0070:
		return "DepositPreauth"
	case 0x0071:
		return "XChainOwnedClaimID"
	case 0x0072:
		return "RippleState"
	case 0x0073:
		return "FeeSettings"
	case 0x0074:
		return "XChainOwnedCreateAccountClaimID"
	case 0x0075:
		return "Escrow"
	case 0x0078:
		return "PayChannel"
	case 0x0079:
		return "AMM"
	case 0x007e:
		return "MPTokenIssuance"
	case 0x007f:
		return "MPToken"
	case 0x0080:
		return "Oracle"
	case 0x0081:
		return "Credential"
	case 0x0082:
		return "PermissionedDomain"
	case 0x0083:
		return "Delegate"
	case 0x0084:
		return "Vault"
	default:
		return fmt.Sprintf("Unknown(0x%04x)", code)
	}
}
