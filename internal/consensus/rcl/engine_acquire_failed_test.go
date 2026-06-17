package rcl

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestEngine_OnLedgerAcquireFailed_UnpinsThenDegrades pins issue #985 part C:
// repeated clean acquisition failures of the pinned wrongLedger first un-pin
// (so checkLedger re-resolves and re-requests) and finally drop the node to a
// degraded observing/tracking resync, so it keeps closing ledgers instead of
// starving the stall watchdog into a fatal os.Exit.
func TestEngine_OnLedgerAcquireFailed_UnpinsThenDegrades(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	id := consensus.LedgerID{0xAB}

	// Each production failure is preceded by checkLedger re-pinning the target;
	// mimic that round-trip. The first failures un-pin without degrading.
	for i := 1; i < wrongLedgerAcquireMaxFailures; i++ {
		e.mode = consensus.ModeWrongLedger
		e.wrongLedgerID = id
		e.OnLedgerAcquireFailed(id)

		if e.mode != consensus.ModeWrongLedger {
			t.Fatalf("failure %d must keep retrying in wrongLedger, got mode %v", i, e.mode)
		}
		if e.wrongLedgerID != (consensus.LedgerID{}) {
			t.Fatalf("failure %d must clear the pin so checkLedger re-resolves and re-requests", i)
		}
	}

	// The final failure drops to a degraded resync.
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = id
	e.OnLedgerAcquireFailed(id)

	if e.mode != consensus.ModeObserving {
		t.Fatalf("persistent failure must drop to ModeObserving, got %v", e.mode)
	}
	if a.GetOperatingMode() != consensus.OpModeTracking {
		t.Fatalf("persistent failure must demote OpModeFull→Tracking, got %v", a.GetOperatingMode())
	}
	if !e.adaptor.Now().Before(e.degradedResyncUntil) {
		t.Fatal("degraded-resync cooldown must be armed so re-pinning is suppressed")
	}
	if e.wrongLedgerAcquireFailures != 0 {
		t.Fatalf("the failure counter must reset on degrade, got %d", e.wrongLedgerAcquireFailures)
	}
}

// TestEngine_OnLedgerAcquireFailed_IgnoredWhenNotPinned confirms the signal is a
// no-op unless the engine is pinned in wrongLedger on exactly that ledger, so a
// stale or unrelated acquisition failure can't disturb a healthy node.
func TestEngine_OnLedgerAcquireFailed_IgnoredWhenNotPinned(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())

	// Not in wrongLedger mode.
	e.mode = consensus.ModeObserving
	e.OnLedgerAcquireFailed(consensus.LedgerID{0xAB})
	if e.mode != consensus.ModeObserving || e.wrongLedgerAcquireFailures != 0 {
		t.Fatal("a failure must be ignored when the engine is not pinned in wrongLedger")
	}

	// Pinned, but on a different ledger than the one that failed.
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = consensus.LedgerID{0x01}
	e.OnLedgerAcquireFailed(consensus.LedgerID{0x02})
	if e.wrongLedgerID != (consensus.LedgerID{0x01}) || e.wrongLedgerAcquireFailures != 0 {
		t.Fatal("a failure for a different ledger must not disturb the current pin")
	}
}
