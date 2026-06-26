package service

import (
	"context"
	"sync/atomic"
	"testing"
)

// The ledger close path must ping the installed stall watchdog callback, so the
// out-of-band watchdog observes that ledger processing is making progress.
func TestService_StallPingFiresOnLedgerClose(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start service: %v", err)
	}

	var pings atomic.Int64
	svc.SetStallPing(func() { pings.Add(1) })

	if _, err := svc.AcceptLedger(context.TODO()); err != nil {
		t.Fatalf("accept ledger: %v", err)
	}
	if got := pings.Load(); got == 0 {
		t.Fatal("ledger close did not ping the stall watchdog")
	}

	// A second close pings again.
	before := pings.Load()
	if _, err := svc.AcceptLedger(context.TODO()); err != nil {
		t.Fatalf("accept ledger: %v", err)
	}
	if pings.Load() <= before {
		t.Fatalf("second close did not ping: %d <= %d", pings.Load(), before)
	}
}

// Clearing the ping with nil is safe and stops further pings.
func TestService_StallPingNilIsSafe(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start service: %v", err)
	}

	svc.SetStallPing(func() {})
	svc.SetStallPing(nil)

	// Must not panic with no ping installed.
	if _, err := svc.AcceptLedger(context.TODO()); err != nil {
		t.Fatalf("accept ledger: %v", err)
	}
}
