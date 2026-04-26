package adaptor

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// XRPL SField type codes — mirror rippled
// include/xrpl/protocol/SField.h:65-87. Off-by-2 for UINT384/UINT512
// was latent (no validation field uses these types) but breaks the
// (type<<16)|field canonical sort order for any future-added field
// of those types.
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
	typeUINT96    = 20
	typeUINT192   = 21
	typeUINT384   = 22
	typeUINT512   = 23
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
	fieldBaseFee       = 5
	fieldCookie        = 10
	fieldServerVersion = 11

	// Hash256 fields (type 5)
	fieldLedgerHash    = 1
	fieldConsensusHash = 23
	fieldValidatedHash = 25

	// Blob/VL fields (type 7)
	fieldSigningPubKey = 3
	fieldSignature     = 6

	// Vector256 fields (type 19).
	// sfAmendments is VECTOR256 field 3 per rippled
	// include/xrpl/protocol/detail/sfields.macro:306. The previous
	// value of 19 matched the TYPE code, not the field code — a
	// common confusion. With the wrong field code, outbound
	// flag-ledger votes were malformed and inbound amendment votes
	// from rippled peers never matched the parser switch.
	fieldAmendments = 3

	// Amount fields (type 6) — post-featureXRPFees fee-voting.
	fieldBaseFeeDrops          = 22
	fieldReserveBaseDrops      = 23
	fieldReserveIncrementDrops = 24
)

// Validation flags. Kept in sync with rippled's STValidation.h.
const (
	// vfFullValidation marks a full validation (vs. a partial one).
	vfFullValidation = 0x00000001

	// vfFullyCanonicalSig asserts the signature is in canonical form
	// (low-S, compressed pubkey). Rippled sets this on every outbound
	// validation (STValidation.cpp:236) and verifies it on inbound if
	// the flag is present. Outbound goXRPL validations without this
	// flag are accepted by rippled today (canonicality is optional),
	// but future rippled releases may make it mandatory — setting it
	// now keeps us forward-compatible and matches the reference impl.
	vfFullyCanonicalSig = 0x80000000
)

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
			v.Flags = flags
			v.Full = (flags & vfFullValidation) != 0

		case typeCode == typeUINT32 && fieldCode == fieldLedgerSequence:
			v.LedgerSeq = binary.BigEndian.Uint32(fieldData)

		case typeCode == typeUINT32 && fieldCode == fieldSigningTime:
			epoch := binary.BigEndian.Uint32(fieldData)
			v.SignTime = xrplEpochToTime(epoch)

		case typeCode == typeUINT32 && fieldCode == fieldCloseTime:
			// sfCloseTime is optional per rippled
			// STValidation.cpp:63 — some validators omit it.
			// Pre-R6b.5c the parser silently discarded this field;
			// now we surface it on the Validation struct for RPC
			// consumers. Does not affect signature verification
			// (SigningData still captures the raw bytes).
			epoch := binary.BigEndian.Uint32(fieldData)
			v.CloseTime = xrplEpochToTime(epoch)

		case typeCode == typeUINT32 && fieldCode == fieldLoadFee:
			v.LoadFee = binary.BigEndian.Uint32(fieldData)

		case typeCode == typeUINT32 && fieldCode == fieldReserveBase:
			v.ReserveBase = binary.BigEndian.Uint32(fieldData)

		case typeCode == typeUINT32 && fieldCode == fieldReserveInc:
			v.ReserveIncrement = binary.BigEndian.Uint32(fieldData)

		case typeCode == typeUINT64 && fieldCode == fieldBaseFee:
			v.BaseFee = binary.BigEndian.Uint64(fieldData)

		case typeCode == typeUINT64 && fieldCode == fieldCookie:
			v.Cookie = binary.BigEndian.Uint64(fieldData)

		case typeCode == typeUINT64 && fieldCode == fieldServerVersion:
			v.ServerVersion = binary.BigEndian.Uint64(fieldData)

		case typeCode == typeAmount && fieldCode == fieldBaseFeeDrops:
			if amt, ok := parseXRPAmount(fieldData); ok {
				v.BaseFeeDrops = amt
			}

		case typeCode == typeAmount && fieldCode == fieldReserveBaseDrops:
			if amt, ok := parseXRPAmount(fieldData); ok {
				v.ReserveBaseDrops = amt
			}

		case typeCode == typeAmount && fieldCode == fieldReserveIncrementDrops:
			if amt, ok := parseXRPAmount(fieldData); ok {
				v.ReserveIncrementDrops = amt
			}

		case typeCode == typeHash256 && fieldCode == fieldLedgerHash:
			if len(fieldData) == 32 {
				copy(v.LedgerID[:], fieldData)
			}

		case typeCode == typeHash256 && fieldCode == fieldConsensusHash:
			if len(fieldData) == 32 {
				copy(v.ConsensusHash[:], fieldData)
			}

		case typeCode == typeHash256 && fieldCode == fieldValidatedHash:
			if len(fieldData) == 32 {
				copy(v.ValidatedHash[:], fieldData)
			}

		case typeCode == typeVector256 && fieldCode == fieldAmendments:
			// Vector256 is VL-wrapped concat of 32-byte IDs. fieldData
			// is the VL payload, so iterate in 32-byte chunks.
			if len(fieldData)%32 == 0 {
				n := len(fieldData) / 32
				v.Amendments = make([][32]byte, 0, n)
				for i := 0; i < n; i++ {
					var id [32]byte
					copy(id[:], fieldData[i*32:(i+1)*32])
					v.Amendments = append(v.Amendments, id)
				}
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
	v.Raw = append([]byte(nil), data...)

	// Validate required fields were present.
	if v.LedgerSeq == 0 || v.LedgerID == (consensus.LedgerID{}) || v.NodeID == (consensus.NodeID{}) {
		return nil, errMissingFields
	}

	return v, nil
}

