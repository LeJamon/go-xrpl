package state

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
	"github.com/LeJamon/go-xrpl/keylet"
)

func badCurrencyRippleStateBlob(t *testing.T) []byte {
	t.Helper()

	amount := func(issuer string) map[string]any {
		return map[string]any{"value": "0", "currency": "USD", "issuer": issuer}
	}
	entry := &ledgerfields.RippleState{}
	entry.SetFlags(0)
	entry.SetBalance(amount(AccountOneAddress))
	entry.SetLowLimit(amount(walkerTestAccount))
	entry.SetHighLimit(amount(walkerTestAccount))
	data, err := entry.Encode()
	if err != nil {
		t.Fatalf("Encode RippleState: %v", err)
	}

	usd := [20]byte{12: 'U', 13: 'S', 14: 'D'}
	bad := keylet.BadCurrency()
	if count := bytes.Count(data, usd[:]); count != 3 {
		t.Fatalf("USD currency payload count = %d, want 3", count)
	}
	return bytes.ReplaceAll(data, usd[:], bad[:])
}

func TestParseRippleStatePreservesBinaryBadCurrency(t *testing.T) {
	raw := badCurrencyRippleStateBlob(t)
	parsed, err := ParseRippleState(raw)
	if err != nil {
		t.Fatalf("ParseRippleState: %v", err)
	}
	for name, amount := range map[string]Amount{
		"Balance": parsed.Balance, "LowLimit": parsed.LowLimit, "HighLimit": parsed.HighLimit,
	} {
		if amount.Currency != badCurrencyHex {
			t.Errorf("%s currency = %q, want %s", name, amount.Currency, badCurrencyHex)
		}
	}

	roundTrip, err := SerializeRippleState(parsed)
	if err != nil {
		t.Fatalf("SerializeRippleState: %v", err)
	}
	if !bytes.Equal(roundTrip, raw) {
		t.Fatal("parsed badCurrency RippleState did not round-trip")
	}

	constructed := *parsed
	constructed.binaryBadCurrency = false
	if _, err := SerializeRippleState(&constructed); err == nil {
		t.Fatal("constructed RippleState writer accepted badCurrency")
	}

	bad := keylet.BadCurrency()
	usd := [20]byte{12: 'U', 13: 'S', 14: 'D'}
	partial := append([]byte(nil), raw...)
	patched := false
	if err := WalkFields(partial, func(field Field) error {
		if !patched && field.TypeCode == stAmount && bytes.Equal(field.Value[8:28], bad[:]) {
			copy(field.Value[8:28], usd[:])
			patched = true
		}
		return nil
	}); err != nil {
		t.Fatalf("patch partial badCurrency RippleState: %v", err)
	}
	if _, err := ParseRippleState(partial); err == nil || !strings.Contains(err.Error(), "inconsistent badCurrency") {
		t.Fatalf("ParseRippleState partial badCurrency error = %v", err)
	}
}

