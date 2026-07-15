package service

import (
	"sync"
	"testing"
	"time"
)

// TestLedgerEventDispatch_FIFOAndSingleThreaded proves the accepted-ledger event
// dispatcher delivers events in emit order and never runs the callback
// concurrently with itself. Under -race the unguarded slice append also flags
// any concurrent delivery, catching a regression to per-event goroutines.
func TestLedgerEventDispatch_FIFOAndSingleThreaded(t *testing.T) {
	svc, err := New(Config{Standalone: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(svc.Stop)

	const n = 50
	var (
		mu       sync.Mutex
		got      []uint32
		inFlight int
		done     = make(chan struct{})
	)
	svc.SetEventCallback(func(event *LedgerAcceptedEvent) {
		mu.Lock()
		inFlight++
		if inFlight != 1 {
			t.Errorf("callback ran concurrently with itself (inFlight=%d)", inFlight)
		}
		got = append(got, event.LedgerInfo.Sequence)
		last := len(got) == n
		inFlight--
		mu.Unlock()
		if last {
			close(done)
		}
	})

	for i := range n {
		svc.dispatchLedgerEvent(&LedgerAcceptedEvent{LedgerInfo: &LedgerInfo{Sequence: uint32(i)}})
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for all events to be delivered")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Fatalf("received %d events, want %d", len(got), n)
	}
	for i, seq := range got {
		if seq != uint32(i) {
			t.Fatalf("out-of-order delivery at index %d: got seq %d, want %d\nfull order: %v", i, seq, i, got)
		}
	}
}
