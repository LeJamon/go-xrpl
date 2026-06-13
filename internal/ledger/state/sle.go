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

// GetOwnerNode extracts the OwnerNode (UInt64 type=3, field=4) from raw
// binary SLE data. Returns 0 if the field is absent or the data is malformed.
// Used by DirRemove callers to find the right directory page when erasing a
// ledger entry.
func GetOwnerNode(data []byte) uint64 {
	var ownerNode uint64
	errFound := errors.New("found")
	err := WalkFields(data, func(f Field) error {
		if f.TypeCode == stUInt64 && f.FieldCode == 4 {
			ownerNode = binary.BigEndian.Uint64(f.Value)
			return errFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFound) {
		return 0
	}
	return ownerNode
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
	return EntryTypeCode(data), nil
}
