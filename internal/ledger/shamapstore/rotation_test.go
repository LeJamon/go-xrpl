package shamapstore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNodePruner records DeleteBefore boundaries and reports a fixed deleted
// count so tests can assert what the rotator asked to delete.
type fakeNodePruner struct {
	mu         sync.Mutex
	boundaries []uint32
	deleted    uint64
	err        error
	before     func()
}

type fakeGenerationPruner struct {
	fakeNodePruner
	rotations     int
	committed     bool
	rotateErr     error
	lastRotated   uint32
	minimumOnline uint32
}

func (f *fakeGenerationPruner) RotateGeneration(
	_ context.Context,
	lastRotated, minimumOnline uint32,
) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotations++
	if f.committed {
		f.lastRotated = lastRotated
		f.minimumOnline = minimumOnline
	}
	return f.committed, f.rotateErr
}

func (f *fakeGenerationPruner) GenerationState() (uint32, uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastRotated, f.minimumOnline
}

func TestRotate_RefreshFailureAbortsDeletion(t *testing.T) {
	r, nodes, rel := newTestRotator(t, false, 256)
	r.SetStateRefresh(func(context.Context, uint32, func(context.Context, time.Duration) error) (uint32, error) {
		return 0, errors.New("missing live node")
	}, nil, nil)
	r.maybeRotate(context.Background(), 500)
	r.maybeRotate(context.Background(), 800)

	if len(nodes.calls()) != 0 || len(rel.calls()) != 0 {
		t.Fatal("refresh failure must abort all deletion")
	}
	if got := r.store.GetLastRotated(); got != 500 {
		t.Fatalf("lastRotated = %d, want 500", got)
	}
}

func TestRotate_WaitsForHealthyNode(t *testing.T) {
	r, nodes, _ := newTestRotator(t, false, 256)
	r.maybeRotate(context.Background(), 500)
	var healthy atomic.Bool
	r.SetHealthCheck(healthy.Load, time.Millisecond)
	done := make(chan struct{})
	go func() {
		r.maybeRotate(context.Background(), 800)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	if len(nodes.calls()) != 0 {
		t.Fatal("unhealthy node started pruning")
	}
	healthy.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("rotation did not resume after health recovered")
	}
	if len(nodes.calls()) != 1 {
		t.Fatalf("prune calls = %v, want one", nodes.calls())
	}
}

func TestRotate_RefreshCheckpointWaitsForHealthRecovery(t *testing.T) {
	r, nodes, _ := newTestRotator(t, false, 256)
	r.maybeRotate(context.Background(), 500)

	var healthy atomic.Bool
	healthy.Store(true)
	r.SetHealthCheck(healthy.Load, time.Millisecond)
	checkpointReached := make(chan struct{})
	r.SetStateRefresh(func(ctx context.Context, seq uint32, checkpoint func(context.Context, time.Duration) error) (uint32, error) {
		healthy.Store(false)
		close(checkpointReached)
		if err := checkpoint(ctx, time.Millisecond); err != nil {
			return 0, err
		}
		return seq, nil
	}, nil, nil)

	done := make(chan struct{})
	go func() {
		r.maybeRotate(context.Background(), 800)
		close(done)
	}()
	<-checkpointReached
	if len(nodes.calls()) != 0 {
		t.Fatal("pruning started while the refresh was paused")
	}
	healthy.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh did not resume after health recovered")
	}
	if got := nodes.calls(); len(got) != 1 || got[0] != 500 {
		t.Fatalf("prune calls = %v, want [500]", got)
	}
}

func TestRotate_RefreshCheckpointHonorsCancellation(t *testing.T) {
	r, nodes, _ := newTestRotator(t, false, 256)
	r.maybeRotate(context.Background(), 500)

	var healthy atomic.Bool
	healthy.Store(true)
	r.SetHealthCheck(healthy.Load, time.Millisecond)
	checkpointReached := make(chan struct{})
	r.SetStateRefresh(func(ctx context.Context, seq uint32, checkpoint func(context.Context, time.Duration) error) (uint32, error) {
		healthy.Store(false)
		close(checkpointReached)
		if err := checkpoint(ctx, time.Millisecond); err != nil {
			return 0, err
		}
		return seq, nil
	}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.maybeRotate(ctx, 800)
		close(done)
	}()
	<-checkpointReached
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh did not stop after cancellation")
	}
	if got := nodes.calls(); len(got) != 0 {
		t.Fatalf("prune calls after cancellation = %v, want none", got)
	}
	if got := r.store.GetLastRotated(); got != 500 {
		t.Fatalf("lastRotated after cancellation = %d, want 500", got)
	}
}

