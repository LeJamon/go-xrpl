package state

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestOfferChangedSnapshotOptionalsAreCleared(t *testing.T) {
	raw, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "Offer",
		"Account":           walkerTestAccount,
		"Sequence":          uint32(1),
		"TakerPays":         "1",
		"TakerGets":         "2",
		"BookDirectory":     zeroHash256,
		"BookNode":          "0",
		"OwnerNode":         "0",
		"Expiration":        uint32(9),
		"Flags":             uint32(0),
		"DomainID":          strings.Repeat("11", 32),
		"Sponsor":           walkerTestAccount,
		"PreviousTxnID":     zeroHash256,
		"PreviousTxnLgrSeq": uint32(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := ParseLedgerOffer(raw)
	if err != nil {
		t.Fatal(err)
	}
	offer.Expiration = 0
	offer.DomainID = [32]byte{}
	offer.Sponsor = ""
	encoded, err := SerializeLedgerOffer(offer)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeSLE(t, encoded)
	for _, name := range []string{"Expiration", "DomainID", "Sponsor"} {
		if _, ok := fields[name]; ok {
			t.Errorf("changed-to-default %s remained present", name)
		}
	}
}

func TestOfferPreservesPresentEmptyAdditionalBooks(t *testing.T) {
	raw, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "Offer",
		"Account":           walkerTestAccount,
		"Sequence":          uint32(1),
		"TakerPays":         "1",
		"TakerGets":         "2",
		"BookDirectory":     zeroHash256,
		"BookNode":          "0",
		"OwnerNode":         "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     zeroHash256,
		"PreviousTxnLgrSeq": uint32(0),
		"AdditionalBooks":   []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := ParseLedgerOffer(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := SerializeLedgerOffer(offer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("present-empty AdditionalBooks changed bytes\nwant %X\n got %X", raw, encoded)
	}
}

func TestPayChannelSnapshotTimesClearAndTagsRoundTrip(t *testing.T) {
	raw, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "PayChannel",
		"Account":           walkerTestAccount,
		"Destination":       walkerTestAccount,
		"Amount":            "100",
		"Balance":           "10",
		"PublicKey":         "ABCD",
		"SettleDelay":       uint32(60),
		"OwnerNode":         "1",
		"Flags":             uint32(0),
		"Expiration":        uint32(9),
		"CancelAfter":       uint32(10),
		"SourceTag":         uint32(11),
		"DestinationTag":    uint32(12),
		"PreviousTxnID":     zeroHash256,
		"PreviousTxnLgrSeq": uint32(17),
	})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := ParsePayChannel(raw)
	if err != nil {
		t.Fatal(err)
	}
	if channel.SourceTag != 11 || channel.DestinationTag != 12 {
		t.Fatalf("tags = (%d, %d), want (11, 12)", channel.SourceTag, channel.DestinationTag)
	}
	unchanged, err := SerializePayChannelFromData(channel)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, unchanged) {
		t.Fatalf("unchanged channel differs: %X != %X", raw, unchanged)
	}
	channel.Expiration = 0
	channel.CancelAfter = 0
	encoded, err := SerializePayChannelFromData(channel)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeSLE(t, encoded)
	for _, name := range []string{"Expiration", "CancelAfter"} {
		if _, ok := fields[name]; ok {
			t.Errorf("changed-to-default %s remained present", name)
		}
	}
	if fields["SourceTag"] != uint32(11) || fields["DestinationTag"] != uint32(12) {
		t.Fatalf("serialized tags = (%v, %v), want (11, 12)", fields["SourceTag"], fields["DestinationTag"])
	}
}

