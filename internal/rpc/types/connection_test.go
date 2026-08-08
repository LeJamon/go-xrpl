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

func TestBookRequestUnmarshalReplacesRawSides(t *testing.T) {
	var request BookRequest
	if err := json.Unmarshal([]byte(`{"taker_pays":{"currency":"USD"},"taker_gets":{"currency":"XRP"}}`), &request); err != nil {
		t.Fatal(err)
	}
	previousPays := append(json.RawMessage(nil), request.TakerPays...)
	previousGets := append(json.RawMessage(nil), request.TakerGets...)
	if err := json.Unmarshal([]byte(`{"taker_gets":{"mpt_issuance_id":"0"}}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.TakerPays != nil {
		t.Fatalf("omitted taker_pays = %s, want nil", request.TakerPays)
	}
	if got := string(request.TakerGets); got != `{"mpt_issuance_id":"0"}` {
		t.Fatalf("taker_gets = %s", got)
	}
	if string(previousPays) != `{"currency":"USD"}` || string(previousGets) != `{"currency":"XRP"}` {
		t.Fatalf("previous raw sides were mutated: pays=%s gets=%s", previousPays, previousGets)
	}
}
