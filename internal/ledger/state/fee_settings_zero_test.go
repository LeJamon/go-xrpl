package state

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func TestFeeSettingsModernZeroValues(t *testing.T) {
	data, err := SerializeFeeSettings(&FeeSettings{XRPFeesMode: true})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ParseFeeSettings(data)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.IsUsingModernFees() {
		t.Fatal("zero-valued modern fee fields were parsed as legacy fees")
	}
	if got := settings.GetBaseFee(); got != 0 {
		t.Fatalf("base fee = %d, want 0", got)
	}
	if got := settings.GetReserveBase(); got != 0 {
		t.Fatalf("reserve base = %d, want 0", got)
	}
	if got := settings.GetReserveIncrement(); got != 0 {
		t.Fatalf("reserve increment = %d, want 0", got)
	}
	decoded, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"BaseFeeDrops", "ReserveBaseDrops", "ReserveIncrementDrops"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("serialized modern FeeSettings omitted %s", field)
		}
	}
	for _, field := range []string{"BaseFee", "ReferenceFeeUnits", "ReserveBase", "ReserveIncrement"} {
		if _, ok := decoded[field]; ok {
			t.Fatalf("serialized modern FeeSettings retained legacy field %s", field)
		}
	}
}

func TestParseFeeSettingsRejectsMixedFieldSets(t *testing.T) {
	data, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "FeeSettings",
		"Flags":           uint32(0),
		"BaseFee":         "a",
		"BaseFeeDrops":    "10",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ParseFeeSettings(data)
	if err == nil || !strings.Contains(err.Error(), "mixes legacy and XRPFees fields") {
		t.Fatalf("ParseFeeSettings error = %v, want mixed-field rejection", err)
	}
	if !errors.Is(err, ErrInvalidFeeSettings) {
		t.Fatalf("ParseFeeSettings error = %v, want ErrInvalidFeeSettings", err)
	}
}

func TestParseFeeSettingsRejectsNonNativeModernFee(t *testing.T) {
	data, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "FeeSettings",
		"Flags":           uint32(0),
		"BaseFeeDrops": map[string]any{
			"value":    "10",
			"currency": "USD",
			"issuer":   "rvYAfWj5gh67oV6fW32ZzP3Aw4Eubs59B",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ParseFeeSettings(data)
	if err == nil || !strings.Contains(err.Error(), "non-native XRP fee") {
		t.Fatalf("ParseFeeSettings error = %v, want non-native fee rejection", err)
	}
	if !errors.Is(err, ErrInvalidFeeSettings) {
		t.Fatalf("ParseFeeSettings error = %v, want ErrInvalidFeeSettings", err)
	}
}

func TestParseFeeSettingsKeepsMalformedDataDistinct(t *testing.T) {
	_, err := ParseFeeSettings([]byte{1, 2, 3})
	if err == nil {
		t.Fatal("ParseFeeSettings error = nil, want malformed-data rejection")
	}
	if errors.Is(err, ErrInvalidFeeSettings) {
		t.Fatalf("ParseFeeSettings error = %v, malformed data must remain an internal error", err)
	}
}

func TestFeeSettingsLegacyZeroValues(t *testing.T) {
	data, err := SerializeFeeSettings(&FeeSettings{})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ParseFeeSettings(data)
	if err != nil {
		t.Fatal(err)
	}
	if settings.IsUsingModernFees() {
		t.Fatal("zero-valued legacy fee fields were parsed as modern fees")
	}
	if got := settings.GetBaseFee(); got != 0 {
		t.Fatalf("base fee = %d, want 0", got)
	}
	if got := settings.GetReserveBase(); got != 0 {
		t.Fatalf("reserve base = %d, want 0", got)
	}
	if got := settings.GetReserveIncrement(); got != 0 {
		t.Fatalf("reserve increment = %d, want 0", got)
	}
	decoded, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"BaseFee", "ReferenceFeeUnits", "ReserveBase", "ReserveIncrement"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("serialized legacy FeeSettings omitted %s", field)
		}
	}
	for _, field := range []string{"BaseFeeDrops", "ReserveBaseDrops", "ReserveIncrementDrops"} {
		if _, ok := decoded[field]; ok {
			t.Fatalf("serialized legacy FeeSettings retained modern field %s", field)
		}
	}
}
