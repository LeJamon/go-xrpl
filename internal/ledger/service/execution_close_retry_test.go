package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/rootlease"
)

func TestServiceStopRetriesOwnedExecutionCloseAfterLeaseRelease(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	store := svc.GetOpenLedger().ExecutionStore()
	if store == nil {
		t.Fatal("open ledger has no execution store")
	}
	var held rootlease.Root
	for _, root := range store.Roots() {
		if root.Hash != ([32]byte{}) {
			held = root
			break
		}
	}
	if held.Hash == ([32]byte{}) {
		t.Fatal("open ledger has no rooted execution state")
	}
	release, err := store.AcquireRoot(held)
	if err != nil {
		t.Fatalf("AcquireRoot: %v", err)
	}
	t.Cleanup(func() {
		release()
		svc.Stop()
	})

	svc.Stop()
	release()
	release = func() {}
	svc.Stop()

	if _, err := store.AcquireRoot(held); err == nil {
		t.Fatal("execution store remained open after the retry")
	}
}
