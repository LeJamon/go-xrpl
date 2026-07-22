package shamap

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"
)

type blockingWalkFamily struct {
	Family
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (f *blockingWalkFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.once.Do(func() {
		close(f.started)
		select {
		case <-f.release:
		case <-ctx.Done():
		}
	})
	return f.Family.Fetch(ctx, hash)
}

func (f *blockingWalkFamily) FullBelowCache() *FullBelowCache {
	return familyFullBelowCache(f.Family)
}

func TestWalkMapParallelDoesNotHoldMapLockWhileWaitingForPrune(t *testing.T) {
	family := NewMemoryNodeStoreFamily()
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if err := sm.StartSync(); err != nil {
		t.Fatalf("StartSync: %v", err)
	}

	endPrune := family.BeginPrune()
	pruneHeld := true
	defer func() {
		if pruneHeld {
			endPrune()
		}
	}()

	walkDone := make(chan error, 1)
	go func() {
		_, err := sm.WalkMapParallelContext(context.Background(), 1, nil)
		walkDone <- err
	}()

	mapLock := make(chan struct{})
	go func() {
		sm.mu.Lock()
		close(mapLock)
		sm.mu.Unlock()
	}()
	select {
	case <-mapLock:
	case <-time.After(time.Second):
		t.Fatal("parallel walk held the SHAMap lock while waiting for FullBelow")
	}
	select {
	case err := <-walkDone:
		t.Fatalf("parallel walk returned before prune ended: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	finishDone := make(chan error, 1)
	go func() {
		finishDone <- sm.FinishSync()
	}()

	select {
	case err := <-finishDone:
		t.Fatalf("FinishSync returned before prune ended: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if !sm.mu.TryLock() {
		t.Fatal("FinishSync held the SHAMap lock while waiting for FullBelow")
	}
	sm.mu.Unlock()

	endPrune()
	pruneHeld = false

	select {
	case err := <-walkDone:
		if err != nil {
			t.Fatalf("WalkMapParallelContext: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parallel walk deadlocked with prune and FinishSync")
	}

	select {
	case err := <-finishDone:
		if err != nil {
			t.Fatalf("FinishSync: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FinishSync deadlocked with parallel walk and prune")
	}
}

func TestWalkMapParallelPruneAndFinishDoNotInvertLocks(t *testing.T) {
	source := buildRandomState(t, 256)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}
	base := NewMemoryNodeStoreFamily()
	if err := base.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatal(err)
	}
	family := &blockingWalkFamily{
		Family:  base,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	walkDone := make(chan error, 1)
	go func() {
		_, err := sm.WalkMapParallelContext(context.Background(), 1, nil)
		walkDone <- err
	}()
	<-family.started

	pruneDone := make(chan struct{})
	go func() {
		unlock := base.BeginPrune()
		unlock()
		close(pruneDone)
	}()
	waitForLockState(t, func() bool {
		if base.FullBelowCache().walks.TryRLock() {
			base.FullBelowCache().walks.RUnlock()
			return false
		}
		return true
	}, "prune did not queue behind the active walk")

	finishDone := make(chan error, 1)
	go func() { finishDone <- sm.FinishSync() }()
	select {
	case err := <-finishDone:
		t.Fatalf("FinishSync returned while the active traversal was blocked: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if !sm.mu.TryLock() {
		t.Fatal("FinishSync held the map lock behind the active traversal")
	}
	sm.mu.Unlock()
	close(family.release)

	select {
	case err := <-walkDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("parallel walk deadlocked with prune and FinishSync")
	}
	select {
	case <-pruneDone:
	case <-time.After(time.Second):
		t.Fatal("prune deadlocked with parallel walk and FinishSync")
	}
	select {
	case err := <-finishDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("FinishSync deadlocked with parallel walk and prune")
	}
}

func TestWalkMapParallelPinsFamilyUntilTraversalEnds(t *testing.T) {
	source := buildRandomState(t, 64)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.FlushDirty()
	if err != nil {
		t.Fatal(err)
	}
	base := NewMemoryNodeStoreFamily()
	if err := base.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatal(err)
	}
	family := &blockingWalkFamily{
		Family:  base,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	sm, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}

	walkDone := make(chan error, 1)
	go func() {
		_, err := sm.WalkMapParallelContext(context.Background(), 1, nil)
		walkDone <- err
	}()
	<-family.started
	switched := make(chan struct{})
	go func() {
		sm.SetFamily(NewMemoryNodeStoreFamily())
		close(switched)
	}()
	select {
	case <-switched:
		t.Fatal("SetFamily overtook an active traversal")
	case <-time.After(25 * time.Millisecond):
	}
	close(family.release)
	select {
	case err := <-walkDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("parallel walk did not finish")
	}
	select {
	case <-switched:
	case <-time.After(time.Second):
		t.Fatal("SetFamily did not resume after traversal")
	}
}

func waitForLockState(t *testing.T, ready func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		runtime.Gosched()
	}
}
