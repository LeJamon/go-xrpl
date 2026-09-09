package adaptor

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestAdaptorLedgerAcceptWorkerIsAsynchronousAndSingleFlight(t *testing.T) {
	a := &Adaptor{}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	queued := make(chan struct{})
	var calls atomic.Int32

	if !a.DeferLedgerAccept(func() {
		calls.Add(1)
		close(started)
		<-release
		close(finished)
	}) {
		t.Fatal("first ledger acceptance was not deferred")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("acceptance worker did not start")
	}
	if !a.DeferLedgerAccept(func() {
		calls.Add(1)
		close(queued)
	}) {
		t.Fatal("one bounded acceptance slot should remain behind the running build")
	}
	if a.DeferLedgerAccept(func() { calls.Add(1) }) {
		t.Fatal("third ledger acceptance exceeded the bounded queue")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- a.StopLedgerAccept() }()
	select {
	case <-stopDone:
		t.Fatal("StopLedgerAccept returned before the in-flight build completed")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("deferred acceptance did not complete")
	}
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("queued acceptance was not drained")
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopLedgerAccept: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StopLedgerAccept did not join the worker")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("completion calls = %d, want 2", got)
	}

	var rejectedCalls atomic.Int32
	if a.DeferLedgerAccept(func() { rejectedCalls.Add(1) }) {
		t.Fatal("ledger acceptance admitted after shutdown")
	}
	a.StopLedgerAccept()
	if got := rejectedCalls.Load(); got != 0 {
		t.Fatalf("rejected completion calls = %d, want 0", got)
	}
}

func TestAdaptorLedgerAcceptWorkerRunsAdmittedCallbackDuringShutdown(t *testing.T) {
	a := &Adaptor{}
	completed := make(chan struct{})
	var calls atomic.Int32
	if !a.DeferLedgerAccept(func() {
		calls.Add(1)
		close(completed)
	}) {
		t.Fatal("ledger acceptance was not deferred")
	}

	if err := a.StopLedgerAccept(); err != nil {
		t.Fatalf("StopLedgerAccept: %v", err)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("admitted completion was abandoned during shutdown")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("completion calls = %d, want 1", got)
	}
}

func TestAdaptorLedgerAcceptWorkerStopsBeforeFirstAdmission(t *testing.T) {
	a := &Adaptor{}
	if err := a.StopLedgerAccept(); err != nil {
		t.Fatalf("StopLedgerAccept: %v", err)
	}
	called := make(chan struct{})
	if a.DeferLedgerAccept(func() { close(called) }) {
		t.Fatal("ledger acceptance admitted after pre-start shutdown")
	}
	select {
	case <-called:
		t.Fatal("rejected completion was retained after pre-start shutdown")
	default:
	}
}
