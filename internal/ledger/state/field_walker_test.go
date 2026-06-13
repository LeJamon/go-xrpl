package state

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	walkerTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	walkerTestIssuer  = "rweYz56rfmQ98cAdRaeTxQS9wVMGnrdsFp"
)

// collectFields walks an SLE and returns every (typeCode, fieldCode) pair plus a
// lookup of the first value seen for each field key.
func collectFields(t *testing.T, data []byte) ([]Field, map[[2]int][]byte) {
	t.Helper()
	var fields []Field
	values := make(map[[2]int][]byte)
	err := WalkFields(data, func(f Field) error {
		fields = append(fields, f)
		key := [2]int{f.TypeCode, f.FieldCode}
		if _, ok := values[key]; !ok {
			values[key] = f.Value
		}
		return nil
	})
	require.NoError(t, err)
	return fields, values
}

// TestWalkFields_AccountRoot round-trips a serialized AccountRoot: the walker
// must reach LedgerEntryType (always first) and decode the UInt32 Sequence /
// Flags and the VL-encoded Account fields without desyncing.
func TestWalkFields_AccountRoot(t *testing.T) {
	t.Parallel()

	ar := &AccountRoot{
		Account:    walkerTestAccount,
		Balance:    1_000_000_000,
		Sequence:   7,
		OwnerCount: 3,
		Flags:      0x00100000,
		Domain:     "example.com",
	}
	data, err := SerializeAccountRoot(ar)
	require.NoError(t, err)

	fields, values := collectFields(t, data)
	require.NotEmpty(t, fields)

	// LedgerEntryType is UInt16 (type 1) field 1 and must be first.
	assert.Equal(t, 1, fields[0].TypeCode)
	assert.Equal(t, 1, fields[0].FieldCode)
	assert.Equal(t, uint16(0x0061), binary.BigEndian.Uint16(fields[0].Value))

	// Sequence is UInt32 (type 2) field 4.
	seqVal, ok := values[[2]int{2, 4}]
	require.True(t, ok, "Sequence field must be present")
	assert.Equal(t, uint32(7), binary.BigEndian.Uint32(seqVal))

	// Flags is UInt32 (type 2) field 2.
	flagsVal, ok := values[[2]int{2, 2}]
	require.True(t, ok, "Flags field must be present")
	assert.Equal(t, uint32(0x00100000), binary.BigEndian.Uint32(flagsVal))

	// Account is AccountID (type 8) field 1: 20 bytes after the 1-byte VL prefix.
	acctVal, ok := values[[2]int{8, 1}]
	require.True(t, ok, "Account field must be present")
	require.Len(t, acctVal, 21, "AccountID value includes the 1-byte length prefix")
	assert.Equal(t, byte(20), acctVal[0])
}

// TestWalkFields_OfferWithAdditionalBooks round-trips an Offer carrying IOU
// amounts (48-byte) and a single-entry AdditionalBooks STArray. The nested array
// must be skipped via its end markers, not by guessing a width.
func TestWalkFields_OfferWithAdditionalBooks(t *testing.T) {
	t.Parallel()

	var bookDir, addlBookDir [32]byte
	bookDir[31] = 0xAB
	addlBookDir[31] = 0xCD

	offer := &LedgerOffer{
		Account:                 walkerTestAccount,
		Sequence:                12,
		Flags:                   0x00040000,
		TakerPays:               NewIssuedAmountFromValue(100, -2, "USD", walkerTestIssuer),
		TakerGets:               NewIssuedAmountFromValue(200, -2, "EUR", walkerTestIssuer),
		BookDirectory:           bookDir,
		BookNode:                1,
		OwnerNode:               2,
		AdditionalBookDirectory: addlBookDir,
		AdditionalBookNode:      3,
	}
	data, err := SerializeLedgerOffer(offer)
	require.NoError(t, err)

	fields, values := collectFields(t, data)

	// Entry type reached.
	assert.Equal(t, uint16(0x006f), EntryTypeCode(data))

	// Two Amount (type 6) fields: TakerPays (field 4) and TakerGets (field 5),
	// each 48 bytes (IOU).
	pays, ok := values[[2]int{6, 4}]
	require.True(t, ok, "TakerPays must be present")
	assert.Len(t, pays, 48)
	gets, ok := values[[2]int{6, 5}]
	require.True(t, ok, "TakerGets must be present")
	assert.Len(t, gets, 48)

	// The AdditionalBooks STArray (type 15) must be present and walked without
	// error — its presence proves nestedEnd consumed the inner Book object.
	var sawArray bool
	for _, f := range fields {
		if f.TypeCode == 15 {
			sawArray = true
		}
	}
	assert.True(t, sawArray, "AdditionalBooks STArray must be walked")
}

