package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// TestStateAccounting_InitialCounts checks rippled NetworkOPs.cpp:163-167:
// fresh tracker starts in DISCONNECTED with exactly one transition into it.
func TestStateAccounting_InitialCounts(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	now := t0
	sa := newStateAccounting(consensus.OpModeDisconnected, func() time.Time { return now })

	snap := sa.snapshot()
	if got := snap.Modes["disconnected"].Transitions; got != 1 {
		t.Fatalf("disconnected.transitions = %d, want 1", got)
	}
	for _, mode := range []string{"connected", "syncing", "tracking", "full"} {
		if got := snap.Modes[mode].Transitions; got != 0 {
			t.Errorf("%s.transitions = %d, want 0", mode, got)
		}
	}
	if snap.InitialSyncUs != 0 {
		t.Errorf("InitialSyncUs = %d, want 0 (no Full transition yet)", snap.InitialSyncUs)
	}
}

// TestStateAccounting_DurationAccrualAndCurrent verifies that elapsed
// time in the current mode rolls into both the per-mode duration AND
// the CurrentDurationUs companion field. server_state_duration_us must
// reflect time-since-last-transition, not process uptime (B2).
func TestStateAccounting_DurationAccrualAndCurrent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	sa := newStateAccounting(consensus.OpModeDisconnected, func() time.Time { return now })

	// 5 seconds elapse in DISCONNECTED, then transition to CONNECTED.
	now = now.Add(5 * time.Second)
	sa.transition(consensus.OpModeConnected)

	// 2 seconds later, snapshot.
	now = now.Add(2 * time.Second)
	snap := sa.snapshot()

	if got := snap.Modes["disconnected"].DurationUs; got != uint64(5*time.Second/time.Microsecond) {
		t.Errorf("disconnected.duration_us = %d, want %d", got, 5*time.Second/time.Microsecond)
	}
	if got := snap.Modes["connected"].DurationUs; got != uint64(2*time.Second/time.Microsecond) {
		t.Errorf("connected.duration_us = %d, want %d", got, 2*time.Second/time.Microsecond)
	}
	if got := snap.CurrentDurationUs; got != uint64(2*time.Second/time.Microsecond) {
		t.Errorf("CurrentDurationUs = %d, want %d (time in CONNECTED since last transition)", got, 2*time.Second/time.Microsecond)
	}
}

// TestStateAccounting_InitialSyncCapturedOnFirstFull mirrors rippled
// NetworkOPs.cpp:4814-4820: initial_sync_duration_us is the time from
// process start to the FIRST transition into Full, and is sticky.
func TestStateAccounting_InitialSyncCapturedOnFirstFull(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	now := start
	sa := newStateAccounting(consensus.OpModeDisconnected, func() time.Time { return now })

	// 30s in DISCONNECTED, 20s in CONNECTED, 15s in SYNCING.
	now = now.Add(30 * time.Second)
	sa.transition(consensus.OpModeConnected)
	now = now.Add(20 * time.Second)
	sa.transition(consensus.OpModeSyncing)
	now = now.Add(15 * time.Second)
	sa.transition(consensus.OpModeFull)

	snap := sa.snapshot()
	wantInitial := uint64(65 * time.Second / time.Microsecond)
	if snap.InitialSyncUs != wantInitial {
		t.Errorf("InitialSyncUs = %d, want %d (sum of pre-Full mode times)", snap.InitialSyncUs, wantInitial)
	}

	// Drop out of Full and back in — InitialSyncUs must not change.
	now = now.Add(5 * time.Second)
	sa.transition(consensus.OpModeTracking)
	now = now.Add(5 * time.Second)
	sa.transition(consensus.OpModeFull)

	snap = sa.snapshot()
	if snap.InitialSyncUs != wantInitial {
		t.Errorf("InitialSyncUs changed after re-entering Full: got %d, want %d (sticky)", snap.InitialSyncUs, wantInitial)
	}
	if got := snap.Modes["full"].Transitions; got != 2 {
		t.Errorf("full.transitions = %d, want 2", got)
	}
}

// TestStateAccounting_NoopTransitionDoesNotBumpCount mirrors rippled
// NetworkOPs.cpp:2560-2561, where setMode returns early before
// invoking accounting_.mode() when the requested state already matches.
func TestStateAccounting_NoopTransitionDoesNotBumpCount(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	sa := newStateAccounting(consensus.OpModeDisconnected, func() time.Time { return now })

	sa.transition(consensus.OpModeDisconnected)
	sa.transition(consensus.OpModeDisconnected)

	snap := sa.snapshot()
	if got := snap.Modes["disconnected"].Transitions; got != 1 {
		t.Errorf("disconnected.transitions = %d after no-op calls, want 1", got)
	}
}