func TestRotate_RefreshPacingHonorsCancellation(t *testing.T) {
	r, nodes, _ := newTestRotator(t, false, 256)
	r.maybeRotate(context.Background(), 500)
	r.cfg.BackOff = time.Hour

	var healthy atomic.Bool
	healthy.Store(true)
	r.SetHealthCheck(healthy.Load, time.Millisecond)
	checkpointReached := make(chan struct{})
	r.SetStateRefresh(func(ctx context.Context, seq uint32, checkpoint func(context.Context, time.Duration) error) (uint32, error) {
		close(checkpointReached)
		if err := checkpoint(ctx, time.Hour); err != nil {
			return 0, err
		}
		return seq, nil
	}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.maybeRotate(ctx, 800)
		close(done)
	}()
	<-checkpointReached
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh pacing did not stop after cancellation")
	}
	if got := nodes.calls(); len(got) != 0 {
		t.Fatalf("prune calls after paced cancellation = %v, want none", got)
	}
	if got := r.store.GetLastRotated(); got != 500 {
		t.Fatalf("lastRotated after paced cancellation = %d, want 500", got)
	}
}

func TestRotator_LongRefreshPersistsActualSequenceAndCoalescesQueuedNotifications(t *testing.T) {
	r, nodes, _ := newTestRotator(t, false, 256)
	refreshRequests := make(chan uint32, 3)
	resumeRefresh := make(chan struct{})
	r.SetStateRefresh(func(_ context.Context, requested uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		refreshRequests <- requested
		if requested == 800 {
			<-resumeRefresh
			return 1100, nil
		}
		return requested, nil
	}, nil, nil)
	r.Start()
	defer r.Stop()

	r.Notify(500)
	waitFor(t, func() bool { return r.store.GetLastRotated() == 500 })
	r.Notify(800)
	if got := <-refreshRequests; got != 800 {
		t.Fatalf("first refresh request = %d, want 800", got)
	}
	// The validated ledger advances to 1100 while the expensive state walk is
	// still in flight, so its notification is stale once that walk completes.
	r.Notify(900)
	r.Notify(1100)
	close(resumeRefresh)
	waitFor(t, func() bool { return r.store.GetLastRotated() == 1100 })

	// A later eligible notification is the next refresh. If either queued
	// notification caused redundant work, it would arrive on refreshRequests
	// before 1356.
	r.Notify(1356)
	select {
	case got := <-refreshRequests:
		if got != 1356 {
			t.Fatalf("refresh after coalescing = %d, want 1356", got)
		}
	case <-time.After(time.Second):
		t.Fatal("eligible refresh did not run")
	}
	waitFor(t, func() bool { return r.store.GetLastRotated() == 1356 })
	if got := nodes.calls(); len(got) != 2 || got[0] != 500 || got[1] != 1100 {
		t.Fatalf("prune calls = %v, want [500 1100]", got)
	}
}

func (f *fakeNodePruner) DeleteBefore(_ context.Context, boundary uint32, _ int) (uint64, error) {
	if f.before != nil {
		f.before()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boundaries = append(f.boundaries, boundary)
	return f.deleted, f.err
}

func TestRotate_PartialPruneResetsCacheWithoutAdvancing(t *testing.T) {
	r, nodes, _ := newTestRotator(t, false, 256)
	r.maybeRotate(context.Background(), 500)
	nodes.deleted = 3
	nodes.err = errors.New("partial prune")
	locked, unlocked := 0, 0
	r.SetStateRefresh(func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		return seq, nil
	}, nil, func() func() {
		locked++
		return func() { unlocked++ }
	})

	r.maybeRotate(context.Background(), 800)

	if locked != 1 || unlocked != 1 {
		t.Fatalf("prune guard = (%d, %d), want (1, 1)", locked, unlocked)
	}
	if got := r.store.GetLastRotated(); got != 500 {
		t.Fatalf("lastRotated = %d, want 500", got)
	}
}

