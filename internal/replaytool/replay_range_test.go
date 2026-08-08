package replaytool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestValidateReplaySnapshotLink(t *testing.T) {
	parentHash := [32]byte{1}
	pre := &statecompare.LedgerSnapshot{LedgerIndex: 41, LedgerHash: parentHash}
	post := &statecompare.LedgerSnapshot{LedgerIndex: 42, ParentHash: parentHash}
	if err := validateReplaySnapshotLink(pre, post, 42); err != nil {
		t.Fatalf("valid snapshot link: %v", err)
	}

	for name, mutate := range map[string]func(*statecompare.LedgerSnapshot, *statecompare.LedgerSnapshot){
		"parent sequence": func(pre, _ *statecompare.LedgerSnapshot) { pre.LedgerIndex = 40 },
		"target sequence": func(_, post *statecompare.LedgerSnapshot) { post.LedgerIndex = 43 },
		"parent hash":     func(_, post *statecompare.LedgerSnapshot) { post.ParentHash = [32]byte{2} },
	} {
		t.Run(name, func(t *testing.T) {
			badPre := *pre
			badPost := *post
			mutate(&badPre, &badPost)
			if err := validateReplaySnapshotLink(&badPre, &badPost, 42); err == nil {
				t.Fatal("expected malformed snapshot link to fail")
			}
		})
	}
}

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
	// A state with no Amendments entry still enables retired rules permanently.
	sm := buildReplayStateMap(t, map[[32]byte][]byte{
		{0x01}: entry(0xaa),
	})
	rules, err := loadRulesFromState(sm)
	if err != nil {
		t.Fatalf("loadRulesFromState: %v", err)
	}
	if rules.Enabled(amendment.FeatureFixCleanup3_2_0) {
		t.Fatal("empty state enabled a non-permanent amendment")
	}
	for _, id := range amendment.PermanentlyEnabledIDs() {
		if !rules.Enabled(id) {
			t.Fatalf("permanent amendment %x is disabled", id)
		}
	}
}

func TestLoadRulesFromState_Populated(t *testing.T) {
	flowID := amendment.FeatureID("Flow")
	checksID := amendment.FeatureID("Checks")
	// AMM is supported but not retired and absent from the SLE, so it stays
	// disabled (Flow/Checks are retired and would be enabled regardless).
	disabledID := amendment.FeatureID("AMM")

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
		t.Error("expected AMM to be disabled")
	}
	if !rules.Enabled(amendment.FeatureFixPayChanRecipientOwnerDir) {
		t.Error("expected the current retired-amendment baseline to enable fixPayChanRecipientOwnerDir")
	}
}

func TestReplayPreFixPayChanRecipientOwnerDir(t *testing.T) {
	capturedParentAmendments, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{
		Amendments: [][32]byte{amendment.FeatureFlow, amendment.FeatureFixPayChanRecipientOwnerDir},
	})
	if err != nil {
		t.Fatalf("serializing captured parent amendments SLE: %v", err)
	}
	parentState := buildReplayStateMap(t, map[[32]byte][]byte{
		keylet.Amendments().Key: capturedParentAmendments,
	})
	rules, err := loadRulesFromState(parentState)
	if err != nil {
		t.Fatalf("loading captured parent amendments SLE: %v", err)
	}
	if !rules.Enabled(amendment.FeatureFixPayChanRecipientOwnerDir) {
		t.Fatal("captured parent rules must enable fixPayChanRecipientOwnerDir")
	}
	if got := replayPreFixPayChanRecipientOwnerDir(42, true, 0); !got {
		t.Fatal("legacy flag must force pre-fix semantics despite the parent Amendments entry")
	}

	for _, tc := range []struct {
		name             string
		targetLedger     uint32
		legacyGate       bool
		firstFixedLedger uint32
		wantPreFix       bool
	}{
		{name: "no-flag default", targetLedger: 99, firstFixedLedger: 100},
		{name: "boundary minus one", targetLedger: 99, legacyGate: true, firstFixedLedger: 100, wantPreFix: true},
		{name: "boundary", targetLedger: 100, legacyGate: true, firstFixedLedger: 100},
		{name: "boundary plus one", targetLedger: 101, legacyGate: true, firstFixedLedger: 100},
		{name: "legacy entire range", targetLedger: 101, legacyGate: true, wantPreFix: true},
		{name: "transition before range", targetLedger: 42, legacyGate: true, firstFixedLedger: 1},
		{name: "transition after range", targetLedger: 42, legacyGate: true, firstFixedLedger: 100, wantPreFix: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := replayPreFixPayChanRecipientOwnerDir(tc.targetLedger, tc.legacyGate, tc.firstFixedLedger)
			if got != tc.wantPreFix {
				t.Fatalf("replayPreFixPayChanRecipientOwnerDir = %t, want %t", got, tc.wantPreFix)
			}
		})
	}
}

func TestReplayRangeLegacyPayChanOwnerDirFlag(t *testing.T) {
	cmd := newReplayRangeCmd()
	for _, tc := range []struct {
		name       string
		defaultVal string
	}{
		{name: "legacy-paychan-owner-dir-gate", defaultVal: "false"},
		{name: "paychan-owner-dir-first-fixed-ledger", defaultVal: "0"},
	} {
		flag := cmd.Flags().Lookup(tc.name)
		if flag == nil {
			t.Fatalf("%s flag is not registered", tc.name)
		}
		if flag.DefValue != tc.defaultVal {
			t.Fatalf("%s default = %q, want %s", tc.name, flag.DefValue, tc.defaultVal)
		}
	}
}

func TestReplayRangePayChanFirstFixedRequiresLegacyFlag(t *testing.T) {
	runner := &replayRangeRunner{
		from:                 41,
		to:                   43,
		payChanDirFirstFixed: 42,
	}
	err := runner.validateFlags()
	if err == nil || err.Error() != "--paychan-owner-dir-first-fixed-ledger requires --legacy-paychan-owner-dir-gate" {
		t.Fatalf("validateFlags error = %v", err)
	}

	for _, firstFixed := range []uint32{1, 100} {
		runner := &replayRangeRunner{
			from:                 41,
			to:                   43,
			legacyPayChanDirGate: true,
			payChanDirFirstFixed: firstFixed,
		}
		if err := runner.validateFlags(); err != nil {
			t.Fatalf("first fixed ledger %d outside selected range: %v", firstFixed, err)
		}
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
	if err := writeCheckpoint(context.Background(), dir, seq, sm); err != nil {
		t.Fatalf("writeCheckpoint: %v", err)
	}

	loaded, gotSeq, err := loadCheckpoint(context.Background(), filepath.Join(dir, "checkpoint_99230000.dat"))
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
	if _, _, err := loadCheckpoint(context.Background(), path); err == nil {
		t.Fatal("expected error for bad magic, got nil")
	}
}
