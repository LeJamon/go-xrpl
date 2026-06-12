package cli

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

	// 1. StateFile wrapper format with entries.
	wrapped := filepath.Join(dir, "wrapped.json")
	if err := os.WriteFile(wrapped, []byte(`{"ledger_index":100,"entries":[{"index":"AB","data":"CD"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := loadStateFile(wrapped)
	if err != nil || len(entries) != 1 || entries[0].Index != "AB" {
		t.Fatalf("wrapped format: entries=%v err=%v", entries, err)
	}

	// 2. Bare array of StateFileEntry.
	bareArr := filepath.Join(dir, "bare.json")
	if err := os.WriteFile(bareArr, []byte(`[{"index":"11","data_hex":"22"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err = loadStateFile(bareArr)
	if err != nil || len(entries) != 1 || entries[0].DataHex != "22" {
		t.Fatalf("bare array format: entries=%v err=%v", entries, err)
	}

	// 3. Map-fallback path: content that does not unmarshal cleanly into
	// []StateFileEntry (here `decoded` is a string, not an object) falls back to
	// generic-map parsing, which keeps only entries carrying a non-empty index.
	mapArr := filepath.Join(dir, "maps.json")
	content := `[{"index":"33","data":"44","decoded":"not-an-object"},{"index":"","data":"noindex"}]`
	if err := os.WriteFile(mapArr, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err = loadStateFile(mapArr)
	if err != nil {
		t.Fatalf("map array format: %v", err)
	}
	if len(entries) != 1 || entries[0].Index != "33" || entries[0].Data != "44" {
		t.Fatalf("map array format: unexpected entries %+v", entries)
	}

	// 4. Missing file → error.
	if _, err := loadStateFile(filepath.Join(dir, "nope.json")); err == nil {
		t.Fatal("expected error for missing file")
	}

	// 5. Unrecognized content → error.
	junk := filepath.Join(dir, "junk.json")
	if err := os.WriteFile(junk, []byte(`"just a string"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStateFile(junk); err == nil {
		t.Fatal("expected unrecognized-format error")
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

func TestBuildStateMap(t *testing.T) {
	feeHex := feeSettingsHex(t)
	entries := []StateFileEntry{
		// data present → decoded lazily from the codec.
		{Index: "AA", Data: feeHex},
		// only data_hex present → falls back to DataHex.
		{Index: "BB", DataHex: feeHex},
		// pre-decoded → used as-is, codec not invoked.
		{Index: "CC", Decoded: map[string]interface{}{"LedgerEntryType": "AccountRoot"}},
		// nothing decodable.
		{Index: "DD"},
	}
	m := buildStateMap(entries)
	if len(m) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(m))
	}
	if m["aa"].Decoded["LedgerEntryType"] != "FeeSettings" {
		t.Errorf("aa not decoded from Data: %+v", m["aa"])
	}
	if m["bb"].DataHex != feeHex || m["bb"].Decoded == nil {
		t.Errorf("bb not decoded from DataHex: %+v", m["bb"])
	}
	if m["cc"].Decoded["LedgerEntryType"] != "AccountRoot" {
		t.Errorf("cc pre-decoded value lost: %+v", m["cc"])
	}
	if m["dd"].Decoded != nil {
		t.Errorf("dd should have no decoded data: %+v", m["dd"])
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

	added, removed, modified, unchanged := compareStates(map1, map2)
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

func TestFindChangedKeys(t *testing.T) {
	if got := findChangedKeys(nil, map[string]interface{}{"a": 1}); got != nil {
		t.Errorf("nil old should yield nil, got %v", got)
	}
	if got := findChangedKeys(map[string]interface{}{"a": 1}, nil); got != nil {
		t.Errorf("nil new should yield nil, got %v", got)
	}
	old := map[string]interface{}{"same": 1, "changed": 1, "removed": 1}
	new := map[string]interface{}{"same": 1, "changed": 2, "added": 1}
	got := findChangedKeys(old, new)
	want := []string{"added", "changed", "removed"} // sorted
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
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

func TestPrintFunctions(t *testing.T) {
	idx := "00000000000000000000000000000000000000000000000000000000000000FF"
	added := []stateEntry{{Index: idx, Decoded: map[string]interface{}{
		"LedgerEntryType": "AccountRoot", "Account": "rAcct", "Balance": "1", "Sequence": uint32(1), "OwnerCount": uint32(0), "Flags": uint32(0),
	}}}
	removed := []stateEntry{{Index: idx, Decoded: nil}} // exercises the "unable to decode" branch
	modified := []modifiedEntry{{
		Index:       idx,
		OldDecoded:  map[string]interface{}{"LedgerEntryType": "RippleState", "Balance": "1"},
		NewDecoded:  map[string]interface{}{"LedgerEntryType": "RippleState", "Balance": "2"},
		ChangedKeys: []string{"Balance"},
	}}
	unchanged := []stateEntry{{Index: idx, Decoded: map[string]interface{}{"LedgerEntryType": "Offer"}}}

	// compareShowAll/compareShowDecoded influence the print depth; restore after.
	prevAll, prevDecoded := compareShowAll, compareShowDecoded
	defer func() { compareShowAll, compareShowDecoded = prevAll, prevDecoded }()
	compareShowAll, compareShowDecoded = true, true

	printAddedEntries(io.Discard, added)
	printRemovedEntries(io.Discard, removed)
	printModifiedEntries(io.Discard, modified)
	printUnchangedEntries(io.Discard, unchanged)

	// Exercise printKeyFields for each well-known entry type.
	for _, d := range []map[string]interface{}{
		{"LedgerEntryType": "AccountRoot", "Account": "rA", "Balance": "1", "Sequence": uint32(1), "OwnerCount": uint32(0), "Flags": uint32(0)},
		{"LedgerEntryType": "RippleState", "Balance": map[string]interface{}{"currency": "USD", "value": "1", "issuer": "rIssuerAcct"}, "LowLimit": "0", "HighLimit": "0", "Flags": uint32(0)},
		{"LedgerEntryType": "Offer", "Account": "rA", "TakerGets": "1", "TakerPays": "2", "Sequence": uint32(1)},
		{"LedgerEntryType": "DirectoryNode", "Owner": "rA", "RootIndex": "X"},
		{"LedgerEntryType": "FeeSettings", "BaseFeeDrops": "10", "ReserveBaseDrops": "1", "ReserveIncrementDrops": "1"},
		{"LedgerEntryType": "Amendments", "Amendments": []interface{}{"a", "b"}},
		{"LedgerEntryType": "SomethingUnknown", "FieldOne": "v", "FieldTwo": uint32(3)},
	} {
		printEntryDetails(io.Discard, d)
	}
}

func TestWriteDiffJSON(t *testing.T) {
	out := filepath.Join(t.TempDir(), "diff.json")
	added := []stateEntry{{Index: "A", Decoded: map[string]interface{}{"k": "v"}}}
	removed := []stateEntry{{Index: "B"}}
	modified := []modifiedEntry{{Index: "C", ChangedKeys: []string{"Balance"}, OldDecoded: map[string]interface{}{"Balance": "1"}, NewDecoded: map[string]interface{}{"Balance": "2"}}}

	if err := writeDiffJSON(io.Discard, out, added, removed, modified); err != nil {
		t.Fatalf("writeDiffJSON: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading diff: %v", err)
	}
	var parsed struct {
		Added    []map[string]interface{} `json:"added"`
		Removed  []map[string]interface{} `json:"removed"`
		Modified []map[string]interface{} `json:"modified"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshalling diff: %v", err)
	}
	if len(parsed.Added) != 1 || len(parsed.Removed) != 1 || len(parsed.Modified) != 1 {
		t.Fatalf("unexpected diff contents: %+v", parsed)
	}
	if parsed.Modified[0]["index"] != "C" {
		t.Errorf("modified index = %v", parsed.Modified[0]["index"])
	}
}

func TestRunCompare_IdenticalFiles(t *testing.T) {
	// Reset the command flags to defaults so a prior test cannot trigger the
	// diff path or a stray file write.
	prevAll, prevDecoded, prevFilter, prevOut := compareShowAll, compareShowDecoded, compareFilterType, compareOutputFormat
	defer func() {
		compareShowAll, compareShowDecoded, compareFilterType, compareOutputFormat = prevAll, prevDecoded, prevFilter, prevOut
	}()
	compareShowAll, compareShowDecoded, compareFilterType, compareOutputFormat = false, true, "", ""

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
	if err := runCompare(cmd, []string{f1, f2}); err != nil {
		t.Fatalf("runCompare on identical files: %v", err)
	}
}

func TestRunCompare_DiffReportsExit(t *testing.T) {
	prevAll, prevDecoded, prevFilter, prevOut := compareShowAll, compareShowDecoded, compareFilterType, compareOutputFormat
	defer func() {
		compareShowAll, compareShowDecoded, compareFilterType, compareOutputFormat = prevAll, prevDecoded, prevFilter, prevOut
	}()
	compareShowAll, compareShowDecoded, compareFilterType, compareOutputFormat = false, true, "", ""

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
	if err := runCompare(cmd, []string{f1, f2}); !errors.Is(err, cmdexit.ErrReported) {
		t.Fatalf("expected cmdexit.ErrReported on diff, got %v", err)
	}
}