func (f *fakeNodePruner) calls() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.boundaries...)
}

type fakeRelPruner struct {
	mu         sync.Mutex
	boundaries []uint32
}

func (f *fakeRelPruner) DeleteLedgersBefore(_ context.Context, boundary uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boundaries = append(f.boundaries, boundary)
	return nil
}

func (f *fakeRelPruner) calls() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.boundaries...)
}

func newTestRotator(t *testing.T, advisory bool, interval uint32) (*Rotator, *fakeNodePruner, *fakeRelPruner) {
	t.Helper()
	store, err := New(advisory, "")
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	nodes := &fakeNodePruner{}
	rel := &fakeRelPruner{}
	r := NewRotator(store, nodes, rel, RotationConfig{DeleteInterval: interval}, nil)
	if r == nil {
		t.Fatal("NewRotator returned nil")
	}
	r.SetStateRefresh(func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		return seq, nil
	}, nil, nil)
	return r, nodes, rel
}

func TestNewRotator_DisabledWhenIntervalZero(t *testing.T) {
	store, _ := New(false, "")
	if r := NewRotator(store, &fakeNodePruner{}, nil, RotationConfig{DeleteInterval: 0}, nil); r != nil {
		t.Fatal("rotator must be nil when online_delete is off")
	}
}

func TestNewRotator_NilWhenNoPruner(t *testing.T) {
	store, _ := New(false, "")
	if r := NewRotator(store, nil, nil, RotationConfig{DeleteInterval: 256}, nil); r != nil {
		t.Fatal("rotator must be nil without a node pruner")
	}
}

func TestRotate_FirstNotificationSeedsBoundaryNoDelete(t *testing.T) {
	r, nodes, rel := newTestRotator(t, false, 256)

	r.maybeRotate(context.Background(), 1000)

	if got := r.store.GetLastRotated(); got != 1000 {
		t.Fatalf("lastRotated = %d, want 1000 (seeded)", got)
	}
	if got := r.MinimumOnline(); got != 0 {
		t.Fatalf("minimumOnline = %d, want 0", got)
	}
	if len(nodes.calls()) != 0 || len(rel.calls()) != 0 {
		t.Fatal("first notification must not delete anything")
	}
}

func TestRotate_WaitsForFullInterval(t *testing.T) {
	r, nodes, _ := newTestRotator(t, false, 256)
	r.maybeRotate(context.Background(), 1000) // seed lastRotated=1000

	// Not yet a full interval past 1000.
	r.maybeRotate(context.Background(), 1000+255)
	if len(nodes.calls()) != 0 {
		t.Fatal("must not rotate before a full delete interval elapses")
	}
	if got := r.store.GetLastRotated(); got != 1000 {
		t.Fatalf("lastRotated = %d, want unchanged 1000", got)
	}

	// Exactly one interval past 1000 → rotate, deleting below the OLD boundary.
	r.maybeRotate(context.Background(), 1000+256)
	calls := nodes.calls()
	if len(calls) != 1 || calls[0] != 1000 {
		t.Fatalf("node delete boundaries = %v, want [1000]", calls)
	}
	if got := r.store.GetLastRotated(); got != 1256 {
		t.Fatalf("lastRotated = %d, want 1256", got)
	}
	if got := r.MinimumOnline(); got != 1001 {
		t.Fatalf("minimumOnline = %d, want 1001 (lastRotated+1)", got)
	}
}

func TestRotate_PrefersGenerationRotationOverSequencePruning(t *testing.T) {
	store, err := New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	nodes := &fakeGenerationPruner{committed: true}
	r := NewRotator(store, nodes, nil, RotationConfig{DeleteInterval: 256}, nil)
	r.SetStateRefresh(func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		return seq, nil
	}, nil, nil)

	r.maybeRotate(context.Background(), 500)
	r.maybeRotate(context.Background(), 800)

	if nodes.rotations != 1 {
		t.Fatalf("generation rotations = %d, want 1", nodes.rotations)
	}
	if got := nodes.calls(); len(got) != 0 {
		t.Fatalf("legacy prune calls = %v, want none", got)
	}
	if got := r.store.GetLastRotated(); got != 800 {
		t.Fatalf("lastRotated = %d, want 800", got)
	}
}

