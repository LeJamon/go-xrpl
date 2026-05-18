package state

import (
	"encoding/json"
	"testing"
)

// TestIOUAmountValue_String_ScientificNotation pins IOUAmountValue.String
// to rippled's STAmount::getText (STAmount.cpp:706-732).
func TestIOUAmountValue_String_ScientificNotation(t *testing.T) {
	t.Parallel()

	const canonical int64 = 1_000_000_000_000_000 // 10^15

	tests := []struct {
		name     string
		mantissa int64
		exponent int
		expected string
	}{
		{"exp=-4 boundary scientific", canonical, -4, "1000000000000000e-4"},
		{"exp=-5 boundary fixed", canonical, -5, "10000000000"},
		{"exp=-25 boundary fixed", canonical, -25, "0.0000000001"},
		{"exp=-26 boundary scientific", canonical, -26, "1000000000000000e-26"},
		{"exp=-96 min scientific", canonical, -96, "1000000000000000e-96"},
		{"exp=-50 negative-deep scientific", canonical, -50, "1000000000000000e-50"},
		{"exp=0 zero offset stays fixed", canonical, 0, "1000000000000000"},
		{"exp=1 positive scientific", canonical, 1, "1000000000000000e1"},
		{"exp=50 positive-deep scientific", canonical, 50, "1000000000000000e50"},
		{"exp=80 max scientific", canonical, 80, "1000000000000000e80"},
		{"negative scientific", -canonical, -50, "-1000000000000000e-50"},
		{"negative scientific exp1", -canonical, 1, "-1000000000000000e1"},
		{"non-trailing-zero mantissa scientific", 1234567890123456, -30, "1234567890123456e-30"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := IOUAmountValue{mantissa: tc.mantissa, exponent: tc.exponent}.String()
			if got != tc.expected {
				t.Fatalf("IOUAmountValue{m=%d, e=%d}.String() = %q, want %q",
					tc.mantissa, tc.exponent, got, tc.expected)
			}
		})
	}
}

// TestAmount_MarshalJSON_IOUScientific verifies the rule reaches the
// JSON-encoding path.
func TestAmount_MarshalJSON_IOUScientific(t *testing.T) {
	t.Parallel()

	const canonical int64 = 1_000_000_000_000_000
	issuer := "rweYz56rfmQ98cAdRaeTxQS9wVMGnrdsFp"

	tests := []struct {
		name     string
		mantissa int64
		exponent int
		expected string
	}{
		{"scientific exp1", canonical, 1, "1000000000000000e1"},
		{"scientific exp-26", canonical, -26, "1000000000000000e-26"},
		{"fixed exp-25", canonical, -25, "0.0000000001"},
		{"fixed exp-5", canonical, -5, "10000000000"},
		{"scientific exp-50 negative", -canonical, -50, "-1000000000000000e-50"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := Amount{
				iou:      IOUAmountValue{mantissa: tc.mantissa, exponent: tc.exponent},
				Currency: "USD",
				Issuer:   issuer,
			}

			if got := a.Value(); got != tc.expected {
				t.Fatalf("Amount.Value() = %q, want %q", got, tc.expected)
			}

			raw, err := json.Marshal(a)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			var decoded map[string]string
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("json.Unmarshal: %v (raw=%s)", err, string(raw))
			}
			if decoded["value"] != tc.expected {
				t.Fatalf("MarshalJSON value = %q, want %q (raw=%s)",
					decoded["value"], tc.expected, string(raw))
			}
			if decoded["currency"] != "USD" || decoded["issuer"] != issuer {
				t.Fatalf("MarshalJSON unexpected envelope: %s", string(raw))
			}
		})
	}
}
