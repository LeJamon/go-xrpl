package adaptor

import (
	"bytes"
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
	blob := SerializeSTValidation(orig)

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

func TestParseSTValidation_PopulatesRaw(t *testing.T) {
	orig := buildTestValidation()
	blob := SerializeSTValidation(orig)

	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	if !bytes.Equal(parsed.Raw, blob) {
		t.Fatalf("Raw mismatch:\n got  %x\n want %x", parsed.Raw, blob)
	}

	// Raw must be an independent copy: mutating the input after parse
	// must not corrupt what the archive later persists.
	blob[0] ^= 0xFF
	if parsed.Raw[0] == blob[0] {
		t.Fatal("Raw aliases input buffer — must be a copy")
	}
}

// TestSTValidation_FlagsRoundTrip pins the wire-flag fidelity gap from
// review round 2: parseSTValidation must capture the full sfFlags word,
// and SerializeSTValidation must re-emit it verbatim, so the archive's
// flags column reflects what the validator signed (not a synthesized
// constant).
func TestSTValidation_FlagsRoundTrip(t *testing.T) {
	orig := buildTestValidation()
	// A vendor flag bit the standard parser doesn't otherwise recognize.
	const customVendorBit = 0x00010000
	orig.Flags = vfFullyCanonicalSig | vfFullValidation | customVendorBit

	blob := SerializeSTValidation(orig)
	parsed, err := parseSTValidation(blob)
	require.NoError(t, err)

	if parsed.Flags != orig.Flags {
		t.Fatalf("Flags lost on round-trip:\n got  0x%08x\n want 0x%08x", parsed.Flags, orig.Flags)
	}
	if !parsed.Full {
		t.Error("Full bit derived from Flags should be true")
	}

	// Backward-compat: a Validation with Flags=0 still serializes to the
	// canonical pair when Full=true (older constructors didn't know
	// about Flags).
	legacy := buildTestValidation()
	legacy.Flags = 0 // explicit
	legacyBlob := SerializeSTValidation(legacy)
	legacyParsed, err := parseSTValidation(legacyBlob)
	require.NoError(t, err)
	if legacyParsed.Flags != (vfFullyCanonicalSig | vfFullValidation) {
		t.Fatalf("legacy Flags=0 should synthesize canonical pair; got 0x%08x", legacyParsed.Flags)
	}
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
	blob := SerializeSTValidation(orig)

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
	blob := SerializeSTValidation(orig)

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
	blob := SerializeSTValidation(v)

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
	blob := SerializeSTValidation(orig)

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
	blob := SerializeSTValidation(orig)
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

// TestSerializeSTValidation_CanonicalOrder_Hash256BeforeAmount asserts
// that Hash256 fields (type 5) precede Amount fields (type 6) in the
// produced blob when both are present. Rippled's
// STObject::getSigningHash re-serializes in canonical order, so a
// validator that emits AMOUNT before HASH256 (as a prior version did)
// produces a signing preimage that rippled peers cannot reproduce →
// signature verification fails on featureXRPFees flag ledgers where
// both HASH256 and AMOUNT fee-vote fields are present.
//
// Distinct from the pre-existing TestSerializeSTValidation_CanonicalOrder
// which only exercises the default field set — that test wouldn't catch
// the AMOUNT/HASH256 swap because buildTestValidation doesn't populate
// any AMOUNT field.
func TestSerializeSTValidation_CanonicalOrder_Hash256BeforeAmount(t *testing.T) {
	v := buildTestValidation()
	// Populate optional fields across several type codes so the order
	// check has something to walk — in particular, a Hash256
	// (ConsensusHash, type 5) and multiple Amounts (BaseFeeDrops etc.,
	// type 6) must be present to exercise the 5-before-6 invariant
	// that the prior bug inverted.
	for i := range v.ConsensusHash {
		v.ConsensusHash[i] = byte(i + 0x40)
	}
	for i := range v.ValidatedHash {
		v.ValidatedHash[i] = byte(i + 0x60)
	}
	v.BaseFeeDrops = 10
	v.ReserveBaseDrops = 20
	v.ReserveIncrementDrops = 5

	blob := SerializeSTValidation(v)
	require.NotEmpty(t, blob)

	// Walk each top-level field header and record the type code. We
	// don't care about field codes here — the canonical rule orders
	// by (type<<16)|field, so strictly-ascending type is a necessary
	// condition (sufficient when we only have one field per type code
	// modulo the known within-type ordering, which is separately
	// enforced by the parser).
	var typesSeen []int
	pos := 0
	for pos < len(blob) {
		typeCode, _, err := readFieldHeader(blob, &pos)
		require.NoError(t, err)
		typesSeen = append(typesSeen, typeCode)
		// Skip past the value bytes.
		_, err = skipFieldData(typeCode, blob, &pos)
		require.NoError(t, err)
	}

	// Types must be non-decreasing across the whole blob.
	for i := 1; i < len(typesSeen); i++ {
		assert.LessOrEqualf(t, typesSeen[i-1], typesSeen[i],
			"field at index %d (type %d) must not precede field at index %d (type %d)",
			i-1, typesSeen[i-1], i, typesSeen[i])
	}

	// And specifically: Hash256 (5) must appear before Amount (6).
	var firstAmount, firstHash256 = -1, -1
	for i, t := range typesSeen {
		if firstHash256 < 0 && t == typeHash256 {
			firstHash256 = i
		}
		if firstAmount < 0 && t == typeAmount {
			firstAmount = i
		}
	}
	require.GreaterOrEqual(t, firstHash256, 0, "Hash256 field expected but missing")
	require.GreaterOrEqual(t, firstAmount, 0, "Amount field expected but missing")
	assert.Less(t, firstHash256, firstAmount,
		"Hash256 (type 5) must precede Amount (type 6) per XRPL canonical ordering")
}

// TestSignVerifyRoundTrip_AllOptionalFields asserts that a Validation
// populated with every optional field (Hash256, Amount, Vector256, and
// the legacy UINT fee-vote fields) signs and self-verifies. Any
// divergence between the serializer's output and the signing preimage
// — e.g., the fields emitted in a different canonical order — produces
// a signature that verifies against the WRONG preimage, so this test
// fails.
//
// This is the load-bearing regression for the AMOUNT-before-HASH256
// bug: before the fix, adding Amount fee-vote fields to a validation
// produced a signing preimage that didn't match the wire bytes.
func TestSignVerifyRoundTrip_AllOptionalFields(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	v := buildTestValidation()
	v.NodeID = identity.NodeID
	for i := range v.ConsensusHash {
		v.ConsensusHash[i] = byte(i + 0x40)
	}
	for i := range v.ValidatedHash {
		v.ValidatedHash[i] = byte(i + 0x60)
	}
	// Post-XRPFees AMOUNT triple. These are what exposed the prior
	// canonical-ordering bug: without AMOUNT fields the bug was dormant.
	v.BaseFeeDrops = 10
	v.ReserveBaseDrops = 20
	v.ReserveIncrementDrops = 5
	v.ServerVersion = 0x0200000000000000

	require.NoError(t, identity.SignValidation(v))
	require.NotEmpty(t, v.Signature)

	// Re-parse the serialized form so the verify path reads the same
	// bytes a peer would receive. If serializer and preimage-builder
	// disagree, the signature doesn't match what the peer hashes and
	// verify fails.
	msg := ValidationToMessage(v)
	reparsed, err := parseSTValidation(msg.Validation)
	require.NoError(t, err)

	assert.NoError(t, VerifyValidation(reparsed),
		"signature must verify against the reparsed wire bytes — divergence here means serializer and signing preimage disagree on field order")
}

// TestParseSTValidation_CloseTime pins R6b.5c: the parser must
// surface sfCloseTime onto Validation.CloseTime when present. Rippled
// lists this field as soeOPTIONAL (STValidation.cpp:63); pre-R6b.5c
// the Go parser silently discarded it, so RPC consumers that need
// per-validation close times couldn't get them.
func TestParseSTValidation_CloseTime(t *testing.T) {
	// Craft a minimal validation wire payload with sfCloseTime set.
	// We build it by hand (type UINT32, field 7) and append the
	// minimal required fields around it so the parser doesn't bail
	// on missing mandatory fields.
	closeEpoch := uint32(946684800 + 123456789 - 946684800) // XRPL epoch seconds
	var closeTimeBytes [4]byte
	binary.BigEndian.PutUint32(closeTimeBytes[:], closeEpoch)

	// Build a representative validation via the serializer, then
	// inject sfCloseTime into its byte stream. Easiest: build a
	// Validation with the struct field set and go through a full
	// sign/serialize/reparse cycle below once that path learns to
	// emit sfCloseTime. For now, verify the parser direction only
	// by crafting the minimal wire frame.
	//
	// Minimal required validation: flags(UINT32 field 2),
	// ledger_sequence(UINT32 field 6), signing_time(UINT32 field 9),
	// ledger_hash(HASH256 field 1), node_id(VL(pubkey) field 3),
	// signature(VL field 6). We include sfCloseTime (UINT32 field 7)
	// in canonical position between ledger_sequence and
	// signing_time.
	buf := make([]byte, 0, 256)

	// flags (type 2, field 2) — full validation
	buf = appendFieldHeader(buf, typeUINT32, fieldFlags)
	var flagsBytes [4]byte
	binary.BigEndian.PutUint32(flagsBytes[:], vfFullValidation)
	buf = append(buf, flagsBytes[:]...)

	// ledger_sequence (type 2, field 6)
	buf = appendFieldHeader(buf, typeUINT32, fieldLedgerSequence)
	var seqBytes [4]byte
	binary.BigEndian.PutUint32(seqBytes[:], 999)
	buf = append(buf, seqBytes[:]...)

	// close_time (type 2, field 7) — THE field under test
	buf = appendFieldHeader(buf, typeUINT32, fieldCloseTime)
	buf = append(buf, closeTimeBytes[:]...)

	// signing_time (type 2, field 9)
	buf = appendFieldHeader(buf, typeUINT32, fieldSigningTime)
	var sigTimeBytes [4]byte
	binary.BigEndian.PutUint32(sigTimeBytes[:], 946684800+1_000_000-946684800)
	buf = append(buf, sigTimeBytes[:]...)

	// ledger_hash (type 5, field 1) — must be non-zero to pass the
	// required-fields check at the end of parseSTValidation.
	buf = appendFieldHeader(buf, typeHash256, fieldLedgerHash)
	ledgerHash := make([]byte, 32)
	for i := range ledgerHash {
		ledgerHash[i] = byte(i + 1)
	}
	buf = append(buf, ledgerHash...)

	// signing_pubkey / node_id (type 7, field 3) — 33-byte secp256k1
	nodeID := make([]byte, 33)
	nodeID[0] = 0x02
	buf = appendFieldHeader(buf, typeBlob, fieldSigningPubKey)
	buf = append(buf, 33) // VL length
	buf = append(buf, nodeID...)

	// signature (type 7, field 6) — dummy; parser doesn't verify here
	buf = appendFieldHeader(buf, typeBlob, fieldSignature)
	buf = append(buf, 70)
	buf = append(buf, make([]byte, 70)...)

	v, err := parseSTValidation(buf)
	require.NoError(t, err, "parser must accept a validation with sfCloseTime")
	assert.False(t, v.CloseTime.IsZero(),
		"CloseTime must be populated when sfCloseTime is present")
	assert.Equal(t, int64(closeEpoch)+946684800, v.CloseTime.Unix(),
		"CloseTime must decode back to the original XRPL epoch seconds")
}

// TestSFieldTypeCodes_MatchRippled pins R6b.4: every type constant in
// stvalidation.go must match rippled's SField.h values. Off-by-N is
// latent only until a validation field of that type is added; a future
// field would then sort in the wrong (type<<16)|field position.
//
// Source of truth: rippled/include/xrpl/protocol/SField.h:65-87.
func TestSFieldTypeCodes_MatchRippled(t *testing.T) {
	expectations := map[string]struct {
		got  int
		want int
	}{
		"UINT16":    {typeUINT16, 1},
		"UINT32":    {typeUINT32, 2},
		"UINT64":    {typeUINT64, 3},
		"HASH128":   {typeHash128, 4},
		"HASH256":   {typeHash256, 5},
		"AMOUNT":    {typeAmount, 6},
		"VL":        {typeBlob, 7},
		"ACCOUNT":   {typeAccountID, 8},
		"OBJECT":    {typeSTObject, 14},
		"ARRAY":     {typeSTArray, 15},
		"UINT8":     {typeUINT8, 16},
		"HASH160":   {typeHash160, 17},
		"PATHSET":   {typePathSet, 18},
		"VECTOR256": {typeVector256, 19},
		"UINT96":    {typeUINT96, 20},
		"UINT192":   {typeUINT192, 21},
		"UINT384":   {typeUINT384, 22},
		"UINT512":   {typeUINT512, 23},
	}
	for name, tc := range expectations {
		t.Run(name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s: got %d, want %d (see rippled SField.h:65-87)", name, tc.got, tc.want)
			}
		})
	}
}