func TestGeneratedDecoderAdaptersPreservePresentZeroFields(t *testing.T) {
	low, err := EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	high, err := EncodeAccountID([20]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	issued := func(issuer string) map[string]any {
		return map[string]any{"value": "0", "currency": "USD", "issuer": issuer}
	}

	t.Run("AccountRoot", func(t *testing.T) {
		want, err := binarycodec.EncodeBytes(map[string]any{
			"LedgerEntryType":      "AccountRoot",
			"Account":              walkerTestAccount,
			"Balance":              "0",
			"Sequence":             uint32(0),
			"OwnerCount":           uint32(0),
			"Flags":                uint32(0),
			"MessageKey":           "",
			"Domain":               "",
			"TransferRate":         uint32(0),
			"TickSize":             0,
			"TicketCount":          uint32(0),
			"WalletSize":           uint32(0),
			"FirstNFTokenSequence": uint32(0),
			"AccountTxnID":         zeroHash256,
			"AMMID":                zeroHash256,
			"VaultID":              zeroHash256,
			"LoanBrokerID":         zeroHash256,
			"PreviousTxnID":        zeroHash256,
			"PreviousTxnLgrSeq":    uint32(0),
		})
		if err != nil {
			t.Fatal(err)
		}
		entry, err := ParseAccountRoot(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := SerializeAccountRoot(entry)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("present-zero round trip changed bytes:\nwant %X\n got %X", want, got)
		}
	})

	t.Run("RippleState", func(t *testing.T) {
		base := map[string]any{
			"LedgerEntryType":   "RippleState",
			"Flags":             uint32(0),
			"Balance":           issued(AccountOneAddress),
			"LowLimit":          issued(low),
			"HighLimit":         issued(high),
			"PreviousTxnID":     zeroHash256,
			"PreviousTxnLgrSeq": uint32(0),
		}
		absent, err := binarycodec.EncodeBytes(base)
		if err != nil {
			t.Fatal(err)
		}
		absentEntry, err := ParseRippleState(absent)
		if err != nil {
			t.Fatal(err)
		}
		if absentEntry.HasLowQualityIn || absentEntry.HasLowQualityOut || absentEntry.HasHighQualityIn || absentEntry.HasHighQualityOut {
			t.Fatal("absent qualities became present")
		}
		absentRoundTrip, err := SerializeRippleState(absentEntry)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(absentRoundTrip, absent) {
			t.Fatalf("absent-quality round trip changed bytes:\nwant %X\n got %X", absent, absentRoundTrip)
		}

		base["LowNode"] = "0"
		base["HighNode"] = "0"
		base["LowQualityIn"] = uint32(0)
		base["LowQualityOut"] = uint32(0)
		base["HighQualityIn"] = uint32(0)
		base["HighQualityOut"] = uint32(0)
		present, err := binarycodec.EncodeBytes(base)
		if err != nil {
			t.Fatal(err)
		}
		presentEntry, err := ParseRippleState(present)
		if err != nil {
			t.Fatal(err)
		}
		if !presentEntry.HasLowNode || !presentEntry.HasHighNode ||
			!presentEntry.HasLowQualityIn || !presentEntry.HasLowQualityOut ||
			!presentEntry.HasHighQualityIn || !presentEntry.HasHighQualityOut {
			t.Fatal("present-zero node or quality presence was lost")
		}
		presentRoundTrip, err := SerializeRippleState(presentEntry)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(presentRoundTrip, present) {
			t.Fatalf("present-zero round trip changed bytes:\nwant %X\n got %X", present, presentRoundTrip)
		}

		presentEntry.HasLowQualityIn = false
		cleared, err := SerializeRippleState(presentEntry)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := binarycodec.DecodeBytes(cleared)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["LowQualityIn"]; ok {
			t.Fatal("cleared LowQualityIn remained present")
		}
	})

	t.Run("Offer", func(t *testing.T) {
		want, err := binarycodec.EncodeBytes(map[string]any{
			"LedgerEntryType":   "Offer",
			"Account":           walkerTestAccount,
			"Sequence":          uint32(1),
			"TakerPays":         "1",
			"TakerGets":         "2",
			"BookDirectory":     zeroHash256,
			"BookNode":          "0",
			"OwnerNode":         "0",
			"Expiration":        uint32(0),
			"Flags":             uint32(0),
			"DomainID":          zeroHash256,
			"PreviousTxnID":     zeroHash256,
			"PreviousTxnLgrSeq": uint32(0),
			"AdditionalBooks": []any{map[string]any{"Book": map[string]any{
				"BookDirectory": strings.Repeat("11", 32),
				"BookNode":      "0",
			}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		entry, err := ParseLedgerOffer(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := SerializeLedgerOffer(entry)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("present-zero round trip changed bytes:\nwant %X\n got %X", want, got)
		}
	})

	t.Run("DirectoryNode", func(t *testing.T) {
		want, err := binarycodec.EncodeBytes(map[string]any{
			"LedgerEntryType":   "DirectoryNode",
			"Flags":             uint32(0),
			"RootIndex":         zeroHash256,
			"Indexes":           []string{},
			"IndexNext":         "0",
			"IndexPrevious":     "0",
			"PreviousTxnID":     zeroHash256,
			"PreviousTxnLgrSeq": uint32(0),
		})
		if err != nil {
			t.Fatal(err)
		}
		entry, err := ParseDirectoryNode(want)
		if err != nil {
			t.Fatal(err)
		}
		if !entry.indexNextSet || !entry.indexPreviousSet {
			t.Fatal("present-zero directory links were lost")
		}
		got, err := SerializeDirectoryNode(entry, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("present-zero round trip changed bytes:\nwant %X\n got %X", want, got)
		}
	})
}

func TestGeneratedDecoderAdaptersRejectMalformedConversions(t *testing.T) {
	low, err := EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	high, err := EncodeAccountID([20]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	data, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType":   "RippleState",
		"Flags":             uint32(0),
		"Balance":           "1",
		"LowLimit":          map[string]any{"value": "0", "currency": "USD", "issuer": low},
		"HighLimit":         map[string]any{"value": "0", "currency": "USD", "issuer": high},
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRippleState(data); err == nil || !strings.Contains(err.Error(), "expected issued-currency amount") {
		t.Fatalf("ParseRippleState error = %v, want issued-currency conversion error", err)
	}

	badBooks := []struct {
		name  string
		value []any
	}{
		{"wrong element", []any{"bad"}},
		{"missing Book", []any{map[string]any{}}},
		{"wrong Book", []any{map[string]any{"Book": "bad"}}},
		{"wrong BookNode", []any{map[string]any{"Book": map[string]any{"BookDirectory": zeroHash256, "BookNode": uint64(0)}}}},
	}
	for _, test := range badBooks {
		t.Run(test.name, func(t *testing.T) {
			if err := decodeAdditionalBook(test.value, &LedgerOffer{}); err == nil {
				t.Fatal("decodeAdditionalBook accepted malformed decoded value")
			}
		})
	}
}
