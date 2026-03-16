package rpc

import "testing"

func TestConnLimiter_Unlimited(t *testing.T) {
	cl := NewConnLimiter()
	// limit=0 means unlimited
	for i := 0; i < 1000; i++ {
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
