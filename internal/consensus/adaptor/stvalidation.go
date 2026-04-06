package adaptor

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// XRPL SField type codes.
const (
	typeUINT16    = 1
	typeUINT32    = 2
	typeUINT64    = 3
	typeHash128   = 4
	typeHash256   = 5
	typeAmount    = 6
	typeBlob      = 7
	typeAccountID = 8
	typeSTObject  = 14
	typeSTArray   = 15
	typeUINT8     = 16
	typeHash160   = 17
	typePathSet   = 18
	typeVector256 = 19
	typeUINT384   = 20
	typeUINT512   = 21
)

// Known field codes within their type groups.
const (
	// UINT32 fields (type 2)
	fieldFlags          = 2
	fieldLedgerSequence = 6
	fieldCloseTime      = 7
	fieldSigningTime    = 9
	fieldLoadFee        = 24
	fieldReserveBase    = 31
	fieldReserveInc     = 32

	// UINT64 fields (type 3)
	fieldBaseFee      = 5
	fieldCookie       = 10
	fieldServerVersion = 11

	// Hash256 fields (type 5)
	fieldLedgerHash    = 1
	fieldConsensusHash = 23
	fieldValidatedHash = 25

	// Blob/VL fields (type 7)
	fieldSigningPubKey = 3
	fieldSignature     = 6
)

// vfFullValidation is the flag bit for a full validation.
const vfFullValidation = 0x00000001

var (
	errShortData     = errors.New("stvalidation: unexpected end of data")
	errInvalidVL     = errors.New("stvalidation: invalid VL encoding")
	errMissingFields = errors.New("stvalidation: missing required fields")
)

// parseSTValidation parses XRPL-binary-encoded STValidation bytes into a
// consensus.Validation. It also populates SigningData with the serialized
// bytes of all fields except sfSignature, suitable for signature verification.
// sfSigningPubKey is included in SigningData (isSigningField=true per XRPL spec).
func parseSTValidation(data []byte) (*consensus.Validation, error) {
	if len(data) < 50 {
		return nil, fmt.Errorf("stvalidation: data too short (%d bytes)", len(data))
	}

	v := &consensus.Validation{}
	var signingBuf []byte

	pos := 0
	for pos < len(data) {
		fieldStart := pos

		// Read field header.
		typeCode, fieldCode, err := readFieldHeader(data, &pos)
		if err != nil {
			return nil, err
		}

		// End-of-object marker for nested objects — stop at top level.
		if typeCode == 0 && fieldCode == 0 {
			break
		}

		// Determine field data length and advance pos past it.
		// For VL-encoded fields, skipFieldData reads past the VL prefix and data,
		// returning just the data length. The actual data ends at pos.
		dataLen, err := skipFieldData(typeCode, data, &pos)
		if err != nil {
			return nil, fmt.Errorf("stvalidation: field (type=%d, field=%d): %w", typeCode, fieldCode, err)
		}

		fieldData := data[pos-dataLen : pos]

		// sfSignature (isSigningField=false) is excluded from the signing hash.
		// All other fields, including sfSigningPubKey (isSigningField=true), are included.
		excludeFromSigning := (typeCode == typeBlob && fieldCode == fieldSignature)

		if !excludeFromSigning {
			signingBuf = append(signingBuf, data[fieldStart:pos]...)
		}

		// Extract known fields.
		switch {
		case typeCode == typeUINT32 && fieldCode == fieldFlags:
			flags := binary.BigEndian.Uint32(fieldData)
			v.Full = (flags & vfFullValidation) != 0

		case typeCode == typeUINT32 && fieldCode == fieldLedgerSequence:
			v.LedgerSeq = binary.BigEndian.Uint32(fieldData)

		case typeCode == typeUINT32 && fieldCode == fieldSigningTime:
			epoch := binary.BigEndian.Uint32(fieldData)
			v.SignTime = xrplEpochToTime(epoch)

		case typeCode == typeUINT32 && fieldCode == fieldLoadFee:
			v.LoadFee = binary.BigEndian.Uint32(fieldData)

		case typeCode == typeUINT64 && fieldCode == fieldCookie:
			v.Cookie = binary.BigEndian.Uint64(fieldData)

		case typeCode == typeHash256 && fieldCode == fieldLedgerHash:
			if len(fieldData) == 32 {
				copy(v.LedgerID[:], fieldData)
			}

		case typeCode == typeBlob && fieldCode == fieldSigningPubKey:
			if len(fieldData) == 33 {
				copy(v.NodeID[:], fieldData)
			}

		case typeCode == typeBlob && fieldCode == fieldSignature:
			v.Signature = make([]byte, len(fieldData))
			copy(v.Signature, fieldData)
		}
	}

	v.SigningData = signingBuf

	// Validate required fields were present.
	if v.LedgerSeq == 0 || v.LedgerID == (consensus.LedgerID{}) || v.NodeID == (consensus.NodeID{}) {
		return nil, errMissingFields
	}

	return v, nil
}

