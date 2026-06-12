package replaytool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// buildReplayStateMap creates a state SHAMap seeded with the given entries.
func buildReplayStateMap(t *testing.T, entries map[[32]byte][]byte) *shamap.SHAMap {
	t.Helper()
	sm := shamap.New(shamap.TypeState)
	for key, data := range entries {
		if err := sm.Put(key, data); err != nil {
			t.Fatalf("putting entry: %v", err)
		}
	}
	return sm
}

// entry returns a state-entry payload of at least the SHAMap minimum item
// size (12 bytes), filled with the given seed byte.
func entry(seed byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = seed
	}
	return b
}

func TestVerifyStateRoot(t *testing.T) {
	sm := buildReplayStateMap(t, map[[32]byte][]byte{
		{0x01}: entry(0xaa),
		{0x02}: entry(0xcc),
	})
	root, err := sm.Hash()
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	if err := verifyStateRoot(sm, root, 42); err != nil {
		t.Fatalf("expected matching root to pass, got: %v", err)
	}

	var wrong [32]byte
	wrong[0] = 0xff
	if err := verifyStateRoot(sm, wrong, 42); err == nil {
		t.Fatal("expected mismatched root to fail, got nil")
	}
}

func TestLoadRulesFromState_Empty(t *testing.T) {
	// A state with no Amendments entry yields EmptyRules().
	sm := buildReplayStateMap(t, map[[32]byte][]byte{
		{0x01}: entry(0xaa),
	})
	rules, err := loadRulesFromState(sm)
	if err != nil {
		t.Fatalf("loadRulesFromState: %v", err)
	}
	if rules.Enabled(amendment.FeatureID("Flow")) {
		t.Fatal("expected no amendments enabled for empty state")
	}
}

func TestLoadRulesFromState_Populated(t *testing.T) {
	flowID := amendment.FeatureID("Flow")
	checksID := amendment.FeatureID("Checks")
	disabledID := amendment.FeatureID("DepositAuth")

	data, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{
		Amendments: [][32]byte{flowID, checksID},
	})
	if err != nil {
		t.Fatalf("serializing amendments SLE: %v", err)
	}

	sm := buildReplayStateMap(t, map[[32]byte][]byte{
		keylet.Amendments().Key: data,
	})

	rules, err := loadRulesFromState(sm)
	if err != nil {
		t.Fatalf("loadRulesFromState: %v", err)
	}
	if !rules.Enabled(flowID) {
		t.Error("expected Flow to be enabled")
	}
	if !rules.Enabled(checksID) {
		t.Error("expected Checks to be enabled")
	}
	if rules.Enabled(disabledID) {
		t.Error("expected DepositAuth to be disabled")
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	const seq = uint32(99230000)
	entries := map[[32]byte][]byte{
		{0x01}:       entry(0xaa),
		{0x02, 0x03}: append(entry(0xde), 0x01, 0x02, 0x03),
		{0xff}:       entry(0x00),
	}
	sm := buildReplayStateMap(t, entries)
	wantRoot, err := sm.Hash()
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	dir := t.TempDir()
	if err := writeCheckpoint(dir, seq, sm); err != nil {
		t.Fatalf("writeCheckpoint: %v", err)
	}

	loaded, gotSeq, err := loadCheckpoint(filepath.Join(dir, "checkpoint_99230000.dat"))
	if err != nil {
		t.Fatalf("loadCheckpoint: %v", err)
	}
	if gotSeq != seq {
		t.Fatalf("seq mismatch: got %d want %d", gotSeq, seq)
	}

	gotRoot, err := loaded.Hash()
	if err != nil {
		t.Fatalf("hashing loaded map: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("root mismatch after round-trip: got %x want %x", gotRoot, wantRoot)
	}

	if loaded.Size() != len(entries) {
		t.Fatalf("entry count mismatch: got %d want %d", loaded.Size(), len(entries))
	}

	// checkpointPath must agree with the file writeCheckpoint produced.
	if got := checkpointPath(dir, seq); got != filepath.Join(dir, "checkpoint_99230000.dat") {
		t.Fatalf("unexpected checkpoint path: %s", got)
	}
}

func TestLoadCheckpoint_BadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint_1.dat")
	if err := os.WriteFile(path, []byte("NOTACKPTxxxxxxxx"), 0o644); err != nil {
		t.Fatalf("seeding bad file: %v", err)
	}
	if _, _, err := loadCheckpoint(path); err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
}