func TestRotate_CommittedGenerationAdvancesDespiteCleanupError(t *testing.T) {
	store, err := New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	nodes := &fakeGenerationPruner{
		committed: true,
		rotateErr: errors.New("retired directory cleanup failed"),
	}
	r := NewRotator(store, nodes, nil, RotationConfig{DeleteInterval: 256}, nil)
	r.SetStateRefresh(func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		return seq, nil
	}, nil, nil)

	r.maybeRotate(context.Background(), 500)
	r.maybeRotate(context.Background(), 800)

	if got := r.store.GetLastRotated(); got != 800 {
		t.Fatalf("lastRotated = %d, want committed generation sequence 800", got)
	}
}

func TestRotator_ReconcilesDurableGenerationAfterBookkeepingCrash(t *testing.T) {
	store, err := New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRotation(500, 1); err != nil {
		t.Fatal(err)
	}
	nodes := &fakeGenerationPruner{
		committed:     true,
		lastRotated:   800,
		minimumOnline: 501,
	}
	r := NewRotator(store, nodes, nil, RotationConfig{DeleteInterval: 256}, nil)
	if err := r.ReconcileGenerationState(); err != nil {
		t.Fatal(err)
	}
	if got := store.GetLastRotated(); got != 800 {
		t.Fatalf("reconciled lastRotated = %d, want 800", got)
	}
	if got := r.MinimumOnline(); got != 501 {
		t.Fatalf("reconciled minimumOnline = %d, want 501", got)
	}
}

func TestRotator_ReconcileRejectsGenerationWithoutMinimumOnline(t *testing.T) {
	store, err := New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetLastRotated(500); err != nil {
		t.Fatal(err)
	}
	nodes := &fakeGenerationPruner{
		committed:   true,
		lastRotated: 800,
	}
	r := NewRotator(store, nodes, nil, RotationConfig{DeleteInterval: 256}, nil)

	if err := r.ReconcileGenerationState(); err == nil {
		t.Fatal("ReconcileGenerationState accepted a generation without a minimum online boundary")
	}
	if got := store.GetLastRotated(); got != 500 {
		t.Fatalf("lastRotated = %d after rejected reconciliation, want 500", got)
	}
}

func TestRotator_ReconcilePreservesHigherExistingMinimumOnline(t *testing.T) {
	store, err := New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRotation(500, 900); err != nil {
		t.Fatal(err)
	}
	nodes := &fakeGenerationPruner{
		committed:     true,
		lastRotated:   800,
		minimumOnline: 501,
	}
	r := NewRotator(store, nodes, nil, RotationConfig{DeleteInterval: 256}, nil)
	if err := r.ReconcileGenerationState(); err != nil {
		t.Fatal(err)
	}
	if got := store.GetLastRotated(); got != 800 {
		t.Fatalf("reconciled lastRotated = %d, want 800", got)
	}
	if got := r.MinimumOnline(); got != 900 {
		t.Fatalf("reconciled minimumOnline = %d, want existing floor 900", got)
	}
}

func TestRotator_ReconcileFailureBlocksAnotherGenerationRotation(t *testing.T) {
	store, err := New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRotation(500, 1); err != nil {
		t.Fatal(err)
	}
	nodes := &fakeGenerationPruner{committed: true}
	r := NewRotator(store, nodes, nil, RotationConfig{DeleteInterval: 256}, nil)
	r.SetStateRefresh(func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		return seq, nil
	}, nil, nil)

	realPersist := store.persist
	writeErr := errors.New("bookkeeping unavailable")
	store.persist = func(next persistedState) (bool, error) {
		if next.LastRotated == 500 {
			return true, nil
		}
		return false, writeErr
	}
	r.maybeRotate(context.Background(), 800)
	if nodes.rotations != 1 {
		t.Fatalf("rotations after committed generation = %d, want 1", nodes.rotations)
	}
	r.maybeRotate(context.Background(), 1100)
	if nodes.rotations != 1 {
		t.Fatalf("rotation proceeded with unreconciled generation: %d", nodes.rotations)
	}

	store.persist = realPersist
	r.maybeRotate(context.Background(), 1100)
	if nodes.rotations != 2 {
		t.Fatalf("rotations after reconciliation = %d, want 2", nodes.rotations)
	}
}

