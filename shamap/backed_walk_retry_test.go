package shamap

import (
	"errors"
	"testing"
	"unsafe"
)

func TestBackedWalkResumesWithSaturatedPendingFrontier(t *testing.T) {
	for _, holdInner := range []bool{false, true} {
		name := "leaves"
		if holdInner {
			name = "inner and leaf replies"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newIssue2043MemoryFixture(t, issue2043FixtureLeaves)
			if holdInner {
				family := fixture.family.(*issue2043HeldMemoryFamily)
				for hash, data := range family.base.store {
					if hash == fixture.rootHash {
						continue
					}
					view, err := decodeTraversalNode(data, hash)
					if err != nil {
						t.Fatal(err)
					}
					if view.inner {
						family.hold(hash)
						fixture.withheld++
						fixture.withheldHashes = append(fixture.withheldHashes, hash)
					}
				}
			}
			budget := NewAcquisitionBudget(4 << 20)
			holder := New(TypeState)
			heldCursor := &backedWalkCursor{budget: budget}
			heldBytes := budget.limit / 8
			if !holder.reservePendingBytes(heldCursor, heldBytes) {
				t.Fatal("reserve another owner's pending frontier")
			}
			defer holder.releasePendingBytes(heldCursor, heldBytes)
			dest := fixture.newDestination(t, budget)
			run := issue2043DriveInterleaved(t, fixture, dest, budget, issue2043DriveOptions{})
			if run.staged != uint64(fixture.withheld) {
				t.Fatalf("staged %d/%d nodes", run.staged, fixture.withheld)
			}
			if err := dest.AcknowledgePersistedAndReleaseContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := budget.Snapshot(); got.Used != heldBytes || got.CursorPeak > got.Limit/4 {
				t.Fatalf("completed acquisition budget: %+v", got)
			}
			if budget.pendingUsed.Load() != heldBytes {
				t.Fatal("pending reservations survived completion")
			}
		})
	}
}

func TestBackedWalkFrameGrowthAccountsForBothAllocations(t *testing.T) {
	sm := New(TypeState)
	fixed := 2 * uint64(unsafe.Sizeof(backedWalkCursor{}))
	budget := NewAcquisitionBudget(4 * (fixed + 3*backedWalkFrameBytes - 1))
	cursor, err := sm.newBackedWalkCursor(sm.tree.root, [32]byte{}, 1, nil, budget)
	if err != nil {
		t.Fatal(err)
	}
	sm.acquisition.cursor = cursor
	lane := &cursor.lanes[0]
	frame := backedWalkFrame{item: backedWalkItem{hash: [32]byte{1}}, nextBranch: 7}
	if err := sm.pushBackedWalkFrame(cursor, lane, frame); err != nil {
		t.Fatal(err)
	}
	if err := sm.pushBackedWalkFrame(cursor, lane, backedWalkFrame{}); !errors.Is(err, ErrAcquisitionMemory) {
		t.Fatalf("growth error = %v; both the old and replacement allocation must fit", err)
	}
	if len(lane.stack) != 1 || lane.stack[0] != frame {
		t.Fatal("failed growth changed continuation")
	}
	sm.releaseBackedWalkCursorLocked()
	if got := budget.Snapshot(); got.Used != 0 {
		t.Fatalf("released cursor budget = %+v", got)
	}
}

func TestBackedWalkCheckpointPreservesBothContinuations(t *testing.T) {
	sm, err := NewBacked(TypeState, newMemoryFamily())
	if err != nil {
		t.Fatal(err)
	}
	hash, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	budget := NewAcquisitionBudget(2 << 20)
	cursor, err := sm.newBackedWalkCursor(sm.tree.root, hash, sm.backing.fullBelow.Generation(), nil, budget)
	if err != nil {
		t.Fatal(err)
	}
	sm.acquisition.cursor = cursor
	lane := &cursor.lanes[0]
	lane.complete = false
	lane.cursor = cursor
	for _, retry := range []bool{false, true} {
		lane.activeRetry = retry
		proofs := backedWalkActiveProofs(lane)
		if err := proofs.addWithBudget([32]byte{byte(proofs.count() + 1)}, 2,
			func(n uint64) bool { return sm.reserveCursorBytes(cursor, n) },
			func(n uint64) { sm.releaseCursorBytes(cursor, n) }); err != nil {
			t.Fatal(err)
		}
		frame := backedWalkFrame{item: backedWalkItem{hash: [32]byte{3}}, nextBranch: 9, loaded: true, proofStart: 1}
		if err := sm.pushBackedWalkFrame(cursor, lane, frame); err != nil {
			t.Fatal(err)
		}
	}
	lane.activeRetry = false
	if err := sm.AcknowledgePersistedAndReleaseContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sm.acquisition.cursor != cursor || sm.backing.fullBelow.Has(cursor.generation, hash) {
		t.Fatal("checkpoint completed an unfinished cursor")
	}
	for _, stack := range [][]backedWalkFrame{lane.stack, lane.retryStack} {
		if len(stack) != 1 || stack[0].nextBranch != 9 || !stack[0].loaded || stack[0].proofStart != 0 {
			t.Fatalf("checkpoint lost continuation: %+v", stack)
		}
	}
	if lane.proofs.count() != 0 || lane.retryProofs.count() != 0 {
		t.Fatal("checkpoint retained published proofs")
	}
	sm.releaseBackedWalkCursorLocked()
	if got := budget.Snapshot(); got.Used != 0 {
		t.Fatalf("retired cursor budget = %+v", got)
	}
}
