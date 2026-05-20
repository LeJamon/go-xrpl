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

// stateAccounting tracks per-OperatingMode transition counts and
// microsecond durations. Mirrors rippled's NetworkOPsImp::StateAccounting
// (NetworkOPs.cpp:4836-4843) so the server_info handler can surface
// real numbers in place of hardcoded zeros (#480).
type stateAccounting struct {
	mu      sync.Mutex
	now     func() time.Time
	current consensus.OperatingMode
	since   time.Time
	counts  [5]uint64 // transitions per mode, indexed by OperatingMode
	durs    [5]time.Duration
}

func newStateAccounting(initial consensus.OperatingMode, now func() time.Time) *stateAccounting {
	if now == nil {
		now = time.Now
	}
	sa := &stateAccounting{now: now, current: initial, since: now()}
	if int(initial) >= 0 && int(initial) < len(sa.counts) {
		sa.counts[initial] = 1
	}
	return sa
}

// transition advances the state machine, charging elapsed time against
// the prior mode and bumping the new mode's transition count. A
// same-mode call is a no-op.
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
	}
}

// snapshot returns a frozen view of the counters with elapsed time in
// the current mode rolled into its duration. Callers iterate the result
// in any order — the keys are rippled's lowercase mode names.
func (s *stateAccounting) snapshot() map[string]StateAccountingEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	durs := s.durs
	if idx := int(s.current); idx >= 0 && idx < len(durs) {
		durs[idx] += s.now().Sub(s.since)
	}
	out := make(map[string]StateAccountingEntry, len(stateAccountingNames))
	for i, name := range stateAccountingNames {
		out[name] = StateAccountingEntry{
			Transitions: s.counts[i],
			DurationUs:  uint64(durs[i].Microseconds()),
		}
	}
	return out
}

// stateAccountingNames maps OperatingMode index → lowercase name used
// by rippled's server_info JSON.
var stateAccountingNames = [5]string{
	consensus.OpModeDisconnected: "disconnected",
	consensus.OpModeConnected:    "connected",
	consensus.OpModeSyncing:      "syncing",
	consensus.OpModeTracking:     "tracking",
	consensus.OpModeFull:         "full",
}
