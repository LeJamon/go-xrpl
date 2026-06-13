package state

import (
	"encoding/binary"
	"fmt"
)

// Serialized type codes for the XRPL binary format. These are the high nibble
// (or extended byte) of a field header. Only the subset that can appear in a
// ledger entry is needed by the walkers; variable-shape composite types that
// no SLE walker traverses (Issue, PathSet, XChainBridge) are reported as
// unsupported so a mis-skip can never silently corrupt the parse.
const (
	stUInt16    = 1
	stUInt32    = 2
	stUInt64    = 3
	stHash128   = 4
	stHash256   = 5
	stAmount    = 6
	stBlob      = 7
	stAccountID = 8
	stNumber    = 9
	stInt32     = 10
	stInt64     = 11
	stObject    = 14
	stArray     = 15
	stUInt8     = 16
	stHash160   = 17
	stVector256 = 19
	stUInt96    = 20
	stHash192   = 21
	stUInt384   = 22
	stUInt512   = 23
	stCurrency  = 26

	// Types 18 (PathSet), 24 (Issue) and 25 (XChainBridge) have variable or
	// composite encodings that no SLE walker traverses; fieldValueEnd reports
	// them (and any unknown type) as unsupported rather than guessing a width.
)

// Field is a single decoded field of a serialized STObject: its serialized type
// code, field code, and the raw value bytes (excluding the header and, for
// variable-length types, excluding the length prefix). For STObject and STArray
// fields Value spans the entire nested content up to and including the matching
// end marker.
type Field struct {
	TypeCode  int
	FieldCode int
	Value     []byte
}

// WalkFields iterates the top-level fields of a serialized STObject, invoking fn
// for each. It decodes every field header (including extended type/field codes)
// and correctly delimits each value by its serialized type — fixed-width types,
// Amount (XRP 8 / IOU 48 / MPT 33), variable-length types with the XRPL 1/2/3
// byte length prefix, and nested STObject/STArray (delimited by their end
// markers). An unsupported or truncated field returns an error rather than
// guessing a width, so a malformed entry never causes a silent desync.
//
// fn may return an error to stop iteration early; that error is returned to the
// caller. The object-level end marker (0xE1) at the top level terminates the
// walk without error.
func WalkFields(data []byte, fn func(Field) error) error {
	offset := 0
	for offset < len(data) {
		// A top-level end marker (object 0xE1 or array 0xF1) terminates the
		// walk. The latter lets WalkFields iterate the content slice of an
		// STArray field, whose elements are each STObjects.
		if data[offset] == objectEndMarker || data[offset] == arrayEndMarker {
			return nil
		}

		typeCode, fieldCode, hdrEnd, ok := parseFieldHeader(data, offset)
		if !ok {
			return fmt.Errorf("truncated field header at offset %d", offset)
		}

		valueStart := hdrEnd
		valueEnd, err := fieldValueEnd(int(typeCode), data, valueStart)
		if err != nil {
			return err
		}

		if err := fn(Field{
			TypeCode:  int(typeCode),
			FieldCode: int(fieldCode),
			Value:     data[valueStart:valueEnd],
		}); err != nil {
			return err
		}
		offset = valueEnd
	}
	return nil
}

// WalkFieldsDeep walks every field at every nesting depth of a serialized
// STObject, invoking fn for each. STObject and STArray fields are reported and
// then descended into, so a callback sees inner-object fields (e.g. a URI Blob
// inside an NFTokens array element) without manual nesting bookkeeping. fn may
// return an error to stop the walk; that error is returned to the caller.
func WalkFieldsDeep(data []byte, fn func(Field) error) error {
	return WalkFields(data, func(f Field) error {
		if err := fn(f); err != nil {
			return err
		}
		if f.TypeCode == stObject || f.TypeCode == stArray {
			return WalkFieldsDeep(f.Value, fn)
		}
		return nil
	})
}

