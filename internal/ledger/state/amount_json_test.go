package state

import (
	"encoding/json"
	"math"
	"testing"
)

// Reference behavior throughout: rippled amountFromJson
// (src/libxrpl/protocol/STAmount.cpp) with partsFromString
// (src/libxrpl/protocol/STNumber.cpp) and the STAmount_test setValue
// corpus (src/test/protocol/STAmount_test.cpp).

const (
	testIssuer    = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	testIssuerHex = "B5F762798A53D543A014CAF8B297CFF8F2F937E8"
	testMPTID     = "00000001B5F762798A53D543A014CAF8B297CFF8F2F937E8"
)

func iouJSON(value, currency, issuer string) string {
	b, _ := json.Marshal(map[string]string{
		"currency": currency, "issuer": issuer, "value": value,
	})
	return string(b)
}

func TestAmountFromJSON_XRP(t *testing.T) {
	tests := []struct {
		name  string
		input string
		drops int64
	}{
		{"string drops", `"1000000"`, 1_000_000},
		{"single drop", `"1"`, 1},
		{"max native (1e17)", `"100000000000000000"`, 100_000_000_000_000_000},
		{"exponent form", `"1e3"`, 1000},
		{"explicit plus", `"+5"`, 5},
		{"negative drops", `"-1"`, -1},
		{"zero", `"0"`, 0},
		{"zero with big exponent", `"0e50"`, 0},
		{"bare int", `1000000`, 1_000_000},
		{"bare negative int", `-1`, -1},
		{"bare int32 max", `2147483647`, 2_147_483_647},
		{"bare uint32 range", `3000000000`, 3_000_000_000},
		{"bare uint32 max", `4294967295`, 4_294_967_295},
		{"array value only", `["5"]`, 5},
		{"empty array defaults to zero", `[]`, 0},
		{"slash with XRP currency", `"5/XRP"`, 5},
		{"empty currency segment", `"5/"`, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			amt, err := AmountFromJSON(json.RawMessage(tt.input))
			if err != nil {
				t.Fatalf("AmountFromJSON(%s): %v", tt.input, err)
			}
			if !amt.IsNative() {
				t.Fatalf("AmountFromJSON(%s): want native", tt.input)
			}
			if amt.Drops() != tt.drops {
				t.Fatalf("AmountFromJSON(%s) = %d drops, want %d", tt.input, amt.Drops(), tt.drops)
			}
		})
	}
}

