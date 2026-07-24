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

func TestModeManager_OnEvent_WrongLedgerToConnected(t *testing.T) {
	mm := newTestModeManager(t)
	mm.SetMode(consensus.OpModeFull)

	mm.OnEvent(&consensus.ModeChangedEvent{
		OldMode: consensus.ModeProposing,
		NewMode: consensus.ModeWrongLedger,
	})

	if mm.Mode() != consensus.OpModeConnected {
		t.Fatalf("ModeChangedEvent → wrongLedger must trigger "+
			"Full→Connected transition; got OperatingMode=%v "+
			"— ModeManager.OnEvent is not wired (#401)",
			mm.Mode())
	}
}

func TestModeManager_OnEvent_LeavingWrongLedgerDoesNotPromote(t *testing.T) {
	mm := newTestModeManager(t)
	mm.SetMode(consensus.OpModeConnected)

	mm.OnEvent(&consensus.ModeChangedEvent{
		OldMode: consensus.ModeWrongLedger,
		NewMode: consensus.ModeSwitchedLedger,
	})

	if mm.Mode() != consensus.OpModeConnected {
		t.Fatalf("leaving wrongLedger must not promote operating mode; got %v", mm.Mode())
	}
}

// TestModeManager_OnEvent_BypassedStateMachine pins the issue #401
// behavior: in production, OperatingMode is promoted to Full by direct
// SetOperatingMode calls in router paths, not through ModeManager. So m.mode can lag while
// adaptor.GetOperatingMode() returns Full. When a
// ModeChangedEvent{wrongLedger} fires, OnEvent MUST consult the adaptor's
// actual opMode and trigger Full → Connected — otherwise the engine drops
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

	if got := mm.Mode(); got != consensus.OpModeConnected {
		t.Fatalf("ModeChangedEvent{wrongLedger} when adaptor.opMode "+
			"is Full must transition to Connected regardless of "+
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
