package state

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

const (
	nativeAdapterAccount = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	nativeAdapterHash    = "0000000000000000000000000000000000000000000000000000000000000000"
)

func encodeNativeAdapterEntry(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	data, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("EncodeBytes: %v", err)
	}
	return data
}

func TestParsePayChannelRejectsInvalidNativeAmounts(t *testing.T) {
	base := map[string]any{
		"LedgerEntryType":   "PayChannel",
		"Account":           nativeAdapterAccount,
		"Destination":       nativeAdapterAccount,
		"Amount":            "1",
		"Balance":           "0",
		"PublicKey":         "02",
		"SettleDelay":       uint32(1),
		"OwnerNode":         "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     nativeAdapterHash,
		"PreviousTxnLgrSeq": uint32(0),
	}
	issued := map[string]any{"value": "1", "currency": "USD", "issuer": nativeAdapterAccount}
	tests := []struct {
		name    string
		field   string
		value   any
		wantErr string
	}{
		{name: "issued amount", field: "Amount", value: issued, wantErr: "expected native XRP amount"},
		{name: "negative amount", field: "Amount", value: "-1", wantErr: "negative XRP amount"},
		{name: "issued balance", field: "Balance", value: issued, wantErr: "expected native XRP amount"},
		{name: "negative balance", field: "Balance", value: "-1", wantErr: "negative XRP amount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := make(map[string]any, len(base))
			for name, value := range base {
				fields[name] = value
			}
			fields[tt.field] = tt.value
			_, err := ParsePayChannel(encodeNativeAdapterEntry(t, fields))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParsePayChannel error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestNativeAmountAdaptersRejectNegativeDrops(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]any
		parse  func([]byte) error
	}{
		{
			name: "Check.SendMax",
			fields: map[string]any{
				"LedgerEntryType":   "Check",
				"Account":           nativeAdapterAccount,
				"Destination":       nativeAdapterAccount,
				"SendMax":           "-1",
				"Sequence":          uint32(1),
				"OwnerNode":         "0",
				"DestinationNode":   "0",
				"Flags":             uint32(0),
				"PreviousTxnID":     nativeAdapterHash,
				"PreviousTxnLgrSeq": uint32(0),
			},
			parse: func(data []byte) error {
				_, err := ParseCheck(data)
				return err
			},
		},
		{
			name: "FeeSettings.BaseFeeDrops",
			fields: map[string]any{
				"LedgerEntryType": "FeeSettings",
				"BaseFeeDrops":    "-1",
				"Flags":           uint32(0),
			},
			parse: func(data []byte) error {
				_, err := ParseFeeSettings(data)
				return err
			},
		},
		{
			name: "Escrow.Amount",
			fields: map[string]any{
				"LedgerEntryType":   "Escrow",
				"Account":           nativeAdapterAccount,
				"Destination":       nativeAdapterAccount,
				"Amount":            "-1",
				"OwnerNode":         "0",
				"Flags":             uint32(0),
				"PreviousTxnID":     nativeAdapterHash,
				"PreviousTxnLgrSeq": uint32(0),
			},
			parse: func(data []byte) error {
				_, err := ParseEscrow(data)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.parse(encodeNativeAdapterEntry(t, tt.fields))
			if err == nil || !strings.Contains(err.Error(), "negative XRP amount") {
				t.Fatalf("parse error = %v, want negative XRP amount", err)
			}
		})
	}
}

func TestRippleStatePreservesNativeZeroRawLimits(t *testing.T) {
	fields := map[string]any{
		"LedgerEntryType":   "RippleState",
		"Flags":             uint32(0),
		"Balance":           map[string]any{"value": "0", "currency": "USD", "issuer": AccountOneAddress},
		"LowLimit":          "0",
		"HighLimit":         "0",
		"PreviousTxnID":     nativeAdapterHash,
		"PreviousTxnLgrSeq": uint32(0),
	}
	raw := encodeNativeAdapterEntry(t, fields)
	parsed, err := ParseRippleState(raw)
	if err != nil {
		t.Fatalf("ParseRippleState with native zero limits: %v", err)
	}
	if !parsed.LowLimit.IsNative() || parsed.LowLimit.Drops() != 0 || !parsed.HighLimit.IsNative() || parsed.HighLimit.Drops() != 0 {
		t.Fatalf("native zero limits = low=%+v high=%+v", parsed.LowLimit, parsed.HighLimit)
	}
	roundTrip, err := SerializeRippleState(parsed)
	if err != nil {
		t.Fatalf("SerializeRippleState with native zero limits: %v", err)
	}
	if !bytes.Equal(roundTrip, raw) {
		t.Fatalf("native zero limits changed on round trip\n got: %X\nwant: %X", roundTrip, raw)
	}
}

func TestEscrowAcceptsExplicitEmptyAccountButRejectsMissingOrMalformedAccount(t *testing.T) {
	base := map[string]any{
		"LedgerEntryType":   "Escrow",
		"Account":           "",
		"Destination":       nativeAdapterAccount,
		"Amount":            "1",
		"OwnerNode":         "0",
		"Flags":             uint32(0),
		"PreviousTxnID":     nativeAdapterHash,
		"PreviousTxnLgrSeq": uint32(0),
	}
	raw := encodeNativeAdapterEntry(t, base)
	parsed, err := ParseEscrow(raw)
	if err != nil {
		t.Fatalf("ParseEscrow with explicit empty Account: %v", err)
	}
	if parsed.Account != [20]byte{} {
		t.Fatalf("empty Account decoded as %x", parsed.Account)
	}

	missing := make(map[string]any, len(base)-1)
	for name, value := range base {
		if name != "Account" {
			missing[name] = value
		}
	}
	if _, err := ParseEscrow(encodeNativeAdapterEntry(t, missing)); err == nil {
		t.Fatal("ParseEscrow accepted missing required Account")
	}
	var malformed entry.Escrow
	malformed.SetAccount("not-an-account")
	if _, err := malformed.GetAccount(); err == nil {
		t.Fatal("GetAccount accepted malformed nonempty Account")
	}
}
