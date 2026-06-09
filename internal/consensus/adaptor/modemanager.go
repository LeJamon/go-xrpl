package adaptor

import (
	"log/slog"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// ModeManager tracks the node's operating mode and manages transitions
// based on overlay events and consensus state.
//
// Transition rules:
//
//	Disconnected → Connected    : 1+ peers connected
//	Connected    → Disconnected : 0 peers
//	Connected    → Syncing      : LCL mismatch detected from peer status
//	Syncing      → Tracking     : acquired correct LCL
//	Tracking     → Full         : receiving validations that confirm our chain
//	Full         → Syncing      : consensus detects wrong ledger
//	Any          → Disconnected : all peers lost
type ModeManager struct {
	mu        sync.RWMutex
	mode      consensus.OperatingMode
	peerCount int
	adaptor   *Adaptor
	logger    *slog.Logger

	// onModeChange is called when the mode transitions.
	onModeChange func(oldMode, newMode consensus.OperatingMode)
}

func NewModeManager(adaptor *Adaptor) *ModeManager {
	return &ModeManager{
		mode:    consensus.OpModeDisconnected,
		adaptor: adaptor,
		logger:  slog.Default().With("component", "mode-manager"),
	}
}

// SetOnModeChange sets a callback for mode transitions.
func (m *ModeManager) SetOnModeChange(fn func(old, new consensus.OperatingMode)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onModeChange = fn
}

// Mode returns the current operating mode.
func (m *ModeManager) Mode() consensus.OperatingMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mode
}

// pendingTransition captures the side effects of a mode transition that
// must run *outside* m.mu to avoid lock inversion against the Adaptor's
// own mutex and against any onModeChange subscriber that might call back
// into ModeManager (issue #418 bootstrap deadlock class).
type pendingTransition struct {
	oldMode   consensus.OperatingMode
	newMode   consensus.OperatingMode
	peerCount int
	cb        func(old, new consensus.OperatingMode)
}

// OnPeerConnected should be called when a new peer connects.
func (m *ModeManager) OnPeerConnected() {
	m.mu.Lock()
	m.peerCount++
	var p *pendingTransition
	if m.mode == consensus.OpModeDisconnected && m.peerCount > 0 {
		p = m.stageTransitionLocked(consensus.OpModeConnected)
	}
	m.mu.Unlock()
	m.applyTransition(p)
}

// OnPeerDisconnected should be called when a peer disconnects.
func (m *ModeManager) OnPeerDisconnected() {
	m.mu.Lock()
	if m.peerCount > 0 {
		m.peerCount--
	}
	var p *pendingTransition
	if m.peerCount == 0 {
		p = m.stageTransitionLocked(consensus.OpModeDisconnected)
	}
	m.mu.Unlock()
	m.applyTransition(p)
}

// OnLCLMismatch should be called when a peer reports a different LCL.
// Transitions Connected → Syncing.
func (m *ModeManager) OnLCLMismatch() {
	m.mu.Lock()
	var p *pendingTransition
	if m.mode == consensus.OpModeConnected {
		p = m.stageTransitionLocked(consensus.OpModeSyncing)
	}
	m.mu.Unlock()
	m.applyTransition(p)
}

// OnLCLAcquired should be called when we have the correct LCL.
// Transitions Syncing → Tracking.
func (m *ModeManager) OnLCLAcquired() {
	m.mu.Lock()
	var p *pendingTransition
	if m.mode == consensus.OpModeSyncing {
		p = m.stageTransitionLocked(consensus.OpModeTracking)
	}
	m.mu.Unlock()
	m.applyTransition(p)
}

// OnValidationsReceived should be called when we receive validations
// confirming our chain. Transitions Tracking → Full.
func (m *ModeManager) OnValidationsReceived() {
	m.mu.Lock()
	var p *pendingTransition
	if m.mode == consensus.OpModeTracking {
		p = m.stageTransitionLocked(consensus.OpModeFull)
	}
	m.mu.Unlock()
	m.applyTransition(p)
}

// OnWrongLedger should be called when consensus detects we're on the
// wrong ledger. Transitions Full → Syncing.
func (m *ModeManager) OnWrongLedger() {
	m.mu.Lock()
	var p *pendingTransition
	if m.mode == consensus.OpModeFull || m.mode == consensus.OpModeTracking {
		p = m.stageTransitionLocked(consensus.OpModeSyncing)
	}
	m.mu.Unlock()
	m.applyTransition(p)
}

// SetMode forces a mode transition (for testing or manual override).
func (m *ModeManager) SetMode(mode consensus.OperatingMode) {
	m.mu.Lock()
	p := m.stageTransitionLocked(mode)
	m.mu.Unlock()
	m.applyTransition(p)
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

// stageTransitionLocked records a pending mode transition under m.mu and
// returns the captured side-effect arguments. It does NOT call into the
// Adaptor or invoke onModeChange — both happen in applyTransition after
// the lock is released, which breaks the ModeManager↔Adaptor lock cycle
// that previously deadlocked at bootstrap (issue #418).
//
// Returns nil when newMode == m.mode so callers can pass the result
// through applyTransition unconditionally.
func (m *ModeManager) stageTransitionLocked(newMode consensus.OperatingMode) *pendingTransition {
	if m.mode == newMode {
		return nil
	}
	p := &pendingTransition{
		oldMode:   m.mode,
		newMode:   newMode,
		peerCount: m.peerCount,
		cb:        m.onModeChange,
	}
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
		"peers", p.peerCount,
	)
	if p.cb != nil {
		p.cb(p.oldMode, p.newMode)
	}
}