func TestAmountFromJSON_XRPErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"null", `null`},
		{"bool", `true`},
		{"fractional drops", `"1.1"`},
		{"fractional via exponent", `"1e-2"`},
		// STAmount_test: 100000000000000001 and 1000000000000000000 fail.
		{"above max native", `"100000000000000001"`},
		{"way above max native", `"1000000000000000000"`},
		{"exponent overflow", `"1e18"`},
		{"leading zero", `"01"`},
		{"empty string", `""`},
		{"not a number", `"abc"`},
		{"whitespace only separators", `"1 2 3 4"`},
		// rippled's Json reader stores these as Real -> invalid amount type.
		{"bare fractional", `1.5`},
		{"bare exponent form", `1e6`},
		{"bare above uint32", `4294967296`},
		{"bare large drops", `1000000000000`},
		{"bare negative below int32", `-3000000000`},
		// boost::lexical_cast overflow on the digit string.
		{"mantissa above uint64", `"99999999999999999999999999"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := AmountFromJSON(json.RawMessage(tt.input)); err == nil {
				t.Fatalf("AmountFromJSON(%s): want error", tt.input)
			}
		})
	}
}

func TestAmountFromJSON_IOU(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		value    string
		currency string
	}{
		{"object string value", iouJSON("5", "USD", testIssuer), "5", "USD"},
		{"decimal preserved", iouJSON("1234567.89", "USD", testIssuer), "1234567.89", "USD"},
		{"negative value", iouJSON("-1", "USD", testIssuer), "-1", "USD"},
		{"exponent form", iouJSON("1e3", "USD", testIssuer), "1000", "USD"},
		{"fractional exponent", iouJSON("15e-1", "USD", testIssuer), "1.5", "USD"},
		{"underflow collapses to zero", iouJSON("1e-100", "USD", testIssuer), "0", "USD"},
		{"hex issuer", iouJSON("5", "USD", testIssuerHex), "5", "USD"},
		{"hex currency", iouJSON("5", "0158415500000000C1F76FF6ECB0BAC600000000", testIssuer),
			"5", "0158415500000000C1F76FF6ECB0BAC600000000"},
		{"symbol currency", iouJSON("5", "$$$", testIssuer), "5", "$$$"},
		{"array form", `["5","USD","` + testIssuer + `"]`, "5", "USD"},
		{"slash string form", `"5/USD/` + testIssuer + `"`, "5", "USD"},
		{"comma separators", `"5,USD,` + testIssuer + `"`, "5", "USD"},
		// One round-half-even step on the 4 digits beyond 16: ...1615 < half.
		{"uint64-max mantissa", iouJSON("18446744073709551615", "USD", testIssuer),
			"1844674407370955e4", "USD"},
		{"near max IOU magnitude", iouJSON("1e85", "USD", testIssuer), "1000000000000000e70", "USD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			amt, err := AmountFromJSON(json.RawMessage(tt.input))
			if err != nil {
				t.Fatalf("AmountFromJSON(%s): %v", tt.input, err)
			}
			if amt.IsNative() {
				t.Fatalf("AmountFromJSON(%s): want issued", tt.input)
			}
			if amt.Currency != tt.currency {
				t.Fatalf("currency = %q, want %q", amt.Currency, tt.currency)
			}
			if amt.Issuer != testIssuer {
				t.Fatalf("issuer = %q, want %q", amt.Issuer, testIssuer)
			}
			if got := amt.Value(); got != tt.value {
				t.Fatalf("value = %q, want %q", got, tt.value)
			}
		})
	}
}

func TestAmountFromJSON_IOUObjectNumericValue(t *testing.T) {
	// Int-range numeric values work inside objects; uint-range ones fail
	// because rippled's UInt branch reads the enclosing value (v.asUInt()).
	amt, err := AmountFromJSON(json.RawMessage(
		`{"currency":"USD","issuer":"` + testIssuer + `","value":5}`))
	if err != nil {
		t.Fatalf("int value in object: %v", err)
	}
	if got := amt.Value(); got != "5" {
		t.Fatalf("value = %q, want 5", got)
	}

	if _, err := AmountFromJSON(json.RawMessage(
		`{"currency":"USD","issuer":"` + testIssuer + `","value":3000000000}`)); err == nil {
		t.Fatal("uint-range value in object: want error (v.asUInt quirk)")
	}
	if _, err := AmountFromJSON(json.RawMessage(
		`["3000000000","USD","` + testIssuer + `"]`)); err != nil {
		t.Fatalf("uint-range STRING in array must parse: %v", err)
	}
}

func TestAmountFromJSON_IOUErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"object without currency", `{"issuer":"` + testIssuer + `","value":"5"}`},
		{"XRP as object", `{"currency":"XRP","issuer":"` + testIssuer + `","value":"5"}`},
		{"empty currency as object", `{"currency":"","value":"5"}`},
		{"non-string currency goes native in object", `{"currency":5,"value":"5"}`},
		{"bad currency length", iouJSON("5", "TOOLONG", testIssuer)},
		{"currency char outside iso set", iouJSON("5", "U-D", testIssuer)},
		{"missing issuer", `{"currency":"USD","value":"5"}`},
		{"bad issuer", iouJSON("5", "USD", "junk")},
		{"all-zero hex currency", iouJSON("5", "0000000000000000000000000000000000000000", testIssuer)},
		{"missing value", `{"currency":"USD","issuer":"` + testIssuer + `"}`},
		{"null value", `{"currency":"USD","issuer":"` + testIssuer + `","value":null}`},
		{"fractional numeric value", `{"currency":"USD","issuer":"` + testIssuer + `","value":1.5}`},
		{"slash form without issuer", `"5/USD"`},
		// Past the IOU range (max mantissa 9999999999999999 at exponent 80).
		{"exponent overflow", iouJSON("1e97", "USD", testIssuer)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := AmountFromJSON(json.RawMessage(tt.input)); err == nil {
				t.Fatalf("AmountFromJSON(%s): want error", tt.input)
			}
		})
	}
}

func TestAmountFromJSON_MPT(t *testing.T) {
	amt, err := AmountFromJSON(json.RawMessage(
		`{"mpt_issuance_id":"` + testMPTID + `","value":"100"}`))
	if err != nil {
		t.Fatalf("MPT amount: %v", err)
	}
	if !amt.IsMPT() {
		t.Fatal("want MPT amount")
	}
	if raw, ok := amt.MPTRaw(); !ok || raw != 100 {
		t.Fatalf("MPTRaw = %d/%v, want 100", raw, ok)
	}
	if amt.MPTIssuanceID() != testMPTID {
		t.Fatalf("issuance id = %q", amt.MPTIssuanceID())
	}

	errCases := []struct {
		name  string
		input string
	}{
		{"mpt with currency", `{"mpt_issuance_id":"` + testMPTID + `","currency":"USD","value":"1"}`},
		{"mpt with issuer", `{"mpt_issuance_id":"` + testMPTID + `","issuer":"` + testIssuer + `","value":"1"}`},
		{"bad issuance id", `{"mpt_issuance_id":"ABCD","value":"1"}`},
		{"fractional mpt", `{"mpt_issuance_id":"` + testMPTID + `","value":"1.5"}`},
		{"mpt out of range", `{"mpt_issuance_id":"` + testMPTID + `","value":"9223372036854775808"}`},
	}
	for _, tt := range errCases {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := AmountFromJSON(json.RawMessage(tt.input)); err == nil {
				t.Fatalf("AmountFromJSON(%s): want error", tt.input)
			}
		})
	}
}

// GHSA-xv89-94jf-8vx2: scaling the mantissa by a positive exponent must not
// wrap uint64 and slip past the range guard. maxMPTAmount (math.MaxInt64,
// ~9.22e18) sits above the uint64 wrap threshold (2^64/10 ~ 1.84e18), so a
// mantissa in that window times ten overflows; the result must be rejected as
// out of range, never silently truncated to a small valid-looking value.
func TestAmountFromJSON_MPTOverflow(t *testing.T) {
	mpt := func(value string) string {
		return `{"mpt_issuance_id":"` + testMPTID + `","value":"` + value + `"}`
	}

	t.Run("rejects", func(t *testing.T) {
		cases := []struct {
			name  string
			value string
		}{
			// Advisory exploit: 1844674407370955162 * 10 = 18446744073709551620,
			// which wraps to 4 mod 2^64 and used to be returned as a valid amount.
			{"exploit wraps to small value", "1844674407370955162e1"},
			// Single multiply with a non-zero high word.
			{"wrap high word set", "2000000000000000000e1"},
			// Two multiplies; the wrap happens on the second iteration.
			{"wrap on second exponent step", "184467440737095517e2"},
			// In range for the mantissa, but the true product exceeds the max
			// without wrapping — the post-multiply guard must still reject it.
			{"over range without wrap", "922337203685477581e1"},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := AmountFromJSON(json.RawMessage(mpt(tt.value))); err == nil {
					t.Fatalf("AmountFromJSON(%s): want out-of-range error", tt.value)
				}
			})
		}
	})

	t.Run("accepts", func(t *testing.T) {
		cases := []struct {
			name  string
			value string
			want  int64
		}{
			{"exact max", "9223372036854775807", math.MaxInt64},
			// 922337203685477580 * 10 = 9223372036854775800 <= max: still valid.
			{"near max via exponent", "922337203685477580e1", 9223372036854775800},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				amt, err := AmountFromJSON(json.RawMessage(mpt(tt.value)))
				if err != nil {
					t.Fatalf("AmountFromJSON(%s): %v", tt.value, err)
				}
				raw, ok := amt.MPTRaw()
				if !ok || raw != tt.want {
					t.Fatalf("AmountFromJSON(%s) = %d/%v, want %d", tt.value, raw, ok, tt.want)
				}
			})
		}
	})
}