// serializeSTValidation produces XRPL-binary-encoded STValidation bytes from a
// consensus.Validation. Fields are written in canonical order (ascending type
// code, then ascending field code within each type).
func serializeSTValidation(v *consensus.Validation) []byte {
	var buf []byte

	// --- UINT32 fields (type 2) ---

	// sfFlags (field 2)
	flags := uint32(0)
	if v.Full {
		flags |= vfFullValidation
	}
	buf = appendFieldHeader(buf, typeUINT32, fieldFlags)
	buf = binary.BigEndian.AppendUint32(buf, flags)

	// sfLedgerSequence (field 6)
	buf = appendFieldHeader(buf, typeUINT32, fieldLedgerSequence)
	buf = binary.BigEndian.AppendUint32(buf, v.LedgerSeq)

	// sfSigningTime (field 9)
	buf = appendFieldHeader(buf, typeUINT32, fieldSigningTime)
	buf = binary.BigEndian.AppendUint32(buf, timeToXrplEpoch(v.SignTime))

	// sfLoadFee (field 24) — optional
	if v.LoadFee != 0 {
		buf = appendFieldHeader(buf, typeUINT32, fieldLoadFee)
		buf = binary.BigEndian.AppendUint32(buf, v.LoadFee)
	}

	// --- UINT64 fields (type 3) ---

	// sfCookie (field 10) — optional
	if v.Cookie != 0 {
		buf = appendFieldHeader(buf, typeUINT64, fieldCookie)
		buf = binary.BigEndian.AppendUint64(buf, v.Cookie)
	}

	// --- Hash256 fields (type 5) ---

	// sfLedgerHash (field 1)
	buf = appendFieldHeader(buf, typeHash256, fieldLedgerHash)
	buf = append(buf, v.LedgerID[:]...)

	// --- Blob/VL fields (type 7) ---

	// sfSigningPubKey (field 3)
	pubKey := v.NodeID[:]
	buf = appendFieldHeader(buf, typeBlob, fieldSigningPubKey)
	buf = appendVL(buf, pubKey)

	// sfSignature (field 6)
	if len(v.Signature) > 0 {
		buf = appendFieldHeader(buf, typeBlob, fieldSignature)
		buf = appendVL(buf, v.Signature)
	}

	return buf
}

// readFieldHeader reads the XRPL field ID at data[*pos] and advances *pos.
// Returns (typeCode, fieldCode).
func readFieldHeader(data []byte, pos *int) (int, int, error) {
	if *pos >= len(data) {
		return 0, 0, errShortData
	}
	b := data[*pos]
	*pos++

	typeCode := int(b >> 4)
	fieldCode := int(b & 0x0F)

	if typeCode == 0 {
		if *pos >= len(data) {
			return 0, 0, errShortData
		}
		typeCode = int(data[*pos])
		*pos++
	}

	if fieldCode == 0 {
		if *pos >= len(data) {
			return 0, 0, errShortData
		}
		fieldCode = int(data[*pos])
		*pos++
	}

	return typeCode, fieldCode, nil
}

// skipFieldData determines the data length for a field of the given type,
// advances *pos past the data, and returns the data length.
func skipFieldData(typeCode int, data []byte, pos *int) (int, error) {
	switch typeCode {
	case typeUINT8:
		return advanceFixed(data, pos, 1)
	case typeUINT16:
		return advanceFixed(data, pos, 2)
	case typeUINT32:
		return advanceFixed(data, pos, 4)
	case typeUINT64:
		return advanceFixed(data, pos, 8)
	case typeHash128:
		return advanceFixed(data, pos, 16)
	case typeHash160:
		return advanceFixed(data, pos, 20)
	case typeHash256:
		return advanceFixed(data, pos, 32)
	case typeUINT384:
		return advanceFixed(data, pos, 48)
	case typeUINT512:
		return advanceFixed(data, pos, 64)

	case typeAmount:
		return skipAmount(data, pos)

	case typeBlob, typeAccountID, typeVector256:
		return skipVL(data, pos)

	case typeSTObject:
		return skipUntilMarker(data, pos, 0xE1)
	case typeSTArray:
		return skipUntilMarker(data, pos, 0xF1)
	case typePathSet:
		return skipUntilMarker(data, pos, 0x00)

	default:
		return 0, fmt.Errorf("unknown type code %d", typeCode)
	}
}

