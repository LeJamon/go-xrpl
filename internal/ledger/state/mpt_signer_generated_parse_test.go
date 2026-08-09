package state

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
)

func TestParseMPTokenIssuanceGeneratedDecoder(t *testing.T) {
	zero := uint64(0)
	domainID := strings.Repeat("11", 32)
	referenceHolding := strings.Repeat("22", 32)
	previousTxnID := [32]byte{1, 2, 3}
	want := &MPTokenIssuanceData{
		Issuer:            [20]byte{1, 2, 3},
		Sequence:          17,
		OwnerNode:         0x1234,
		OutstandingAmount: 99,
		TransferFee:       42,
		AssetScale:        6,
		MaximumAmount:     &zero,
		LockedAmount:      &zero,
		MPTokenMetadata:   "abcdef",
		DomainID:          &domainID,
		ReferenceHolding:  &referenceHolding,
		Flags:             7,
		ImmutableFlags:    9,
		PreviousTxnID:     previousTxnID,
		PreviousTxnLgrSeq: 23,
	}

	data, err := SerializeMPTokenIssuance(want)
	if err != nil {
		t.Fatalf("SerializeMPTokenIssuance: %v", err)
	}
	got, err := ParseMPTokenIssuance(data)
	if err != nil {
		t.Fatalf("ParseMPTokenIssuance: %v", err)
	}
	if got.Issuer != want.Issuer || got.Sequence != want.Sequence || got.OwnerNode != want.OwnerNode ||
		got.OutstandingAmount != want.OutstandingAmount || got.TransferFee != want.TransferFee ||
		got.AssetScale != want.AssetScale || got.Flags != want.Flags || got.ImmutableFlags != want.ImmutableFlags ||
		got.PreviousTxnID != want.PreviousTxnID || got.PreviousTxnLgrSeq != want.PreviousTxnLgrSeq {
		t.Fatalf("fixed fields differ:\n got  %+v\n want %+v", got, want)
	}
	if got.MaximumAmount == nil || *got.MaximumAmount != 0 {
		t.Fatalf("MaximumAmount = %v, want present zero", got.MaximumAmount)
	}
	if got.LockedAmount == nil || *got.LockedAmount != 0 {
		t.Fatalf("LockedAmount = %v, want present zero", got.LockedAmount)
	}
	if got.MPTokenMetadata != want.MPTokenMetadata || got.DomainID == nil || *got.DomainID != domainID ||
		got.ReferenceHolding == nil || *got.ReferenceHolding != referenceHolding {
		t.Fatalf("hex fields differ: got metadata=%q domain=%v reference=%v", got.MPTokenMetadata, got.DomainID, got.ReferenceHolding)
	}
}

func TestParseMPTokenGeneratedDecoder(t *testing.T) {
	zero := uint64(0)
	want := &MPTokenData{
		Account:           [20]byte{4, 5, 6},
		MPTokenIssuanceID: [24]byte{7, 8, 9},
		OwnerNode:         0x9876,
		MPTAmount:         123,
		LockedAmount:      &zero,
		Flags:             3,
		PreviousTxnID:     [32]byte{10, 11, 12},
		PreviousTxnLgrSeq: 29,
	}

	data, err := SerializeMPToken(want)
	if err != nil {
		t.Fatalf("SerializeMPToken: %v", err)
	}
	got, err := ParseMPToken(data)
	if err != nil {
		t.Fatalf("ParseMPToken: %v", err)
	}
	if got.Account != want.Account || got.MPTokenIssuanceID != want.MPTokenIssuanceID ||
		got.OwnerNode != want.OwnerNode || got.MPTAmount != want.MPTAmount || got.Flags != want.Flags ||
		got.PreviousTxnID != want.PreviousTxnID || got.PreviousTxnLgrSeq != want.PreviousTxnLgrSeq {
		t.Fatalf("fixed fields differ:\n got  %+v\n want %+v", got, want)
	}
	if got.LockedAmount == nil || *got.LockedAmount != 0 {
		t.Fatalf("LockedAmount = %v, want present zero", got.LockedAmount)
	}
}

func TestParseSignerListGeneratedNestedEntries(t *testing.T) {
	account, err := EncodeAccountID([20]byte{1, 2, 3})
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	walletLocator := strings.Repeat("AB", 32)
	wire := &ledgerfields.SignerList{}
	wire.SetSignerQuorum(4)
	wire.SetOwnerNode("1234")
	wire.SetSignerListID(9)
	wire.SetFlags(7)
	wire.SetSignerEntries([]any{map[string]any{
		"SignerEntry": map[string]any{
			"Account":       account,
			"SignerWeight":  uint16(4),
			"WalletLocator": walletLocator,
		},
	}})
	data, err := wire.Encode()
	if err != nil {
		t.Fatalf("encode SignerList: %v", err)
	}

	got, err := ParseSignerList(data)
	if err != nil {
		t.Fatalf("ParseSignerList: %v", err)
	}
	if got.SignerListID != 9 || got.SignerQuorum != 4 || got.Flags != 7 || got.OwnerNode != 0x1234 {
		t.Fatalf("top-level fields differ: %+v", got)
	}
	if len(got.SignerEntries) != 1 || got.SignerEntries[0].Account != account ||
		got.SignerEntries[0].SignerWeight != 4 || got.SignerEntries[0].WalletLocator != walletLocator {
		t.Fatalf("SignerEntries = %+v", got.SignerEntries)
	}
}

func TestParseDepositPreauthGeneratedDecoder(t *testing.T) {
	account := [20]byte{1, 2, 3}
	data, err := SerializeDepositPreauth(account, [20]byte{4, 5, 6}, 0x1234)
	if err != nil {
		t.Fatalf("SerializeDepositPreauth: %v", err)
	}
	got, err := ParseDepositPreauth(data)
	if err != nil {
		t.Fatalf("ParseDepositPreauth: %v", err)
	}
	if got.Account != account || got.OwnerNode != 0x1234 {
		t.Fatalf("ParseDepositPreauth = %+v, want Account=%x OwnerNode=1234", got, account)
	}
}
