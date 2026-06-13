package adaptor

import (
	"log/slog"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// ModeManager translates the engine's ModeChangedEvents into adaptor
// OperatingMode transitions. It is subscribed to the engine at startup
// and its only live entry point is OnEvent.
//
// Production paths (router.go, AdoptLedgerFromHeader, catch-up) set the
// operating mode directly via Adaptor.SetOperatingMode, so m.mode is not
// the authoritative source of truth — OnEvent reads the adaptor's mode
// and only steers the wrongLedger ↔ syncing/tracking edges consensus
// signals. The earlier peer-count / LCL-acquisition ladder
// (Disconnected→Connected→Syncing→Tracking→Full) was never wired to
// overlay events and has been removed; SetMode remains for manual
// override.
type ModeManager struct {
	mu      sync.RWMutex
	mode    consensus.OperatingMode
	adaptor *Adaptor
	logger  *slog.Logger
}

func NewModeManager(adaptor *Adaptor) *ModeManager {
	return &ModeManager{
		mode:    consensus.OpModeDisconnected,
		adaptor: adaptor,
		logger:  slog.Default().With("component", "mode-manager"),
	}
}

// Mode returns the current operating mode.
func (m *ModeManager) Mode() consensus.OperatingMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mode
}

// pendingTransition captures a mode transition's side effects so they run
// *outside* m.mu — avoiding lock inversion against the Adaptor's own mutex
// (issue #418 bootstrap deadlock class).
type pendingTransition struct {
	oldMode consensus.OperatingMode
	newMode consensus.OperatingMode
}

// OnEvent translates engine ModeChangedEvents into adaptor OperatingMode
// transitions. Reads adaptor.GetOperatingMode() rather than m.mode because
// production paths (router.go, AdoptLedgerFromHeader) bypass this state
// machine via direct SetOperatingMode calls, so m.mode isn't authoritative.
func (m *ModeManager) OnEvent(event consensus.Event) {
	mc, ok := event.(*consensus.ModeChangedEvent)
	if !ok {
		return
	}
	current := m.adaptor.GetOperatingMode()
	var p *pendingTransition
	if mc.NewMode == consensus.ModeWrongLedger {
		if current == consensus.OpModeFull || current == consensus.OpModeTracking {
			m.mu.Lock()
			p = m.stageTransitionLocked(consensus.OpModeSyncing)
			m.mu.Unlock()
		}
	} else if mc.OldMode == consensus.ModeWrongLedger {
		if current == consensus.OpModeSyncing {
			m.mu.Lock()
			p = m.stageTransitionLocked(consensus.OpModeTracking)
			m.mu.Unlock()
		}
	}
	m.applyTransition(p)
}

// SetMode forces a mode transition (manual override / testing).
func (m *ModeManager) SetMode(mode consensus.OperatingMode) {
	m.mu.Lock()
	p := m.stageTransitionLocked(mode)
	m.mu.Unlock()
	m.applyTransition(p)
}

// stageTransitionLocked records a pending mode transition under m.mu and
// returns the captured side-effect arguments. It does NOT call into the
// Adaptor — that happens in applyTransition after the lock is released,
// which breaks the ModeManager↔Adaptor lock cycle that previously
// deadlocked at bootstrap (issue #418).
//
// Returns nil when newMode == m.mode so callers can pass the result
// through applyTransition unconditionally.
func (m *ModeManager) stageTransitionLocked(newMode consensus.OperatingMode) *pendingTransition {
	if m.mode == newMode {
		return nil
	}
	p := &pendingTransition{oldMode: m.mode, newMode: newMode}
	m.mode = newMode
	return p
}

// applyTransition runs the side effects staged by stageTransitionLocked.
// MUST be called with m.mu released.
func (m *ModeManager) applyTransition(p *pendingTransition) {
	if p == nil {
		return
	}
	m.adaptor.SetOperatingMode(p.newMode)
	m.logger.Info("Operating mode changed",
		"from", p.oldMode.String(),
		"to", p.newMode.String(),
	)
}
