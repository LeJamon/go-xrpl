package loadtrack

import (
	"testing"
	"time"
)

func TestCharge_NoKeyAlwaysOK(t *testing.T) {
	tr := New()
	for i := range 100 {
		if got := tr.Charge("", LoadHeavy); got != OutcomeOK {
			t.Fatalf("empty key must always be OK, got %v on iter %d", got, i)
		}
	}
}

func TestCharge_ReferenceStaysBelowWarning(t *testing.T) {
	tr := New()
	// The raw sample is normalized by the 32-second decay window.
	for i := range 200 {
		if got := tr.Charge("1.2.3.4", LoadReference); got != OutcomeOK {
			t.Fatalf("got %v at iter %d, balance %v", got, i, tr.Balance("1.2.3.4"))
		}
	}
}

func TestCharge_HeavyCrossesWarnThenDrop(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	for i := range 53 {
		if got := tr.Charge("1.2.3.4", LoadHeavy); got != OutcomeOK {
			t.Fatalf("charge %d: expected OK, got %v", i+1, got)
		}
	}
	if got := tr.Charge("1.2.3.4", LoadHeavy); got != OutcomeWarn {
		t.Fatalf("charge 54: expected Warn, got %v (balance %v)", got, tr.Balance("1.2.3.4"))
	}
	for range 300 {
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
	tr.Charge("1.2.3.4", LoadKind(3200))
	if got := tr.Balance("1.2.3.4"); got != 100 {
		t.Fatalf("initial normalized balance = %v, want 100", got)
	}
	now = now.Add(time.Second)
	if got := tr.Balance("1.2.3.4"); got != 96 {
		t.Fatalf("one-second normalized balance = %v, want 96", got)
	}
	now = now.Add(4*DecayWindow + time.Second)
	if got := tr.Balance("1.2.3.4"); got != 0 {
		t.Fatalf("balance after reset horizon = %v, want 0", got)
	}
}

func TestCharge_PerKeyIsolated(t *testing.T) {
	tr := New()
	for range 20 {
		tr.Charge("hot", LoadHeavy)
	}
	if got := tr.Charge("cold", LoadReference); got != OutcomeOK {
		t.Fatalf("cold IP should not be affected by hot IP, got %v", got)
	}
}

func TestSweep_EvictsIdleEntries(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr := newWithClock(func() time.Time { return now })
	tr.Charge("1.2.3.4", LoadKind(decayWindowSeconds))
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
