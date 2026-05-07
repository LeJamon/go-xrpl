package adaptor

import (
	"log/slog"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/consensus"
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

// NewModeManager creates a new ModeManager.
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

// OnPeerConnected should be called when a new peer connects.
func (m *ModeManager) OnPeerConnected() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.peerCount++
	if m.mode == consensus.OpModeDisconnected && m.peerCount > 0 {
		m.transitionLocked(consensus.OpModeConnected)
	}
}

// OnPeerDisconnected should be called when a peer disconnects.
func (m *ModeManager) OnPeerDisconnected() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.peerCount > 0 {
		m.peerCount--
	}
	if m.peerCount == 0 {
		m.transitionLocked(consensus.OpModeDisconnected)
	}
}

// OnLCLMismatch should be called when a peer reports a different LCL.
// Transitions Connected → Syncing.
func (m *ModeManager) OnLCLMismatch() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mode == consensus.OpModeConnected {
		m.transitionLocked(consensus.OpModeSyncing)
	}
}

// OnLCLAcquired should be called when we have the correct LCL.
// Transitions Syncing → Tracking.
func (m *ModeManager) OnLCLAcquired() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mode == consensus.OpModeSyncing {
		m.transitionLocked(consensus.OpModeTracking)
	}
}

// OnValidationsReceived should be called when we receive validations
// confirming our chain. Transitions Tracking → Full.
func (m *ModeManager) OnValidationsReceived() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mode == consensus.OpModeTracking {
		m.transitionLocked(consensus.OpModeFull)
	}
}

// OnWrongLedger should be called when consensus detects we're on the
// wrong ledger. Transitions Full → Syncing.
func (m *ModeManager) OnWrongLedger() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mode == consensus.OpModeFull || m.mode == consensus.OpModeTracking {
		m.transitionLocked(consensus.OpModeSyncing)
	}
}

// SetMode forces a mode transition (for testing or manual override).
func (m *ModeManager) SetMode(mode consensus.OperatingMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transitionLocked(mode)
}

// OnEvent implements consensus.EventSubscriber. The mode manager
// subscribes to the engine's event bus so the operator-visible
// OperatingMode (used by server_info and gating proposing in
// startRoundLocked) reflects what the consensus engine actually
// observes about our LCL.
//
// We listen for ModeChangedEvent and translate the internal
// consensus.Mode (proposing/observing/wrongLedger/switchedLedger)
// into the network-level OperatingMode transitions defined in this
// type's struct doc:
//   - new == ModeWrongLedger
//       → OnWrongLedger (Full/Tracking → Syncing). This stops us
//         from proposing while we're on a side chain — the only
//         protocol-correct posture, since proposals on a wrong
//         parent never count toward peers' quorum. Mirrors
//         rippled's effective behavior where wrongLedger mode
//         keeps validating_=false until handleWrongLedger
//         succeeds.
//   - new in {ModeSwitchedLedger, ModeProposing, ModeObserving}
//     coming OUT of ModeWrongLedger
//       → OnLCLAcquired (Syncing → Tracking). We have the right
//         parent again; bump up to Tracking. The Tracking →
//         Full transition fires elsewhere on validations
//         confirming our chain.
func (m *ModeManager) OnEvent(event consensus.Event) {
	mc, ok := event.(*consensus.ModeChangedEvent)
	if !ok {
		return
	}
	if mc.NewMode == consensus.ModeWrongLedger {
		m.OnWrongLedger()
		return
	}
	if mc.OldMode == consensus.ModeWrongLedger {
		m.OnLCLAcquired()
	}
}

// transitionLocked performs a mode transition while holding the lock.
func (m *ModeManager) transitionLocked(newMode consensus.OperatingMode) {
	if m.mode == newMode {
		return
	}
	oldMode := m.mode
	m.mode = newMode

	// Update the adaptor's operating mode
	m.adaptor.SetOperatingMode(newMode)

	m.logger.Info("Operating mode changed",
		"from", oldMode.String(),
		"to", newMode.String(),
		"peers", m.peerCount,
	)

	if m.onModeChange != nil {
		m.onModeChange(oldMode, newMode)
	}
}
