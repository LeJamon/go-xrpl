package inbound

import (
	"log/slog"
	"testing"
)

func TestTracker_GetOrCreateDedupesByHash(t *testing.T) {
	tr := NewTracker()
	h := hashN(1)

	calls := 0
	factory := func() *Ledger {
		calls++
		return New(h, 10, 7, slog.Default())
	}

	first, created := tr.GetOrCreate(h, factory)
	if !created || first == nil {
		t.Fatalf("first GetOrCreate: created=%v ledger=%v", created, first)
	}
	second, created2 := tr.GetOrCreate(h, factory)
	if created2 {
		t.Fatalf("second GetOrCreate must reuse the in-flight acquisition")
	}
	if second != first {
		t.Fatalf("second GetOrCreate returned a different ledger")
	}
	if calls != 1 {
		t.Fatalf("factory ran %d times, want 1", calls)
	}
	if got := tr.Find(h); got != first {
		t.Fatalf("Find returned %v, want %v", got, first)
	}
}

func TestTracker_GetOrCreateNilFactory(t *testing.T) {
	tr := NewTracker()
	h := hashN(2)
	l, created := tr.GetOrCreate(h, func() *Ledger { return nil })
	if l != nil || created {
		t.Fatalf("nil factory must yield (nil,false), got (%v,%v)", l, created)
	}
	if tr.Find(h) != nil {
		t.Fatalf("a nil acquisition must not be registered")
	}
}

func TestTracker_RemoveComplete(t *testing.T) {
	tr := NewTracker()
	h := hashN(3)
	tr.GetOrCreate(h, func() *Ledger { return New(h, 20, 7, slog.Default()) })

	tr.Remove(h, true)
	if tr.Find(h) != nil {
		t.Fatalf("Remove must drop the acquisition from the in-flight set")
	}
	info := tr.Info()
	// A real (post-genesis) sequence keys by decimal seq.
	if _, ok := info["20"]; !ok {
		t.Fatalf("completed acquisition must still appear in fetch_info, got %v", info)
	}
	if entry := info["20"].(map[string]any); entry["complete"] != true {
		t.Fatalf("completed entry must report complete:true, got %v", entry)
	}
}

func TestTracker_RemoveFailure(t *testing.T) {
	tr := NewTracker()
	h := hashN(4)
	tr.GetOrCreate(h, func() *Ledger { return New(h, 21, 7, slog.Default()) })

	tr.Remove(h, false)
	if tr.Find(h) != nil {
		t.Fatalf("Remove must drop the acquisition from the in-flight set")
	}
	info := tr.Info()
	entry, ok := info["21"].(map[string]any)
	if !ok {
		t.Fatalf("failed acquisition must appear in fetch_info, got %v", info)
	}
	if entry["failed"] != true {
		t.Fatalf("failed entry must report failed:true, got %v", entry)
	}
}

func TestTracker_RemoveUnknownIsNoop(t *testing.T) {
	tr := NewTracker()
	tr.Remove(hashN(9), true) // must not panic
	if len(tr.Info()) != 0 {
		t.Fatalf("removing an unknown hash must not record anything")
	}
}
