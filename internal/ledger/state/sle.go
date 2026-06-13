package state

import (
	"encoding/binary"
	"errors"
)

// IsDefaultValue reports whether a decoded field value should be omitted
// from CreatedNode.NewFields metadata. Used by the generic (non-typed)
// metadata path in internal/tx/apply_state_table.go; the typed path in
// internal/tx/ledgerfields filters defaults per-field at codegen time.
func IsDefaultValue(value any) bool {
	if value == nil {
		return true
	}
	switch v := value.(type) {
	case int:
		return v == 0
	case int64:
		return v == 0
	case uint32:
		return v == 0
	case uint64:
		return v == 0
	case float64:
		return v == 0
	case string:
		if v == "" || v == "0" {
			return true
		}
		if isAllZeroHex(v) {
			return true
		}
		return false
	case []byte:
		return len(v) == 0
	case [32]byte:
		var zero [32]byte
		return v == zero
	case map[string]any:
		// IOU/MPT amounts carry currency/issuer even when value is zero;
		// they're never default once serialized.
		return false
	}
	return false
}

// isAllZeroHex reports whether s is a non-empty string consisting entirely
// of '0' characters — i.e. the hex representation of a zero hash.
func isAllZeroHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c != '0' {
			return false
		}
	}
	return true
}

// fieldCodeOwnerNode is the field code of sfOwnerNode (a UInt64). With the
// UInt64 type code (3) it forms the field header byte 0x34.
const fieldCodeOwnerNode = 4

// GetOwnerNode extracts the OwnerNode (sfOwnerNode: UInt64, field code 4) from
// raw binary SLE data by walking the serialized fields with their typed widths.
// Returns 0 if the field is absent. Used by DirRemove to locate the directory
// page when erasing a ledger entry.
//
// A blind scan for the header byte 0x34 is unsafe: that byte also occurs inside
// the value of an earlier field (e.g. a TicketSequence of 52 serializes as
// 0x00000034), which would return a garbage page hint and orphan the entry's
// directory record. Only a width-correct field walk extracts it reliably.
func GetOwnerNode(data []byte) uint64 {
	offset := 0
	for offset < len(data) {
		typeCode, fieldCode, newOffset, ok := parseFieldHeader(data, offset)
		if !ok {
			break
		}
		offset = newOffset
		if typeCode == FieldTypeUInt64 && fieldCode == fieldCodeOwnerNode {
			if offset+8 > len(data) {
				break
			}
			return binary.BigEndian.Uint64(data[offset : offset+8])
		}
		offset = skipFieldValue(data, offset, typeCode)
		if offset < 0 {
			break
		}
	}
	return 0
}

// GetLedgerEntryType extracts the LedgerEntryType (UInt16, field code 1)
// from raw binary SLE data without a full codec decode. XRPL serialization
// always places this field first with header byte 0x11.
func GetLedgerEntryType(data []byte) (uint16, error) {
	if len(data) < 3 {
		return 0, errors.New("data too short to contain LedgerEntryType")
	}
	if data[0] != 0x11 {
		return 0, errors.New("unexpected header byte, expected 0x11 for LedgerEntryType")
	}
	return binary.BigEndian.Uint16(data[1:3]), nil
}
