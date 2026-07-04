package rcl

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestEngine_StartRound_AmendmentBlockedObserves pins the round-start gate:
// an amendment-blocked validator can no longer build correct ledgers, so it
// must enter rounds as an observer and report itself as not validating, even
// when fully synced (rippled preStartRound: validating_ requires !isBlocked).
func TestEngine_StartRound_AmendmentBlockedObserves(t *testing.T) {
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}

	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	engine := NewEngine(adaptor, DefaultConfig())

	engine.StartRound(round, true)
	if mode := engine.Mode(); mode != consensus.ModeProposing {
		t.Fatalf("unblocked full validator: want Proposing, got %v", mode)
	}
	if !engine.IsValidating() {
		t.Fatal("unblocked full validator: IsValidating must be true")
	}

	adaptor.mu.Lock()
	adaptor.amendmentBlocked = true
	adaptor.mu.Unlock()

	engine.StartRound(round, true)
	if mode := engine.Mode(); mode != consensus.ModeObserving {
		t.Fatalf("amendment-blocked validator: want Observing, got %v", mode)
	}
	if engine.IsValidating() {
		t.Fatal("amendment-blocked validator: IsValidating must be false")
	}
}

// TestEngine_AcceptLedger_AmendmentBlockedSuppressesValidation pins the
// emission gate. The gate is intentionally mode-independent (observers emit
// partials, #451), so the round-start demotion to Observing alone would NOT
// stop a blocked node from signing validations for the wrong, un-amended
// ledgers it builds — the blocked check must kill emission itself.
func TestEngine_AcceptLedger_AmendmentBlockedSuppressesValidation(t *testing.T) {
	run := func(t *testing.T, blocked bool) int {
		t.Helper()
		adaptor := newMockAdaptor()
		adaptor.validator = true
		adaptor.opMode = consensus.OpModeFull
		adaptor.amendmentBlocked = blocked

		engine := NewEngine(adaptor, DefaultConfig())
		engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}, true)
		driveToEstablish(t, engine, adaptor)

		engine.mu.Lock()
		engine.acceptLedger(consensus.ResultSuccess)
		engine.mu.Unlock()

		adaptor.mu.RLock()
		defer adaptor.mu.RUnlock()
		return len(adaptor.validationsBroadcast)
	}

	if got := run(t, false); got != 1 {
		t.Fatalf("unblocked validator must emit one validation on accept, got %d", got)
	}
	if got := run(t, true); got != 0 {
		t.Fatalf("amendment-blocked validator must emit no validation (not even a partial), got %d", got)
	}
}

// TestEngine_TimerEntry_AmendmentBlockedDemotesToConnected pins the mode
// latch: once blocked, the heartbeat drops the operating mode to Connected so
// the node stops claiming to be synced (rippled setMode caps while blocked).
func TestEngine_TimerEntry_AmendmentBlockedDemotesToConnected(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.amendmentBlocked = true

	engine := NewEngine(adaptor, DefaultConfig())
	engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}, true)

	engine.timerEntry()

	if got := adaptor.GetOperatingMode(); got != consensus.OpModeConnected {
		t.Fatalf("blocked node after tick: want OpModeConnected, got %v", got)
	}
}
