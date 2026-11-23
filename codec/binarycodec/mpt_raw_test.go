package binarycodec

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"

	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

const rawMPTTestIssuanceID = "1234567890abcdef1234567890abcdef1234567890abcdef"

func rawMPTTestFields(value binarytypes.RawMPTValue) map[string]any {
	return map[string]any{
		"Amount": map[string]any{
			"value":           value,
			"mpt_issuance_id": rawMPTTestIssuanceID,
		},
	}
}

func TestDecodeBytesAllowOverCapPreservesRawMPTWireValues(t *testing.T) {
	tests := []struct {
		name       string
		value      binarytypes.RawMPTValue
		wirePrefix string
		display    string
	}{
		{"negative zero", binarytypes.RawMPTValue{Negative: true}, "61200000000000000000", "0"},
		{"first over cap", binarytypes.RawMPTValue{Magnitude: 1 << 63}, "61608000000000000000", "9223372036854775808"},
		{"negative unsigned maximum", binarytypes.RawMPTValue{Magnitude: math.MaxUint64, Negative: true}, "6120ffffffffffffffff", "-18446744073709551615"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := EncodeDecodedFields(rawMPTTestFields(tc.value))
			if err != nil {
				t.Fatalf("EncodeDecodedFields: %v", err)
			}

			if got, want := hex.EncodeToString(wire), tc.wirePrefix+rawMPTTestIssuanceID; got != want {
				t.Fatalf("serialized Amount = %s, want %s", got, want)
			}
			decoded, err := DecodeBytesAllowOverCap(wire)
			if err != nil {
				t.Fatalf("DecodeBytesAllowOverCap: %v", err)
			}
			amount, ok := decoded["Amount"].(map[string]any)
			if !ok {
				t.Fatalf("decoded Amount = %#v, want MPT amount map", decoded["Amount"])
			}
			got, ok := amount["value"].(binarytypes.RawMPTValue)
			if !ok || got != tc.value {
				t.Fatalf("decoded raw MPT value = %#v, want %#v", amount["value"], tc.value)
			}
			if amount["mpt_issuance_id"] != strings.ToUpper(rawMPTTestIssuanceID) {
				t.Fatalf("decoded issuance ID = %#v, want uppercase ID", amount["mpt_issuance_id"])
			}

			reencoded, err := EncodeDecodedFields(decoded)
			if err != nil {
				t.Fatalf("EncodeDecodedFields(decoded): %v", err)
			}
			if got, want := hex.EncodeToString(reencoded), hex.EncodeToString(wire); got != want {
				t.Fatalf("raw MPT round trip = %s, want %s", got, want)
			}

			jsonBytes, err := json.Marshal(amount)
			if err != nil {
				t.Fatalf("json.Marshal(decoded Amount): %v", err)
			}
			wantJSON := `{"mpt_issuance_id":"` + strings.ToUpper(rawMPTTestIssuanceID) + `","value":"` + tc.display + `"}`
			if got := string(jsonBytes); got != wantJSON {
				t.Fatalf("decoded MPT JSON = %s, want %s", got, wantJSON)
			}

			strict, err := DecodeBytes(wire)
			if tc.value.Magnitude > math.MaxInt64 {
				if err == nil {
					t.Fatal("DecodeBytes accepted an over-cap raw MPT value")
				}
			} else {
				if err != nil {
					t.Fatalf("DecodeBytes negative-zero compatibility path: %v", err)
				}
				strictAmount, ok := strict["Amount"].(map[string]any)
				if !ok || strictAmount["value"] != "0" {
					t.Fatalf("strict MPT JSON = %#v, want canonical zero", strict["Amount"])
				}
			}
		})
	}
}

func TestEncodeDecodedSigningFieldsPreservesRawMPTWireValue(t *testing.T) {
	value := binarytypes.RawMPTValue{Magnitude: math.MaxUint64, Negative: true}
	fields := rawMPTTestFields(value)
	wire, err := EncodeDecodedFields(fields)
	if err != nil {
		t.Fatalf("EncodeDecodedFields: %v", err)
	}

	signing, err := EncodeDecodedSigningFields(fields, nil, TransactionRole, false)
	if err != nil {
		t.Fatalf("EncodeDecodedSigningFields: %v", err)
	}
	want := txSigPrefix + strings.ToUpper(hex.EncodeToString(wire))
	if signing != want {
		t.Fatalf("decoded MPT signing payload = %s, want %s", signing, want)
	}
}

func TestRawMPTValueRemainsJSONInputCapped(t *testing.T) {
	if _, err := EncodeBytes(rawMPTTestFields(binarytypes.RawMPTValue{Magnitude: math.MaxUint64})); err == nil {
		t.Fatal("EncodeBytes accepted a typed raw MPT value from ordinary JSON input")
	}
	if _, err := EncodeBytes(map[string]any{
		"Amount": map[string]any{
			"value":           "9223372036854775808",
			"mpt_issuance_id": rawMPTTestIssuanceID,
		},
	}); err == nil {
		t.Fatal("EncodeBytes accepted an over-cap MPT JSON value")
	}
}
