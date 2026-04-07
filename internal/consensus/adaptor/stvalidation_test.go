package adaptor

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestValidation creates a Validation with realistic field values.
func buildTestValidation() *consensus.Validation {
	v := &consensus.Validation{
		Full:      true,
		LedgerSeq: 100,
		SignTime:  time.Unix(946684800+828618000, 0),
		Cookie:    12345,
		LoadFee:   5000,
	}
	for i := range v.LedgerID {
		v.LedgerID[i] = byte(i + 1)
	}
	for i := range v.NodeID {
		v.NodeID[i] = byte(i + 0x10)
	}
	v.NodeID[0] = 0x02                           // valid compressed pubkey prefix
	v.Signature = []byte{0x30, 0x44, 0x02, 0x20} // DER prefix + padding
	v.Signature = append(v.Signature, make([]byte, 68)...)
	return v
}

func TestParseSTValidation_Roundtrip(t *testing.T) {
	orig := buildTestValidation()
	blob := serializeSTValidation(orig)

	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	assert.Equal(t, orig.Full, parsed.Full)
	assert.Equal(t, orig.LedgerSeq, parsed.LedgerSeq)
	assert.Equal(t, orig.LedgerID, parsed.LedgerID)
	assert.Equal(t, orig.NodeID, parsed.NodeID)
	assert.Equal(t, orig.Signature, parsed.Signature)
	assert.Equal(t, orig.Cookie, parsed.Cookie)
	assert.Equal(t, orig.LoadFee, parsed.LoadFee)
	assert.WithinDuration(t, orig.SignTime, parsed.SignTime, time.Second)
}

func TestParseSTValidation_MinimalFields(t *testing.T) {
	// Build minimal STValidation: Flags + LedgerSequence + SigningTime +
	// LedgerHash + SigningPubKey + Signature.
	var buf []byte

	// sfFlags (0x22)
	buf = append(buf, 0x22)
	buf = binary.BigEndian.AppendUint32(buf, vfFullValidation)

	// sfLedgerSequence (0x26)
	buf = append(buf, 0x26)
	buf = binary.BigEndian.AppendUint32(buf, 50)

	// sfSigningTime (0x29)
	buf = append(buf, 0x29)
	buf = binary.BigEndian.AppendUint32(buf, 828618000)

	// sfLedgerHash (0x51)
	buf = append(buf, 0x51)
	var ledgerHash [32]byte
	ledgerHash[0] = 0xAA
	ledgerHash[31] = 0xBB
	buf = append(buf, ledgerHash[:]...)

	// sfSigningPubKey (0x73) - VL encoded, 33 bytes
	buf = append(buf, 0x73, 33) // VL length = 33
	pubKey := make([]byte, 33)
	pubKey[0] = 0x02
	pubKey[1] = 0xFF
	buf = append(buf, pubKey...)

	// sfSignature (0x76) - VL encoded, 70 bytes
	sig := make([]byte, 70)
	sig[0] = 0x30
	buf = append(buf, 0x76, 70)
	buf = append(buf, sig...)

	parsed, err := parseSTValidation(buf)
	require.NoError(t, err)

	assert.True(t, parsed.Full)
	assert.Equal(t, uint32(50), parsed.LedgerSeq)
	assert.Equal(t, ledgerHash, [32]byte(parsed.LedgerID))
	assert.Equal(t, pubKey[0], parsed.NodeID[0])
	assert.Equal(t, pubKey[1], parsed.NodeID[1])
	assert.Len(t, parsed.Signature, 70)
	assert.NotEmpty(t, parsed.SigningData)
}

func TestParseSTValidation_SigningDataExcludesSigOnly(t *testing.T) {
	orig := buildTestValidation()
	blob := serializeSTValidation(orig)

	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	// SigningData should include all fields except sfSignature.
	// sfSigningPubKey has isSigningField=true per XRPL spec, so it IS included.
	// Only sfSignature (isSigningField=false) is excluded.
	signatureFieldSize := 1 + 1 + len(orig.Signature) // header + VL + data
	expectedSigningLen := len(blob) - signatureFieldSize
	assert.Equal(t, expectedSigningLen, len(parsed.SigningData))
}

