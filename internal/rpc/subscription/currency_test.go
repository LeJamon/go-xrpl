package subscription

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

// TestCurrencyToID pins currencyToID against rippled's to_currency: empty and
// "XRP" are the all-zero native currency, a 3-char ISO code packs at bytes
// 12-14, 40 hex digits are taken verbatim, and anything else is rejected.
func TestCurrencyToID(t *testing.T) {
	var (
		xrpID [20]byte
		usdID = [20]byte{12: 'U', 13: 'S', 14: 'D'}
		hex40 = "0123456789ABCDEF0123456789ABCDEF01234567"
		hexID [20]byte
	)
	if _, err := hex.Decode(hexID[:], []byte(hex40)); err != nil {
		t.Fatalf("decoding test fixture: %v", err)
	}

	cases := []struct {
		name   string
		code   string
		want   [20]byte
		wantOK bool
	}{
		{"empty is native XRP", "", xrpID, true},
		{"XRP is native XRP", "XRP", xrpID, true},
		{"forty hex zeroes is native XRP", "0000000000000000000000000000000000000000", xrpID, true},
		{"three-letter ISO code", "USD", usdID, true},
		{"forty hex digits taken verbatim", hex40, hexID, true},
		{"too short", "US", [20]byte{}, false},
		{"bad ISO char", "US ", [20]byte{}, false},
		{"non-hex forty chars", "ZZZZ000000000000000000000000000000000000", [20]byte{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := currencyToID(tc.code)
			if ok != tc.wantOK {
				t.Fatalf("currencyToID(%q) ok = %v, want %v", tc.code, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("currencyToID(%q) = %x, want %x", tc.code, got, tc.want)
			}
		})
	}
}

// TestParseBookSideXRP verifies an XRP book side (currency "XRP", no issuer)
// resolves to the all-zero native currency and passes parseBookSide's XRP-ness
// cross-check (an XRP currency must not carry an issuer).
func TestParseBookSideXRP(t *testing.T) {
	side := map[string]json.RawMessage{"currency": json.RawMessage(`"XRP"`)}
	cur, iss, rpcErr := parseBookSide(side, true)
	if rpcErr != nil {
		t.Fatalf("parseBookSide(XRP) returned error: %v", rpcErr)
	}
	if cur != ([20]byte{}) {
		t.Fatalf("parseBookSide(XRP) currency = %x, want all zero", cur)
	}
	if iss != ([20]byte{}) {
		t.Fatalf("parseBookSide(XRP) issuer = %x, want all zero", iss)
	}
}
