package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestEngine_SetMode_StampsWrongLedgerSince checks the watchdog clock is set on
// entry to ModeWrongLedger, preserved while pinned (re-pins keep the same mode,
// so the continuous-time measurement survives a churning target), and cleared
// on exit.
func TestEngine_SetMode_StampsWrongLedgerSince(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())

	e.setMode(consensus.ModeObserving)
	if !e.wrongLedgerSince.IsZero() {
		t.Fatal("wrongLedgerSince must be zero outside ModeWrongLedger")
	}

	entered := a.now
	e.setMode(consensus.ModeWrongLedger)
	if !e.wrongLedgerSince.Equal(entered) {
		t.Fatalf("entering ModeWrongLedger must stamp wrongLedgerSince=%v, got %v", entered, e.wrongLedgerSince)
	}

	// A re-pin to a fresh target stays in ModeWrongLedger, so setMode is a
	// no-op and the original stamp is preserved — the watchdog measures
	// continuous time pinned, not time-on-this-hash.
	a.now = entered.Add(30 * time.Second)
	e.setMode(consensus.ModeWrongLedger)
	if !e.wrongLedgerSince.Equal(entered) {
		t.Fatalf("re-pin must preserve the original stamp %v, got %v", entered, e.wrongLedgerSince)
	}

	e.setMode(consensus.ModeObserving)
	if !e.wrongLedgerSince.IsZero() {
		t.Fatal("leaving ModeWrongLedger must clear wrongLedgerSince")
	}
}

// TestEngine_WrongLedgerStuckWatchdog_DropsToDegradedResync pins issue #1136:
// a node pinned in wrongLedger whose acquisition never reports a clean failure
// — because it livelocks, or because the network advances and each clean
// failure lands on a stale target the hatch no longer matches — would close no
// ledgers forever. The wall-clock watchdog drops it to a degraded resync once
// it has been pinned past wrongLedgerStuckTimeout, regardless.
func TestEngine_WrongLedgerStuckWatchdog_DropsToDegradedResync(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())

	start := a.now
	e.setMode(consensus.ModeWrongLedger)
	e.wrongLedgerID = consensus.LedgerID{0x01}

	// Before the timeout the watchdog must not fire. prevLedger is nil, so
	// checkLedger returns right after the watchdog gate — isolating it.
	a.now = start.Add(wrongLedgerStuckTimeout - time.Second)
	e.checkLedger()
	if e.mode != consensus.ModeWrongLedger {
		t.Fatalf("watchdog fired early at %v, mode=%v", a.now.Sub(start), e.mode)
	}

	// Model the wedge: the target keeps moving and the only failures reported
	// land on stale ledgers, so the clean-failure hatch never arms.
	e.wrongLedgerID = consensus.LedgerID{0x02}
	e.OnLedgerAcquireFailed(consensus.LedgerID{0x01}) // stale → no-op
	if e.wrongLedgerAcquireFailures != 0 {
		t.Fatalf("stale-target failure must not arm the hatch, got %d", e.wrongLedgerAcquireFailures)
	}

	// Past the timeout the watchdog drops to a degraded resync so closes resume.
	a.now = start.Add(wrongLedgerStuckTimeout + time.Second)
	e.checkLedger()

	if e.mode != consensus.ModeObserving {
		t.Fatalf("stuck watchdog must drop to ModeObserving, got %v", e.mode)
	}
	if a.GetOperatingMode() != consensus.OpModeTracking {
		t.Fatalf("stuck watchdog must demote OpModeFull→Tracking, got %v", a.GetOperatingMode())
	}
	if !a.Now().Before(e.degradedResyncUntil) {
		t.Fatal("degraded-resync cooldown must be armed so re-pinning is suppressed")
	}
	if e.wrongLedgerID != (consensus.LedgerID{}) {
		t.Fatal("watchdog must un-pin so checkLedger re-resolves after the cooldown")
	}
	if !e.wrongLedgerSince.IsZero() {
		t.Fatal("leaving ModeWrongLedger must clear the watchdog clock")
	}
}
