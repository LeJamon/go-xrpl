package state

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// TestSerializeSignerList_MacroFieldSet asserts the serialized SignerList blob
// carries exactly the ltSIGNER_LIST template field set and is byte-identical to a
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

	data, err := SerializeSignerList(3, entries, 0, false, 0, nil)
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
		"LedgerEntryType":   "SignerList",
		"Flags":             uint32(0),
		"SignerQuorum":      uint32(3),
		"SignerListID":      uint32(0),
		"OwnerNode":         "0",
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
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
