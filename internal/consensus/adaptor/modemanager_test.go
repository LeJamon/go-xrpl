package adaptor

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
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

func TestModeManagerPeerConnected(t *testing.T) {
	mm := newTestModeManager(t)

	mm.OnPeerConnected()
	assert.Equal(t, consensus.OpModeConnected, mm.Mode())
}

func TestModeManagerAllPeersDisconnected(t *testing.T) {
	mm := newTestModeManager(t)

	mm.OnPeerConnected()
	mm.OnPeerConnected()
	assert.Equal(t, consensus.OpModeConnected, mm.Mode())

	mm.OnPeerDisconnected()
	assert.Equal(t, consensus.OpModeConnected, mm.Mode()) // still 1 peer

	mm.OnPeerDisconnected()
	assert.Equal(t, consensus.OpModeDisconnected, mm.Mode()) // 0 peers
}

func TestModeManagerFullTransitionPath(t *testing.T) {
	mm := newTestModeManager(t)

	// Disconnected → Connected
	mm.OnPeerConnected()
	assert.Equal(t, consensus.OpModeConnected, mm.Mode())

	// Connected → Syncing
	mm.OnLCLMismatch()
	assert.Equal(t, consensus.OpModeSyncing, mm.Mode())

	// Syncing → Tracking
	mm.OnLCLAcquired()
	assert.Equal(t, consensus.OpModeTracking, mm.Mode())

	// Tracking → Full
	mm.OnValidationsReceived()
	assert.Equal(t, consensus.OpModeFull, mm.Mode())
}

func TestModeManagerWrongLedgerRecovery(t *testing.T) {
	mm := newTestModeManager(t)

	// Get to Full mode
	mm.OnPeerConnected()
	mm.OnLCLMismatch()
	mm.OnLCLAcquired()
	mm.OnValidationsReceived()
	assert.Equal(t, consensus.OpModeFull, mm.Mode())

	// Full → Syncing (wrong ledger)
	mm.OnWrongLedger()
	assert.Equal(t, consensus.OpModeSyncing, mm.Mode())

	// Recover: Syncing → Tracking → Full
	mm.OnLCLAcquired()
	assert.Equal(t, consensus.OpModeTracking, mm.Mode())
	mm.OnValidationsReceived()
	assert.Equal(t, consensus.OpModeFull, mm.Mode())
}

func TestModeManagerDisconnectFromAnyState(t *testing.T) {
	mm := newTestModeManager(t)

	// Get to Full mode
	mm.OnPeerConnected()
	mm.OnLCLMismatch()
	mm.OnLCLAcquired()
	mm.OnValidationsReceived()
	assert.Equal(t, consensus.OpModeFull, mm.Mode())

	// Losing all peers → Disconnected
	mm.OnPeerDisconnected()
	assert.Equal(t, consensus.OpModeDisconnected, mm.Mode())
}

func TestModeManagerNoopTransitions(t *testing.T) {
	mm := newTestModeManager(t)

	// These should be no-ops in Disconnected state
	mm.OnLCLMismatch()
	assert.Equal(t, consensus.OpModeDisconnected, mm.Mode())

	mm.OnLCLAcquired()
	assert.Equal(t, consensus.OpModeDisconnected, mm.Mode())

	mm.OnValidationsReceived()
	assert.Equal(t, consensus.OpModeDisconnected, mm.Mode())
}