func TestRippleStateChangedSponsorsAreCleared(t *testing.T) {
	issued := func(issuer string) map[string]any {
		return map[string]any{"value": "0", "currency": "USD", "issuer": issuer}
	}
	raw, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "RippleState",
		"Flags":             uint32(0),
		"Balance":           issued(AccountOneAddress),
		"LowLimit":          issued(walkerTestAccount),
		"HighLimit":         issued(walkerTestAccount),
		"HighSponsor":       walkerTestAccount,
		"LowSponsor":        walkerTestAccount,
		"PreviousTxnID":     zeroHash256,
		"PreviousTxnLgrSeq": uint32(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	line, err := ParseRippleState(raw)
	if err != nil {
		t.Fatal(err)
	}
	line.HighSponsor = ""
	line.LowSponsor = ""
	encoded, err := SerializeRippleState(line)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeSLE(t, encoded)
	for _, name := range []string{"HighSponsor", "LowSponsor"} {
		if _, ok := fields[name]; ok {
			t.Errorf("cleared %s remained present", name)
		}
	}
}

func TestDirectoryChangedOptionalsAreCleared(t *testing.T) {
	raw, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "DirectoryNode",
		"Flags":             uint32(0),
		"RootIndex":         zeroHash256,
		"Indexes":           []string{},
		"Owner":             walkerTestAccount,
		"ExchangeRate":      "5",
		"NFTokenID":         strings.Repeat("11", 32),
		"DomainID":          strings.Repeat("22", 32),
		"PreviousTxnID":     zeroHash256,
		"PreviousTxnLgrSeq": uint32(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := ParseDirectoryNode(raw)
	if err != nil {
		t.Fatal(err)
	}
	dir.Owner = [20]byte{}
	dir.ExchangeRate = 0
	dir.NFTokenID = [32]byte{}
	dir.DomainID = [32]byte{}
	encoded, err := SerializeDirectoryNode(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeSLE(t, encoded)
	for _, name := range []string{"Owner", "ExchangeRate", "NFTokenID", "DomainID"} {
		if _, ok := fields[name]; ok {
			t.Errorf("changed-to-default %s remained present", name)
		}
	}
}

func TestFeeSettingsChangedThreadingIsCleared(t *testing.T) {
	raw, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "FeeSettings",
		"Flags":             uint32(0),
		"BaseFee":           "a",
		"ReferenceFeeUnits": uint32(10),
		"ReserveBase":       uint32(20),
		"ReserveIncrement":  uint32(30),
		"PreviousTxnID":     strings.Repeat("11", 32),
		"PreviousTxnLgrSeq": uint32(7),
	})
	if err != nil {
		t.Fatal(err)
	}
	fee, err := ParseFeeSettings(raw)
	if err != nil {
		t.Fatal(err)
	}
	fee.PreviousTxnID = [32]byte{}
	fee.PreviousTxnLgrSeq = 0
	encoded, err := SerializeFeeSettings(fee)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeSLE(t, encoded)
	for _, name := range []string{"PreviousTxnID", "PreviousTxnLgrSeq"} {
		if _, ok := fields[name]; ok {
			t.Errorf("cleared %s remained present", name)
		}
	}
}

func TestMPTokenIssuanceClearsOptionalSnapshotValues(t *testing.T) {
	maximum, locked := uint64(3), uint64(4)
	domain, reference := strings.Repeat("11", 32), strings.Repeat("22", 32)
	want := &MPTokenIssuanceData{
		Issuer:                        [20]byte{1},
		Sequence:                      1,
		OwnerNode:                     2,
		OutstandingAmount:             3,
		TransferFee:                   4,
		AssetScale:                    5,
		MaximumAmount:                 &maximum,
		LockedAmount:                  &locked,
		MPTokenMetadata:               "ABCD",
		DomainID:                      &domain,
		ReferenceHolding:              &reference,
		ImmutableFlags:                6,
		Sponsor:                       walkerTestAccount,
		IssuerEncryptionKey:           []byte{7},
		AuditorEncryptionKey:          []byte{8},
		ConfidentialOutstandingAmount: 9,
		PreviousTxnID:                 [32]byte{10},
		PreviousTxnLgrSeq:             11,
	}
	raw, err := SerializeMPTokenIssuance(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseMPTokenIssuance(raw)
	if err != nil {
		t.Fatal(err)
	}
	got.MaximumAmount = nil
	got.LockedAmount = nil
	got.MPTokenMetadata = ""
	got.DomainID = nil
	got.ReferenceHolding = nil
	got.IssuerEncryptionKey = nil
	got.AuditorEncryptionKey = nil
	got.Sponsor = ""
	got.TransferFee = 0
	got.AssetScale = 0
	got.ImmutableFlags = 0
	got.ConfidentialOutstandingAmount = 0
	got.PreviousTxnID = [32]byte{}
	got.PreviousTxnLgrSeq = 0
	encoded, err := SerializeMPTokenIssuance(got)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeSLE(t, encoded)
	for _, name := range []string{"MaximumAmount", "LockedAmount", "MPTokenMetadata", "DomainID", "ReferenceHolding", "IssuerEncryptionKey", "AuditorEncryptionKey", "Sponsor"} {
		if _, ok := fields[name]; ok {
			t.Errorf("cleared %s remained present", name)
		}
	}
	for _, name := range []string{"TransferFee", "AssetScale", "ImmutableFlags", "ConfidentialOutstandingAmount"} {
		if _, ok := fields[name]; ok {
			t.Errorf("changed-to-default %s remained present", name)
		}
	}
	for _, name := range []string{"PreviousTxnID", "PreviousTxnLgrSeq"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("required threading field %s was omitted", name)
		}
	}
}

func TestMPTokenPreservesPresentEmptyAccountAndBlobs(t *testing.T) {
	var wire ledgerfields.MPToken
	wire.SetAccount("")
	wire.SetMPTokenIssuanceID(strings.Repeat("00", 24))
	wire.SetOwnerNode("1")
	wire.SetMPTAmount("1")
	wire.SetFlags(0)
	wire.SetConfidentialBalanceInbox("")
	wire.SetSponsor("")
	wire.SetPreviousTxnID(strings.Repeat("00", 32))
	wire.SetPreviousTxnLgrSeq(0)
	raw, err := wire.Encode()
	if err != nil {
		t.Fatal(err)
	}
	token, err := ParseMPToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := SerializeMPToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("present-empty account/blob fields changed bytes\nwant %X\n got %X", raw, encoded)
	}
}

func TestDirectoryPreservesEmptyOwnerWireValue(t *testing.T) {
	raw, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "DirectoryNode", "Flags": uint32(0), "RootIndex": zeroHash256,
		"Indexes": []string{}, "Owner": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := ParseDirectoryNode(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := SerializeDirectoryNode(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, encoded) {
		t.Fatalf("empty Owner changed bytes: %X != %X", raw, encoded)
	}
}
