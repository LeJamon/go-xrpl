package adaptor

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStvReadFieldHeader_TypeZero(t *testing.T) {
	// typeCode == 0: next byte is the extended type code.
	data := []byte{0x0A, 0x10} // high nibble 0 → extended type; low nibble 10 = fieldCode
	pos := 0
	tc, fc, err := readFieldHeader(data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 0x10, tc)
	assert.Equal(t, 10, fc)
	assert.Equal(t, 2, pos)
}

func TestStvReadFieldHeader_FieldZero(t *testing.T) {
	// fieldCode == 0: next byte is the extended field code.
	data := []byte{0x20, 0x10} // high nibble 2, low nibble 0 → extended field; next byte = 16
	pos := 0
	tc, fc, err := readFieldHeader(data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 2, tc)
	assert.Equal(t, 0x10, fc)
	assert.Equal(t, 2, pos)
}

func TestStvReadFieldHeader_BothZero(t *testing.T) {
	data := []byte{0x00, 0x00, 0x00}
	pos := 0
	_, _, err := readFieldHeader(data, &pos)
	assert.ErrorIs(t, err, errNonCanonicalFieldID)
}

func TestStvReadFieldHeader_TypeZeroTruncated(t *testing.T) {
	data := []byte{0x0A}
	pos := 0
	_, _, err := readFieldHeader(data, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvReadFieldHeader_FieldZeroTruncated(t *testing.T) {
	data := []byte{0x20}
	pos := 0
	_, _, err := readFieldHeader(data, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvReadFieldHeader_Empty(t *testing.T) {
	pos := 0
	_, _, err := readFieldHeader([]byte{}, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvSkipFieldData_AllFixedTypes(t *testing.T) {
	cases := []struct {
		typeCode int
		size     int
	}{
		{typeUINT8, 1},
		{typeUINT16, 2},
		{typeUINT32, 4},
		{typeUINT64, 8},
		{typeHash128, 16},
		{typeHash160, 20},
		{typeHash256, 32},
		{typeUINT384, 48},
		{typeUINT512, 64},
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			data := make([]byte, tc.size+10)
			pos := 0
			n, err := skipFieldData(tc.typeCode, data, &pos)
			require.NoError(t, err)
			assert.Equal(t, tc.size, n)
			assert.Equal(t, tc.size, pos)
		})
	}
}

func TestStvSkipFieldData_AllFixedTypesTruncated(t *testing.T) {
	cases := []struct {
		typeCode int
		size     int
	}{
		{typeUINT8, 1},
		{typeUINT16, 2},
		{typeUINT32, 4},
		{typeUINT64, 8},
		{typeHash128, 16},
		{typeHash160, 20},
		{typeHash256, 32},
		{typeUINT384, 48},
		{typeUINT512, 64},
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			data := make([]byte, tc.size-1) // one byte short
			pos := 0
			_, err := skipFieldData(tc.typeCode, data, &pos)
			assert.ErrorIs(t, err, errShortData)
		})
	}
}

func TestStvSkipFieldData_Amount_XRP(t *testing.T) {
	data := make([]byte, 8)
	data[0] = 0x40
	pos := 0
	n, err := skipFieldData(typeAmount, data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 8, n)
}

func TestStvSkipFieldData_Amount_IOU_NonZero(t *testing.T) {
	// non-zero IOU: 48 bytes, first byte has high bit set, not canonical zero
	data := make([]byte, 48)
	data[0] = 0x80
	data[1] = 0x01 // non-zero → 48 bytes
	pos := 0
	n, err := skipFieldData(typeAmount, data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 48, n)
}

func TestStvSkipFieldData_Amount_IOU_CanonicalZero(t *testing.T) {
	data := make([]byte, 48)
	data[0] = 0x80
	pos := 0
	n, err := skipFieldData(typeAmount, data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 48, n)
}

func TestStvSkipFieldData_Amount_MPT(t *testing.T) {
	data := make([]byte, 33)
	data[0] = 0x20
	pos := 0
	n, err := skipFieldData(typeAmount, data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 33, n)
}

func TestStvSkipFieldData_Amount_NativeNegativeZero(t *testing.T) {
	data := make([]byte, 8)
	pos := 0
	_, err := skipFieldData(typeAmount, data, &pos)
	require.ErrorContains(t, err, "negative zero is not canonical")
}

func TestStvSkipFieldData_Amount_Truncated(t *testing.T) {
	data := make([]byte, 4) // only 4 bytes, need 8
	pos := 0
	_, err := skipFieldData(typeAmount, data, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvSkipFieldData_Amount_IOUNonZero_Truncated(t *testing.T) {
	data := make([]byte, 10)
	data[0] = 0x80
	data[1] = 0x01
	pos := 0
	_, err := skipFieldData(typeAmount, data, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvSkipFieldData_Amount_MPT_Truncated(t *testing.T) {
	data := make([]byte, 32)
	data[0] = 0x20
	pos := 0
	_, err := skipFieldData(typeAmount, data, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvSkipFieldData_Blob(t *testing.T) {
	// typeBlob: VL-prefixed, length=5 bytes
	data := []byte{0x05, 0x01, 0x02, 0x03, 0x04, 0x05}
	pos := 0
	n, err := skipFieldData(typeBlob, data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
}

func TestStvSkipFieldData_AccountID(t *testing.T) {
	// typeAccountID: VL-prefixed
	data := []byte{0x03, 0xAA, 0xBB, 0xCC}
	pos := 0
	n, err := skipFieldData(typeAccountID, data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

func TestStvSkipFieldData_Vector256(t *testing.T) {
	// typeVector256: VL-prefixed, 32 bytes
	data := make([]byte, 33)
	data[0] = 32 // VL length
	pos := 0
	n, err := skipFieldData(typeVector256, data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 32, n)
}

func TestStvSkipFieldData_STObject(t *testing.T) {
	// typeSTObject: reads until 0xE1 marker
	// header byte 0x22 (UINT32 field 2) + 4 bytes + 0xE1
	data := []byte{0x22, 0x00, 0x00, 0x00, 0x01, 0xE1}
	pos := 0
	n, err := skipFieldData(typeSTObject, data, &pos)
	require.NoError(t, err)
	assert.Greater(t, n, 0)
}

func TestStvSkipFieldData_STArray(t *testing.T) {
	// typeSTArray: reads until 0xF1 marker
	data := []byte{0xF1}
	pos := 0
	n, err := skipFieldData(typeSTArray, data, &pos)
	require.NoError(t, err)
	assert.Greater(t, n, 0)
}

func TestStvSkipFieldData_PathSet(t *testing.T) {
	// typePathSet: reads until 0x00 byte
	data := []byte{0x00}
	pos := 0
	n, err := skipFieldData(typePathSet, data, &pos)
	require.NoError(t, err)
	assert.Greater(t, n, 0)
}

func TestStvSkipFieldData_UnknownType(t *testing.T) {
	data := make([]byte, 10)
	pos := 0
	_, err := skipFieldData(99, data, &pos)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown type code")
}

func TestStvReadVLLength_OneByte(t *testing.T) {
	data := []byte{100}
	pos := 0
	n, err := readVLLength(data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 100, n)
}

func TestStvReadVLLength_TwoByte(t *testing.T) {
	// b1 in [193, 240]: length = 193 + ((b1-193)*256) + b2
	// b1=193, b2=0 → 193 + 0 + 0 = 193
	data := []byte{193, 0}
	pos := 0
	n, err := readVLLength(data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 193, n)
}

func TestStvReadVLLength_TwoByte_Larger(t *testing.T) {
	// b1=200, b2=10 → 193 + (7*256) + 10 = 193 + 1792 + 10 = 1995
	data := []byte{200, 10}
	pos := 0
	n, err := readVLLength(data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 1995, n)
}

func TestStvReadVLLength_TwoByte_Truncated(t *testing.T) {
	data := []byte{193} // b1 in two-byte range, but no b2
	pos := 0
	_, err := readVLLength(data, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvReadVLLength_ThreeByte(t *testing.T) {
	// b1 in [241, 254]: 12481 + ((b1-241)*65536) + (b2*256) + b3
	// b1=241, b2=0, b3=0 → 12481
	data := []byte{241, 0, 0}
	pos := 0
	n, err := readVLLength(data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 12481, n)
}

func TestStvReadVLLength_ThreeByte_Truncated(t *testing.T) {
	data := []byte{241, 0} // need 2 more bytes
	pos := 0
	_, err := readVLLength(data, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvReadVLLength_Invalid(t *testing.T) {
	// b1 == 255 → invalid
	data := []byte{255}
	pos := 0
	_, err := readVLLength(data, &pos)
	assert.ErrorIs(t, err, errInvalidVL)
}

func TestStvReadVLLength_Empty(t *testing.T) {
	pos := 0
	_, err := readVLLength([]byte{}, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvSkipVL_DataTruncated(t *testing.T) {
	// VL says length=10 but only 3 bytes follow
	data := []byte{10, 0x01, 0x02, 0x03}
	pos := 0
	_, err := skipVL(data, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvSkipUntilMarker_ImmediateMarker(t *testing.T) {
	data := []byte{0xE1}
	pos := 0
	n, err := skipUntilMarker(data, &pos, 0xE1)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, pos)
}

func TestStvSkipUntilMarker_WithNestedField(t *testing.T) {
	// nested UINT32 field (header 0x22 + 4 bytes) then 0xE1
	data := []byte{0x22, 0x00, 0x00, 0x00, 0x01, 0xE1}
	pos := 0
	n, err := skipUntilMarker(data, &pos, 0xE1)
	require.NoError(t, err)
	assert.Equal(t, 6, n)
}

func TestStvSkipUntilMarker_MissingMarker(t *testing.T) {
	data := []byte{0x22, 0x00, 0x00, 0x00, 0x01} // no 0xE1
	pos := 0
	_, err := skipUntilMarker(data, &pos, 0xE1)
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "missing end marker"))
}

func TestStvSkipUntilMarker_NestedReadError(t *testing.T) {
	// a field header with unknown type that causes skipFieldData to fail
	// type=99 (unknown): 0x63 = 0110 0011 → typeCode=6(Amount), field=3
	// Actually let's use a header that leads to a short-data error.
	// Header 0x22 → typeUINT32, field 2 → needs 4 bytes, but only 2 follow + marker
	data := []byte{0x22, 0x00, 0x00} // header + 2 bytes (not 4) — no marker, then EOF
	pos := 0
	_, err := skipUntilMarker(data, &pos, 0xE1)
	assert.Error(t, err)
}

func TestStvAppendFieldHeader_SmallTypeLargeField(t *testing.T) {
	// typeCode < 16, fieldCode >= 16: two bytes: byte(type<<4), byte(field)
	buf := appendFieldHeader(nil, 2, 16)
	require.Len(t, buf, 2)
	assert.Equal(t, byte(0x20), buf[0])
	assert.Equal(t, byte(16), buf[1])
}

func TestStvAppendFieldHeader_LargeTypeSmallField(t *testing.T) {
	// typeCode >= 16, fieldCode < 16: two bytes: byte(field), byte(type)
	buf := appendFieldHeader(nil, 16, 3)
	require.Len(t, buf, 2)
	assert.Equal(t, byte(3), buf[0])
	assert.Equal(t, byte(16), buf[1])
}

func TestStvAppendFieldHeader_BothLarge(t *testing.T) {
	// typeCode >= 16, fieldCode >= 16: three bytes: 0x00, byte(type), byte(field)
	buf := appendFieldHeader(nil, 16, 16)
	require.Len(t, buf, 3)
	assert.Equal(t, byte(0), buf[0])
	assert.Equal(t, byte(16), buf[1])
	assert.Equal(t, byte(16), buf[2])
}

func TestStvAppendFieldHeader_SmallBoth(t *testing.T) {
	// typeCode < 16, fieldCode < 16: one byte: (type<<4) | field
	buf := appendFieldHeader(nil, 2, 6)
	require.Len(t, buf, 1)
	assert.Equal(t, byte(0x26), buf[0])
}

func TestStvAppendVL_OneByte(t *testing.T) {
	data := make([]byte, 100)
	buf := appendVL(nil, data)
	assert.Equal(t, byte(100), buf[0])
	assert.Len(t, buf, 101)
}

func TestStvAppendVL_TwoBytes(t *testing.T) {
	// length in [193, 12480] → 2-byte prefix
	// length = 193 → n-=193=0, buf = [193+0>>8, 0&0xFF] = [193, 0]
	data := make([]byte, 193)
	buf := appendVL(nil, data)
	require.GreaterOrEqual(t, len(buf), 3)
	assert.Equal(t, byte(193), buf[0])
	assert.Equal(t, byte(0), buf[1])
}

func TestStvAppendVL_TwoBytes_Larger(t *testing.T) {
	// length = 12480 → max for 2-byte range
	data := make([]byte, 12480)
	buf := appendVL(nil, data)
	require.Len(t, buf, 12482)
	assert.Equal(t, byte(193+((12480-193)>>8)), buf[0])
}

func TestStvAppendVL_ThreeBytes(t *testing.T) {
	// length > 12480 → 3-byte prefix
	data := make([]byte, 12481)
	buf := appendVL(nil, data)
	require.Len(t, buf, 12484)
	assert.Equal(t, byte(241), buf[0]) // 241 + 0 = 241
}

func TestStvAppendVL_RoundTrip_TwoByte(t *testing.T) {
	payload := make([]byte, 500)
	for i := range payload {
		payload[i] = byte(i)
	}
	buf := appendVL(nil, payload)
	pos := 0
	n, err := readVLLength(buf, &pos)
	require.NoError(t, err)
	assert.Equal(t, 500, n)
	assert.Equal(t, payload, buf[pos:pos+n])
}

func TestStvAppendVL_RoundTrip_ThreeByte(t *testing.T) {
	payload := make([]byte, 13000)
	buf := appendVL(nil, payload)
	pos := 0
	n, err := readVLLength(buf, &pos)
	require.NoError(t, err)
	assert.Equal(t, 13000, n)
}

func TestStvParseXRPAmount_WrongLength(t *testing.T) {
	_, ok := parseXRPAmount([]byte{0x01, 0x02})
	assert.False(t, ok)
}

func TestStvParseXRPAmount_IOU(t *testing.T) {
	// high bit set → IOU, should return false
	data := []byte{0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
	_, ok := parseXRPAmount(data)
	assert.False(t, ok)
}

func TestStvParseXRPAmount_NativeNegativeZero(t *testing.T) {
	_, ok := parseXRPAmount(make([]byte, 8))
	assert.False(t, ok)
}

func TestStvParseXRPAmount_ValidXRP(t *testing.T) {
	// 1000 drops: encode with sign bit (bit 62) set
	drops := uint64(1000)
	encoded := drops | (1 << 62)
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], encoded)
	val, ok := parseXRPAmount(data[:])
	require.True(t, ok)
	assert.EqualValues(t, drops, val)
}

func TestStvParseSTValidation_EndOfObjectMarker(t *testing.T) {
	// A valid validation that includes a 0x00 terminator mid-stream.
	// Build a minimal valid blob, then insert 0x00 at a point that would
	// terminate the loop early — the parser should stop and return
	// errMissingFields since required fields weren't all parsed.
	buf := make([]byte, 0, 51)
	buf = append(buf, 0x00) // immediate EOO → missing fields
	buf = append(buf, make([]byte, 50)...)
	_, err := parseSTValidation(buf)
	assert.Error(t, err)
}

func TestStvParseSTValidation_InvalidAmendmentsLength(t *testing.T) {
	// Amendments with non-multiple-of-32 payload — parser skips the field silently.
	orig := buildTestValidation()
	// Build a blob with a valid validation and add a Vector256 field whose payload
	// is 33 bytes (not a multiple of 32) — the parser must not crash and must still
	// return a valid Validation (amendments just won't be populated).
	var extraBuf []byte
	extraBuf = appendFieldHeader(extraBuf, typeVector256, fieldAmendments)
	payload := make([]byte, 33) // 33 bytes — not divisible by 32
	extraBuf = appendVL(extraBuf, payload)

	base := SerializeSTValidation(orig)
	combined := append(base, extraBuf...)
	_, err := parseSTValidation(combined)
	// It may succeed or fail depending on whether signatures and required fields
	// are still present; either way, no panic.
	_ = err
}

func TestStvParseSTValidation_ShortSigningPubKey(t *testing.T) {
	var buf []byte
	buf = appendFieldHeader(buf, typeUINT32, fieldFlags)
	buf = binary.BigEndian.AppendUint32(buf, vfFullValidation)

	buf = appendFieldHeader(buf, typeUINT32, fieldLedgerSequence)
	buf = binary.BigEndian.AppendUint32(buf, 10)

	buf = appendFieldHeader(buf, typeUINT32, fieldSigningTime)
	buf = binary.BigEndian.AppendUint32(buf, 828618000)

	buf = appendFieldHeader(buf, typeHash256, fieldLedgerHash)
	ledgerHash := make([]byte, 32)
	ledgerHash[0] = 0xAA
	buf = append(buf, ledgerHash...)

	// SigningPubKey with wrong length (20 bytes instead of 33)
	buf = appendFieldHeader(buf, typeBlob, fieldSigningPubKey)
	buf = appendVL(buf, make([]byte, 20))

	buf = appendFieldHeader(buf, typeBlob, fieldSignature)
	buf = appendVL(buf, make([]byte, 70))

	_, err := parseSTValidation(buf)
	assert.ErrorIs(t, err, errInvalidFieldValue)
}

func TestStvParseSTValidation_AllOptionalUINT32Fields(t *testing.T) {
	// Verify ReserveBase and ReserveIncrement branches are exercised.
	orig := buildTestValidation()
	orig.ReserveBase = 200_000_000
	orig.ReserveIncrement = 50_000_000

	blob := SerializeSTValidation(orig)
	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	assert.Equal(t, orig.ReserveBase, parsed.ReserveBase)
	assert.Equal(t, orig.ReserveIncrement, parsed.ReserveIncrement)
}

func TestStvParseSTValidation_AllUINT64Fields(t *testing.T) {
	// BaseFee, Cookie, ServerVersion branches.
	orig := buildTestValidation()
	orig.BaseFee = 10
	orig.Cookie = 98765
	orig.ServerVersion = 0x0200000000000000

	blob := SerializeSTValidation(orig)
	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	assert.Equal(t, orig.BaseFee, parsed.BaseFee)
	assert.Equal(t, orig.Cookie, parsed.Cookie)
	assert.Equal(t, orig.ServerVersion, parsed.ServerVersion)
}

func TestStvParseSTValidation_AmendmentsField(t *testing.T) {
	// Vector256 amendments field with valid 32-byte entries.
	orig := buildTestValidation()
	var id1, id2 [32]byte
	for i := range id1 {
		id1[i] = byte(i + 1)
		id2[i] = byte(i + 0x80)
	}
	orig.Amendments = [][32]byte{id1, id2}

	blob := SerializeSTValidation(orig)
	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	require.Len(t, parsed.Amendments, 2)
	assert.Equal(t, id1, parsed.Amendments[0])
	assert.Equal(t, id2, parsed.Amendments[1])
}

func TestStvParseSTValidation_ConsensusAndValidatedHash(t *testing.T) {
	orig := buildTestValidation()
	for i := range orig.ConsensusHash {
		orig.ConsensusHash[i] = byte(i + 0x10)
	}
	for i := range orig.ValidatedHash {
		orig.ValidatedHash[i] = byte(i + 0x20)
	}

	blob := SerializeSTValidation(orig)
	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	assert.Equal(t, orig.ConsensusHash, parsed.ConsensusHash)
	assert.Equal(t, orig.ValidatedHash, parsed.ValidatedHash)
}

func TestStvParseSTValidation_FieldHeaderError(t *testing.T) {
	// Enough bytes to pass the length check (>= 50), but the first
	// field header byte causes an extended-type read that runs out of data.
	data := make([]byte, 50)
	data[0] = 0x0F // typeCode nibble = 0 → extended type, but next byte is 0x00 which gives typeCode=0,fieldCode=0 → EOO
	_, err := parseSTValidation(data)
	// Should return errMissingFields (loop exits on EOO, required fields absent)
	assert.Error(t, err)
}

func TestStvSerializeSTValidation_ZeroFlagsNotFull(t *testing.T) {
	orig := buildTestValidation()
	orig.Flags = 0
	orig.Full = false

	blob := SerializeSTValidation(orig)
	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	assert.Zero(t, parsed.Flags)
	assert.False(t, parsed.Full)
}

func TestStvSerializeSTValidation_WithSignature(t *testing.T) {
	// Ensure Signature field is emitted when non-empty and omitted when empty.
	orig := buildTestValidation()
	orig.Signature = nil

	blob := SerializeSTValidation(orig)
	_, err := parseSTValidation(blob)
	assert.ErrorIs(t, err, errMissingFields)
}

func TestStvSkipAmount_IOUNonZeroMiddleBytes(t *testing.T) {
	// IOU: high bit set, but byte[0]=0x80 and a non-zero byte in the middle
	data := make([]byte, 48)
	data[0] = 0x80
	data[4] = 0x01 // non-zero byte not at position 0 → not canonical zero
	pos := 0
	n, err := skipAmount(data, &pos)
	require.NoError(t, err)
	assert.Equal(t, 48, n)
}

func TestStvParseSTValidation_ReadFieldHeaderError(t *testing.T) {
	// Seven UINT32 and three UINT16 fields place an extended header in the
	// final byte, where its required type byte is truncated.
	buf := make([]byte, 50)
	pos := 0
	// sfFlags UINT32 (field 2)
	buf[pos] = 0x22
	pos++
	// 4 bytes value
	pos += 4

	// 7 unknown UINT32 fields (use field code 1 = header 0x21)
	for i := 0; i < 7; i++ {
		buf[pos] = 0x21
		pos += 5 // 1 header + 4 data
	}

	// 3 UINT16 fields (use field code 1 = header 0x11)
	for i := 0; i < 3; i++ {
		buf[pos] = 0x11
		pos += 3 // 1 header + 2 data
	}
	buf[49] = 0x09

	_, err := parseSTValidation(buf)
	assert.Error(t, err)
}

func TestStvSkipVL_ReadVLLengthError(t *testing.T) {
	// readVLLength fails when data is empty
	pos := 0
	_, err := skipVL([]byte{}, &pos)
	assert.ErrorIs(t, err, errShortData)
}

func TestStvSkipUntilMarker_ReadFieldHeaderError(t *testing.T) {
	// marker not at start, then a truncated field header (end of data mid-header)
	// byte 0x09 at pos=0 → typeCode=0, needs extended byte, but data ends
	data := []byte{0x09}
	pos := 0
	_, err := skipUntilMarker(data, &pos, 0xE1)
	assert.Error(t, err)
}

func TestStvSkipUntilMarker_SkipFieldDataError(t *testing.T) {
	// Valid header (UINT32 = 0x22) but only 2 payload bytes (need 4) → skipFieldData fails
	data := []byte{0x22, 0x00, 0x00} // header + 2 bytes (not 4 needed for UINT32)
	pos := 0
	_, err := skipUntilMarker(data, &pos, 0xE1)
	assert.Error(t, err)
}

func TestStvReadFieldHeader_LargeTypeShortData(t *testing.T) {
	// typeCode == 0 but we're at end of data for extended type byte
	// byte with high nibble=0, low!=0 → typeCode=0 (needs extension)
	data := []byte{0x09} // typeCode=0 (needs extension), fieldCode=9
	pos := 0
	_, _, err := readFieldHeader(data, &pos)
	assert.ErrorIs(t, err, errShortData)
}
