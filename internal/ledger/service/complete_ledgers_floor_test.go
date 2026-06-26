package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
)

// seedHistory replaces the in-memory history window with placeholder ledgers
// for [lo, hi], so GetServerInfo derives complete_ledgers from a known range.
// Start() seeds the genesis ledger; clear it first so the range is exactly
// [lo, hi].
func seedHistory(t *testing.T, svc *Service, lo, hi uint32) {
	t.Helper()
	svc.ledgerHistory = make(map[uint32]*ledger.Ledger)
	for seq := lo; seq <= hi; seq++ {
		stateMap, err := svc.genesisLedger.StateMapSnapshot()
		if err != nil {
			t.Fatalf("StateMapSnapshot: %v", err)
		}
		txMap, err := svc.genesisLedger.TxMapSnapshot()
		if err != nil {
			t.Fatalf("TxMapSnapshot: %v", err)
		}
		var h header.LedgerHeader
		h.LedgerIndex = seq
		svc.ledgerHistory[seq] = ledger.NewOpenWithHeader(h, stateMap, txMap, drops.Fees{})
	}
}

// TestGetServerInfo_CompleteLedgers_ClampedToFloor verifies that after a
// rotation, complete_ledgers reports the durable lower bound (the online-delete
// floor), not the broader in-memory history window — which is swept
// independently and can still name reclaimed ledgers.
func TestGetServerInfo_CompleteLedgers_ClampedToFloor(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	seedHistory(t, svc, 10, 100)

	// Without a floor the range is the full window.
	if got := svc.GetServerInfo().CompleteLedgers; got != "10-100" {
		t.Fatalf("unclamped complete_ledgers = %q, want %q", got, "10-100")
	}

	// A floor at 50 narrows the lower bound; the upper bound is unchanged.
	svc.SetMinimumOnlineFunc(func() uint32 { return 50 })
	if got := svc.GetServerInfo().CompleteLedgers; got != "50-100" {
		t.Fatalf("clamped complete_ledgers = %q, want %q", got, "50-100")
	}
}

// TestGetServerInfo_CompleteLedgers_FloorBelowWindow verifies a floor below the
// window's lower bound (or zero, no rotation yet) leaves the range untouched.
func TestGetServerInfo_CompleteLedgers_FloorBelowWindow(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	seedHistory(t, svc, 10, 100)

	svc.SetMinimumOnlineFunc(func() uint32 { return 5 }) // below window lo
	if got := svc.GetServerInfo().CompleteLedgers; got != "10-100" {
		t.Fatalf("floor below window changed range = %q, want %q", got, "10-100")
	}

	svc.SetMinimumOnlineFunc(func() uint32 { return 0 }) // no rotation yet
	if got := svc.GetServerInfo().CompleteLedgers; got != "10-100" {
		t.Fatalf("zero floor changed range = %q, want %q", got, "10-100")
	}
}

// TestGetServerInfo_CompleteLedgers_FloorAboveWindow verifies that when the
// floor exceeds the whole window (nothing durable left to advertise),
// complete_ledgers is reported empty rather than as an inverted range.
func TestGetServerInfo_CompleteLedgers_FloorAboveWindow(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	seedHistory(t, svc, 10, 100)

	svc.SetMinimumOnlineFunc(func() uint32 { return 200 }) // above window hi
	if got := svc.GetServerInfo().CompleteLedgers; got != "" {
		t.Fatalf("floor above window = %q, want empty", got)
	}
}
