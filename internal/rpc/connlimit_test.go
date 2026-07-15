package rpc

import "testing"

func TestConnLimiter_Unlimited(t *testing.T) {
	cl := NewConnLimiter()
	// limit=0 means unlimited
	for i := range 1000 {
		if !cl.TryAcquire("port1", 0) {
			t.Fatalf("TryAcquire should always succeed with limit=0, failed at i=%d", i)
		}
	}
}

func TestConnLimiter_EnforcesLimit(t *testing.T) {
	cl := NewConnLimiter()
	if !cl.TryAcquire("port1", 2) {
		t.Fatal("first acquire should succeed")
	}
	if !cl.TryAcquire("port1", 2) {
		t.Fatal("second acquire should succeed")
	}
	if cl.TryAcquire("port1", 2) {
		t.Fatal("third acquire should fail (limit=2)")
	}
}

func TestConnLimiter_ReleaseFreesSlot(t *testing.T) {
	cl := NewConnLimiter()
	cl.TryAcquire("port1", 1)
	if cl.TryAcquire("port1", 1) {
		t.Fatal("should be at limit")
	}
	cl.Release("port1")
	if !cl.TryAcquire("port1", 1) {
		t.Fatal("should succeed after release")
	}
}

func TestConnLimiter_PerPort(t *testing.T) {
	cl := NewConnLimiter()
	cl.TryAcquire("port1", 1)
	// Different port should not be affected
	if !cl.TryAcquire("port2", 1) {
		t.Fatal("port2 should be independent of port1")
	}
}

func TestConnLimiter_ReleaseNoUnderflow(t *testing.T) {
	cl := NewConnLimiter()
	// Release on empty port should not panic or go negative
	cl.Release("port1")
	if cl.Count("port1") != 0 {
		t.Fatal("count should stay at 0")
	}
}

// TestConnLimiter_GlobalCeiling verifies the process-wide ceiling bounds total
// connections across ports even when every per-port limit is unset (0), which
// is the memory-DoS surface the ceiling closes.
func TestConnLimiter_GlobalCeiling(t *testing.T) {
	cl := NewConnLimiter()
	cl.SetGlobalLimit(3)

	// Spread unlimited-per-port acquisitions across distinct ports.
	if !cl.TryAcquire("ws1", 0) || !cl.TryAcquire("ws2", 0) || !cl.TryAcquire("ws3", 0) {
		t.Fatal("first three acquisitions should succeed under the global ceiling")
	}
	if cl.TryAcquire("ws4", 0) {
		t.Fatal("acquisition past the global ceiling must fail despite per-port limit=0")
	}
	if cl.Total() != 3 {
		t.Fatalf("total = %d, want 3", cl.Total())
	}
	// A release frees a global slot.
	cl.Release("ws1")
	if !cl.TryAcquire("ws5", 0) {
		t.Fatal("acquisition should succeed after a release frees a global slot")
	}
}

// TestConnLimiter_DefaultBounded pins that a fresh limiter is bounded by default
// rather than unlimited, so an operator who never sets a limit is still
// protected.
func TestConnLimiter_DefaultBounded(t *testing.T) {
	cl := NewConnLimiter()
	for i := range DefaultMaxTotalConnections {
		if !cl.TryAcquire("ws", 0) {
			t.Fatalf("acquire failed early at i=%d, want up to the default ceiling", i)
		}
	}
	if cl.TryAcquire("ws", 0) {
		t.Fatal("acquisition past the default ceiling must fail")
	}
}

// TestConnLimiter_GlobalDisabled confirms a negative override restores the
// unlimited-global behaviour (per-port limits still apply).
func TestConnLimiter_GlobalDisabled(t *testing.T) {
	cl := NewConnLimiter()
	cl.SetGlobalLimit(-1)
	for i := range DefaultMaxTotalConnections + 100 {
		if !cl.TryAcquire("ws", 0) {
			t.Fatalf("acquire failed at i=%d with the global cap disabled", i)
		}
	}
}

func TestConnLimiter_Count(t *testing.T) {
	cl := NewConnLimiter()
	cl.TryAcquire("port1", 0)
	cl.TryAcquire("port1", 0)
	if cl.Count("port1") != 2 {
		t.Fatalf("expected count 2, got %d", cl.Count("port1"))
	}
	cl.Release("port1")
	if cl.Count("port1") != 1 {
		t.Fatalf("expected count 1, got %d", cl.Count("port1"))
	}
}
