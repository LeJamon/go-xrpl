package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	ledgerFileTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ledgerFileTestIndex   = "1111111111111111111111111111111111111111111111111111111111111111"
)

func TestLoadLedgerJSONWrappersAndDefaults(t *testing.T) {
	defaultCloseTime := time.Date(2026, time.July, 27, 14, 15, 16, 0, time.UTC)
	entry := ledgerFileTestAccountRoot(ledgerFileTestIndex)
	tests := []struct {
		name           string
		document       any
		wantSequence   uint32
		wantCloseTime  time.Time
		wantResolution uint32
		wantDrops      uint64
		wantFlags      uint8
	}{
		{
			name:           "bare account state",
			document:       []any{entry},
			wantSequence:   1,
			wantCloseTime:  defaultCloseTime,
			wantResolution: 30,
		},
		{
			name: "ledger wrapper and API v1 sequence",
			document: map[string]any{
				"ledger": map[string]any{
					"ledger_index":          "7",
					"close_time":            800000000,
					"close_time_resolution": 20,
					"close_time_estimated":  true,
					"total_coins":           "999999999",
					"ledger_hash":           strings.Repeat("A", 64),
					"account_hash":          strings.Repeat("B", 64),
					"transaction_hash":      strings.Repeat("C", 64),
					"parent_hash":           strings.Repeat("D", 64),
					"parent_close_time":     799999990,
					"close_flags":           0,
					"accountState":          []any{entry},
				},
			},
			wantSequence:   7,
			wantCloseTime:  protocol.FromRippleTime(800000000),
			wantResolution: 20,
			wantDrops:      999999999,
			wantFlags:      header.LCFNoConsensusTime,
		},
		{
			name: "result and ledger wrappers with API v2 sequence",
			document: map[string]any{
				"result": map[string]any{
					"ledger": map[string]any{
						"ledger_index": 8,
						"accountState": []any{entry},
					},
				},
			},
			wantSequence:   8,
			wantCloseTime:  defaultCloseTime,
			wantResolution: 30,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := ledgerFileTestService(t)
			loaded, err := svc.loadLedgerJSON(
				context.Background(),
				strings.NewReader(ledgerFileMarshal(t, test.document)),
				defaultCloseTime,
			)
			if err != nil {
				t.Fatalf("loadLedgerJSON() error = %v", err)
			}

			gotHeader := loaded.Header()
			if gotHeader.LedgerIndex != test.wantSequence {
				t.Errorf("LedgerIndex = %d, want %d", gotHeader.LedgerIndex, test.wantSequence)
			}
			if !gotHeader.CloseTime.Equal(test.wantCloseTime) {
				t.Errorf("CloseTime = %v, want %v", gotHeader.CloseTime, test.wantCloseTime)
			}
			if uint32(gotHeader.CloseTimeResolution) != test.wantResolution {
				t.Errorf("CloseTimeResolution = %d, want %d", gotHeader.CloseTimeResolution, test.wantResolution)
			}
			if gotHeader.Drops != test.wantDrops {
				t.Errorf("Drops = %d, want %d", gotHeader.Drops, test.wantDrops)
			}
			if gotHeader.CloseFlags != test.wantFlags {
				t.Errorf("CloseFlags = %d, want %d", gotHeader.CloseFlags, test.wantFlags)
			}
			if gotHeader.ParentHash != ([32]byte{}) {
				t.Errorf("ParentHash = %x, want zero", gotHeader.ParentHash)
			}
			if !gotHeader.Accepted || gotHeader.Validated {
				t.Errorf("accepted/validated = %v/%v, want true/false", gotHeader.Accepted, gotHeader.Validated)
			}
			if !loaded.IsClosed() {
				t.Error("loaded ledger is not closed")
			}

			wantStateHash := ledgerFileTestStateHash(t, entry)
			gotStateHash, err := loaded.StateMapHash()
			if err != nil {
				t.Fatalf("StateMapHash() error = %v", err)
			}
			if gotStateHash != wantStateHash {
				t.Errorf("AccountHash = %x, want %x", gotStateHash, wantStateHash)
			}
			if gotHeader.AccountHash != wantStateHash {
				t.Errorf("header AccountHash = %x, want %x", gotHeader.AccountHash, wantStateHash)
			}

			gotTxHash, err := loaded.TxMapHash()
			if err != nil {
				t.Fatalf("TxMapHash() error = %v", err)
			}
			if gotHeader.TxHash != gotTxHash {
				t.Errorf("header TxHash = %x, map hash %x", gotHeader.TxHash, gotTxHash)
			}
			if gotHeader.Hash != header.CalculateHash(gotHeader) {
				t.Errorf("ledger hash = %x, want recomputed %x", gotHeader.Hash, header.CalculateHash(gotHeader))
			}
		})
	}
}