// TestModeManager_OnEvent_WrongLedgerToSyncing pins the issue #401
// layer-5 wiring: when the engine fires ModeChangedEvent with
// NewMode=ModeWrongLedger, the mode manager must transition the
// network-level OperatingMode from Full to Syncing — so
// startRoundLocked stops promoting us to ModeProposing on the next
// round. Without this subscription a node that detects a wrong LCL
// silently keeps proposing on its side chain (the Frankenstein-
// validation path before commit 16ffac5 — and even after that fix,
// the node still WAKES UP every round to re-evaluate the bad
// position).
func TestModeManager_OnEvent_WrongLedgerToSyncing(t *testing.T) {
	mm := newTestModeManager(t)
	// Get to Full mode first so the wrongLedger transition has work to do.
	mm.OnPeerConnected()
	mm.OnLCLMismatch()
	mm.OnLCLAcquired()
	mm.OnValidationsReceived()
	if mm.Mode() != consensus.OpModeFull {
		t.Fatalf("preconditions: want Full, got %v", mm.Mode())
	}

	// Engine signals it just dropped to wrongLedger.
	mm.OnEvent(&consensus.ModeChangedEvent{
		OldMode: consensus.ModeProposing,
		NewMode: consensus.ModeWrongLedger,
	})

	if mm.Mode() != consensus.OpModeSyncing {
		t.Fatalf("ModeChangedEvent → wrongLedger must trigger "+
			"Full→Syncing transition; got OperatingMode=%v "+
			"— ModeManager.OnEvent is not wired (#401 layer 5)",
			mm.Mode())
	}
}

// TestModeManager_OnEvent_LeavingWrongLedgerBumpsToTracking pins
// the recovery half of the wiring: when the engine fires
// ModeChangedEvent with OldMode=ModeWrongLedger and a non-wrong
// new mode (we acquired the right LCL and are about to run a
// switchedLedger / proposing / observing round), the mode manager
// must bump us from Syncing to Tracking. Tracking is still
// non-Full, so we don't immediately re-enter proposing — that
// transition fires when validations on the recovered chain
// confirm we're caught up.
func TestModeManager_OnEvent_LeavingWrongLedgerBumpsToTracking(t *testing.T) {
	mm := newTestModeManager(t)
	mm.OnPeerConnected()
	mm.OnLCLMismatch()
	if mm.Mode() != consensus.OpModeSyncing {
		t.Fatalf("preconditions: want Syncing, got %v", mm.Mode())
	}

	// Engine signals recovery: wrongLedger → switchedLedger.
	mm.OnEvent(&consensus.ModeChangedEvent{
		OldMode: consensus.ModeWrongLedger,
		NewMode: consensus.ModeSwitchedLedger,
	})

	if mm.Mode() != consensus.OpModeTracking {
		t.Fatalf("leaving wrongLedger must bump Syncing→Tracking; "+
			"got %v — ModeManager.OnEvent recovery branch "+
			"missing (#401 layer 5)", mm.Mode())
	}
}

// TestModeManager_OnEvent_IgnoresUnrelatedEvents pins the
// safety property that OnEvent silently drops events it doesn't
// care about — no panic, no spurious transition. The event bus
// fires many event types and the subscriber must be tolerant.
func TestModeManager_OnEvent_IgnoresUnrelatedEvents(t *testing.T) {
	mm := newTestModeManager(t)
	mm.OnPeerConnected()
	mm.OnLCLMismatch()
	mm.OnLCLAcquired()
	mm.OnValidationsReceived()
	beforeMode := mm.Mode()

	// Some other event the bus will deliver.
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

func TestModeManagerCallback(t *testing.T) {
	mm := newTestModeManager(t)

	var transitions []struct {
		from, to consensus.OperatingMode
	}

	mm.SetOnModeChange(func(old, new consensus.OperatingMode) {
		transitions = append(transitions, struct {
			from, to consensus.OperatingMode
		}{old, new})
	})

	mm.OnPeerConnected()
	mm.OnLCLMismatch()

	assert.Len(t, transitions, 2)
	assert.Equal(t, consensus.OpModeDisconnected, transitions[0].from)
	assert.Equal(t, consensus.OpModeConnected, transitions[0].to)
	assert.Equal(t, consensus.OpModeConnected, transitions[1].from)
	assert.Equal(t, consensus.OpModeSyncing, transitions[1].to)
}

func TestModeManagerPeerCountUnderflow(t *testing.T) {
	mm := newTestModeManager(t)

	// Disconnecting with 0 peers should not underflow
	mm.OnPeerDisconnected()
	assert.Equal(t, consensus.OpModeDisconnected, mm.Mode())
}

func TestModeManagerForceSetMode(t *testing.T) {
	mm := newTestModeManager(t)

	mm.SetMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, mm.Mode())
}
