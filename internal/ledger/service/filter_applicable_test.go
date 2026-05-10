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
// side-effect-free: every piece of mutable Service state the multi-pass
// apply COULD touch must be unchanged by a call. The engine's
// closeLedger calls this every round; mutation here would corrupt the
// build path that runs immediately after.
//
// Pinned invariants (each one a separate failure mode if violated):
//   1. openLedger pointer identity unchanged (helper rebuilds a private
//      freshLedger; assigning it to s.openLedger would silently swap
//      out the engine's open view mid-round).
//   2. openLedger StateMap root unchanged (a leaked AddTransactionWithMeta
//      would mutate the in-memory state tree even if the pointer stayed).
//   3. closedLedger pointer identity unchanged.
//   4. ledgerHistory size unchanged (no spurious by-seq insert).
//   5. txIndex size unchanged (no spurious tx → seq mapping).
//   6. pendingTxs length unchanged.
//
// Junk blobs alone don't reach AddTransactionWithMeta (parsing rejects
// them), but the test pins the surface so that a future change which
// makes synthetic blobs parseable can't silently start mutating these
// fields without tripping the assertion.
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

	svc.mu.RLock()
	beforeOpenPtr := svc.openLedger
	beforeClosedPtr := svc.closedLedger
	beforeHistoryLen := len(svc.ledgerHistory)
	beforeTxIndexLen := len(svc.txIndex)
	svc.mu.RUnlock()

	beforeOpenStateRoot, err := beforeOpenPtr.StateMapHash()
	if err != nil {
		t.Fatalf("StateMapHash before: %v", err)
	}
	beforePending := len(svc.GetPendingTxBlobs())

	junk := [][]byte{{0xFF}, {0xDE, 0xAD}, make([]byte, 32)}
	_ = svc.FilterApplicableTxs(parent, junk)

	svc.mu.RLock()
	afterOpenPtr := svc.openLedger
	afterClosedPtr := svc.closedLedger
	afterHistoryLen := len(svc.ledgerHistory)
	afterTxIndexLen := len(svc.txIndex)
	svc.mu.RUnlock()

	afterOpenStateRoot, err := afterOpenPtr.StateMapHash()
	if err != nil {
		t.Fatalf("StateMapHash after: %v", err)
	}

	if afterOpenPtr != beforeOpenPtr {
		t.Errorf("openLedger pointer changed (filter swapped the engine's "+
			"open view mid-round): before=%p after=%p", beforeOpenPtr, afterOpenPtr)
	}
	if afterOpenStateRoot != beforeOpenStateRoot {
		t.Errorf("openLedger StateMap root drifted: before=%x after=%x — "+
			"filter leaked an AddTransactionWithMeta into the live state tree",
			beforeOpenStateRoot[:8], afterOpenStateRoot[:8])
	}
	if afterClosedPtr != beforeClosedPtr {
		t.Errorf("closedLedger pointer changed: before=%p after=%p",
			beforeClosedPtr, afterClosedPtr)
	}
	if afterHistoryLen != beforeHistoryLen {
		t.Errorf("ledgerHistory size drifted: %d → %d (spurious by-seq write)",
			beforeHistoryLen, afterHistoryLen)
	}
	if afterTxIndexLen != beforeTxIndexLen {
		t.Errorf("txIndex size drifted: %d → %d (spurious tx → seq mapping)",
			beforeTxIndexLen, afterTxIndexLen)
	}
	if got := len(svc.GetPendingTxBlobs()); got != beforePending {
		t.Errorf("pendingTxs len drifted: %d → %d", beforePending, got)
	}
}
