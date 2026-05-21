package adaptor

import (
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// StateAccountingEntry is one row of the operating-mode state machine
// surfaced by server_info.state_accounting. Transitions counts the
// number of times the node has entered the mode and DurationUs is the
// cumulative microseconds spent in it.
type StateAccountingEntry struct {
	Transitions uint64
	DurationUs  uint64
}

// StateAccountingSnapshot is everything server_info needs to render
// state_accounting + the two top-level companion fields
// (server_state_duration_us, initial_sync_duration_us). Mirrors the
// data rippled's NetworkOPsImp::StateAccounting::json emits.
type StateAccountingSnapshot struct {
	// Modes is the per-mode counts/durations table.
	Modes map[string]StateAccountingEntry
	// CurrentDurationUs is the time spent in the current operating
	// mode since the last transition. Rippled exposes it as the
	// top-level server_state_duration_us.
	CurrentDurationUs uint64
	// InitialSyncUs is the time from process start to the first
	// transition into OpModeFull; zero before that transition has
	// occurred. Rippled emits initial_sync_duration_us only when
	// non-zero.
	InitialSyncUs uint64
}

// stateAccounting tracks per-OperatingMode transition counts and
// microsecond durations. Mirrors rippled's NetworkOPsImp::StateAccounting
// at NetworkOPs.cpp:143-200, 4808-4849.
type stateAccounting struct {
	mu           sync.Mutex
	now          func() time.Time
	current      consensus.OperatingMode
	since        time.Time
	processStart time.Time
	initialSync  time.Duration
	counts       [5]uint64 // transitions per mode, indexed by OperatingMode
	durs         [5]time.Duration
}

func newStateAccounting(initial consensus.OperatingMode, now func() time.Time) *stateAccounting {
	if now == nil {
		now = time.Now
	}
	start := now()
	sa := &stateAccounting{now: now, current: initial, since: start, processStart: start}
	if int(initial) >= 0 && int(initial) < len(sa.counts) {
		sa.counts[initial] = 1
	}
	return sa
}

// transition advances the state machine, charging elapsed time against
// the prior mode and bumping the new mode's transition count. A
// same-mode call is a no-op (matches rippled's setMode early-return at
// NetworkOPs.cpp:2560-2561, which gates accounting_.mode()).
func (s *stateAccounting) transition(mode consensus.OperatingMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode == s.current {
		return
	}
	now := s.now()
	if idx := int(s.current); idx >= 0 && idx < len(s.durs) {
		s.durs[idx] += now.Sub(s.since)
	}
	s.current = mode
	s.since = now
	if idx := int(mode); idx >= 0 && idx < len(s.counts) {
		s.counts[idx]++
		// First transition into Full: record initial sync duration.
		// Mirrors rippled NetworkOPs.cpp:4814-4820.
		if mode == consensus.OpModeFull && s.counts[idx] == 1 && s.initialSync == 0 {
			s.initialSync = now.Sub(s.processStart)
		}
	}
}

// snapshot returns a frozen view of the counters with elapsed time in
// the current mode rolled into its duration, plus the current-state
// duration and initial-sync duration used by the top-level
// server_info fields. Map keys are rippled's lowercase mode names.
func (s *stateAccounting) snapshot() StateAccountingSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	current := now.Sub(s.since)
	durs := s.durs
	if idx := int(s.current); idx >= 0 && idx < len(durs) {
		durs[idx] += current
	}
	modes := make(map[string]StateAccountingEntry, len(stateAccountingNames))
	for i, name := range stateAccountingNames {
		modes[name] = StateAccountingEntry{
			Transitions: s.counts[i],
			DurationUs:  uint64(durs[i].Microseconds()),
		}
	}
	return StateAccountingSnapshot{
		Modes:             modes,
		CurrentDurationUs: uint64(current.Microseconds()),
		InitialSyncUs:     uint64(s.initialSync.Microseconds()),
	}
}

// stateAccountingNames maps OperatingMode index → lowercase name used
// by rippled's server_info JSON. Ordered to match rippled's
// iteration (NetworkOPs.cpp:871-872 stateNames + 4837-4845 loop).
var stateAccountingNames = [5]string{
	consensus.OpModeDisconnected: "disconnected",
	consensus.OpModeConnected:    "connected",
	consensus.OpModeSyncing:      "syncing",
	consensus.OpModeTracking:     "tracking",
	consensus.OpModeFull:         "full",
}
