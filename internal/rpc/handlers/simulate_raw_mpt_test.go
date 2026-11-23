package handlers

import (
	"math"
	"reflect"
	"testing"

	binarycodectypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

func TestNormalizeDecodedMPTValuesForSimulation(t *testing.T) {
	const issuanceID = "00000001B5F762798A53D543A014CAF8B297CFF8F2F937E8"
	reserved := map[string]any{
		"currency": "1",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"value":    "1",
	}
	reservedPath := map[string]any{
		"currency": "1",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	fields := map[string]any{
		"Amount": map[string]any{
			"mpt_issuance_id": issuanceID,
			"value":           binarycodectypes.RawMPTValue{Negative: true},
		},
		"Paths": []any{[]any{reservedPath}},
		"RawTransactions": []any{map[string]any{
			"RawTransaction": map[string]any{
				"Amount": map[string]any{
					"mpt_issuance_id": issuanceID,
					"value":           binarycodectypes.RawMPTValue{Magnitude: math.MaxUint64},
				},
			},
		}},
		"Reserved": reserved,
	}

	normalizeDecodedMPTValuesForSimulation(fields)

	if got := fields["Amount"].(map[string]any)["value"]; got != "0" {
		t.Fatalf("negative zero = %#v, want %q", got, "0")
	}
	path := fields["Paths"].([]any)[0].([]any)[0].(map[string]any)
	if got := path["currency"]; got != "1" {
		t.Fatalf("reserved path currency = %#v, want unchanged sentinel", got)
	}
	inner := fields["RawTransactions"].([]any)[0].(map[string]any)["RawTransaction"].(map[string]any)
	if got := inner["Amount"].(map[string]any)["value"]; got != "18446744073709551615" {
		t.Fatalf("batch MPT value = %#v, want unsigned decimal", got)
	}
	if got := fields["Reserved"]; !reflect.DeepEqual(got, reserved) {
		t.Fatalf("reserved decoded field = %#v, want unchanged %#v", got, reserved)
	}
}
