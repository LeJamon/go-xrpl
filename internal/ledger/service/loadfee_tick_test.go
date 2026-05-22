package service

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/feetrack"
)

// TestTickLoadFee_NoOverload_LowersToLoadBase pins the rippled
// LoadManager.cpp:177-186 lower-branch: when the overload signal is
// false the local fee decays back to LoadBase.
func TestTickLoadFee_NoOverload_LowersToLoadBase(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Pre-load the tracker: two raises lift local fee above LoadBase.
	ft := svc.feeTrack
	ft.RaiseLocalFee()
	ft.RaiseLocalFee()
	if got := ft.GetLocalFee(); got <= feetrack.LoadBase {
		t.Fatalf("setup: pre-tick local fee = %d; want > LoadBase", got)
	}

	// Fresh open ledger has no txs → TxQ feeEscalation collapses to
	// reference, so the tick fires the lower branch on every call. A
	// fixed number of ticks must drive the fee back to LoadBase.
	svc.mu.Lock()
	for i := 0; i < 80; i++ {
		svc.tickLoadFeeLocked()
	}
	svc.mu.Unlock()

	if got := ft.GetLocalFee(); got != feetrack.LoadBase {
		t.Fatalf("post-tick local fee = %d; want LoadBase=%d", got, feetrack.LoadBase)
	}
}

// TestTickLoadFee_NilTracker is a defensive no-op check: a Service
// without a tracker (legacy/test fixture, or any future codepath that
// nils it out) must not panic when the tick runs.
func TestTickLoadFee_NilTracker(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	svc.feeTrack = nil

	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.tickLoadFeeLocked() // must not panic
}
