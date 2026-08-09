package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/spf13/cobra"
)

// feeSettingsHex returns a binary-codec-encoded FeeSettings entry as a hex
// string. It is a convenient known-good blob for exercising the decode paths.
func feeSettingsHex(t *testing.T) string {
	t.Helper()
	blob, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           true,
		BaseFeeDrops:          10,
		ReserveBaseDrops:      10_000_000,
		ReserveIncrementDrops: 2_000_000,
	})
	if err != nil {
		t.Fatalf("serializing fee settings: %v", err)
	}
	return hex.EncodeToString(blob)
}

func TestLoadStateFile_Formats(t *testing.T) {
	dir := t.TempDir()

	wrapped := filepath.Join(dir, "wrapped.json")
	if err := os.WriteFile(wrapped, []byte(`{"ledger_index":100,"entries":[{"index":"AB","data":"CD"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := loadStateFile(wrapped)
	if err != nil || len(entries) != 1 || entries["ab"].Index != "AB" {
		t.Fatalf("wrapped format: entries=%v err=%v", entries, err)
	}

	bareArr := filepath.Join(dir, "bare.json")
	if err := os.WriteFile(bareArr, []byte(`[{"index":"11","data_hex":"22"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err = loadStateFile(bareArr)
	if err != nil || len(entries) != 1 || entries["11"].DataHex != "22" {
		t.Fatalf("bare array format: entries=%v err=%v", entries, err)
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"entries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err = loadStateFile(empty)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty wrapper: entries=%v err=%v", entries, err)
	}

	if _, err := loadStateFile(filepath.Join(dir, "nope.json")); err == nil {
		t.Fatal("expected error for missing file")
	}

	junk := filepath.Join(dir, "junk.json")
	if err := os.WriteFile(junk, []byte(`"just a string"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStateFile(junk); err == nil {
		t.Fatal("expected unrecognized-format error")
	}
}

func TestLoadStateFileRejectsAmbiguousEntries(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "missing wrapper entries",
			content: `{"ledger_index":100}`,
			wantErr: "state snapshot is missing entries",
		},
		{
			name:    "missing index",
			content: `[{"data":"11"}]`,
			wantErr: "entry 1 has no index",
		},
		{
			name:    "blank index",
			content: `[{"index":"  ","data":"11"}]`,
			wantErr: "entry 1 has no index",
		},
		{
			name:    "duplicate normalized index",
			content: `[{"index":"AB","data":"11"},{"index":"ab","data":"22"}]`,
			wantErr: `entry 2 index "ab" duplicates "AB"`,
		},
		{
			name:    "decoded only",
			content: `[{"index":"AB","decoded":{"LedgerEntryType":"AccountRoot"}}]`,
			wantErr: `entry "AB" has no raw data`,
		},
		{
			name:    "both raw fields",
			content: `[{"index":"AB","data":"11","data_hex":"11"}]`,
			wantErr: `entry "AB" has both data and data_hex`,
		},
		{
			name:    "empty data with data hex",
			content: `[{"index":"AB","data":"","data_hex":"11"}]`,
			wantErr: `entry "AB" has both data and data_hex`,
		},
		{
			name:    "null data with data hex",
			content: `[{"index":"AB","data":null,"data_hex":"11"}]`,
			wantErr: `entry "AB" has both data and data_hex`,
		},
		{
			name:    "non-string raw data",
			content: `[{"index":"AB","data":11}]`,
			wantErr: `entry "AB" raw data must be a string`,
		},
		{
			name:    "invalid raw data",
			content: `[{"index":"AB","data":"not-hex"}]`,
			wantErr: `entry "AB" has invalid raw data`,
		},
		{
			name:    "wrong decoded type",
			content: `[{"index":"AB","data":"11","decoded":"object required"}]`,
			wantErr: "decoding state entries",
		},
		{
			name:    "null entries",
			content: `{"entries":null}`,
			wantErr: "state snapshot entries must be an array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := loadStateFile(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("loadStateFile() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecodeStateData(t *testing.T) {
	if got := decodeStateData(feeSettingsHex(t)); got == nil || got["LedgerEntryType"] != "FeeSettings" {
		t.Fatalf("expected FeeSettings decode, got %v", got)
	}
	if got := decodeStateData("nothex!!"); got != nil {
		t.Fatalf("expected nil for invalid hex, got %v", got)
	}
}

func TestNormalizeStateEntries(t *testing.T) {
	feeHex := feeSettingsHex(t)
	entries := []stateFileEntry{
		{Index: "AA", Data: json.RawMessage(`"` + feeHex + `"`)},
		{Index: "BB", DataHex: json.RawMessage(`"` + feeHex + `"`), Decoded: map[string]interface{}{"LedgerEntryType": "Stale"}},
		{Index: "CC", DataHex: json.RawMessage(`"00"`), Decoded: map[string]interface{}{"LedgerEntryType": "AccountRoot"}},
	}
	m, err := normalizeStateEntries(entries)
	if err != nil {
		t.Fatalf("normalizeStateEntries: %v", err)
	}
	if len(m) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(m))
	}
	if m["aa"].Decoded["LedgerEntryType"] != "FeeSettings" {
		t.Errorf("aa not decoded from Data: %+v", m["aa"])
	}
	if m["bb"].DataHex != feeHex || m["bb"].Decoded == nil {
		t.Errorf("bb not decoded from DataHex: %+v", m["bb"])
	}
	if m["bb"].Decoded["LedgerEntryType"] != "FeeSettings" {
		t.Errorf("bb used stale supplied decoded data: %+v", m["bb"])
	}
	if m["cc"].Decoded["LedgerEntryType"] != "AccountRoot" {
		t.Errorf("cc pre-decoded value lost: %+v", m["cc"])
	}
}

func TestCompareStates(t *testing.T) {
	map1 := map[string]stateEntry{
		"aa": {Index: "AA", DataHex: "1111", Decoded: map[string]interface{}{"LedgerEntryType": "AccountRoot", "Balance": "1"}},
		"bb": {Index: "BB", DataHex: "2222", Decoded: map[string]interface{}{"Balance": "1", "Flags": "0"}},
		"cc": {Index: "CC", DataHex: "3333"},
	}
	map2 := map[string]stateEntry{
		"aa": {Index: "AA", DataHex: "1111", Decoded: map[string]interface{}{"LedgerEntryType": "AccountRoot", "Balance": "1"}},
		"bb": {Index: "BB", DataHex: "22FF", Decoded: map[string]interface{}{"Balance": "2", "Flags": "0"}},
		"dd": {Index: "DD", DataHex: "4444"},
	}

	added, removed, modified, unchanged, err := compareStates(map1, map2)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0].Index != "DD" {
		t.Errorf("added = %+v", added)
	}
	if len(removed) != 1 || removed[0].Index != "CC" {
		t.Errorf("removed = %+v", removed)
	}
	if len(unchanged) != 1 || unchanged[0].Index != "AA" {
		t.Errorf("unchanged = %+v", unchanged)
	}
	if len(modified) != 1 || modified[0].Index != "BB" {
		t.Fatalf("modified = %+v", modified)
	}
	if len(modified[0].ChangedKeys) != 1 || modified[0].ChangedKeys[0] != "Balance" {
		t.Errorf("changed keys = %v", modified[0].ChangedKeys)
	}
}

func TestFilterByType(t *testing.T) {
	entries := []stateEntry{
		{Index: "A", Decoded: map[string]interface{}{"LedgerEntryType": "AccountRoot"}},
		{Index: "B", Decoded: map[string]interface{}{"LedgerEntryType": "Offer"}},
		{Index: "C", Decoded: nil},
	}
	got := filterByType(entries, "accountroot") // case-insensitive
	if len(got) != 1 || got[0].Index != "A" {
		t.Fatalf("filterByType = %+v", got)
	}

	mods := []modifiedEntry{
		{Index: "A", NewDecoded: map[string]interface{}{"LedgerEntryType": "Offer"}},
		{Index: "B", NewDecoded: map[string]interface{}{"LedgerEntryType": "AccountRoot"}},
		{Index: "C", NewDecoded: nil},
	}
	gotMod := filterModifiedByType(mods, "Offer")
	if len(gotMod) != 1 || gotMod[0].Index != "A" {
		t.Fatalf("filterModifiedByType = %+v", gotMod)
	}
}

func TestFormatValue(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"iou with issuer", map[string]interface{}{"currency": "USD", "value": "100", "issuer": "rIssuerAccount"}, "100 USD (rIssuerA...)"},
		{"iou no issuer", map[string]interface{}{"currency": "USD", "value": "100"}, "100 USD"},
		{"array", []interface{}{1, 2, 3}, "[3 items]"},
		{"scalar", uint32(42), "42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatValue(tc.in); got != tc.want {
				t.Errorf("formatValue(%v) = %q want %q", tc.in, got, tc.want)
			}
		})
	}

	// A map that is not an Amount falls back to JSON marshalling.
	if got := formatValue(map[string]interface{}{"x": "y"}); got != `{"x":"y"}` {
		t.Errorf("non-amount map = %q", got)
	}
}

func TestPrintEntries(t *testing.T) {
	idx := "00000000000000000000000000000000000000000000000000000000000000FF"
	entry := stateEntry{Index: idx, Decoded: map[string]interface{}{
		"LedgerEntryType": "AccountRoot", "Account": "rAcct", "Balance": "1", "Sequence": uint32(1), "OwnerCount": uint32(0), "Flags": uint32(0),
	}}

	tests := []struct {
		name       string
		showAll    bool
		showDecode bool
		want       string
	}{
		{
			name: "summary only",
			want: "\n[+] Entry 1: " + idx + "\n    Type: AccountRoot\n\n",
		},
		{
			name:       "decoded key fields",
			showDecode: true,
			want: "\n[+] Entry 1: " + idx + "\n" +
				"    Type: AccountRoot\n" +
				"    Account: rAcct\n" +
				"    Balance: 1\n" +
				"    Sequence: 1\n" +
				"    OwnerCount: 0\n" +
				"    Flags: 0\n\n",
		},
		{
			name:    "all with decoded disabled",
			showAll: true,
			want:    "\n[+] Entry 1: " + idx + "\n    Type: AccountRoot\n\n",
		},
		{
			name:       "all decoded",
			showAll:    true,
			showDecode: true,
			want: "\n[+] Entry 1: " + idx + "\n" +
				"    Type: AccountRoot\n" +
				"    Account: rAcct\n" +
				"    Balance: 1\n" +
				"    Sequence: 1\n" +
				"    OwnerCount: 0\n" +
				"    Flags: 0\n" +
				"    Full data:\n" +
				"    {\n" +
				"      \"Account\": \"rAcct\",\n" +
				"      \"Balance\": \"1\",\n" +
				"      \"Flags\": 0,\n" +
				"      \"LedgerEntryType\": \"AccountRoot\",\n" +
				"      \"OwnerCount\": 0,\n" +
				"      \"Sequence\": 1\n" +
				"    }\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := &compareOptions{showAll: tt.showAll, showDecoded: tt.showDecode}
			var output bytes.Buffer
			printAddedEntries(&output, []stateEntry{entry}, options)
			got := output.String()
			const banner = "================================================================================\n" +
				"                              ADDED ENTRIES\n" +
				"================================================================================\n"
			if got != banner+tt.want {
				t.Fatalf("printAddedEntries() =\n%q\nwant\n%q", got, banner+tt.want)
			}
		})
	}
}

func TestPrintUnchangedEntries(t *testing.T) {
	var output bytes.Buffer
	printUnchangedEntries(&output, []stateEntry{{
		Index:   "123456789012345678901234567890123",
		Decoded: map[string]any{"LedgerEntryType": "Offer"},
	}})
	want := "================================================================================\n" +
		"                           UNCHANGED ENTRIES\n" +
		"================================================================================\n" +
		"[=] 1: 12345678901234567890123456789012... (Offer)\n\n"
	if got := output.String(); got != want {
		t.Fatalf("printUnchangedEntries() =\n%q\nwant\n%q", got, want)
	}
}

func TestPrintUnknownFieldsSorted(t *testing.T) {
	var output bytes.Buffer
	printEntryDetails(&output, map[string]any{
		"LedgerEntryType": "SomethingUnknown",
		"Zulu":            3,
		"Alpha":           1,
		"Middle":          2,
	}, &compareOptions{showDecoded: true})
	want := "    Type: SomethingUnknown\n" +
		"    Alpha: 1\n" +
		"    Middle: 2\n" +
		"    Zulu: 3\n"
	if got := output.String(); got != want {
		t.Fatalf("printEntryDetails() =\n%q\nwant\n%q", got, want)
	}
}

func TestPrintModifiedEntriesDecodedFlag(t *testing.T) {
	entry := modifiedEntry{
		Index:       "AB",
		OldDecoded:  map[string]any{"LedgerEntryType": "RippleState", "Balance": "1"},
		NewDecoded:  map[string]any{"LedgerEntryType": "RippleState", "Balance": "2"},
		ChangedKeys: []string{"Balance"},
	}
	const banner = "================================================================================\n" +
		"                            MODIFIED ENTRIES\n" +
		"================================================================================\n" +
		"\n[~] Entry 1: AB\n" +
		"    Type: RippleState\n" +
		"    Changed fields: [Balance]\n" +
		"    ---\n"
	for _, tt := range []struct {
		name    string
		decoded bool
		want    string
	}{
		{"decoded", true, banner + "    Balance:\n      - 1\n      + 2\n\n"},
		{"raw summary", false, banner + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			printModifiedEntries(&output, []modifiedEntry{entry}, &compareOptions{showDecoded: tt.decoded})
			if got := output.String(); got != tt.want {
				t.Fatalf("printModifiedEntries() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestWriteDiffJSON(t *testing.T) {
	out := filepath.Join(t.TempDir(), "diff.json")
	added := []stateEntry{{Index: "A", DataHex: "AA", Decoded: map[string]interface{}{"k": "v"}}}
	removed := []stateEntry{{Index: "B", DataHex: "BB"}}
	modified := []modifiedEntry{{
		Index:      "C",
		OldDataHex: "CC",
		NewDataHex: "DD",
	}}

	if err := writeDiffJSON(io.Discard, out, added, removed, modified); err != nil {
		t.Fatalf("writeDiffJSON: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading diff: %v", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		t.Fatalf("compacting diff: %v", err)
	}
	want := `{"added":[{"data_hex":"AA","decoded":{"k":"v"},"index":"A"}],` +
		`"modified":[{"changed_keys":null,"index":"C","new":null,` +
		`"new_data_hex":"DD","old":null,"old_data_hex":"CC"}],` +
		`"removed":[{"data_hex":"BB","decoded":null,"index":"B"}]}`
	if got := compact.String(); got != want {
		t.Fatalf("writeDiffJSON() =\n%s\nwant\n%s", got, want)
	}
}

func TestRunCompare_IdenticalFiles(t *testing.T) {
	dir := t.TempDir()
	content := []byte(`{"entries":[{"index":"00000000000000000000000000000000000000000000000000000000000000FF","data":"` + feeSettingsHex(t) + `"}]}`)
	f1 := filepath.Join(dir, "a.json")
	f2 := filepath.Join(dir, "b.json")
	if err := os.WriteFile(f1, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	// Identical files produce no differences, so runCompare returns nil
	// (cmdexit.ErrReported is only returned when there is a diff).
	if err := runCompare(cmd, []string{f1, f2}, &compareOptions{showDecoded: true}); err != nil {
		t.Fatalf("runCompare on identical files: %v", err)
	}
}

func TestRunCompare_DiffReportsExit(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.json")
	f2 := filepath.Join(dir, "b.json")
	if err := os.WriteFile(f1, []byte(`{"entries":[{"index":"AA","data":"1111"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte(`{"entries":[{"index":"BB","data":"2222"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := runCompare(cmd, []string{f1, f2}, &compareOptions{showDecoded: true}); !errors.Is(err, cmdexit.ErrReported) {
		t.Fatalf("expected cmdexit.ErrReported on diff, got %v", err)
	}
}
