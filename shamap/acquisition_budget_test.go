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
	parent := NewAcquisitionBudget(128)
	child, err := NewAcquisitionBudgetWithParent(256, parent)
	if err != nil {
		t.Fatal(err)
	}
	if !child.TryReserve(100) {
		t.Fatal("child reservation was rejected")
	}
	if got := parent.Snapshot().Used; got != 100 {
		t.Fatalf("parent used = %d, want 100", got)
	}
	if child.TryReserve(100) {
		t.Fatal("child admitted beyond parent capacity")
	}
	if got := child.Snapshot().Used; got != 100 {
		t.Fatalf("failed parent admission changed child usage: %d", got)
	}
	if got := parent.Snapshot().Used; got != 100 {
		t.Fatalf("failed parent admission changed parent usage: %d", got)
	}
	child.Release(100)
	if got := parent.Snapshot().Used; got != 0 {
		t.Fatalf("parent usage after child release = %d", got)
	}
}

func TestAcquisitionBudgetCursorAndLoadedChargeParentOnce(t *testing.T) {
	parent := NewAcquisitionBudget(4096)
	child, err := NewAcquisitionBudgetWithParent(256, parent)
	if err != nil {
		t.Fatal(err)
	}
	if !child.tryReserveCursor(64) {
		t.Fatal("cursor reservation was rejected")
	}
	if got := parent.Snapshot().Used; got != 64 {
		t.Fatalf("parent usage after cursor reservation = %d, want 64", got)
	}
	child.releaseCursor(64)
	if got := parent.Snapshot().Used; got != 0 {
		t.Fatalf("parent usage after cursor release = %d", got)
	}
	if !child.tryReserveLoaded(64) {
		t.Fatal("loaded reservation was rejected")
	}
	if got := parent.Snapshot().Used; got != 64 {
		t.Fatalf("parent usage after loaded reservation = %d, want 64", got)
	}
	child.releaseLoaded(64)
	if got := parent.Snapshot().Used; got != 0 {
		t.Fatalf("parent usage after loaded release = %d", got)
	}
}

func TestAcquisitionBudgetChangedWakesParentBackedWaiters(t *testing.T) {
	parent := NewAcquisitionBudget(128)
	child, err := NewAcquisitionBudgetWithParent(128, parent)
	if err != nil {
		t.Fatal(err)
	}
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

func TestAcquisitionBudgetProtectedCategoriesKeepParentFloor(t *testing.T) {
	parent := NewAcquisitionBudget(4 << 30)
	child, err := NewAcquisitionBudgetWithParent(4<<30, parent)
	if err != nil {
		t.Fatal(err)
	}
	const protected = 512 << 20
	if got := parent.Snapshot().Used; got != protected {
		t.Fatalf("parent protected usage = %d, want %d", got, protected)
	}

	if !child.TryReserveReplayHead(8<<20) || !child.TryReserveReplayProcessing(8<<20) {
		t.Fatal("protected replay categories rejected their reserved workspace")
	}
	if !child.tryReserveCursor(128 << 20) {
		t.Fatal("cursor reservation was rejected")
	}
	if got := parent.Snapshot().Used; got != protected+(64<<20) {
		t.Fatalf("parent usage after category excess = %d, want %d", got, protected+(64<<20))
	}
	child.releaseCursor(128 << 20)
	child.ReleaseReplayProcessing(8 << 20)
	child.ReleaseReplayHead(8 << 20)
	if got := parent.Snapshot().Used; got != protected {
		t.Fatalf("parent usage after category release = %d, want %d", got, protected)
	}
	if !child.Close() {
		t.Fatal("close rejected after all category owners drained")
	}
	if got := parent.Snapshot().Used; got != 0 {
		t.Fatalf("parent usage after close = %d, want 0", got)
	}
}

func TestAcquisitionBudgetProtectedMinimumAdmissionError(t *testing.T) {
	parent := NewAcquisitionBudget(256 << 20)
	if _, err := NewAcquisitionBudgetWithParent(4<<30, parent); err == nil {
		t.Fatal("expected protected-minimum admission error")
	}
}

func TestAcquisitionBudgetLocalAndParentWakeOnProtectedRelease(t *testing.T) {
	parent := NewAcquisitionBudget(4 << 30)
	child, err := NewAcquisitionBudgetWithParent(4<<30, parent)
	if err != nil {
		t.Fatal(err)
	}
	if !child.TryReserveReplayHead(8 << 20) {
		t.Fatal("replay-head reservation was rejected")
	}
	localChanged := child.LocalChanged()
	child.ReleaseReplayHead(8 << 20)
	select {
	case <-localChanged:
	default:
		t.Fatal("LocalChanged channel did not wake on child release")
	}
	if !child.Close() {
		t.Fatal("close rejected after protected release")
	}
}
