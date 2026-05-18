package state

import (
	"encoding/json"
	"testing"
)

func TestMPTAmountParse(t *testing.T) {
	t.Parallel()
	data := []byte(`{"value": "9223372036854775807", "mpt_issuance_id": "00000004ae123a8556f3cf91154711376afb0f894f832b3d"}`)

	var amt Amount
	if err := json.Unmarshal(data, &amt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !amt.IsMPT() {
		t.Fatalf("IsMPT = false, want true")
	}
	if amt.IsNative() {
		t.Fatalf("IsNative = true, want false")
	}
	if got := amt.Value(); got != "9223372036854775807" {
		t.Fatalf("Value = %q, want %q", got, "9223372036854775807")
	}
	raw, ok := amt.MPTRaw()
	if !ok || raw != 9223372036854775807 {
		t.Fatalf("MPTRaw = (%d, %v), want (9223372036854775807, true)", raw, ok)
	}
}

// TestAmount_MarshalJSON_MPTRawInteger pins MPT JSON output to the raw int64,
// matching rippled's STAmount::getText for MPTIssue-held amounts where mOffset
// is asserted to be 0 (STAmount.cpp:336-348). Going through IOUAmountValue.String
// would otherwise emit scientific notation for any raw value >= 10^16 after IOU
// canonicalization.
func TestAmount_MarshalJSON_MPTRawInteger(t *testing.T) {
	t.Parallel()

	issuanceID := "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"
	issuer := "rweYz56rfmQ98cAdRaeTxQS9wVMGnrdsFp"

	tests := []struct {
		name     string
		value    int64
		expected string
	}{
		{"small mpt", 100, "100"},
		{"max canonical IOU mantissa", 9_999_999_999_999_999, "9999999999999999"},
		{"10^16 first that triggered IOU scientific", 10_000_000_000_000_000, "10000000000000000"},
		{"10^17 native-XRP-scale", 100_000_000_000_000_000, "100000000000000000"},
		{"max int64", 9_223_372_036_854_775_807, "9223372036854775807"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := NewMPTAmountWithIssuanceID(tc.value, issuer, issuanceID)

			if got := a.Value(); got != tc.expected {
				t.Fatalf("Value() = %q, want %q", got, tc.expected)
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
			if decoded["mpt_issuance_id"] != issuanceID {
				t.Fatalf("MarshalJSON mpt_issuance_id = %q, want %q",
					decoded["mpt_issuance_id"], issuanceID)
			}
		})
	}
}
