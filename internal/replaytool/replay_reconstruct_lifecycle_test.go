package replaytool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/keylet"
)

type staticTransactionSource []statecompare.Transaction

func (s staticTransactionSource) Transactions(context.Context, *statecompare.LedgerSnapshot) ([]statecompare.Transaction, error) {
	return s, nil
}

func TestReconstructMainnetStateAppliesLedgerHashesLifecycle(t *testing.T) {
	tests := []struct {
		name              string
		parentSequence    uint32
		wantHistoricalSLE bool
	}{
		{name: "rolling", parentSequence: 10},
		{name: "rolling and historical", parentSequence: 256, wantHistoricalSLE: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := walkGenesisTo(t, test.parentSequence)
			preState, err := parent.StateMapSnapshot()
			if err != nil {
				t.Fatalf("StateMapSnapshot: %v", err)
			}
			targetSequence := test.parentSequence + 1
			parentHash := parent.Hash()

			expected, err := preState.SnapshotMutable()
			if err != nil {
				t.Fatalf("SnapshotMutable: %v", err)
			}
			if err := skiplist.UpdateOnMap(expected, targetSequence, parentHash); err != nil {
				t.Fatalf("UpdateOnMap: %v", err)
			}
			expectedRoot, err := expected.Hash()
			if err != nil {
				t.Fatalf("expected Hash: %v", err)
			}

			snapshot := &statecompare.LedgerSnapshot{
				LedgerIndex: targetSequence,
				ParentHash:  parentHash,
				AccountHash: expectedRoot,
			}
			corrected, verified, err := reconstructMainnetState(
				context.Background(),
				staticTransactionSource(nil),
				preState,
				snapshot,
				reconstructionTestRules(),
			)
			if err != nil {
				t.Fatalf("reconstructMainnetState: %v", err)
			}
			if !verified {
				t.Fatal("reconstruction_verified = false, want true")
			}

			_, rollingHashes, rollingLast, err := skiplist.ReadLedgerHashesSLE(corrected, keylet.LedgerHashes().Key)
			if err != nil {
				t.Fatalf("read rolling LedgerHashes: %v", err)
			}
			if rollingLast != test.parentSequence {
				t.Fatalf("rolling LastLedgerSequence = %d, want %d", rollingLast, test.parentSequence)
			}
			if want := min(int(test.parentSequence), 256); len(rollingHashes) != want {
				t.Fatalf("rolling Hashes length = %d, want %d", len(rollingHashes), want)
			}
			if got := rollingHashes[len(rollingHashes)-1]; got != parentHash {
				t.Fatalf("rolling final hash = %x, want %x", got, parentHash)
			}

			historical, historicalHashes, historicalLast, err := skiplist.ReadLedgerHashesSLE(
				corrected,
				keylet.LedgerHashesForSeq(test.parentSequence).Key,
			)
			if err != nil {
				t.Fatalf("read historical LedgerHashes: %v", err)
			}
			if !test.wantHistoricalSLE {
				if historical != nil {
					t.Fatal("unexpected historical LedgerHashes SLE")
				}
				return
			}
			if historical == nil {
				t.Fatal("historical LedgerHashes SLE is missing")
			}
			if historicalLast != test.parentSequence {
				t.Fatalf("historical LastLedgerSequence = %d, want %d", historicalLast, test.parentSequence)
			}
			if len(historicalHashes) != 1 || historicalHashes[0] != parentHash {
				t.Fatalf("historical Hashes = %x, want [%x]", historicalHashes, parentHash)
			}
		})
	}
}

