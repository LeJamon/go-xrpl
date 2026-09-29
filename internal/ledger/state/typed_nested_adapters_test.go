package state

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func TestDelegateTypedPermissionsRoundTrip(t *testing.T) {
	previous := threadTestTxnID()
	destinationNode := uint64(9)
	data, err := SerializeDelegate(
		[20]byte{1},
		[20]byte{2},
		[]uint32{1, 0x10001},
		7,
		&destinationNode,
		walkerTestAccount,
		previous,
		11,
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseDelegate(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Permissions) != 2 || parsed.Permissions[0] != 1 || parsed.Permissions[1] != 0x10001 {
		t.Fatalf("permissions = %v", parsed.Permissions)
	}
	if !parsed.HasDestinationNode || parsed.DestinationNode != destinationNode {
		t.Fatalf("destination node = (%d, %t)", parsed.DestinationNode, parsed.HasDestinationNode)
	}
	encoded, err := SerializeDelegate(parsed.Account, parsed.Authorize, parsed.Permissions, parsed.OwnerNode, &parsed.DestinationNode, parsed.Sponsor, parsed.PreviousTxnID, parsed.PreviousTxnLgrSeq)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, data) {
		t.Fatalf("typed Delegate round-trip changed bytes\nwant %X\n got %X", data, encoded)
	}
}

func TestDIDChangedOptionalBlobsAreCleared(t *testing.T) {
	account, err := EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := SerializeDID(&DIDData{
		Account:     [20]byte{1},
		URI:         "ABCD",
		DIDDocument: "1122",
		Data:        "3344",
	}, account)
	if err != nil {
		t.Fatal(err)
	}
	did, err := ParseDID(raw)
	if err != nil {
		t.Fatal(err)
	}
	did.URI, did.DIDDocument, did.Data = "", "", ""
	encoded, err := SerializeDID(did, account)
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeSLE(t, encoded)
	for _, name := range []string{"URI", "DIDDocument", "Data"} {
		if _, ok := fields[name]; ok {
			t.Errorf("cleared %s remained present", name)
		}
	}
}

func TestPermissionedDomainTypedCredentialsRoundTrip(t *testing.T) {
	owner, err := EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	pd := &PermissionedDomainData{
		Owner:     [20]byte{1},
		Sequence:  5,
		OwnerNode: 7,
		AcceptedCredentials: []PermissionedDomainCredential{
			{Issuer: [20]byte{2}, CredentialType: []byte("KYC")},
			{Issuer: [20]byte{3}, CredentialType: []byte("AML")},
		},
	}
	raw, err := SerializePermissionedDomain(pd, owner)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePermissionedDomain(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.AcceptedCredentials) != 2 || !bytes.Equal(parsed.AcceptedCredentials[0].CredentialType, []byte("KYC")) || !bytes.Equal(parsed.AcceptedCredentials[1].CredentialType, []byte("AML")) {
		t.Fatalf("credentials = %+v", parsed.AcceptedCredentials)
	}
	encoded, err := SerializePermissionedDomain(parsed, owner)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("typed PermissionedDomain round-trip changed bytes\nwant %X\n got %X", raw, encoded)
	}
}

func TestSponsorshipChangedOptionalsAreCleared(t *testing.T) {
	raw, err := SerializeSponsorship(&SponsorshipData{
		Owner:               [20]byte{1},
		Sponsee:             [20]byte{2},
		FeeAmount:           100,
		HasFeeAmount:        true,
		MaxFee:              200,
		HasMaxFee:           true,
		RemainingOwnerCount: 3,
		OwnerNode:           4,
		SponseeNode:         5,
		Sponsor:             [20]byte{3},
		HasSponsor:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseSponsorship(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.HasFeeAmount = false
	parsed.HasMaxFee = false
	parsed.RemainingOwnerCount = 0
	parsed.HasSponsor = false
	encoded, err := SerializeSponsorship(parsed)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := binarycodec.DecodeBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"FeeAmount", "MaxFee", "RemainingOwnerCount", "Sponsor"} {
		if _, ok := fields[name]; ok {
			t.Errorf("cleared %s remained present", name)
		}
	}
	if fields["PreviousTxnID"] != strings.Repeat("0", 64) {
		t.Fatalf("PreviousTxnID = %v, want zero hash", fields["PreviousTxnID"])
	}
}
