package txprojection

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestProjectJSONVersionShapeAndInputIsolation(t *testing.T) {
	original := map[string]any{
		"TransactionType": "Payment",
		"Amount":          "100",
		"Nested":          map[string]any{"keep": true},
	}
	wantOriginal := map[string]any{
		"TransactionType": "Payment",
		"Amount":          "100",
		"Nested":          map[string]any{"keep": true},
	}

	v1 := ProjectJSON(original, "abcdef", 1)
	if v1["Amount"] != "100" || v1["DeliverMax"] != "100" || v1["hash"] != "ABCDEF" {
		t.Fatalf("unexpected v1 projection: %#v", v1)
	}
	v2 := ProjectJSON(original, "abcdef", 2)
	if _, ok := v2["Amount"]; ok || v2["DeliverMax"] != "100" {
		t.Fatalf("unexpected v2 projection: %#v", v2)
	}
	if !reflect.DeepEqual(original, wantOriginal) {
		t.Fatalf("projection mutated input: got %#v want %#v", original, wantOriginal)
	}
}

func TestProjectRawAndDeliverMaxRules(t *testing.T) {
	raw := json.RawMessage(`{"TransactionType":"Payment","Amount":"10"}`)
	projected, err := ProjectRaw(raw, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(projected, &got); err != nil {
		t.Fatal(err)
	}
	if got["DeliverMax"] != "10" {
		t.Fatalf("missing DeliverMax: %#v", got)
	}
	if _, ok := got["Amount"]; ok {
		t.Fatalf("API v2 projection retained Amount: %#v", got)
	}
	if string(raw) != `{"TransactionType":"Payment","Amount":"10"}` {
		t.Fatalf("raw input changed: %s", raw)
	}

	for _, tc := range []struct {
		name string
		json map[string]any
	}{
		{name: "non-payment", json: map[string]any{"TransactionType": "OfferCreate", "Amount": "10"}},
		{name: "missing amount", json: map[string]any{"TransactionType": "Payment"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := make(map[string]any, len(tc.json))
			for k, v := range tc.json {
				before[k] = v
			}
			InjectDeliverMax(tc.json, 2)
			if !reflect.DeepEqual(tc.json, before) {
				t.Fatalf("unexpected mutation for %s: %#v", tc.name, tc.json)
			}
		})
	}
}
