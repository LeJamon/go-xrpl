package shamapstore

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeNodePruner records DeleteBefore boundaries and reports a fixed deleted
// count so tests can assert what the rotator asked to delete.
type fakeNodePruner struct {
	mu         sync.Mutex
	boundaries []uint32
	deleted    uint64
}

func (f *fakeNodePruner) DeleteBefore(_ context.Context, boundary uint32, _ int) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boundaries = append(f.boundaries, boundary)
	return f.deleted, nil
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
	if got := r.MinimumOnline(); got != 1000 {
		t.Fatalf("minimumOnline = %d, want 1000", got)
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

func TestRotate_DeletesBelowOldBoundaryInBothStores(t *testing.T) {
	r, nodes, rel := newTestRotator(t, false, 256)
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
