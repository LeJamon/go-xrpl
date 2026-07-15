package state

import (
	"encoding/hex"
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
