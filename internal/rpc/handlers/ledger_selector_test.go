package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func TestResolveLedgerSelector(t *testing.T) {
	const validHash = "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652"

	t.Run("neither field defaults to current", func(t *testing.T) {
		sel, rpcErr := resolveLedgerSelector(json.RawMessage(`{}`))
		if rpcErr != nil {
			t.Fatalf("unexpected error: %v", rpcErr)
		}
		if sel != "current" {
			t.Errorf("selector = %q, want current", sel)
		}
	})

	t.Run("ledger_index only", func(t *testing.T) {
		sel, rpcErr := resolveLedgerSelector(json.RawMessage(`{"ledger_index":"12345"}`))
		if rpcErr != nil {
			t.Fatalf("unexpected error: %v", rpcErr)
		}
		if sel != "12345" {
			t.Errorf("selector = %q, want 12345", sel)
		}
	})

	t.Run("ledger_hash only threads the hash", func(t *testing.T) {
		sel, rpcErr := resolveLedgerSelector(json.RawMessage(`{"ledger_hash":"` + validHash + `"}`))
		if rpcErr != nil {
			t.Fatalf("unexpected error: %v", rpcErr)
		}
		if sel != validHash {
			t.Errorf("selector = %q, want the hash itself", sel)
		}
		// A hash selector must resolve to closed-ledger shape, never the open one.
		if isOpenLedgerSelector(sel) {
			t.Errorf("hash selector must not be treated as the open ledger")
		}
	})

	t.Run("rejects hash and index together", func(t *testing.T) {
		_, rpcErr := resolveLedgerSelector(json.RawMessage(
			`{"ledger_hash":"` + validHash + `","ledger_index":"validated"}`,
		))
		if rpcErr == nil {
			t.Fatal("expected conflicting-selector error")
		}
		if rpcErr.Message != "Exactly one of 'ledger_hash' or 'ledger_index' can be specified." {
			t.Errorf("message = %q", rpcErr.Message)
		}
	})

	t.Run("malformed hash — wrong length", func(t *testing.T) {
		_, rpcErr := resolveLedgerSelector(json.RawMessage(`{"ledger_hash":"DEADBEEF"}`))
		if rpcErr == nil {
			t.Fatalf("want rpcINVALID_PARAMS, got nil")
		}
		if rpcErr.Code != types.RpcErrorInvalidParams("").Code {
			t.Errorf("error code = %d, want invalid_params", rpcErr.Code)
		}
		if rpcErr.Message != "Invalid field 'ledger_hash', not hex string." {
			t.Errorf("message = %q", rpcErr.Message)
		}
	})

	t.Run("malformed hash — non-hex", func(t *testing.T) {
		_, rpcErr := resolveLedgerSelector(json.RawMessage(`{"ledger_hash":"` + strings.Repeat("z", 64) + `"}`))
		if rpcErr == nil {
			t.Fatalf("want rpcINVALID_PARAMS for non-hex hash, got nil")
		}
	})

	t.Run("legacy ledger uses only 64-character strings as hashes", func(t *testing.T) {
		_, rpcErr := resolveLedgerSelector(types.LedgerSpecifier{Ledger: "1234567890123"})
		if rpcErr == nil {
			t.Fatal("want rpcINVALID_PARAMS for non-numeric legacy ledger index")
		}
		if rpcErr.Message != "Invalid field 'ledger', not string or number." {
			t.Errorf("message = %q", rpcErr.Message)
		}
	})
}

