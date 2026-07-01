package invariants

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// TestIsNativeXRPCurrency pins the renderings of the all-zero (native XRP)
// currency and rejects look-alikes: badCurrency, and any code whose bytes
// 12-14 merely spell "XRP" beside non-zero bytes (mainnet "Wellgistics XRP",
// ledger 99342512).
func TestIsNativeXRPCurrency(t *testing.T) {
	tests := []struct {
		name string
		curr string
		want bool
	}{
		{"empty string", "", true},
		{"literal XRP", "XRP", true},
		{"three NUL bytes", "\x00\x00\x00", true},
		{"all-zero hex", "0000000000000000000000000000000000000000", true},
		{"badCurrency hex", "0000000000000000000000005852500000000000", false},
		{"Wellgistics XRP mainnet IOU", "57656C6C67697374696373205852500000000000", false},
		{"Wellgistics XRP lowercase hex", "57656c6c67697374696373205852500000000000", false},
		{"single non-zero byte", "0000000000000000000000000000000000000001", false},
		{"noCurrency sentinel", "1", false},
		{"ISO USD", "USD", false},
		{"40 chars but invalid hex", "ZZ00000000000000000000000000000000000000", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNativeXRPCurrency(tt.curr); got != tt.want {
				t.Errorf("isNativeXRPCurrency(%q) = %v, want %v", tt.curr, got, tt.want)
			}
		})
	}
}

// TestIsBadCurrency pins rippled's badCurrency() boundary (UintTypes.cpp:132-137):
// exactly bytes 12-14 "XRP" with every other byte zero, hex form only.
func TestIsBadCurrency(t *testing.T) {
	tests := []struct {
		name string
		curr string
		want bool
	}{
		{"badCurrency hex", "0000000000000000000000005852500000000000", true},
		{"all-zero hex", "0000000000000000000000000000000000000000", false},
		{"literal XRP", "XRP", false},
		{"empty string", "", false},
		{"Wellgistics XRP mainnet IOU", "57656C6C67697374696373205852500000000000", false},
		{"Wellgistics XRP lowercase hex", "57656c6c67697374696373205852500000000000", false},
		{"XRP at 12-14 with non-zero suffix", "0000000000000000000000005852500000000001", false},
		{"XRP at 12-14 with non-zero prefix byte", "0100000000000000000000005852500000000000", false},
		{"40 chars but invalid hex", "ZZ00000000000000000000005852500000000000", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBadCurrency(tt.curr); got != tt.want {
				t.Errorf("isBadCurrency(%q) = %v, want %v", tt.curr, got, tt.want)
			}
		})
	}
}

// encodeTrustLine builds a canonical RippleState blob whose LowLimit currency
// is the standard-form "ABC" (unique in the blob, so tests can patch its 3
// currency bytes in place).
func encodeTrustLine(t *testing.T) []byte {
	t.Helper()
	const balanceIssuer = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	const owner = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

	jsonObj := map[string]any{
		"LedgerEntryType": "RippleState",
		"Flags":           uint32(0),
		"Balance": map[string]any{
			"currency": "USD",
			"issuer":   balanceIssuer,
			"value":    "0",
		},
		"LowLimit": map[string]any{
			"currency": "ABC",
			"issuer":   owner,
			"value":    "10",
		},
		"HighLimit": map[string]any{
			"currency": "USD",
			"issuer":   balanceIssuer,
			"value":    "20",
		},
	}
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return blob
}

// TestNoXRPTrustLines_BadCurrencyBoundary mirrors rippled's NoXRPTrustLines
// predicate (InvariantCheck.cpp:591-593, issue() == xrpIssue()): a zero-currency
// limit fires, a badCurrency limit does not. rippled pins the firing side in
// Invariants_test.cpp testNoXRPTrustLine.
func TestNoXRPTrustLines_BadCurrencyBoundary(t *testing.T) {
	good := encodeTrustLine(t)
	if v := checkNoXRPTrustLines([]InvariantEntry{{EntryType: "RippleState", After: good}}); v != nil {
		t.Fatalf("IOU trust line: unexpected violation %v", v)
	}

	idx := bytes.Index(good, []byte("ABC"))
	if idx < 0 {
		t.Fatal("ABC currency marker not found in encoded RippleState")
	}

	badCur := make([]byte, len(good))
	copy(badCur, good)
	copy(badCur[idx:idx+3], "XRP")
	if v := checkNoXRPTrustLines([]InvariantEntry{{EntryType: "RippleState", After: badCur}}); v != nil {
		t.Fatalf("badCurrency limit must not fire NoXRPTrustLines: %v", v)
	}

	zeroCur := make([]byte, len(good))
	copy(zeroCur, good)
	copy(zeroCur[idx:idx+3], []byte{0, 0, 0})
	if v := checkNoXRPTrustLines([]InvariantEntry{{EntryType: "RippleState", After: zeroCur}}); v == nil {
		t.Fatal("expected violation: trust line limit with the zero (XRP) currency")
	}
}