func TestParseSTValidation_TooShort(t *testing.T) {
	_, err := parseSTValidation([]byte{0x01, 0x02, 0x03})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

func TestParseSTValidation_MissingRequiredFields(t *testing.T) {
	// Only sfFlags — missing LedgerSequence, LedgerHash, SigningPubKey.
	var buf []byte
	buf = append(buf, 0x22)
	buf = binary.BigEndian.AppendUint32(buf, 0)

	// Pad to minimum size.
	buf = append(buf, make([]byte, 50)...)

	_, err := parseSTValidation(buf)
	assert.Error(t, err)
}

func TestParseSTValidation_UnknownFieldsSkipped(t *testing.T) {
	orig := buildTestValidation()
	blob := serializeSTValidation(orig)

	// Insert an unknown UINT32 field (type=2, field=15 = 0x2F) before the last field.
	// Find sfSigningPubKey (0x73) position and insert before it.
	var modified []byte
	for i := 0; i < len(blob); i++ {
		if blob[i] == 0x73 {
			// Insert unknown UINT32 field before sfSigningPubKey.
			// But wait, we must maintain canonical order. type=2 fields come before
			// type=5 and type=7. Let's insert after all type=2 fields instead.
			break
		}
		modified = append(modified, blob[i])
	}

	// Simpler approach: append an unknown Hash256 field (type=5, field=15 → 0x5F)
	// right before sfSigningPubKey in the blob.
	modified = nil
	insertPos := -1
	for i := 0; i < len(blob); i++ {
		if blob[i] == 0x73 {
			insertPos = i
			break
		}
	}
	require.NotEqual(t, -1, insertPos)

	modified = append(modified, blob[:insertPos]...)
	// Unknown Hash256 field: type=5, field=15 → 0x5F + 32 zero bytes
	modified = append(modified, 0x5F)
	modified = append(modified, make([]byte, 32)...)
	modified = append(modified, blob[insertPos:]...)

	parsed, err := parseSTValidation(modified)
	require.NoError(t, err)

	assert.Equal(t, orig.LedgerSeq, parsed.LedgerSeq)
	assert.Equal(t, orig.LedgerID, parsed.LedgerID)
	assert.Equal(t, orig.NodeID, parsed.NodeID)
}

func TestSerializeSTValidation_CanonicalOrder(t *testing.T) {
	v := buildTestValidation()
	blob := serializeSTValidation(v)

	// Verify field order by checking field header bytes appear in order.
	var fieldHeaders []byte
	pos := 0
	for pos < len(blob) {
		header := blob[pos]
		typeCode := int(header >> 4)
		fieldCode := int(header & 0x0F)

		if typeCode == 0 || fieldCode == 0 {
			// Multi-byte header, just track position
			break
		}
		fieldHeaders = append(fieldHeaders, header)

		// Skip field data.
		pos++
		switch typeCode {
		case typeUINT32:
			pos += 4
		case typeUINT64:
			pos += 8
		case typeHash256:
			pos += 32
		case typeBlob:
			l, _ := readVLLength(blob, &pos)
			pos += l
		}
	}

	// Canonical order: type ascending, then field ascending within type.
	for i := 1; i < len(fieldHeaders); i++ {
		prev := fieldHeaders[i-1]
		curr := fieldHeaders[i]
		prevType := prev >> 4
		currType := curr >> 4
		if currType < prevType {
			t.Errorf("field 0x%02X appears before 0x%02X but has lower type", curr, prev)
		}
		if currType == prevType {
			prevField := prev & 0x0F
			currField := curr & 0x0F
			if currField < prevField {
				t.Errorf("field 0x%02X appears before 0x%02X in same type group", curr, prev)
			}
		}
	}
}

func TestValidationFromMessage_Integration(t *testing.T) {
	orig := buildTestValidation()
	blob := serializeSTValidation(orig)

	msg := &message.Validation{Validation: blob}
	parsed, err := ValidationFromMessage(msg)
	require.NoError(t, err)

	assert.Equal(t, orig.LedgerSeq, parsed.LedgerSeq)
	assert.Equal(t, orig.LedgerID, parsed.LedgerID)
	assert.Equal(t, orig.Full, parsed.Full)
	assert.NotZero(t, parsed.SeenTime) // should be set by ValidationFromMessage
}

func TestSignSerializeParseVerify_Roundtrip(t *testing.T) {
	// This test simulates the full outbound → inbound path:
	// 1. Create a validation and sign it (like sendValidation)
	// 2. Serialize to wire format (like ValidationToMessage)
	// 3. Parse back from wire format (like ValidationFromMessage)
	// 4. Verify the signature (like OnValidation → VerifyValidation)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	orig := &consensus.Validation{
		LedgerSeq: 42,
		SignTime:  time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		Full:      true,
		NodeID:    identity.NodeID,
	}
	for i := range orig.LedgerID {
		orig.LedgerID[i] = byte(i + 1)
	}

	// Sign (outbound path)
	err = identity.SignValidation(orig)
	require.NoError(t, err)
	require.NotEmpty(t, orig.Signature)

	// Direct verify should work (self-signed, no SigningData)
	err = VerifyValidation(orig)
	require.NoError(t, err, "direct verify failed")

	// Serialize to wire format
	blob := serializeSTValidation(orig)
	require.NotEmpty(t, blob)

	// Parse back (inbound path)
	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	t.Logf("orig.NodeID[:4]:   %x", orig.NodeID[:4])
	t.Logf("parsed.NodeID[:4]: %x", parsed.NodeID[:4])
	t.Logf("parsed.LedgerSeq:  %d", parsed.LedgerSeq)
	t.Logf("parsed.SigningData len: %d", len(parsed.SigningData))
	t.Logf("parsed.Signature len:   %d", len(parsed.Signature))

	assert.Equal(t, orig.LedgerSeq, parsed.LedgerSeq)
	assert.Equal(t, orig.LedgerID, parsed.LedgerID)
	assert.Equal(t, orig.NodeID, parsed.NodeID)

	// Verify the parsed validation (inbound path, uses SigningData)
	err = VerifyValidation(parsed)
	assert.NoError(t, err, "roundtrip verify failed")
}