func TestRecordDivergenceResetContinuesFromReconstructedLifecycleState(t *testing.T) {
	parent := walkGenesisTo(t, 10)
	preState, err := parent.StateMapSnapshot()
	if err != nil {
		t.Fatalf("StateMapSnapshot: %v", err)
	}
	targetSequence := parent.Sequence() + 1
	parentHash := parent.Hash()

	expected, err := preState.SnapshotMutable()
	if err != nil {
		t.Fatalf("SnapshotMutable: %v", err)
	}
	if err := skiplist.UpdateOnMap(expected, targetSequence, parentHash); err != nil {
		t.Fatalf("UpdateOnMap: %v", err)
	}
	expectedRoot, err := expected.Hash()
	if err != nil {
		t.Fatalf("expected Hash: %v", err)
	}

	divergent, err := expected.SnapshotMutable()
	if err != nil {
		t.Fatalf("divergent SnapshotMutable: %v", err)
	}
	if err := divergent.Put([32]byte{0xf0}, make([]byte, 12)); err != nil {
		t.Fatalf("inject divergence: %v", err)
	}
	divergentRoot, err := divergent.Hash()
	if err != nil {
		t.Fatalf("divergent Hash: %v", err)
	}

	rules := reconstructionTestRules()
	snapshot := &statecompare.LedgerSnapshot{
		LedgerIndex: targetSequence,
		ParentHash:  parentHash,
		AccountHash: expectedRoot,
	}
	result := &blockResult{
		AccountHash:         divergentRoot,
		ExpectedAccountHash: expectedRoot,
		PostSnapshot:        snapshot,
		Rules:               rules,
	}
	findingsPath := filepath.Join(t.TempDir(), "findings.jsonl")
	findings, err := newFindingsWriter(findingsPath)
	if err != nil {
		t.Fatalf("newFindingsWriter: %v", err)
	}

	resumed, err := recordDivergenceAndReset(
		context.Background(),
		staticTransactionSource(nil),
		findings,
		"test",
		targetSequence,
		parentHash,
		result,
		preState,
		divergent,
	)
	if err != nil {
		_ = findings.Close()
		t.Fatalf("recordDivergenceAndReset: %v", err)
	}
	if resumed == nil {
		_ = findings.Close()
		t.Fatal("recordDivergenceAndReset returned nil state")
	}
	if err := findings.Close(); err != nil {
		t.Fatalf("close findings: %v", err)
	}

	data, err := os.ReadFile(findingsPath)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	var gotFinding finding
	if err := json.Unmarshal(data, &gotFinding); err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	if !gotFinding.ReconstructionVerified {
		t.Fatal("reconstruction_verified = false, want true")
	}
	if gotFinding.DivergingObjects == nil || len(*gotFinding.DivergingObjects) != 1 {
		t.Fatalf("diverging_objects = %+v, want one object", gotFinding.DivergingObjects)
	}

	resumedRoot, err := resumed.Hash()
	if err != nil {
		t.Fatalf("resumed Hash: %v", err)
	}
	if resumedRoot != expectedRoot {
		t.Fatalf("resumed root = %x, want %x", resumedRoot, expectedRoot)
	}

	nextSequence := targetSequence + 1
	nextParentHash := [32]byte{0x42}
	expectedNext, err := resumed.SnapshotMutable()
	if err != nil {
		t.Fatalf("next SnapshotMutable: %v", err)
	}
	if err := skiplist.UpdateOnMap(expectedNext, nextSequence, nextParentHash); err != nil {
		t.Fatalf("next UpdateOnMap: %v", err)
	}
	expectedNextRoot, err := expectedNext.Hash()
	if err != nil {
		t.Fatalf("expected next Hash: %v", err)
	}
	fees, err := feesFromStateMap(resumed)
	if err != nil {
		t.Fatalf("feesFromStateMap: %v", err)
	}
	parentCloseTime := parent.CloseTime().Add(10 * time.Second)
	executed, err := executeBlock(context.Background(), blockExecution{
		StateMap:            resumed,
		LedgerIndex:         nextSequence,
		ParentHash:          nextParentHash,
		ParentCloseTime:     parentCloseTime,
		CloseTime:           parentCloseTime.Add(10 * time.Second),
		CloseTimeResolution: uint8(parent.CloseTimeResolution()),
		TotalCoins:          parent.TotalDrops(),
		Fees:                fees,
		Rules:               rules,
	})
	if err != nil {
		t.Fatalf("executeBlock after reset: %v", err)
	}
	if executed.AccountHash != expectedNextRoot {
		t.Fatalf("continued account root = %x, want %x", executed.AccountHash, expectedNextRoot)
	}
	_, _, rollingLast, err := skiplist.ReadLedgerHashesSLE(executed.StateMap, keylet.LedgerHashes().Key)
	if err != nil {
		t.Fatalf("read continued rolling LedgerHashes: %v", err)
	}
	if rollingLast != targetSequence {
		t.Fatalf("continued LastLedgerSequence = %d, want %d", rollingLast, targetSequence)
	}
}

var _ ledgerTransactionSource = staticTransactionSource(nil)
