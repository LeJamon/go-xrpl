package service

import (
	"testing"
)

// TestFilterApplicableTxs_NilParent guards the contract that a nil
// parent ledger short-circuits to nil rather than panicking. Engine
// callers may hit this during the bootstrap window before prevLedger
// is set.
func TestFilterApplicableTxs_NilParent(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := svc.FilterApplicableTxs(nil, [][]byte{{1, 2, 3}}); got != nil {
		t.Errorf("expected nil for nil parent, got %d blobs", len(got))
	}
}

// TestFilterApplicableTxs_EmptyInput proves the no-tx path is a clean
// no-op: no apply work, no error, returns nil.
func TestFilterApplicableTxs_EmptyInput(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("no closed ledger after Start")
	}
	if got := svc.FilterApplicableTxs(parent, nil); got != nil {
		t.Errorf("expected nil for empty input, got %d blobs", len(got))
	}
	if got := svc.FilterApplicableTxs(parent, [][]byte{}); got != nil {
		t.Errorf("expected nil for empty slice, got %d blobs", len(got))
	}
}

// TestFilterApplicableTxs_AllUnparseableDropped proves the filter
// drops every blob that can't be parsed — matching rippled's
// open-ledger behavior where a malformed tx never enters the open
// view in the first place. Without this, goxrpl would propose
// garbage that no peer can validate.
func TestFilterApplicableTxs_AllUnparseableDropped(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("no closed ledger after Start")
	}
	junk := [][]byte{
		{0xFF},
		{0xDE, 0xAD, 0xBE, 0xEF},
		{},
	}
	got := svc.FilterApplicableTxs(parent, junk)
	if len(got) != 0 {
		t.Errorf("expected 0 applicable blobs from junk input, got %d", len(got))
	}
}

// TestFilterApplicableTxs_NoSideEffects proves the filter is
// side-effect-free: openLedger, closedLedger, and pendingTxs are
// unchanged by a call. The engine's closeLedger calls this every
// round; mutation here would corrupt the build path.
func TestFilterApplicableTxs_NoSideEffects(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("no closed ledger after Start")
	}

	beforeOpen := svc.GetCurrentLedgerIndex()
	beforeClosed := svc.GetClosedLedgerIndex()
	beforePending := len(svc.GetPendingTxBlobs())

	junk := [][]byte{{0xFF}, {0xDE, 0xAD}}
	_ = svc.FilterApplicableTxs(parent, junk)

	if got := svc.GetCurrentLedgerIndex(); got != beforeOpen {
		t.Errorf("openLedger seq drifted: was %d, now %d", beforeOpen, got)
	}
	if got := svc.GetClosedLedgerIndex(); got != beforeClosed {
		t.Errorf("closedLedger seq drifted: was %d, now %d", beforeClosed, got)
	}
	if got := len(svc.GetPendingTxBlobs()); got != beforePending {
		t.Errorf("pendingTxs len drifted: was %d, now %d", beforePending, got)
	}
}