func TestRotator_LifecycleIsIdempotent(t *testing.T) {
	store, err := New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRotator(store, &fakeNodePruner{}, nil, RotationConfig{DeleteInterval: 256}, nil)

	var starts sync.WaitGroup
	for range 20 {
		starts.Add(1)
		go func() {
			defer starts.Done()
			r.Start()
		}()
	}
	starts.Wait()

	var stops sync.WaitGroup
	for range 20 {
		stops.Add(1)
		go func() {
			defer stops.Done()
			r.Stop()
		}()
	}
	stops.Wait()
	r.Start()
	r.Stop()
}

func TestRotator_StopBeforeStartReturns(t *testing.T) {
	store, err := New(false, "")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRotator(store, &fakeNodePruner{}, nil, RotationConfig{DeleteInterval: 256}, nil)
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop before Start blocked")
	}
	r.Start()
	r.Stop()
}

func TestRotator_StateRefreshHooksCanBeUpdatedConcurrently(t *testing.T) {
	r, _, _ := newTestRotator(t, false, 256)
	refresh := func(
		_ context.Context,
		seq uint32,
		_ func(context.Context, time.Duration) error,
	) (uint32, error) {
		return seq, nil
	}
	r.SetStateRefresh(refresh, func(uint32) {}, func() func() {
		return func() {}
	})
	r.maybeRotate(context.Background(), 500)

	var updates sync.WaitGroup
	updates.Add(1)
	go func() {
		defer updates.Done()
		for range 1000 {
			r.SetStateRefresh(refresh, func(uint32) {}, func() func() {
				return func() {}
			})
		}
	}()
	r.maybeRotate(context.Background(), 800)
	updates.Wait()
}

func TestRotator_StateRefreshUsesConsistentHookSnapshot(t *testing.T) {
	r, _, _ := newTestRotator(t, false, 256)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var oldAdvance, oldPrune, newPrune int
	r.SetStateRefresh(
		func(
			_ context.Context,
			seq uint32,
			_ func(context.Context, time.Duration) error,
		) (uint32, error) {
			close(entered)
			<-release
			return seq, nil
		},
		func(uint32) { oldAdvance++ },
		func() func() {
			oldPrune++
			return func() {}
		},
	)
	r.maybeRotate(context.Background(), 500)
	go func() {
		r.maybeRotate(context.Background(), 800)
		close(done)
	}()
	<-entered
	r.SetStateRefresh(
		func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
			return seq, nil
		},
		func(uint32) {},
		func() func() {
			newPrune++
			return func() {}
		},
	)
	close(release)
	<-done

	if oldAdvance != 1 || oldPrune != 1 || newPrune != 0 {
		t.Fatalf(
			"mixed hook bundle: oldAdvance=%d oldPrune=%d newPrune=%d",
			oldAdvance, oldPrune, newPrune,
		)
	}
}

func TestRotate_DeletesBelowOldBoundaryInBothStores(t *testing.T) {
	r, nodes, rel := newTestRotator(t, false, 256)
	nodes.before = func() {
		if got := r.store.GetMinimumOnline(); got != 501 {
			t.Fatalf("minimumOnline at destructive prune = %d, want durable floor 501", got)
		}
	}
	r.maybeRotate(context.Background(), 500) // seed lastRotated=500
	r.maybeRotate(context.Background(), 800) // 800 >= 500+256 → rotate

	if nc := nodes.calls(); len(nc) != 1 || nc[0] != 500 {
		t.Fatalf("node delete boundaries = %v, want [500]", nc)
	}
	if rc := rel.calls(); len(rc) != 1 || rc[0] != 500 {
		t.Fatalf("relational delete boundaries = %v, want [500]", rc)
	}
	if got := r.store.GetLastRotated(); got != 800 {
		t.Fatalf("lastRotated = %d, want 800", got)
	}
}

func TestRotator_MinimumOnlineFloorPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := New(false, dir)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRotator(store, &fakeNodePruner{}, nil, RotationConfig{DeleteInterval: 256}, nil)
	if err := r.SetMinimumOnlineFloor(700); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMinimumOnlineFloor(650); err != nil {
		t.Fatal(err)
	}

	reloaded, err := New(false, dir)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewRotator(reloaded, &fakeNodePruner{}, nil, RotationConfig{DeleteInterval: 256}, nil)
	if got := restarted.MinimumOnline(); got != 700 {
		t.Fatalf("minimumOnline after restart = %d, want 700", got)
	}
}

