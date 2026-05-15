package loadtrack

import (
	"testing"
	"time"
)

func TestCharge_NoKeyAlwaysOK(t *testing.T) {
	tr := New()
	for i := 0; i < 100; i++ {
		if got := tr.Charge("", LoadHeavy); got != OutcomeOK {
			t.Fatalf("empty key must always be OK, got %v on iter %d", got, i)
		}
	}
}

func TestCharge_ReferenceStaysBelowWarning(t *testing.T) {
	tr := New()
	// 200 × ChargeReference = 4000 — below warning (5000).
	for i := 0; i < 200; i++ {
		if got := tr.Charge("1.2.3.4", LoadReference); got != OutcomeOK {
			t.Fatalf("got %v at iter %d, balance %v", got, i, tr.Balance("1.2.3.4"))
		}
	}
}

func TestCharge_HeavyCrossesWarnThenDrop(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	// 1 × Heavy (3000) — below warning still.
	if got := tr.Charge("1.2.3.4", LoadHeavy); got != OutcomeOK {
		t.Fatalf("1×heavy: expected OK, got %v", got)
	}
	// 2 × Heavy (6000) — over warning, below drop.
	if got := tr.Charge("1.2.3.4", LoadHeavy); got != OutcomeWarn {
		t.Fatalf("2×heavy: expected Warn, got %v (balance %v)", got, tr.Balance("1.2.3.4"))
	}
	// Subsequent heavies climb to >= 25000 ⇒ Drop.
	for i := 0; i < 10; i++ {
		got := tr.Charge("1.2.3.4", LoadHeavy)
		if got == OutcomeDrop {
			return
		}
	}
	t.Fatalf("never reached drop, final balance %v", tr.Balance("1.2.3.4"))
}

func TestCharge_DecayRecovers(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	for i := 0; i < 3; i++ {
		tr.Charge("1.2.3.4", LoadHeavy) // 9000
	}
	if got := tr.Balance("1.2.3.4"); got < 8000 {
		t.Fatalf("expected balance ~9000, got %v", got)
	}
	// Advance two full decay windows — balance should fall by ~75%.
	now = now.Add(2 * DecayWindow)
	if got := tr.Balance("1.2.3.4"); got > 9000*0.30 {
		t.Fatalf("expected decay to <30%% of initial, got %v", got)
	}
}

func TestCharge_PerKeyIsolated(t *testing.T) {
	tr := New()
	for i := 0; i < 20; i++ {
		tr.Charge("hot", LoadHeavy)
	}
	if got := tr.Charge("cold", LoadReference); got != OutcomeOK {
		t.Fatalf("cold IP should not be affected by hot IP, got %v", got)
	}
}

func TestSweep_EvictsIdleEntries(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	tr.Charge("1.2.3.4", LoadReference)
	if tr.Balance("1.2.3.4") == 0 {
		t.Fatal("expected non-zero balance immediately after charge")
	}
	// Advance past expiration and force a sweep via a charge for a
	// different key.
	now = now.Add(EntryExpiration + time.Second)
	tr.Charge("9.9.9.9", LoadReference)
	if got := tr.Balance("1.2.3.4"); got != 0 {
		t.Fatalf("expected 1.2.3.4 to be evicted, got balance %v", got)
	}
}