// fieldValueEnd returns the offset just past the value of a field of the given
// serialized type whose value begins at start. It returns an error if the value
// is truncated or the type's width cannot be determined without ambiguity.
func fieldValueEnd(typeCode int, data []byte, start int) (int, error) {
	if width, fixed := fixedWidth(typeCode); fixed {
		end := start + width
		if end > len(data) {
			return 0, fmt.Errorf("truncated %d-byte value for type %d at offset %d", width, typeCode, start)
		}
		return end, nil
	}

	switch typeCode {
	case stAmount:
		return amountValueEnd(data, start)
	case stBlob, stAccountID, stVector256:
		return vlValueEnd(data, start)
	case stObject:
		return nestedEnd(data, start, objectEndMarker)
	case stArray:
		return nestedEnd(data, start, arrayEndMarker)
	default:
		// Issue, PathSet, XChainBridge and any unknown type have variable or
		// composite encodings that no SLE walker needs to traverse. Refuse to
		// guess: a wrong width would desync the rest of the object.
		return 0, fmt.Errorf("unsupported serialized type %d at offset %d", typeCode, start)
	}
}

// fixedWidth returns the byte width of a fixed-width serialized type.
func fixedWidth(typeCode int) (int, bool) {
	switch typeCode {
	case stUInt8:
		return 1, true
	case stUInt16:
		return 2, true
	case stUInt32, stInt32:
		return 4, true
	case stUInt64, stInt64:
		return 8, true
	case stNumber, stUInt96:
		return 12, true
	case stHash128:
		return 16, true
	case stHash160, stCurrency:
		return 20, true
	case stHash192:
		return 24, true
	case stHash256:
		return 32, true
	case stUInt384:
		return 48, true
	case stUInt512:
		return 64, true
	}
	return 0, false
}

// amountValueEnd returns the end offset of an Amount value beginning at start.
// XRP is 8 bytes; an MPT amount (high bit clear, second-highest bit set) is 33
// bytes; an IOU amount (high bit set) is 48 bytes.
func amountValueEnd(data []byte, start int) (int, error) {
	if start >= len(data) {
		return 0, fmt.Errorf("truncated Amount at offset %d", start)
	}
	b := data[start]
	var width int
	switch {
	case b&0x80 != 0:
		width = 48 // IOU
	case b&0x20 != 0:
		width = 33 // MPT: 1 header byte + 8-byte value + 24-byte issuance ID
	default:
		width = 8 // XRP
	}
	end := start + width
	if end > len(data) {
		return 0, fmt.Errorf("truncated %d-byte Amount at offset %d", width, start)
	}
	return end, nil
}

// vlValueEnd returns the end offset of a variable-length value (Blob, AccountID,
// Vector256) beginning with its length prefix at start. The XRPL length prefix
// is 1, 2, or 3 bytes; the formulas match the binary codec's ReadVariableLength.
func vlValueEnd(data []byte, start int) (int, error) {
	length, prefixLen, err := readVLLength(data, start)
	if err != nil {
		return 0, err
	}
	end := start + prefixLen + length
	if end > len(data) {
		return 0, fmt.Errorf("truncated variable-length value at offset %d", start)
	}
	return end, nil
}

// readVLLength decodes the XRPL variable-length prefix at offset start, returning
// the payload length and the number of prefix bytes consumed.
func readVLLength(data []byte, start int) (length, prefixLen int, err error) {
	if start >= len(data) {
		return 0, 0, fmt.Errorf("truncated length prefix at offset %d", start)
	}
	b1 := int(data[start])
	switch {
	case b1 <= 192:
		return b1, 1, nil
	case b1 <= 240:
		if start+1 >= len(data) {
			return 0, 0, fmt.Errorf("truncated 2-byte length prefix at offset %d", start)
		}
		b2 := int(data[start+1])
		return 193 + ((b1 - 193) * 256) + b2, 2, nil
	case b1 <= 254:
		if start+2 >= len(data) {
			return 0, 0, fmt.Errorf("truncated 3-byte length prefix at offset %d", start)
		}
		b2 := int(data[start+1])
		b3 := int(data[start+2])
		return 12481 + ((b1 - 241) * 65536) + (b2 * 256) + b3, 3, nil
	default:
		return 0, 0, fmt.Errorf("invalid length prefix byte 0x%02x at offset %d", b1, start)
	}
}

