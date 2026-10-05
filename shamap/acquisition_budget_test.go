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
