package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestEngine_OnLedgerAcquireFailed_StaysWrongLedger(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	id := consensus.LedgerID{0xAB}

	for i := 1; i <= 5; i++ {
		e.mode = consensus.ModeWrongLedger
		e.wrongLedgerID = id
		e.OnLedgerAcquireFailed(id)

		if e.mode != consensus.ModeWrongLedger {
			t.Fatalf("failure %d must keep retrying in wrongLedger, got mode %v", i, e.mode)
		}
		if e.wrongLedgerID != (consensus.LedgerID{}) {
			t.Fatalf("failure %d must clear the pin so checkLedger re-resolves and re-requests", i)
		}
		if a.GetOperatingMode() != consensus.OpModeFull {
			t.Fatalf("failure %d must not mutate operating mode, got %v", i, a.GetOperatingMode())
		}
	}
}

func TestEngine_WrongLedgerDoesNotExpireWithTime(t *testing.T) {
	a := newMockAdaptor()
	e := NewEngine(a, DefaultConfig())
	e.mode = consensus.ModeWrongLedger
	e.modeAtomic.Store(int32(consensus.ModeWrongLedger))
	e.wrongLedgerID = consensus.LedgerID{0xAB}
	a.mu.Lock()
	a.now = a.now.Add(2 * time.Hour)
	a.mu.Unlock()

	e.timerEntry()

	if got := e.Mode(); got != consensus.ModeWrongLedger {
		t.Fatalf("elapsed time must not release wrongLedger, got %v", got)
	}
	if e.wrongLedgerID != (consensus.LedgerID{0xAB}) {
		t.Fatal("elapsed time must not clear the active acquisition pin")
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
	if e.mode != consensus.ModeObserving {
		t.Fatal("a failure must be ignored when the engine is not pinned in wrongLedger")
	}

	// Pinned, but on a different ledger than the one that failed.
	e.mode = consensus.ModeWrongLedger
	e.wrongLedgerID = consensus.LedgerID{0x01}
	e.OnLedgerAcquireFailed(consensus.LedgerID{0x02})
	if e.wrongLedgerID != (consensus.LedgerID{0x01}) {
		t.Fatal("a failure for a different ledger must not disturb the current pin")
	}
}
