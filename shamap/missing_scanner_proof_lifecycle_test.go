package shamap

import (
	"context"
	"errors"
	"testing"
)

func holdMissingScannerCompletionHash(family *missingScannerCompletionFamily, hash [32]byte) {
	family.mu.Lock()
	family.held[hash] = struct{}{}
	family.mu.Unlock()
}

func findMissingScannerCompletedInner(t *testing.T, fixture *missingScannerCompletionFixture) [32]byte {
	t.Helper()
	reads := fixture.family.snapshotReads()
	generation := fixture.family.fullBelow.Generation()
	for hash, count := range reads {
		if count == 0 {
			continue
		}
		if _, onTargetPath := fixture.pathHashes[hash]; onTargetPath {
			continue
		}
		fixture.family.mu.Lock()
		data := append([]byte(nil), fixture.family.data[hash]...)
		fixture.family.mu.Unlock()
		node, err := deserializeFromPrefix(data)
		if err != nil {
			continue
		}
		if _, ok := node.(InnerNodeReader); !ok || !fixture.family.fullBelow.Has(generation, hash) {
			continue
		}
		return hash
	}
	t.Fatal("fixture has no previously completed inner outside the withheld path")
	return [32]byte{}
}

func scanForMissingScannerHash(t *testing.T, dest *SHAMap, filter SyncFilter, want [32]byte) bool {
	t.Helper()
	for call := 0; call < 64; call++ {
		result, err := dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 2048), 256, filter)
		if err != nil {
			t.Fatalf("rediscovery scan slice %d: %v", call+1, err)
		}
		for _, missing := range result.Nodes {
			if missing.Hash == want {
				return true
			}
		}
		switch result.Status {
		case MissingNodesWaiting:
			return false
		case MissingNodesComplete:
			t.Fatalf("scanner completed while %x was withheld", want[:4])
		case MissingNodesMemory:
			t.Fatalf("rediscovery scan exhausted cursor memory: %+v", result)
		}
	}
	t.Fatal("rediscovery scan did not finish within 64 slices")
	return false
}

func attachMissingScannerCompletionTarget(t *testing.T, fixture *missingScannerCompletionFixture) {
	t.Helper()
	attached, entry, err := fixture.dest.AddKnownNodeByIDWithEntryContext(
		t.Context(), fixture.target.nodeID, fixture.target.wire,
	)
	if err != nil {
		t.Fatalf("attach delayed subtree: %v", err)
	}
	if attached != NodeUseful {
		t.Fatalf("attach delayed subtree = %v, want useful", attached)
	}
	if err := fixture.family.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
		t.Fatalf("persist delayed subtree: %v", err)
	}
	fixture.family.release(fixture.target.hash)
}

func completeMissingScannerFixture(t *testing.T, fixture *missingScannerCompletionFixture) {
	t.Helper()
	for attempt := 0; attempt < 64; attempt++ {
		result, err := fixture.dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 2048), 256, nil)
		if err != nil {
			t.Fatalf("completion slice %d: %v", attempt+1, err)
		}
		switch result.Status {
		case MissingNodesComplete:
			return
		case MissingNodesPersistence:
			if err := fixture.dest.AcknowledgePersistedContext(t.Context()); err != nil {
				t.Fatalf("acknowledge completion slice %d: %v", attempt+1, err)
			}
		case MissingNodesMemory:
			t.Fatalf("completion slice %d exhausted cursor memory: %+v", attempt+1, result)
		}
	}
	t.Fatal("scanner did not complete after delayed arrival")
}

func TestMissingScannerGenerationBumpDropsStaleCompletionProof(t *testing.T) {
	fixture := newMissingScannerCompletionFixture(t, NewFullBelowCache())
	filter := missingScannerCompletionFilter{hash: fixture.target.hash}
	if err := scanMissingScannerCompletionPass(t, fixture.dest, filter); err != nil {
		t.Fatal(err)
	}
	completed := findMissingScannerCompletedInner(t, fixture)
	oldGeneration := fixture.family.fullBelow.Generation()
	fixture.family.fullBelow.Bump()
	newGeneration := fixture.family.fullBelow.Generation()
	if newGeneration == oldGeneration {
		t.Fatalf("cache generation did not advance: %d", newGeneration)
	}
	if fixture.family.fullBelow.Has(newGeneration, completed) {
		t.Fatal("generation bump retained a completion mark")
	}
	holdMissingScannerCompletionHash(fixture.family, completed)
	defer fixture.family.release(completed)
	if !scanForMissingScannerHash(t, fixture.dest, filter, completed) {
		t.Fatalf("generation-bumped scan did not rediscover withheld inner %x", completed[:4])
	}
}

