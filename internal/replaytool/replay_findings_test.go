package replaytool

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFindingsWriter_JSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.jsonl")
	w, err := newFindingsWriter(path)
	if err != nil {
		t.Fatalf("newFindingsWriter: %v", err)
	}

	result := &BlockResult{
		TxCount:             2,
		ExpectedLedgerHash:  [32]byte{0xDE, 0xAD},
		ExpectedAccountHash: [32]byte{0xBE, 0xEF},
		AccountHash:         [32]byte{0x12},
		TotalCoins:          99,
		ExpectedTotalCoins:  100,
		TxResults: []TxApplyInfo{
			{Index: 0, Hash: "abc", TxType: "Payment", Result: "tesSUCCESS"},
		},
	}
	diverging := []divergingObject{{Index: "00ff", GoXRPL: "aa", Mainnet: "bb"}}

	var parent [32]byte
	parent[0] = 0x77
	f1 := buildFinding("deadbeef0001", 100, parent, result, true, diverging)
	f2 := buildFinding("deadbeef0001", 200, parent, result, false, nil)
	if err := w.Write(f1); err != nil {
		t.Fatalf("write f1: %v", err)
	}
	if err := w.Write(f2); err != nil {
		t.Fatalf("write f2: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	var lines int
	scanner := bufio.NewScanner(file)
	var first Finding
	for scanner.Scan() {
		var fd Finding
		if err := json.Unmarshal(scanner.Bytes(), &fd); err != nil {
			t.Fatalf("line %d not valid JSON: %v", lines, err)
		}
		if lines == 0 {
			first = fd
		}
		lines++
	}
	if lines != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d", lines)
	}
	if first.Schema != findingSchema {
		t.Fatalf("schema = %q, want %q", first.Schema, findingSchema)
	}
	if first.LedgerIndex != 100 || !first.ReconstructionVerified {
		t.Fatalf("unexpected first finding: %+v", first)
	}
	if len(first.DivergingObjects) != 1 || first.DivergingObjects[0].Index != "00ff" {
		t.Fatalf("diverging objects not recorded: %+v", first.DivergingObjects)
	}
	// account_expected is hex of [32]byte{0xBE,0xEF}: "beef" then zeros.
	if len(first.Hashes.AccountExpected) != 64 || first.Hashes.AccountExpected[:4] != "beef" {
		t.Fatalf("account_expected hex wrong: %s", first.Hashes.AccountExpected)
	}
}

func TestGoxrplCommit_Override(t *testing.T) {
	if got := goxrplCommit("my-tag"); got != "my-tag" {
		t.Fatalf("override ignored: %q", got)
	}
	// Without an override the value is build-info dependent; it must at least
	// be non-empty so findings are always tagged.
	if got := goxrplCommit(""); got == "" {
		t.Fatal("commit tag is empty")
	}
}
