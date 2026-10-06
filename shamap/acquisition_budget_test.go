package shamap

import "testing"

func TestAcquisitionBudgetReserveRelease(t *testing.T) {
	budget := NewAcquisitionBudget(1024)
	if stats := budget.Snapshot(); stats.Limit != 1024 || stats.Used != 0 {
		t.Fatalf("initial budget = %+v", stats)
	}
	if !budget.TryReserve(256) {
		t.Fatal("first reservation was rejected")
	}
	if budget.TryReserve(769) {
		t.Fatal("reservation exceeded the shared limit")
	}
	stats := budget.Snapshot()
	if stats.Used != 256 || stats.Peak != 256 {
		t.Fatalf("reserved budget = %+v", stats)
	}
	budget.Release(256)
	if stats := budget.Snapshot(); stats.Used != 0 {
		t.Fatalf("released budget = %+v", stats)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("over-release did not report an ownership error")
		}
	}()
	budget.Release(1)
}

func TestAcquisitionBudgetChargesParentAndRollsBack(t *testing.T) {
	parent := NewAcquisitionBudget(512)
	child := NewAcquisitionBudgetWithParent(1024, parent)
	if !child.TryReserve(400) {
		t.Fatal("child reservation was rejected")
	}
	if got := parent.Snapshot().Used; got != 400 {
		t.Fatalf("parent used = %d, want 400", got)
	}
	if child.TryReserve(200) {
		t.Fatal("child admitted beyond parent capacity")
	}
	if got := child.Snapshot().Used; got != 400 {
		t.Fatalf("failed parent admission changed child usage: %d", got)
	}
	if got := parent.Snapshot().Used; got != 400 {
		t.Fatalf("failed parent admission changed parent usage: %d", got)
	}
	child.Release(400)
	if got := parent.Snapshot().Used; got != 0 {
		t.Fatalf("parent usage after child release = %d", got)
	}
}

func TestAcquisitionBudgetCursorAndLoadedChargeParentOnce(t *testing.T) {
	parent := NewAcquisitionBudget(4096)
	child := NewAcquisitionBudgetWithParent(4096, parent)
	if !child.tryReserveCursor(256) {
		t.Fatal("cursor reservation was rejected")
	}
	if got := parent.Snapshot().Used; got != 256 {
		t.Fatalf("parent usage after cursor reservation = %d, want 256", got)
	}
	child.releaseCursor(256)
	if got := parent.Snapshot().Used; got != 0 {
		t.Fatalf("parent usage after cursor release = %d", got)
	}
	if !child.tryReserveLoaded(256) {
		t.Fatal("loaded reservation was rejected")
	}
	if got := parent.Snapshot().Used; got != 256 {
		t.Fatalf("parent usage after loaded reservation = %d, want 256", got)
	}
	child.releaseLoaded(256)
	if got := parent.Snapshot().Used; got != 0 {
		t.Fatalf("parent usage after loaded release = %d", got)
	}
}

func TestAcquisitionBudgetChangedWakesParentBackedWaiters(t *testing.T) {
	parent := NewAcquisitionBudget(128)
	child := NewAcquisitionBudgetWithParent(128, parent)
	if !parent.TryReserve(128) {
		t.Fatal("parent reservation was rejected")
	}
	changed := child.Changed()
	parent.Release(128)
	select {
	case <-changed:
	default:
		t.Fatal("parent-backed Changed channel did not wake after release")
	}
}