func TestMissingScannerReleaseAfterAcknowledgementCancellationReleasesProofs(t *testing.T) {
	fixture := newMissingScannerCompletionFixture(t, NewFullBelowCache())
	filter := missingScannerCompletionFilter{hash: fixture.target.hash}
	if err := scanMissingScannerCompletionPass(t, fixture.dest, filter); err != nil {
		t.Fatal(err)
	}
	before := fixture.dest.BackedWalkStats()
	if before.ProofEntries == 0 || before.CursorBytes == 0 {
		t.Fatalf("incomplete scan retained no proof cursor state: %+v", before)
	}
	if err := fixture.dest.AcknowledgePersistedAndReleaseContext(t.Context()); err != nil {
		t.Fatalf("acknowledge and release: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fixture.dest.ScanMissingNodesContext(canceled, 256, filter); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan error = %v, want context.Canceled", err)
	}
	fixture.dest.ReleaseAcquisitionBudget()
	if stats := fixture.dest.BackedWalkStats(); stats.CursorBytes != 0 || stats.ProofEntries != 0 {
		t.Fatalf("explicit retirement retained proof cursor state: %+v", stats)
	}
	if budget := fixture.budget.Snapshot(); budget.Used != 0 || budget.CursorUsed != 0 {
		t.Fatalf("explicit retirement retained acquisition credits: %+v", budget)
	}
}

func TestMissingScannerFallsBackWhenProofAdmissionIsDenied(t *testing.T) {
	fixture := newMissingScannerCompletionFixture(t, NewFullBelowCache())
	filter := missingScannerCompletionFilter{hash: fixture.target.hash}
	first, err := fixture.dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 1), 256, filter)
	if err != nil {
		t.Fatalf("initial bounded slice: %v", err)
	}
	if first.Status == MissingNodesMemory || fixture.dest.BackedWalkStats().ActiveFrames == 0 {
		t.Fatalf("initial slice did not admit a resumable stack: result=%+v stats=%+v", first, fixture.dest.BackedWalkStats())
	}

	budget := fixture.budget
	snapshot := budget.Snapshot()
	cursorLimit := snapshot.Limit / acquisitionCursorBudgetFraction
	if cursorLimit <= snapshot.CursorUsed+backedScanProofBytes {
		t.Fatalf("budget too small to deny proof admission after stack allocation: %+v", snapshot)
	}
	filler := cursorLimit - snapshot.CursorUsed - backedScanProofBytes + 1
	if !budget.tryReserveCursor(filler) {
		t.Fatalf("could not reserve proof-denial filler: %d bytes", filler)
	}
	defer func() {
		if filler != 0 {
			budget.releaseCursor(filler)
		}
		fixture.dest.ReleaseAcquisitionBudget()
	}()

	for round := range 2 {
		if err := scanMissingScannerCompletionPass(t, fixture.dest, filter); err != nil {
			t.Fatalf("fallback scan round %d: %v", round+1, err)
		}
		stats := fixture.dest.BackedWalkStats()
		if stats.ProofEntries != 0 {
			t.Fatalf("proof admission unexpectedly succeeded under filler pressure: %+v", stats)
		}
	}

	attachMissingScannerCompletionTarget(t, fixture)
	completeMissingScannerFixture(t, fixture)
	if stats := fixture.dest.BackedWalkStats(); stats.CursorBytes != 0 || stats.ProofEntries != 0 {
		t.Fatalf("fallback completion retained cursor metadata: %+v", stats)
	}
	budget.releaseCursor(filler)
	filler = 0
	fixture.dest.ReleaseAcquisitionBudget()
	if snapshot := budget.Snapshot(); snapshot.Used != 0 || snapshot.CursorUsed != 0 {
		t.Fatalf("fallback completion leaked acquisition credits: %+v", snapshot)
	}
}
