package replaytool

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/protocol"
)

// feeSettingsIndexHex is keylet::fees() — the singleton FeeSettings key the
// replay fee extractors look the entry up by.
const feeSettingsIndexHex = "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A651"

func TestParseDrops(t *testing.T) {
	if got, err := parseDrops("12345"); err != nil || got != 12345 {
		t.Errorf("parseDrops(12345) = (%d,%v)", got, err)
	}
	if _, err := parseDrops("not-a-number"); err == nil {
		t.Error("expected error for non-numeric drops")
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
	// Empty declarations still include rippled's permanently enabled rules.
	empty, err := buildRulesFromAmendments(nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Enabled(amendment.FeatureFixCleanup3_2_0) {
		t.Error("empty declarations enabled a non-permanent amendment")
	}
	for _, id := range amendment.PermanentlyEnabledIDs() {
		if !empty.Enabled(id) {
			t.Fatalf("permanent amendment %x is disabled", id)
		}
	}

	// By name.
	flowID := amendment.FeatureID("Flow")
	byName, err := buildRulesFromAmendments([]string{"Flow"})
	if err != nil {
		t.Fatal(err)
	}
	if !byName.Enabled(flowID) {
		t.Error("Flow should be enabled by name")
	}

	// By 64-char hex id.
	idHex := hex.EncodeToString(flowID[:])
	byID, err := buildRulesFromAmendments([]string{idHex})
	if err != nil {
		t.Fatal(err)
	}
	if !byID.Enabled(flowID) {
		t.Error("Flow should be enabled by hex id")
	}
	if _, err := buildRulesFromAmendments([]string{"NotARealAmendmentName"}); err == nil {
		t.Fatal("unknown amendment name should fail")
	}
	if _, err := buildRulesFromAmendments([]string{"Flow", idHex}); err == nil {
		t.Fatal("duplicate amendment should fail")
	}
	nonPermanent, err := buildRulesFromAmendments([]string{"fixCleanup3_2_0"})
	if err != nil {
		t.Fatal(err)
	}
	if sameRuleSet(empty, nonPermanent) {
		t.Fatal("different amendment declarations compared equal")
	}
}

func TestWriteResultJSON(t *testing.T) {
	out := filepath.Join(t.TempDir(), "result.json")
	res := &replayResult{
		Success:         true,
		LedgerHash:      [32]byte{0xDE, 0xAD},
		AccountHash:     [32]byte{0xBE, 0xEF},
		TransactionHash: [32]byte{0xCA, 0xFE},
		TotalCoins:      99,
		PreStateCount:   3,
		PostStateCount:  4,
		Duration:        5 * time.Millisecond,
		Errors:          []string{},
		TxResults:       []txApplyInfo{{Index: 0, Hash: "abc"}},
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
	if transactions, ok := parsed["transactions"].([]any); !ok || len(transactions) != 1 {
		t.Fatalf("transactions = %#v", parsed["transactions"])
	}
	if parsed["ledger_hash"].(string)[:4] != "dead" {
		t.Errorf("ledger_hash = %v", parsed["ledger_hash"])
	}
}

func TestWriteAtomicJSONPreservesPriorArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	prior := []byte(`{"valid":true}`)
	if err := os.WriteFile(path, prior, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomicJSON(path, make(chan int)); err == nil {
		t.Fatal("unsupported JSON value should fail")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(prior) {
		t.Fatalf("prior artifact changed: %q", got)
	}
}

func TestLoadValidatedFixtureRejectsCanonicalDuplicateStateKey(t *testing.T) {
	dir := t.TempDir()
	zeroHash := strings.Repeat("0", 64)
	index := feeSettingsIndexHex
	entryBlob, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode: true, BaseFeeDrops: 10, ReserveBaseDrops: 10_000_000, ReserveIncrementDrops: 2_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	entryData := hex.EncodeToString(entryBlob)
	files := map[string]string{
		"state.json": `{"ledger_index":1,"account_hash":"` + zeroHash + `","entries":[` +
			`{"index":"` + index + `","data":"` + entryData + `"},` +
			`{"index":"` + strings.ToLower(index) + `","data":"` + entryData + `"}]}`,
		"env.json": `{"ledger_index":2,"parent_hash":"` + zeroHash + `","parent_close_time":0,"close_time":0,` +
			`"close_time_resolution":10,"close_flags":0,"total_coins":"0",` +
			`"fees":{"base_fee":10,"reserve_base":10000000,"reserve_increment":2000000},"amendments":[]}`,
		"txs.json": `{"transactions":[]}`,
		"expected.json": `{"ledger_index":2,"ledger_hash":"` + zeroHash + `","account_hash":"` + zeroHash +
			`","transaction_hash":"` + zeroHash + `","total_coins":"0","transactions":[]}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadValidatedFixture(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "duplicates index") {
		t.Fatalf("loadValidatedFixture error = %v", err)
	}
}

func TestLoadStrictJSONRejectsUnknownAndTrailingValues(t *testing.T) {
	for name, data := range map[string]string{
		"unknown":   `{"ledger_index":1,"account_hash":"AA","entries":[],"extra":true}`,
		"trailing":  `{"ledger_index":1,"account_hash":"AA","entries":[]} {}`,
		"duplicate": `{"ledger_index":1,"ledger_index":2,"account_hash":"AA","entries":[]}`,
		"null":      `{"ledger_index":1,"account_hash":null,"entries":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := loadStrictJSON(path, &stateFixture{}, "ledger_index", "account_hash", "entries"); err == nil {
				t.Fatal("invalid JSON was accepted")
			}
		})
	}
}

func TestReplayCloseTimeBounds(t *testing.T) {
	for _, seconds := range []int64{0, math.MaxUint32} {
		got, err := replayCloseTime(seconds)
		if err != nil {
			t.Fatalf("replayCloseTime(%d): %v", seconds, err)
		}
		if want := protocol.FromRippleTime(uint32(seconds)); !got.Equal(want) {
			t.Errorf("replayCloseTime(%d) = %v, want %v", seconds, got, want)
		}
	}

	for _, seconds := range []int64{-1, math.MaxUint32 + 1} {
		if _, err := replayCloseTime(seconds); err == nil {
			t.Errorf("replayCloseTime(%d) succeeded", seconds)
		}
	}
}
