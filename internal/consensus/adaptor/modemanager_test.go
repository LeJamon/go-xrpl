package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/assert"
)

func newTestModeManager(t *testing.T) *ModeManager {
	t.Helper()
	a := newTestAdaptor(t)
	return NewModeManager(a)
}

func TestModeManagerInitialState(t *testing.T) {
	mm := newTestModeManager(t)
	assert.Equal(t, consensus.OpModeDisconnected, mm.Mode())
}

func TestModeManagerForceSetMode(t *testing.T) {
	mm := newTestModeManager(t)

	mm.SetMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, mm.Mode())
	// SetMode also drives the adaptor's operating mode.
	assert.Equal(t, consensus.OpModeFull, mm.adaptor.GetOperatingMode())
}

// TestModeManager_OnEvent_WrongLedgerToSyncing pins the issue #401
// wiring: when the engine fires ModeChangedEvent with
// NewMode=ModeWrongLedger, the mode manager must transition the
// network-level OperatingMode from Full to Syncing — so
// startRoundLocked stops promoting us to ModeProposing on the next
// round. Without this subscription a node that detects a wrong LCL
// silently keeps proposing on its side chain.
func TestModeManager_OnEvent_WrongLedgerToSyncing(t *testing.T) {
	mm := newTestModeManager(t)
	mm.SetMode(consensus.OpModeFull)

	mm.OnEvent(&consensus.ModeChangedEvent{
		OldMode: consensus.ModeProposing,
		NewMode: consensus.ModeWrongLedger,
	})

	if mm.Mode() != consensus.OpModeSyncing {
		t.Fatalf("ModeChangedEvent → wrongLedger must trigger "+
			"Full→Syncing transition; got OperatingMode=%v "+
			"— ModeManager.OnEvent is not wired (#401)",
			mm.Mode())
	}
}

// TestModeManager_OnEvent_LeavingWrongLedgerBumpsToTracking pins
// the recovery half of the wiring: when the engine fires
// ModeChangedEvent with OldMode=ModeWrongLedger and a non-wrong
// new mode (we acquired the right LCL and are about to run a
// switchedLedger / proposing / observing round), the mode manager
// must bump us from Syncing to Tracking. Tracking is still
// non-Full, so we don't immediately re-enter proposing.
func TestModeManager_OnEvent_LeavingWrongLedgerBumpsToTracking(t *testing.T) {
	mm := newTestModeManager(t)
	mm.SetMode(consensus.OpModeSyncing)

	mm.OnEvent(&consensus.ModeChangedEvent{
		OldMode: consensus.ModeWrongLedger,
		NewMode: consensus.ModeSwitchedLedger,
	})

	if mm.Mode() != consensus.OpModeTracking {
		t.Fatalf("leaving wrongLedger must bump Syncing→Tracking; "+
			"got %v — ModeManager.OnEvent recovery branch "+
			"missing (#401)", mm.Mode())
	}
}

// TestModeManager_OnEvent_BypassedStateMachine pins the issue #401
// behavior: in production, OperatingMode is promoted to Full by direct
// SetOperatingMode calls in router.go and adaptor.AdoptLedgerFromHeader,
// NOT through ModeManager. So m.mode can lag while
// adaptor.GetOperatingMode() returns Full. When a
// ModeChangedEvent{wrongLedger} fires, OnEvent MUST consult the adaptor's
// actual opMode and trigger Full → Syncing — otherwise the engine drops
// to wrongLedger silently while opMode stays at Full and startRoundLocked
// keeps re-promoting us to ModeProposing.
func TestModeManager_OnEvent_BypassedStateMachine(t *testing.T) {
	mm := newTestModeManager(t)
	mm.SetMode(consensus.OpModeConnected)
	// Diverge m.mode (Connected) from the adaptor's authoritative
	// opMode (Full), as a direct production SetOperatingMode would.
	mm.adaptor.SetOperatingMode(consensus.OpModeFull)

	if mm.Mode() != consensus.OpModeConnected {
		t.Fatalf("preconditions: m.mode want Connected, got %v", mm.Mode())
	}
	if mm.adaptor.GetOperatingMode() != consensus.OpModeFull {
		t.Fatalf("preconditions: adaptor opMode want Full, got %v",
			mm.adaptor.GetOperatingMode())
	}

	mm.OnEvent(&consensus.ModeChangedEvent{
		OldMode: consensus.ModeProposing,
		NewMode: consensus.ModeWrongLedger,
	})

	if got := mm.Mode(); got != consensus.OpModeSyncing {
		t.Fatalf("ModeChangedEvent{wrongLedger} when adaptor.opMode "+
			"is Full must transition to Syncing regardless of "+
			"m.mode; got %v — bypassed-state-machine path "+
			"silently no-op'd (#401)", got)
	}
}

func TestModeManager_OnEvent_IgnoresUnrelatedEvents(t *testing.T) {
	mm := newTestModeManager(t)
	mm.SetMode(consensus.OpModeFull)
	beforeMode := mm.Mode()

	mm.OnEvent(&consensus.PhaseChangedEvent{
		OldPhase: consensus.PhaseOpen,
		NewPhase: consensus.PhaseEstablish,
	})
	mm.OnEvent(&consensus.RoundStartedEvent{})

	if mm.Mode() != beforeMode {
		t.Fatalf("unrelated events must not change OperatingMode; "+
			"before=%v after=%v", beforeMode, mm.Mode())
	}
}