// TestWalkFields_PermissionedDomainCredentials round-trips a PermissionedDomain
// whose AcceptedCredentials STArray holds inner Credential objects with a
// VL-encoded CredentialType blob — exercising nested object + VL skipping.
func TestWalkFields_PermissionedDomainCredentials(t *testing.T) {
	t.Parallel()

	issuerID, err := DecodeAccountID(walkerTestIssuer)
	require.NoError(t, err)

	pd := &PermissionedDomainData{
		Sequence:  5,
		OwnerNode: 1,
		AcceptedCredentials: []PermissionedDomainCredential{
			{Issuer: issuerID, CredentialType: []byte("KYC")},
			{Issuer: issuerID, CredentialType: []byte("AML")},
		},
	}
	data, err := SerializePermissionedDomain(pd, walkerTestAccount)
	require.NoError(t, err)

	fields, _ := collectFields(t, data)
	assert.Equal(t, uint16(0x0082), EntryTypeCode(data))

	var sawArray bool
	for _, f := range fields {
		if f.TypeCode == 15 { // AcceptedCredentials STArray
			sawArray = true
		}
	}
	assert.True(t, sawArray, "AcceptedCredentials STArray must be walked")

	// Re-parsing through the codec confirms the walker delimited every field
	// correctly (a desync would leave trailing garbage the codec rejects).
	parsed, err := ParsePermissionedDomain(data)
	require.NoError(t, err)
	assert.Len(t, parsed.AcceptedCredentials, 2)
}

// TestWalkFields_MPTAmount verifies the 33-byte MPT Amount form is delimited
// correctly (high bit clear, 0x20 bit set).
func TestWalkFields_MPTAmount(t *testing.T) {
	t.Parallel()

	// Build a minimal STObject by hand: LedgerEntryType (0x11 + 2 bytes) then an
	// Amount field (type 6, field 8 -> header 0x68) holding a 33-byte MPT value.
	data := []byte{0x11, 0x00, 0x6f} // pretend Offer type
	data = append(data, 0x68)        // Amount, field 8
	mpt := make([]byte, 33)
	mpt[0] = 0x20 // MPT marker: high bit clear, 0x20 set
	data = append(data, mpt...)

	_, values := collectFields(t, data)
	amt, ok := values[[2]int{6, 8}]
	require.True(t, ok)
	assert.Len(t, amt, 33, "MPT Amount must be delimited at 33 bytes")
}

// TestWalkFields_UnsupportedType ensures the walker refuses to guess a width for
// a composite/variable type it does not handle, rather than silently desyncing.
func TestWalkFields_UnsupportedType(t *testing.T) {
	t.Parallel()

	// PathSet (type 18, field 1). Type code > 15 uses the extended form:
	// header high nibble 0, then the type byte (0x12), then the field nibble.
	data := []byte{0x01, 0x12, 0x00, 0x00}
	err := WalkFields(data, func(Field) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported serialized type 18")
}

// TestWalkFields_Truncated ensures a truncated value is reported as an error.
func TestWalkFields_Truncated(t *testing.T) {
	t.Parallel()

	// UInt32 (type 2) field 4 -> header 0x24, but only 2 value bytes present.
	data := []byte{0x24, 0x00, 0x00}
	err := WalkFields(data, func(Field) error { return nil })
	require.Error(t, err)
}

// TestEntryTypeName covers the code→name mapping, including the deprecated and
// newer entry types.
func TestEntryTypeName(t *testing.T) {
	t.Parallel()

	cases := map[uint16]string{
		0x0061: "AccountRoot",
		0x006f: "Offer",
		0x0072: "RippleState",
		0x0079: "AMM",
		0x0084: "Vault",
		0x0070: "DepositPreauth",
		0x0067: "GeneratorMap", // deprecated, never instantiated
	}
	for code, name := range cases {
		assert.Equal(t, name, EntryTypeName(code))
	}
	assert.Contains(t, EntryTypeName(0x9999), "Unknown")
}

// TestGetOwnerNode_TicketSeqContainsHeaderByte pins the regression where a
// 0x34 byte inside an earlier field's VALUE (here TicketSequence = 52 =
// 0x00000034) was mistaken for the OwnerNode field header by the old byte-scan,
// yielding a garbage directory-page hint. The walker-based parse must return
// the real OwnerNode.
func TestGetOwnerNode_TicketSeqContainsHeaderByte(t *testing.T) {
	t.Parallel()

	// Minimal Ticket-shaped SLE: LedgerEntryType, Flags, TicketSequence(52),
	// OwnerNode(2), in canonical (type, nth) order.
	data := []byte{
		0x11, 0x00, 0x54, // LedgerEntryType = Ticket
		0x22, 0x00, 0x00, 0x00, 0x00, // Flags = 0
		0x20, 0x29, 0x00, 0x00, 0x00, 0x34, // TicketSequence = 52
		0x34, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, // OwnerNode = 2
	}
	assert.Equal(t, uint64(2), GetOwnerNode(data))
}

// TestGetOwnerNode_Absent returns 0 when the SLE has no OwnerNode field.
func TestGetOwnerNode_Absent(t *testing.T) {
	t.Parallel()

	data := []byte{
		0x11, 0x00, 0x54, // LedgerEntryType = Ticket
		0x22, 0x00, 0x00, 0x00, 0x00, // Flags = 0
	}
	assert.Equal(t, uint64(0), GetOwnerNode(data))
}
