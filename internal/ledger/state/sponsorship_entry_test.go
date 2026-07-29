package state

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func TestSponsorshipEntryRoundTrip(t *testing.T) {
	entry := &SponsorshipData{
		Owner:               [20]byte{1},
		Sponsee:             [20]byte{2},
		FeeAmount:           1_000,
		HasFeeAmount:        true,
		MaxFee:              100,
		HasMaxFee:           true,
		RemainingOwnerCount: 3,
		OwnerNode:           4,
		SponseeNode:         5,
		Flags:               0x00030000,
		Sponsor:             [20]byte{3},
		HasSponsor:          true,
		PreviousTxnID:       [32]byte{0xAA},
		PreviousTxnLgrSeq:   9,
	}
	encoded, err := SerializeSponsorship(entry)
	if err != nil {
		t.Fatalf("SerializeSponsorship: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatalf("DecodeBytes: %v", err)
	}
	if fields["LedgerEntryType"] != "Sponsorship" {
		t.Fatalf("LedgerEntryType = %#v, want Sponsorship", fields["LedgerEntryType"])
	}
	if fields["FeeAmount"] != "1000" || fields["MaxFee"] != "100" {
		t.Fatalf("fee fields = (%#v, %#v), want (1000, 100)", fields["FeeAmount"], fields["MaxFee"])
	}

	parsed, err := ParseSponsorship(encoded)
	if err != nil {
		t.Fatalf("ParseSponsorship: %v", err)
	}
	if *parsed != *entry {
		t.Fatalf("parsed Sponsorship = %#v, want %#v", parsed, entry)
	}
	roundTrip, err := SerializeSponsorship(parsed)
	if err != nil {
		t.Fatalf("re-serialize Sponsorship: %v", err)
	}
	if !bytes.Equal(roundTrip, encoded) {
		t.Fatalf("Sponsorship round-trip changed bytes\nwant %X\n got %X", encoded, roundTrip)
	}
}

func TestSponsorshipNativeFeeBoundaries(t *testing.T) {
	entry := &SponsorshipData{
		Owner:        [20]byte{1},
		Sponsee:      [20]byte{2},
		FeeAmount:    0,
		HasFeeAmount: true,
		MaxFee:       MaxNativeDrops,
		HasMaxFee:    true,
		OwnerNode:    1,
		SponseeNode:  2,
	}
	encoded, err := SerializeSponsorship(entry)
	if err != nil {
		t.Fatalf("SerializeSponsorship boundary values: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatalf("DecodeBytes: %v", err)
	}
	if fields["FeeAmount"] != "0" || fields["MaxFee"] != "100000000000000000" {
		t.Fatalf("fee boundaries = (%#v, %#v)", fields["FeeAmount"], fields["MaxFee"])
	}
	parsed, err := ParseSponsorship(encoded)
	if err != nil {
		t.Fatalf("ParseSponsorship boundary values: %v", err)
	}
	if !parsed.HasFeeAmount || parsed.FeeAmount != 0 || !parsed.HasMaxFee || parsed.MaxFee != MaxNativeDrops {
		t.Fatalf("parsed fee boundaries = %#v", parsed)
	}

	entry.MaxFee = MaxNativeDrops + 1
	if _, err := SerializeSponsorship(entry); err == nil {
		t.Fatal("SerializeSponsorship accepted MaxFee above cMaxNativeN")
	}
}

func TestParseSponsorshipRejectsNonNativeOrNegativeFees(t *testing.T) {
	owner, err := EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	sponsee, err := EncodeAccountID([20]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"LedgerEntryType":   "Sponsorship",
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
		"Owner":             owner,
		"Sponsee":           sponsee,
		"OwnerNode":         "1",
		"SponseeNode":       "2",
		"Flags":             uint32(0),
	}

	for _, test := range []struct {
		name string
		fee  any
	}{
		{name: "negative", fee: "-1"},
		{name: "IOU", fee: map[string]any{"value": "1", "currency": "USD", "issuer": owner}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fields := make(map[string]any, len(base)+1)
			for name, value := range base {
				fields[name] = value
			}
			fields["FeeAmount"] = test.fee
			raw, err := binarycodec.EncodeBytes(fields)
			if err != nil {
				t.Fatalf("EncodeBytes: %v", err)
			}
			if _, err := ParseSponsorship(raw); err == nil {
				t.Fatalf("ParseSponsorship accepted %s FeeAmount", test.name)
			}
		})
	}
}

func TestSponsorshipEntryOptionalFieldsAbsent(t *testing.T) {
	encoded, err := SerializeSponsorship(&SponsorshipData{
		Owner:       [20]byte{1},
		Sponsee:     [20]byte{2},
		OwnerNode:   1,
		SponseeNode: 2,
	})
	if err != nil {
		t.Fatalf("SerializeSponsorship: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatalf("DecodeBytes: %v", err)
	}
	for _, name := range []string{"FeeAmount", "MaxFee", "RemainingOwnerCount", "Sponsor"} {
		if _, ok := fields[name]; ok {
			t.Errorf("optional field %s should be absent", name)
		}
	}
}