// nestedEnd returns the offset just past the matching end marker (0xE1 for an
// STObject, 0xF1 for an STArray) of a nested value beginning at start. It walks
// the inner fields, tracking nesting depth so that inner objects/arrays do not
// terminate the outer one prematurely.
func nestedEnd(data []byte, start int, endMarker byte) (int, error) {
	offset := start
	for offset < len(data) {
		if data[offset] == endMarker {
			return offset + 1, nil
		}
		// A nested object/array end marker that doesn't match the one we're
		// scanning for is an encoding error at this position.
		if data[offset] == objectEndMarker || data[offset] == arrayEndMarker {
			return 0, fmt.Errorf("mismatched end marker 0x%02x at offset %d", data[offset], offset)
		}

		typeCode, _, hdrEnd, ok := parseFieldHeader(data, offset)
		if !ok {
			return 0, fmt.Errorf("truncated field header at offset %d", offset)
		}
		valueEnd, err := fieldValueEnd(int(typeCode), data, hdrEnd)
		if err != nil {
			return 0, err
		}
		offset = valueEnd
	}
	return 0, fmt.Errorf("unterminated nested structure starting at offset %d", start)
}

// EntryTypeCode extracts the raw LedgerEntryType code (UInt16, field code 1)
// from a serialized SLE. XRPL serialization always places LedgerEntryType first
// with header byte 0x11, so this reads it directly. Returns 0 when the data is
// too short or does not begin with the expected header.
func EntryTypeCode(data []byte) uint16 {
	if len(data) < 3 || data[0] != 0x11 {
		return 0
	}
	return binary.BigEndian.Uint16(data[1:3])
}

// EntryTypeName returns the name of the ledger entry type for the given type
// code, or "Unknown(0x....)" if the code is not a known ledger entry type.
func EntryTypeName(code uint16) string {
	if name, ok := ledgerEntryTypeNames[code]; ok {
		return name
	}
	return fmt.Sprintf("Unknown(0x%04x)", code)
}

// EntryType extracts the ledger entry type name from a serialized SLE. Returns
// "" when the type code cannot be extracted (data too short or missing the
// standard 0x11 LedgerEntryType header).
func EntryType(data []byte) string {
	code := EntryTypeCode(data)
	if code == 0 {
		return ""
	}
	return EntryTypeName(code)
}

// ledgerEntryTypeNames maps ledger entry type codes to their canonical names.
// Reference: rippled ledger_entries.macro.
var ledgerEntryTypeNames = map[uint16]string{
	0x0037: "NFTokenOffer",
	0x0043: "Check",
	0x0049: "DID",
	0x004e: "NegativeUNL",
	0x0050: "NFTokenPage",
	0x0053: "SignerList",
	0x0054: "Ticket",
	0x0061: "AccountRoot",
	0x0063: "Contract", // deprecated
	0x0064: "DirectoryNode",
	0x0066: "Amendments",
	0x0067: "GeneratorMap", // deprecated
	0x0068: "LedgerHashes",
	0x0069: "Bridge",
	0x006e: "Nickname", // deprecated
	0x006f: "Offer",
	0x0070: "DepositPreauth",
	0x0071: "XChainOwnedClaimID",
	0x0072: "RippleState",
	0x0073: "FeeSettings",
	0x0074: "XChainOwnedCreateAccountClaimID",
	0x0075: "Escrow",
	0x0078: "PayChannel",
	0x0079: "AMM",
	0x007e: "MPTokenIssuance",
	0x007f: "MPToken",
	0x0080: "Oracle",
	0x0081: "Credential",
	0x0082: "PermissionedDomain",
	0x0083: "Delegate",
	0x0084: "Vault",
}