func TestVerifyRippledValidation(t *testing.T) {
	// Real STValidation captured from a rippled 2.6.2 node in a Kurtosis test network.
	rawHex := "22800000012600000007293163d07951fa2f307cae2053f9af20873f47bc8895d6ef9b087de9102aad99fa6a4eef215a5017c22905aa36768a95ee860d755531f8e23dc067024f69c9e1b0efb364b59dbbd87321027bd68e66c8f38f73595632131ffac4eeb96ce64fcbc3ed1c3c6b707b17adec1b76473045022100cf3f08913e0a0f2537981fcb2afee8ea10b68269bdaa63669e73787e8851b1b30220470417db44a3242ce1f88ff53ff51e130e045e9678e32678e2d3138524577fd8"
	rawBytes, err := hex.DecodeString(rawHex)
	require.NoError(t, err)

	v, err := parseSTValidation(rawBytes)
	require.NoError(t, err)

	assert.Equal(t, uint32(7), v.LedgerSeq)
	assert.True(t, v.Full)
	assert.Equal(t, byte(0x02), v.NodeID[0])

	err = VerifyValidation(v)
	assert.NoError(t, err, "rippled validation should verify correctly")
}

func TestValidationToMessage_ProducesValidBlob(t *testing.T) {
	orig := buildTestValidation()

	msg := ValidationToMessage(orig)
	require.NotEmpty(t, msg.Validation)

	// Parse the produced blob back.
	parsed, err := parseSTValidation(msg.Validation)
	require.NoError(t, err)

	assert.Equal(t, orig.LedgerSeq, parsed.LedgerSeq)
	assert.Equal(t, orig.LedgerID, parsed.LedgerID)
	assert.Equal(t, orig.NodeID, parsed.NodeID)
}
