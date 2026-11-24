package replaytool

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestDefaultFees(t *testing.T) {
	f := drops.DefaultFees()
	if f.Base != 10 || f.Reserve != 10_000_000 || f.Increment != 2_000_000 {
		t.Errorf("defaultFees = %+v", f)
	}
}

func feeIndexKey(t *testing.T) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(feeSettingsIndexHex)
	if err != nil || len(b) != 32 {
		t.Fatalf("decoding fee index: %v", err)
	}
	var key [32]byte
	copy(key[:], b)
	return key
}

func TestExtractFeesFromSHAMap_Default(t *testing.T) {
	sm := shamap.New(shamap.TypeState)
	// No FeeSettings entry → defaults.
	f, err := feesFromStateMap(context.Background(), sm, drops.DefaultFees())
	if err != nil {
		t.Fatal(err)
	}
	if f != drops.DefaultFees() {
		t.Errorf("extractFeesFromSHAMap (empty) = %+v want defaults", f)
	}
}

func TestExtractFeesFromSHAMap_Modern(t *testing.T) {
	sm := shamap.New(shamap.TypeState)
	blob, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           true,
		BaseFeeDrops:          20,
		ReserveBaseDrops:      7_500_000,
		ReserveIncrementDrops: 1_500_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(feeIndexKey(t), blob); err != nil {
		t.Fatalf("seeding fee settings: %v", err)
	}
	f, err := feesFromStateMap(context.Background(), sm, drops.DefaultFees())
	if err != nil {
		t.Fatal(err)
	}
	if f.Base != 20 || f.Reserve != 7_500_000 || f.Increment != 1_500_000 {
		t.Errorf("extractFeesFromSHAMap (modern) = %+v", f)
	}
}

func TestExtractFeesFromSHAMapAppliesInheritedFields(t *testing.T) {
	sm := shamap.New(shamap.TypeState)
	blob, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "FeeSettings",
		"Flags":           uint32(0),
		"BaseFeeDrops":    "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(feeIndexKey(t), blob); err != nil {
		t.Fatalf("seeding partial fee settings: %v", err)
	}
	inherited := drops.Fees{Base: 31, Reserve: 42, Increment: 53}
	got, err := feesFromStateMap(context.Background(), sm, inherited)
	if err != nil {
		t.Fatal(err)
	}
	want := drops.Fees{Base: 0, Reserve: 42, Increment: 53}
	if got != want {
		t.Fatalf("feesFromStateMap(partial) = %+v, want %+v", got, want)
	}
}

func TestFindingsPath(t *testing.T) {
	// Explicit --findings-out wins verbatim.
	explicit := filepath.Join(t.TempDir(), "custom.jsonl")
	r := &replayRangeRunner{findingsOut: explicit}
	if got := r.findingsPath(); got != explicit {
		t.Errorf("explicit findings path = %q want %q", got, explicit)
	}

	// Otherwise it falls back to <dump-dir>/findings.jsonl, creating the dir.
	dumpDir := filepath.Join(t.TempDir(), "debug")
	r = &replayRangeRunner{dumpDir: dumpDir}
	want := filepath.Join(dumpDir, "findings.jsonl")
	if got := r.findingsPath(); got != want {
		t.Errorf("derived findings path = %q want %q", got, want)
	}
}
