package state

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func TestAccountRootSponsorCountersRoundTrip(t *testing.T) {
	entry := &AccountRoot{
		Account:                walkerTestAccount,
		SponsoredOwnerCount:    3,
		SponsoringOwnerCount:   4,
		SponsoringAccountCount: 2,
	}
	encoded, err := SerializeAccountRoot(entry)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	fields, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatalf("DecodeBytes: %v", err)
	}
	for name, want := range map[string]uint32{
		"SponsoredOwnerCount":    3,
		"SponsoringOwnerCount":   4,
		"SponsoringAccountCount": 2,
	} {
		if got := fields[name]; got != want {
			t.Errorf("%s = %#v, want %d", name, got, want)
		}
	}
	parsed, err := ParseAccountRoot(encoded)
	if err != nil {
		t.Fatalf("ParseAccountRoot: %v", err)
	}
	if parsed.SponsoredOwnerCount != 3 || parsed.SponsoringOwnerCount != 4 || parsed.SponsoringAccountCount != 2 {
		t.Fatalf("parsed sponsor counters = (%d, %d, %d)", parsed.SponsoredOwnerCount, parsed.SponsoringOwnerCount, parsed.SponsoringAccountCount)
	}
	roundTrip, err := SerializeAccountRoot(parsed)
	if err != nil {
		t.Fatalf("re-serialize AccountRoot: %v", err)
	}
	if !bytes.Equal(roundTrip, encoded) {
		t.Fatalf("AccountRoot sponsor counter round-trip changed bytes\nwant %X\n got %X", encoded, roundTrip)
	}
}

func TestRippleStateSponsorsRoundTrip(t *testing.T) {
	low, err := EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	high, err := EncodeAccountID([20]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	line := &RippleState{
		Balance:     NewIssuedAmountFromValue(0, zeroExponent, "USD", accountOne),
		LowLimit:    NewIssuedAmountFromValue(0, zeroExponent, "USD", low),
		HighLimit:   NewIssuedAmountFromValue(0, zeroExponent, "USD", high),
		HighSponsor: walkerTestAccount,
		LowSponsor:  low,
	}
	encoded, err := SerializeRippleState(line)
	if err != nil {
		t.Fatalf("SerializeRippleState: %v", err)
	}
	parsed, err := ParseRippleState(encoded)
	if err != nil {
		t.Fatalf("ParseRippleState: %v", err)
	}
	if parsed.HighSponsor != line.HighSponsor || parsed.LowSponsor != line.LowSponsor {
		t.Fatalf("parsed sponsors = (%q, %q), want (%q, %q)", parsed.HighSponsor, parsed.LowSponsor, line.HighSponsor, line.LowSponsor)
	}
	roundTrip, err := SerializeRippleState(parsed)
	if err != nil {
		t.Fatalf("re-serialize RippleState: %v", err)
	}
	if !bytes.Equal(roundTrip, encoded) {
		t.Fatalf("RippleState sponsor round-trip changed bytes\nwant %X\n got %X", encoded, roundTrip)
	}
}
