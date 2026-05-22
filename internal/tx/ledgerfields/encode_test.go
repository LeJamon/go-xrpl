package ledgerfields

import (
	"bytes"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/protocol"
)

// TestRoundTrip_TypedSLE verifies that Decode → Encode is byte-identical
// against the canonical binarycodec output for a representative set of
// ledger entries. Each case covers a distinct value-shape category
// (XRP/IOU Amount, Vector256, STArray, Blob, UInt64-hex, etc.) so a
// regression in any of the typed encoder's per-type paths trips at least
// one case.
func TestRoundTrip_TypedSLE(t *testing.T) {
	cases := []struct {
		name string
		json map[string]any
	}{
		{
			name: "AccountRoot_XRP_Amount",
			json: map[string]any{
				"LedgerEntryType":   "AccountRoot",
				"Account":           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Balance":           "1000000",
				"Sequence":          uint32(1),
				"OwnerCount":        uint32(0),
				"Flags":             uint32(0),
				"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq": uint32(1),
			},
		},
		{
			name: "Offer_IOU_Amounts",
			json: map[string]any{
				"LedgerEntryType": "Offer",
				"Account":         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"Sequence":        uint32(7),
				"TakerPays":       map[string]any{"value": "100", "currency": "USD", "issuer": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
				"TakerGets":       "1000000",
				"BookDirectory":   "0000000000000000000000000000000000000000000000000000000000000000",
				"BookNode":        "0",
				"OwnerNode":       "0",
				"Flags":           uint32(0),
			},
		},
		{
			name: "DirectoryNode_Vector256_Indexes",
			json: map[string]any{
				"LedgerEntryType": "DirectoryNode",
				"Flags":           uint32(0),
				"RootIndex":       "1111111111111111111111111111111111111111111111111111111111111111",
				"Indexes": []any{
					"2222222222222222222222222222222222222222222222222222222222222222",
					"3333333333333333333333333333333333333333333333333333333333333333",
				},
				"Owner": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
		},
		{
			name: "SignerList_STArray_SignerEntries",
			json: map[string]any{
				"LedgerEntryType": "SignerList",
				"Flags":           uint32(0),
				"OwnerNode":       "0",
				"SignerQuorum":    uint32(3),
				"SignerEntries": []any{
					map[string]any{
						"SignerEntry": map[string]any{
							"Account":      "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
							"SignerWeight": uint32(1),
						},
					},
				},
				"SignerListID":      uint32(0),
				"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
				"PreviousTxnLgrSeq": uint32(1),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := binarycodec.EncodeBytes(tc.json)
			if err != nil {
				t.Fatalf("binarycodec.EncodeBytes: %v", err)
			}

			entry := New(tc.json["LedgerEntryType"].(string))
			if entry == nil {
				t.Fatalf("no typed entry registered for %q", tc.json["LedgerEntryType"])
			}
			if err := entry.Decode(canonical); err != nil {
				t.Fatalf("Decode: %v", err)
			}

			enc, ok := entry.(interface {
				Encode() ([]byte, error)
			})
			if !ok {
				t.Fatalf("entry %T does not implement Encode()", entry)
			}
			got, err := enc.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(got, canonical) {
				t.Fatalf("round-trip mismatch:\ncanonical: %x\nencoded:   %x", canonical, got)
			}
		})
	}
}

// TestHash_LeafNodeFormula pins the SLE hash to rippled's
// sha512Half(HashPrefixLeafNode || serializedData || index) formula. The
// generated Hash method must produce the same bytes a SHAMap account-state
// leaf would store for this entry.
func TestHash_LeafNodeFormula(t *testing.T) {
	json := map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"Balance":           "1000000",
		"Sequence":          uint32(1),
		"OwnerCount":        uint32(0),
		"Flags":             uint32(0),
		"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
		"PreviousTxnLgrSeq": uint32(1),
	}
	canonical, err := binarycodec.EncodeBytes(json)
	if err != nil {
		t.Fatalf("binarycodec.EncodeBytes: %v", err)
	}

	var index [32]byte
	for i := range index {
		index[i] = byte(i + 1)
	}

	expected := common.Sha512Half(protocol.HashPrefixLeafNode[:], canonical, index[:])

	entry := New("AccountRoot")
	if err := entry.Decode(canonical); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	hasher, ok := entry.(interface {
		Hash(index [32]byte) ([32]byte, error)
	})
	if !ok {
		t.Fatalf("entry %T does not implement Hash()", entry)
	}
	got, err := hasher.Hash(index)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if got != expected {
		t.Fatalf("hash mismatch:\nexpected: %x\ngot:      %x", expected, got)
	}
}