func TestParseRawLedgerSelector(t *testing.T) {
	const validHash = "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652"
	parseProbe := func(t *testing.T, input string) map[string]json.RawMessage {
		t.Helper()
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(input), &probe); err != nil {
			t.Fatal(err)
		}
		return probe
	}

	tests := []struct {
		name     string
		input    string
		wantKind ledgerselector.Kind
		wantSeq  uint32
		wantHash string
	}{
		{"integer ledger_index", `{"ledger_index":4294967295}`, ledgerselector.KindSequence, ^uint32(0), ""},
		{"integer legacy ledger", `{"ledger":7}`, ledgerselector.KindSequence, 7, ""},
		{"non-64 legacy string is index", `{"ledger":"0000000000001"}`, ledgerselector.KindSequence, 1, ""},
		{"64-char legacy string is hash", `{"ledger":"` + validHash + `"}`, ledgerselector.KindHash, 0, validHash},
		{"zero shorthand explicit hash", `{"ledger_hash":"0"}`, ledgerselector.KindHash, 0, strings.Repeat("0", 64)},
		{"zero legacy ledger is index", `{"ledger":"0"}`, ledgerselector.KindSequence, 0, ""},
		{"zero-padded explicit index stays index", `{"ledger_index":"` + strings.Repeat("0", 63) + `1"}`, ledgerselector.KindSequence, 1, ""},
		{"leading plus explicit index", `{"ledger_index":"+1"}`, ledgerselector.KindSequence, 1, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection, rpcErr := parseRawLedgerSelector(
				parseProbe(t, test.input),
				ledgerselector.Current(),
				lookupLedgerSelectorErrors,
			)
			if rpcErr != nil {
				t.Fatalf("unexpected error: %v", rpcErr)
			}
			if selection.Kind() != test.wantKind {
				t.Fatalf("kind = %v, want %v", selection.Kind(), test.wantKind)
			}
			if test.wantKind == ledgerselector.KindSequence {
				sequence, ok := selection.Sequence()
				if !ok || sequence != test.wantSeq {
					t.Fatalf("sequence = %d, %v; want %d", sequence, ok, test.wantSeq)
				}
			}
			if test.wantKind == ledgerselector.KindHash && selection.String() != test.wantHash {
				t.Fatalf("hash = %q, want %q", selection.String(), test.wantHash)
			}
		})
	}

	t.Run("conflicting members", func(t *testing.T) {
		_, rpcErr := parseRawLedgerSelector(
			parseProbe(t, `{"ledger_hash":"`+validHash+`","ledger_index":1}`),
			ledgerselector.Current(),
			lookupLedgerSelectorErrors,
		)
		if rpcErr == nil || rpcErr.Message != "Exactly one of 'ledger_hash' or 'ledger_index' can be specified." {
			t.Fatalf("error = %#v", rpcErr)
		}
	})

	for _, test := range []struct {
		name    string
		input   string
		message string
	}{
		{"null hash", `{"ledger_hash":null}`, "Invalid field 'ledger_hash', not hex string."},
		{"null index", `{"ledger_index":null}`, "Invalid field 'ledger_index', not string or number."},
		{"null still conflicts", `{"ledger_hash":null,"ledger_index":null}`, "Exactly one of 'ledger_hash' or 'ledger_index' can be specified."},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, rpcErr := parseRawLedgerSelector(
				parseProbe(t, test.input),
				ledgerselector.Current(),
				lookupLedgerSelectorErrors,
			)
			if rpcErr == nil || rpcErr.Message != test.message {
				t.Fatalf("error = %#v, want %q", rpcErr, test.message)
			}
		})
	}
}

func TestFillLedgerFieldsUsesResolvedCurrentIndex(t *testing.T) {
	response := map[string]any{}
	fillLedgerFields(response, "10", "ABCD", 10, 10, false)
	if response["ledger_current_index"] != uint32(10) {
		t.Fatalf("ledger_current_index = %#v", response["ledger_current_index"])
	}
	if _, ok := response["ledger_hash"]; ok {
		t.Fatal("open ledger response must not include ledger_hash")
	}
	if _, ok := response["ledger_index"]; ok {
		t.Fatal("open ledger response must not include ledger_index")
	}

	response = map[string]any{}
	fillLedgerFields(response, "9", "ABCD", 9, 10, true)
	if response["ledger_hash"] != "ABCD" || response["ledger_index"] != uint32(9) {
		t.Fatalf("closed ledger fields = %#v", response)
	}
}
