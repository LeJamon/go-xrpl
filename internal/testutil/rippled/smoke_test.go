//go:build docker

package rippled

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsensusSmokeRejectsMissingOrMismatchedRoots(t *testing.T) {
	script, err := filepath.Abs("../../../scripts/consensus-smoke/smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	response := func(field string, value any) string {
		t.Helper()
		ledger := map[string]any{
			"ledger_index":     20,
			"ledger_hash":      strings.Repeat("A", 64),
			"account_hash":     strings.Repeat("B", 64),
			"transaction_hash": strings.Repeat("C", 64),
		}
		if field != "" {
			ledger[field] = value
		}
		data, err := json.Marshal(map[string]any{"result": map[string]any{"ledger": ledger}})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	const harness = `source "$1"
rpc_url() { printf '%s' "$1"; }
rpc_call() {
  if [[ "$1" == goxrpl-0 ]]; then
    printf '%s' "$SMOKE_GO_RESPONSE"
  else
    printf '%s' "$SMOKE_RESPONSE"
  fi
}
compare_ledger_at_seq contract 20
`
	good := response("", nil)
	for _, test := range []struct {
		name, reply string
		pass        bool
	}{
		{"equal", good, true},
		{"transaction_root", response("transaction_hash", strings.Repeat("D", 64)), false},
		{"account_root", response("account_hash", strings.Repeat("D", 64)), false},
		{"ledger_hash", response("ledger_hash", strings.Repeat("D", 64)), false},
		{"missing_root", response("transaction_hash", nil), false},
		{"malformed_root", response("account_hash", "not a hash"), false},
		{"wrong_ledger", response("ledger_index", 21), false},
		{"missing_ledger", `{"result":{"error":"lgrNotFound"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SMOKE_RESPONSE", good)
			t.Setenv("SMOKE_GO_RESPONSE", test.reply)
			cmd := exec.CommandContext(t.Context(), "bash", "-c", harness, "smoke-contract", script)
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.pass {
				t.Fatalf("want pass=%t, got %v\n%s", test.pass, err, output)
			}
		})
	}
}
