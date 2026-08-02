package rpc

import (
	"encoding/json"
	"testing"
)

func TestServerStatusEventRequiredFieldsPreserveZero(t *testing.T) {
	data, err := json.Marshal(ServerStatusEvent{})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"base_fee", "load_base", "load_factor", "load_factor_server", "server_status"} {
		if _, ok := event[field]; !ok {
			t.Fatalf("required field %q omitted from %s", field, data)
		}
	}
}
