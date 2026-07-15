package state

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

func TestParseIOUAmountBinaryAcceptsCanonicalAmount(t *testing.T) {
	data := make([]byte, types.CurrencyAmountByteLength)
	value, err := types.SerializeIssuedCurrencyValue("1")
	if err != nil {
		t.Fatalf("serialize value: %v", err)
	}
	copy(data, value)
	copy(data[20:23], "USD")
	issuer, err := hex.DecodeString(testIssuerHex)
	if err != nil {
		t.Fatalf("decode issuer: %v", err)
	}
	copy(data[28:], issuer)

	amount, err := ParseIOUAmountBinary(data)
	if err != nil {
		t.Fatalf("parse canonical amount: %v", err)
	}
	if got := amount.Value(); got != "1" {
		t.Fatalf("value = %q, want 1", got)
	}
	if amount.Currency != "USD" {
		t.Fatalf("currency = %q, want USD", amount.Currency)
	}
	if amount.Issuer != testIssuer {
		t.Fatalf("issuer = %q, want %s", amount.Issuer, testIssuer)
	}
}

func TestParseIOUAmountBinaryRejectsNonCanonicalMantissa(t *testing.T) {
	data := make([]byte, types.CurrencyAmountByteLength)
	const belowMinimumMantissa = uint64(types.MinIOUMantissa - 1)
	const minimumEncodedExponent = uint64(1)
	binary.BigEndian.PutUint64(data, uint64(types.ZeroCurrencyAmountHex)|types.PosSignBitMask|
		minimumEncodedExponent<<54|belowMinimumMantissa)

	if _, err := ParseIOUAmountBinary(data); err == nil {
		t.Fatal("expected non-canonical IOU mantissa to be rejected")
	}
}