func advanceFixed(data []byte, pos *int, n int) (int, error) {
	if *pos+n > len(data) {
		return 0, errShortData
	}
	*pos += n
	return n, nil
}

// skipAmount determines the length of an Amount field.
// Bit 63 (0x80 in byte 0) is the "not XRP" flag:
//   - Clear: XRP amount, always 8 bytes.
//   - Set:   IOU amount — 48 bytes (8 value + 20 currency + 20 issuer),
//            UNLESS it's the canonical zero IOU (0x8000000000000000), which is 8 bytes.
func skipAmount(data []byte, pos *int) (int, error) {
	if *pos+8 > len(data) {
		return 0, errShortData
	}
	isNotXRP := (data[*pos] & 0x80) != 0
	if !isNotXRP {
		// XRP amount: always 8 bytes.
		*pos += 8
		return 8, nil
	}
	// IOU: check for canonical zero (exactly 0x8000000000000000).
	isZero := data[*pos] == 0x80
	if isZero {
		for i := 1; i < 8; i++ {
			if data[*pos+i] != 0 {
				isZero = false
				break
			}
		}
	}
	if isZero {
		*pos += 8
		return 8, nil
	}
	// Non-zero IOU: 8 (value) + 20 (currency) + 20 (issuer).
	if *pos+48 > len(data) {
		return 0, errShortData
	}
	*pos += 48
	return 48, nil
}

// skipVL reads a variable-length prefix and advances past the data.
func skipVL(data []byte, pos *int) (int, error) {
	length, err := readVLLength(data, pos)
	if err != nil {
		return 0, err
	}
	if *pos+length > len(data) {
		return 0, errShortData
	}
	*pos += length
	return length, nil
}

// readVLLength reads the XRPL variable-length prefix and returns the data length.
func readVLLength(data []byte, pos *int) (int, error) {
	if *pos >= len(data) {
		return 0, errShortData
	}
	b1 := int(data[*pos])
	*pos++

	if b1 <= 192 {
		return b1, nil
	}
	if b1 <= 240 {
		if *pos >= len(data) {
			return 0, errShortData
		}
		b2 := int(data[*pos])
		*pos++
		return 193 + ((b1 - 193) * 256) + b2, nil
	}
	if b1 <= 254 {
		if *pos+1 >= len(data) {
			return 0, errShortData
		}
		b2 := int(data[*pos])
		b3 := int(data[*pos+1])
		*pos += 2
		return 12481 + ((b1 - 241) * 65536) + (b2 * 256) + b3, nil
	}
	return 0, errInvalidVL
}

// skipUntilMarker advances past a nested structure until the end marker byte.
func skipUntilMarker(data []byte, pos *int, marker byte) (int, error) {
	start := *pos
	for *pos < len(data) {
		if data[*pos] == marker {
			*pos++
			return *pos - start, nil
		}
		// Skip the nested field.
		typeCode, _, err := readFieldHeader(data, pos)
		if err != nil {
			return 0, err
		}
		if _, err := skipFieldData(typeCode, data, pos); err != nil {
			return 0, err
		}
	}
	return 0, fmt.Errorf("missing end marker 0x%02X", marker)
}

// appendFieldHeader appends the XRPL field ID encoding for the given
// type and field codes.
func appendFieldHeader(buf []byte, typeCode, fieldCode int) []byte {
	if typeCode < 16 && fieldCode < 16 {
		return append(buf, byte((typeCode<<4)|fieldCode))
	}
	if typeCode < 16 && fieldCode >= 16 {
		return append(buf, byte(typeCode<<4), byte(fieldCode))
	}
	if typeCode >= 16 && fieldCode < 16 {
		return append(buf, byte(fieldCode), byte(typeCode))
	}
	return append(buf, 0, byte(typeCode), byte(fieldCode))
}

// appendVL appends a variable-length encoded blob (length prefix + data).
func appendVL(buf []byte, data []byte) []byte {
	n := len(data)
	if n <= 192 {
		buf = append(buf, byte(n))
	} else if n <= 12480 {
		n -= 193
		buf = append(buf, byte(193+(n>>8)), byte(n&0xFF))
	} else {
		n -= 12481
		buf = append(buf, byte(241+(n>>16)), byte((n>>8)&0xFF), byte(n&0xFF))
	}
	return append(buf, data...)
}