func TestLoadLedgerJSONRebuildsExpandedLedgerState(t *testing.T) {
	source, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	accountState := make([]any, 0, source.StateMap.Size())
	if err := source.StateMap.ForEach(func(item *shamap.Item) bool {
		entry, decodeErr := binarycodec.Decode(hex.EncodeToString(item.Data()))
		if decodeErr != nil {
			t.Errorf("decode source entry %x: %v", item.Key(), decodeErr)
			return false
		}
		entry["index"] = fmt.Sprintf("%X", item.Key())
		accountState = append(accountState, entry)
		return true
	}); err != nil {
		t.Fatal(err)
	}

	document := map[string]any{
		"result": map[string]any{
			"ledger": map[string]any{
				"ledger_index":          source.Header.LedgerIndex,
				"close_time":            protocol.ToRippleTime(source.Header.CloseTime),
				"close_time_resolution": source.Header.CloseTimeResolution,
				"total_coins":           fmt.Sprintf("%d", source.Header.Drops),
				"accountState":          accountState,
			},
		},
	}

	loaded, err := ledgerFileTestService(t).loadLedgerJSON(
		context.Background(),
		strings.NewReader(ledgerFileMarshal(t, document)),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("loadLedgerJSON() error = %v", err)
	}
	gotHash, err := loaded.StateMapHash()
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := source.StateMap.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHash {
		t.Errorf("state map hash = %x, want %x", gotHash, wantHash)
	}
	loadedSize := 0
	if err := loaded.ForEach(func(_ [32]byte, _ []byte) bool {
		loadedSize++
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if loadedSize != source.StateMap.Size() {
		t.Errorf("state map size = %d, want %d", loadedSize, source.StateMap.Size())
	}
}

func TestLoadLedgerJSONRejectsMalformedState(t *testing.T) {
	validEntry := ledgerFileTestAccountRoot(ledgerFileTestIndex)
	duplicateEntry := ledgerFileTestAccountRoot(ledgerFileTestIndex)
	zeroIndexEntry := ledgerFileTestAccountRoot(strings.Repeat("0", 64))
	missingIndexEntry := ledgerFileTestAccountRoot(ledgerFileTestIndex)
	delete(missingIndexEntry, "index")

	tests := []struct {
		name      string
		document  string
		wantError string
	}{
		{name: "truncated JSON", document: `{"ledger":`, wantError: "decode ledger JSON"},
		{name: "multiple JSON values", document: `[] []`, wantError: "multiple JSON values"},
		{name: "object without account state", document: `{}`, wantError: "state nodes must be an array"},
		{name: "state is object", document: `{"accountState":{}}`, wantError: "state nodes must be an array"},
		{name: "empty state", document: `[]`, wantError: "state map is empty"},
		{name: "null state node", document: `[null]`, wantError: "must be an object"},
		{
			name:      "missing index",
			document:  ledgerFileMarshal(t, []any{missingIndexEntry}),
			wantError: "missing index",
		},
		{
			name:      "short index",
			document:  ledgerFileMarshal(t, []any{ledgerFileTestAccountRoot("01")}),
			wantError: "exactly 64",
		},
		{
			name:      "zero index",
			document:  ledgerFileMarshal(t, []any{zeroIndexEntry}),
			wantError: "must not be zero",
		},
		{
			name:      "duplicate index",
			document:  ledgerFileMarshal(t, []any{validEntry, duplicateEntry}),
			wantError: "duplicates index",
		},
		{
			name: "unknown ledger entry type",
			document: ledgerFileMarshal(t, []any{map[string]any{
				"index":           ledgerFileTestIndex,
				"LedgerEntryType": "Unknown",
			}}),
			wantError: "unknown LedgerEntryType",
		},
		{
			name: "missing required entry field",
			document: ledgerFileMarshal(t, []any{map[string]any{
				"index":             ledgerFileTestIndex,
				"LedgerEntryType":   "AccountRoot",
				"Account":           ledgerFileTestAccount,
				"Flags":             0,
				"OwnerCount":        0,
				"Sequence":          1,
				"PreviousTxnID":     strings.Repeat("0", 64),
				"PreviousTxnLgrSeq": 0,
			}}),
			wantError: "required field Balance is missing",
		},
	}

	svc := ledgerFileTestService(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.loadLedgerJSON(
				context.Background(),
				strings.NewReader(test.document),
				time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC),
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("loadLedgerJSON() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestLoadLedgerJSONRejectsInvalidNumbers(t *testing.T) {
	entryJSON := ledgerFileMarshal(t, []any{ledgerFileTestAccountRoot(ledgerFileTestIndex)})
	tests := []struct {
		name      string
		document  string
		wantError string
	}{
		{
			name:      "negative close time",
			document:  `{"close_time":-1,"accountState":` + entryJSON + `}`,
			wantError: "close_time must be non-negative",
		},
		{
			name:      "sequence overflow",
			document:  `{"ledger_index":4294967296,"accountState":` + entryJSON + `}`,
			wantError: "ledger_index is out of range",
		},
		{
			name:      "drops overflow",
			document:  `{"total_coins":"18446744073709551616","accountState":` + entryJSON + `}`,
			wantError: "invalid total_coins",
		},
	}

	svc := ledgerFileTestService(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.loadLedgerJSON(
				context.Background(),
				strings.NewReader(test.document),
				time.Now(),
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("loadLedgerJSON() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestLedgerFileBoolMatchesRippledJSONTruthiness(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{name: "null", value: nil},
		{name: "false", value: false},
		{name: "true", value: true, want: true},
		{name: "zero integer", value: int64(0)},
		{name: "nonzero integer", value: int64(-1), want: true},
		{name: "zero real", value: float64(0)},
		{name: "nonzero real", value: 0.5, want: true},
		{name: "empty string", value: ""},
		{name: "nonempty string", value: "false", want: true},
		{name: "empty array", value: []any{}},
		{name: "nonempty array", value: []any{nil}, want: true},
		{name: "empty object", value: map[string]any{}},
		{name: "nonempty object", value: map[string]any{"value": nil}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ledgerFileBool(test.value); got != test.want {
				t.Errorf("ledgerFileBool(%#v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestLedgerFileUint32MatchesRippledJSONConversions(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		want    uint32
		wantErr bool
	}{
		{name: "null", value: nil},
		{name: "false", value: false},
		{name: "true", value: true, want: 1},
		{name: "integer", value: int64(43), want: 43},
		{name: "fraction truncates", value: 43.9, want: 43},
		{name: "decimal string", value: "43", want: 43},
		{name: "negative", value: int64(-1), wantErr: true},
		{name: "overflow", value: int64(4294967296), wantErr: true},
		{name: "object", value: map[string]any{}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ledgerFileUint32("value", test.value)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ledgerFileUint32(%#v) succeeded", test.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("ledgerFileUint32(%#v) error = %v", test.value, err)
			}
			if got != test.want {
				t.Errorf("ledgerFileUint32(%#v) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestLoadLedgerFile(t *testing.T) {
	svc := ledgerFileTestService(t)
	path := filepath.Join(t.TempDir(), "ledger.json")
	document := ledgerFileMarshal(t, map[string]any{
		"ledger": map[string]any{
			"accountState": []any{ledgerFileTestAccountRoot(ledgerFileTestIndex)},
		},
	})
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := svc.loadLedgerFile(context.Background(), path, time.Now())
	if err != nil {
		t.Fatalf("loadLedgerFile() error = %v", err)
	}
	if loaded == nil {
		t.Fatal("loadLedgerFile() returned nil ledger")
	}

	_, err = svc.loadLedgerFile(context.Background(), path+".missing", time.Now())
	if err == nil || !strings.Contains(err.Error(), "open ledger file") {
		t.Fatalf("missing file error = %v, want open ledger file error", err)
	}
}

func ledgerFileTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func ledgerFileTestAccountRoot(index string) map[string]any {
	return map[string]any{
		"index":             index,
		"LedgerEntryType":   "AccountRoot",
		"Account":           ledgerFileTestAccount,
		"Balance":           "100000000",
		"Flags":             0,
		"OwnerCount":        0,
		"Sequence":          1,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": 0,
	}
}

func ledgerFileTestStateHash(t *testing.T, state map[string]any) [32]byte {
	t.Helper()
	index, err := ledgerFileIndex(state["index"])
	if err != nil {
		t.Fatal(err)
	}
	object := make(map[string]any, len(state)-1)
	for key, value := range state {
		if key != "index" {
			object[key] = value
		}
	}
	blob, err := binarycodec.EncodeBytes(object)
	if err != nil {
		t.Fatal(err)
	}
	stateMap := shamap.New(shamap.TypeState)
	if err := stateMap.Put(index, blob); err != nil {
		t.Fatal(err)
	}
	hash, err := stateMap.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func ledgerFileMarshal(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
