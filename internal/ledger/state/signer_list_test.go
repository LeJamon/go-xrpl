package state

import (
	"bytes"
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// TestSerializeSignerList_MacroFieldSet asserts the serialized SignerList blob
// carries exactly the ltSIGNER_LIST template field set (SignerEntries,
// SignerQuorum, SignerListID, Flags, OwnerNode) and is byte-identical to a
// hand-built rippled-canonical blob. The SLE must NOT carry a top-level
// Account: rippled's ltSIGNER_LIST has no sfAccount and template enforcement
// would reject one, forking account_hash on the first SignerListSet.
func TestSerializeSignerList_MacroFieldSet(t *testing.T) {
	addrA, _ := EncodeAccountID([20]byte{0x01})
	addrB, _ := EncodeAccountID([20]byte{0x02})
	entries := []SignerEntry{
		{Account: addrA, SignerWeight: 1},
		{Account: addrB, SignerWeight: 2},
	}

	data, err := SerializeSignerList(3, entries, 0, false, 0)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := fields["Account"]; ok {
		t.Errorf("SignerList must not carry a top-level Account (rippled ltSIGNER_LIST has none)")
	}
	for _, name := range []string{"SignerQuorum", "SignerListID", "Flags", "OwnerNode", "SignerEntries"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("SignerList missing required field %q", name)
		}
	}
	if v, _ := soeToUint64(fields["SignerListID"]); v != 0 {
		t.Errorf("SignerListID = %v, want 0 (rippled defaultSignerListID_)", fields["SignerListID"])
	}
	if v, _ := soeToUint64(fields["Flags"]); v != 0 {
		t.Errorf("Flags = %v, want 0 (soeREQUIRED, present at default)", fields["Flags"])
	}

	// Byte-lockstep: a hand-built rippled-canonical blob carrying exactly the
	// macro field set must equal the serializer output (binarycodec orders
	// fields by code, matching rippled's canonical STObject serialization).
	canonical := map[string]any{
		"LedgerEntryType": "SignerList",
		"Flags":           uint32(0),
		"SignerQuorum":    uint32(3),
		"SignerListID":    uint32(0),
		"OwnerNode":       "0",
		"SignerEntries": []map[string]any{
			{"SignerEntry": map[string]any{"Account": addrA, "SignerWeight": uint16(1)}},
			{"SignerEntry": map[string]any{"Account": addrB, "SignerWeight": uint16(2)}},
		},
	}
	canonHex, err := binarycodec.Encode(canonical)
	if err != nil {
		t.Fatalf("encode canonical: %v", err)
	}
	canonBytes, err := hex.DecodeString(canonHex)
	if err != nil {
		t.Fatalf("decode canonical hex: %v", err)
	}
	if !bytes.Equal(data, canonBytes) {
		t.Errorf("SLE bytes diverge from rippled-canonical blob:\n got  %x\n want %x", data, canonBytes)
	}
}

// TestParseSignerList_LegacyAccountBlob asserts the read path still decodes
// legacy go-xrpl blobs that carry a top-level Account and lack SignerListID.
// Pre-fix releases wrote them; a node reading such an entry must not error
// (dual-read, single-write).
func TestParseSignerList_LegacyAccountBlob(t *testing.T) {
	addrA, _ := EncodeAccountID([20]byte{0x01})
	addrB, _ := EncodeAccountID([20]byte{0x02})
	legacy := map[string]any{
		"LedgerEntryType": "SignerList",
		"Account":         addrA,
		"Flags":           uint32(0),
		"SignerQuorum":    uint32(3),
		"OwnerNode":       "0",
		"SignerEntries": []map[string]any{
			{"SignerEntry": map[string]any{"Account": addrA, "SignerWeight": uint16(1)}},
			{"SignerEntry": map[string]any{"Account": addrB, "SignerWeight": uint16(2)}},
		},
	}
	legacyHex, err := binarycodec.Encode(legacy)
	if err != nil {
		t.Fatalf("encode legacy: %v", err)
	}
	legacyBytes, err := hex.DecodeString(legacyHex)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}

	info, err := ParseSignerList(legacyBytes)
	if err != nil {
		t.Fatalf("ParseSignerList must tolerate a legacy Account blob: %v", err)
	}
	if info.SignerQuorum != 3 {
		t.Errorf("SignerQuorum = %d, want 3", info.SignerQuorum)
	}
	if len(info.SignerEntries) != 2 {
		t.Fatalf("SignerEntries = %d, want 2", len(info.SignerEntries))
	}
	if info.SignerEntries[0].SignerWeight != 1 || info.SignerEntries[1].SignerWeight != 2 {
		t.Errorf("signer weights = %d,%d, want 1,2",
			info.SignerEntries[0].SignerWeight, info.SignerEntries[1].SignerWeight)
	}
}
