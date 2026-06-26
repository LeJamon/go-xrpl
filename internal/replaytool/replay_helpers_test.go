package replaytool

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// feeSettingsIndexHex is keylet::fees() — the singleton FeeSettings key the
// replay fee extractors look the entry up by.
const feeSettingsIndexHex = "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A651"

func TestParseHexOrDecimal(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"255", 255, true},
		{"0x1F", 31, true},
		{"0", 0, true},
	}
	for _, tc := range cases {
		got, err := parseHexOrDecimal(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("parseHexOrDecimal(%q) = (%d,%v) want (%d,ok=%v)", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestParseDrops(t *testing.T) {
	if got, err := parseDrops("12345"); err != nil || got != 12345 {
		t.Errorf("parseDrops(12345) = (%d,%v)", got, err)
	}
	if _, err := parseDrops("not-a-number"); err == nil {
		t.Error("expected error for non-numeric drops")
	}
}

func TestHexToHash32(t *testing.T) {
	good := "00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF"
	h, err := hexToHash32(good)
	if err != nil {
		t.Fatalf("hexToHash32: %v", err)
	}
	if h[0] != 0x00 || h[1] != 0x11 || h[31] != 0xFF {
		t.Errorf("unexpected hash bytes: %x", h)
	}
	if _, err := hexToHash32("00"); err == nil {
		t.Error("expected length error for short hex")
	}
	if _, err := hexToHash32("zz"); err == nil {
		t.Error("expected decode error for non-hex")
	}
}

func TestStatusEmoji(t *testing.T) {
	if statusEmoji(true) != "[OK]" {
		t.Error("statusEmoji(true)")
	}
	if statusEmoji(false) != "[MISMATCH]" {
		t.Error("statusEmoji(false)")
	}
}

func TestDecodeEntryData(t *testing.T) {
	blob, err := state.SerializeFeeSettings(&state.FeeSettings{XRPFeesMode: true, BaseFeeDrops: 10, ReserveBaseDrops: 1, ReserveIncrementDrops: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeEntryData(hex.EncodeToString(blob)); got == nil || got["LedgerEntryType"] != "FeeSettings" {
		t.Errorf("decodeEntryData = %v", got)
	}
	if got := decodeEntryData("zzzz"); got != nil {
		t.Errorf("invalid hex should decode to nil, got %v", got)
	}
}

func TestBuildRulesFromAmendments(t *testing.T) {
	// Empty list → no amendments enabled.
	empty := buildRulesFromAmendments(nil)
	if empty.Enabled(amendment.FeatureID("Flow")) {
		t.Error("empty rules should enable nothing")
	}

	// By name.
	flowID := amendment.FeatureID("Flow")
	byName := buildRulesFromAmendments([]string{"Flow"})
	if !byName.Enabled(flowID) {
		t.Error("Flow should be enabled by name")
	}

	// By 64-char hex id, plus an unknown name that must be ignored without error.
	idHex := hex.EncodeToString(flowID[:])
	byID := buildRulesFromAmendments([]string{idHex, "NotARealAmendmentName"})
	if !byID.Enabled(flowID) {
		t.Error("Flow should be enabled by hex id")
	}
}

func TestWriteResultJSON(t *testing.T) {
	out := filepath.Join(t.TempDir(), "result.json")
	res := &ReplayResult{
		Success:         true,
		LedgerHash:      [32]byte{0xDE, 0xAD},
		AccountHash:     [32]byte{0xBE, 0xEF},
		TransactionHash: [32]byte{0xCA, 0xFE},
		TotalCoins:      99,
		PreStateCount:   3,
		PostStateCount:  4,
		Duration:        5 * time.Millisecond,
		Errors:          []string{},
		TxResults:       []TxApplyInfo{{Index: 0, Hash: "abc"}},
	}
	if err := writeResultJSON(out, res); err != nil {
		t.Fatalf("writeResultJSON: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if parsed["success"] != true {
		t.Errorf("success = %v", parsed["success"])
	}
	if parsed["transaction_count"].(float64) != 1 {
		t.Errorf("transaction_count = %v", parsed["transaction_count"])
	}
	if parsed["ledger_hash"].(string)[:4] != "dead" {
		t.Errorf("ledger_hash = %v", parsed["ledger_hash"])
	}
}

func TestExtractFeesFromState(t *testing.T) {
	// No FeeSettings entry → defaults.
	def := extractFeesFromState(nil)
	if def.Base != 10 || def.Reserve != 10_000_000 || def.Increment != 2_000_000 {
		t.Errorf("default fees = %+v", def)
	}

	// Modern XRPFees entry stored at the fees keylet → read back exactly.
	blob, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           true,
		BaseFeeDrops:          15,
		ReserveBaseDrops:      5_000_000,
		ReserveIncrementDrops: 1_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := []StateEntry{{Index: feeSettingsIndexHex, Data: hex.EncodeToString(blob)}}
	fees := extractFeesFromState(entries)
	if fees.Base != 15 || fees.Reserve != 5_000_000 || fees.Increment != 1_000_000 {
		t.Errorf("modern fees = %+v", fees)
	}
}

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "obj.json")
	if err := os.WriteFile(path, []byte(`{"ledger_index":42,"account_hash":"ABCD"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var sf StateFixture
	if err := loadJSON(path, &sf); err != nil {
		t.Fatalf("loadJSON: %v", err)
	}
	if sf.LedgerIndex != 42 || sf.AccountHash != "ABCD" {
		t.Errorf("loaded fixture = %+v", sf)
	}
	if err := loadJSON(filepath.Join(dir, "missing.json"), &sf); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadFixtures(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"state.json":    `{"ledger_index":100,"account_hash":"AA","entries":[]}`,
		"env.json":      `{"ledger_index":100,"parent_hash":"BB","total_coins":"100"}`,
		"txs.json":      `{"transactions":[]}`,
		"expected.json": `{"ledger_index":100,"ledger_hash":"CC"}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stateFx, env, txs, expected, err := loadFixtures(dir)
	if err != nil {
		t.Fatalf("loadFixtures: %v", err)
	}
	if stateFx.LedgerIndex != 100 || env.ParentHash != "BB" || txs == nil || expected.LedgerHash != "CC" {
		t.Errorf("unexpected fixtures: state=%+v env=%+v expected=%+v", stateFx, env, expected)
	}

	// Removing a required file surfaces an error rather than partial fixtures.
	if err := os.Remove(filepath.Join(dir, "txs.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := loadFixtures(dir); err == nil {
		t.Error("expected error when a fixture file is missing")
	}
}
