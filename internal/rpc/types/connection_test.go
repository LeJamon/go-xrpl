package types

import (
	"encoding/json"
	"testing"
)

func TestBookRequestJsonCppFlagCoercion(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{"null", "null", false},
		{"false", "false", false},
		{"true", "true", true},
		{"zero", "0", false},
		{"nonzero", "-2", true},
		{"empty string", `""`, false},
		{"string", `"false"`, true},
		{"empty array", "[]", false},
		{"array", "[0]", true},
		{"empty object", "{}", false},
		{"object", `{"x":0}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request BookRequest
			body := []byte(`{"both":` + test.value + `,"both_sides":` + test.value + `,"snapshot":` + test.value + `,"state_now":` + test.value + `}`)
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			if request.Both != test.want || request.BothSides != test.want || request.Snapshot != test.want || request.StateNow != test.want {
				t.Fatalf("flags = %+v, want %t", request, test.want)
			}
		})
	}
}

func TestBookRequestUnmarshalResetsOptionalStrings(t *testing.T) {
	var request BookRequest
	if err := json.Unmarshal([]byte(`{"taker":"rTaker","domain":"ABC"}`), &request); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"taker_pays":{},"taker_gets":{}}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.Taker != "" || request.Domain != "" {
		t.Fatalf("omitted optional strings retained: taker=%q domain=%q", request.Taker, request.Domain)
	}
}