func TestRotate_AdvisoryDeleteGatesOnCanDelete(t *testing.T) {
	r, nodes, _ := newTestRotator(t, true, 256)
	r.maybeRotate(context.Background(), 500) // seed lastRotated=500

	// can_delete defaults to 0: it does not yet permit deleting below 500,
	// so a ready interval must still be held back (rippled: canDelete_ >=
	// lastRotated-1).
	r.maybeRotate(context.Background(), 800)
	if len(nodes.calls()) != 0 {
		t.Fatal("advisory delete must block rotation until can_delete permits it")
	}
	if got := r.store.GetLastRotated(); got != 500 {
		t.Fatalf("lastRotated = %d, want unchanged 500", got)
	}

	// Operator advances can_delete to 499 (== lastRotated-1) → permitted.
	if _, err := r.store.SetCanDelete(499); err != nil {
		t.Fatalf("SetCanDelete: %v", err)
	}
	r.maybeRotate(context.Background(), 800)
	if nc := nodes.calls(); len(nc) != 1 || nc[0] != 500 {
		t.Fatalf("node delete boundaries = %v, want [500] after can_delete permits", nc)
	}
	if got := r.store.GetLastRotated(); got != 800 {
		t.Fatalf("lastRotated = %d, want 800", got)
	}
}

func TestRotate_AdvisoryDeleteAlwaysPermits(t *testing.T) {
	// can_delete "always" maps to max uint32; it must permit rotation without
	// overflowing the gate arithmetic.
	r, nodes, _ := newTestRotator(t, true, 256)
	r.maybeRotate(context.Background(), 500) // seed lastRotated=500
	if _, err := r.store.SetCanDelete(^uint32(0)); err != nil {
		t.Fatalf("SetCanDelete: %v", err)
	}
	r.maybeRotate(context.Background(), 800)
	if nc := nodes.calls(); len(nc) != 1 || nc[0] != 500 {
		t.Fatalf("node delete boundaries = %v, want [500] with can_delete=always", nc)
	}
}

func TestRotate_TolerantOfNilRelationalPruner(t *testing.T) {
	store, _ := New(false, "")
	nodes := &fakeNodePruner{}
	r := NewRotator(store, nodes, nil, RotationConfig{DeleteInterval: 256}, nil)
	if r == nil {
		t.Fatal("rotator nil with valid node pruner")
	}
	r.SetStateRefresh(func(_ context.Context, seq uint32, _ func(context.Context, time.Duration) error) (uint32, error) {
		return seq, nil
	}, nil, nil)
	r.maybeRotate(context.Background(), 500)
	r.maybeRotate(context.Background(), 800)
	if nc := nodes.calls(); len(nc) != 1 || nc[0] != 500 {
		t.Fatalf("node delete boundaries = %v, want [500] (rel nil)", nc)
	}
}

func TestRotator_NotifyEndToEnd(t *testing.T) {
	r, nodes, _ := newTestRotator(t, false, 256)
	r.Start()
	defer r.Stop()

	r.Notify(500) // seeds
	// Wait for the seed to land.
	waitFor(t, func() bool { return r.store.GetLastRotated() == 500 })

	r.Notify(800) // triggers a rotation deleting below 500
	waitFor(t, func() bool { return r.store.GetLastRotated() == 800 })

	if nc := nodes.calls(); len(nc) != 1 || nc[0] != 500 {
		t.Fatalf("node delete boundaries = %v, want [500]", nc)
	}
	if got := r.MinimumOnline(); got != 501 {
		t.Fatalf("minimumOnline = %d, want 501", got)
	}
}

func TestRotator_NilSafe(t *testing.T) {
	var r *Rotator
	// All methods must be no-ops / zero on a nil rotator so callers needn't
	// branch on "online delete off".
	r.Start()
	r.Notify(123)
	if got := r.MinimumOnline(); got != 0 {
		t.Fatalf("nil MinimumOnline = %d, want 0", got)
	}
	r.Stop()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