// SerializeSTValidation produces XRPL-binary-encoded STValidation bytes from a
// consensus.Validation. Fields are written in canonical order (ascending type
// code, then ascending field code within each type).
//
// Outbound validations set both vfFullValidation and vfFullyCanonicalSig on
// sfFlags, matching rippled's STValidation::sign semantics. Optional
// supplementary fields (Cookie, LoadFee, ConsensusHash, ServerVersion) are
// emitted only when non-zero.
//
// Exported so external packages (the validation archive) can reserialize
// self-built validations whose Raw field is nil.
func SerializeSTValidation(v *consensus.Validation) []byte {
	var buf []byte

	// --- UINT32 fields (type 2) ---

	// sfFlags (field 2). For round-trip fidelity, prefer the original
	// wire word captured by parseSTValidation (so we re-emit any vendor
	// bits the validator set). For self-built validations whose Flags
	// field is zero, synthesize the rippled-canonical pair: stamp
	// vfFullyCanonicalSig on every outbound validation so canonical-sig-
	// strict peers don't need to special-case us, plus vfFullValidation
	// when v.Full.
	flags := v.Flags
	if flags == 0 {
		flags = vfFullyCanonicalSig
		if v.Full {
			flags |= vfFullValidation
		}
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

	// sfReserveBase (field 31) — optional flag-ledger fee vote (legacy
	// pre-XRPFees form). Rippled RCLConsensus.cpp:882-883 via
	// FeeVote::doValidation.
	if v.ReserveBase != 0 {
		buf = appendFieldHeader(buf, typeUINT32, fieldReserveBase)
		buf = binary.BigEndian.AppendUint32(buf, v.ReserveBase)
	}

	// sfReserveIncrement (field 32) — optional flag-ledger fee vote.
	if v.ReserveIncrement != 0 {
		buf = appendFieldHeader(buf, typeUINT32, fieldReserveInc)
		buf = binary.BigEndian.AppendUint32(buf, v.ReserveIncrement)
	}

	// --- UINT64 fields (type 3) ---

	// sfBaseFee (field 5) — optional flag-ledger fee vote (legacy
	// pre-XRPFees form).
	if v.BaseFee != 0 {
		buf = appendFieldHeader(buf, typeUINT64, fieldBaseFee)
		buf = binary.BigEndian.AppendUint64(buf, v.BaseFee)
	}

	// sfCookie (field 10) — optional
	if v.Cookie != 0 {
		buf = appendFieldHeader(buf, typeUINT64, fieldCookie)
		buf = binary.BigEndian.AppendUint64(buf, v.Cookie)
	}

	// sfServerVersion (field 11) — optional. Rippled populates this
	// with its build version on first validation per peer session so
	// the network can track implementation versions in play.
	if v.ServerVersion != 0 {
		buf = appendFieldHeader(buf, typeUINT64, fieldServerVersion)
		buf = binary.BigEndian.AppendUint64(buf, v.ServerVersion)
	}

	// --- Hash256 fields (type 5) ---
	// Must come BEFORE AMOUNT (type 6) per canonical ascending-type
	// ordering. Rippled STObject::getSigningHash re-serializes in that
	// order; a preimage mismatch against rippled peers would cause
	// signature verification to fail on flag ledgers where AMOUNT
	// fee-vote fields are present.

	// sfLedgerHash (field 1)
	buf = appendFieldHeader(buf, typeHash256, fieldLedgerHash)
	buf = append(buf, v.LedgerID[:]...)

	// sfConsensusHash (field 23) — optional. Ties the validation to
	// a specific transaction-set agreement. Rippled uses it to
	// disambiguate between concurrent ledgers at the same seq with
	// divergent tx sets. Zero-hash means "not set".
	if v.ConsensusHash != ([32]byte{}) {
		buf = appendFieldHeader(buf, typeHash256, fieldConsensusHash)
		buf = append(buf, v.ConsensusHash[:]...)
	}

	// sfValidatedHash (field 25) — optional. Rippled emits this under
	// featureHardenedValidations (RCLConsensus.cpp:858-859); it's the
	// hash of the validator's current fully-validated ledger at sign
	// time, giving peers an additional fork-detection signal beyond
	// sfLedgerHash. Zero-hash means "not set".
	if v.ValidatedHash != ([32]byte{}) {
		buf = appendFieldHeader(buf, typeHash256, fieldValidatedHash)
		buf = append(buf, v.ValidatedHash[:]...)
	}

	// --- Amount fields (type 6) ---
	// Emitted AFTER Hash256 per canonical ordering. See note above.

	// Post-featureXRPFees fee-voting fields (rippled uses AMOUNT-typed
	// variants once XRPFees is enabled). Encoded as 8-byte XRP amounts
	// with the native (high-bit-clear) flag. The adaptor is
	// responsible for populating these mutually-exclusively with the
	// legacy sfBaseFee/sfReserveBase/sfReserveIncrement triple based
	// on the parent ledger's rules.enabled(featureXRPFees) — see
	// FeeVoteImpl.cpp:120-192 for rippled's if/else branching. We
	// keep the non-zero gate here as defense-in-depth: a bug in the
	// population layer produces a MISSING field (rejected by the
	// parser's field presence check), not a DOUBLE field (which
	// would parse but diverge semantically from rippled).
	if v.BaseFeeDrops != 0 {
		buf = appendFieldHeader(buf, typeAmount, fieldBaseFeeDrops)
		buf = appendXRPAmount(buf, v.BaseFeeDrops)
	}
	if v.ReserveBaseDrops != 0 {
		buf = appendFieldHeader(buf, typeAmount, fieldReserveBaseDrops)
		buf = appendXRPAmount(buf, v.ReserveBaseDrops)
	}
	if v.ReserveIncrementDrops != 0 {
		buf = appendFieldHeader(buf, typeAmount, fieldReserveIncrementDrops)
		buf = appendXRPAmount(buf, v.ReserveIncrementDrops)
	}

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

	// --- Vector256 fields (type 19) ---

	// sfAmendments — VECTOR256 (type 19) FIELD 3 per rippled
	// include/xrpl/protocol/detail/sfields.macro:306. Flag-ledger
	// amendment vote. Rippled emits this on isVotingLedger
	// (RCLConsensus.cpp:886-894); each entry is a 32-byte amendment
	// ID the validator wishes to signal support for. Encoded as
	// VL(concat(ids)). Emitted last because Vector256 (type 19)
	// follows typeBlob (7) in canonical ordering.
	if len(v.Amendments) > 0 {
		buf = appendFieldHeader(buf, typeVector256, fieldAmendments)
		blob := make([]byte, 0, 32*len(v.Amendments))
		for _, id := range v.Amendments {
			blob = append(blob, id[:]...)
		}
		buf = appendVL(buf, blob)
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
//     UNLESS it's the canonical zero IOU (0x8000000000000000), which is 8 bytes.
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

// parseXRPAmount decodes an 8-byte native XRPL Amount into a drops
// value. Returns (_, false) if the "not XRP" flag is set (i.e. an IOU)
// — fee-vote fields are always native, so an IOU here indicates a
// malformed validation and is dropped silently.
func parseXRPAmount(data []byte) (uint64, bool) {
	if len(data) != 8 {
		return 0, false
	}
	raw := binary.BigEndian.Uint64(data)
	if raw&(1<<63) != 0 {
		return 0, false // IOU form — not expected for fee-vote fields.
	}
	// Strip the positive-sign bit; remaining 62 bits carry drops.
	return raw &^ (1 << 62), true
}

// appendXRPAmount appends an XRPL-encoded native Amount (8 bytes).
// Rippled Amount encoding: bit 63 = "not XRP" flag (clear for XRP),
// bit 62 = sign bit (always set for positive / non-negative), lower
// bits carry the drops value. Used to emit the post-featureXRPFees
// fee-vote fields (sfBaseFeeDrops, sfReserveBaseDrops,
// sfReserveIncrementDrops) which are AMOUNT-typed.
func appendXRPAmount(buf []byte, drops uint64) []byte {
	// High bit clear = XRP; second-highest bit set = positive.
	// drops must fit in 62 bits, which is enforced by the XRPL total
	// drops invariant (< 100 billion XRP × 10^6 drops/XRP).
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], drops|(1<<62))
	return append(buf, encoded[:]...)
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
