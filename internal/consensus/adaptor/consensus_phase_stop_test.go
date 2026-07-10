package adaptor

import "testing"

// TestAdaptor_StopConsensusPhaseDispatcher_JoinsAndIsIdempotent proves the
// lazily-started consensus-phase dispatcher is joined by its stop method (so an
// in-process restart doesn't leak one goroutine per cycle), that stop is
// idempotent, and that an emit after stop is a dropped no-op rather than a
// send-on-closed panic or a silent restart.
func TestAdaptor_StopConsensusPhaseDispatcher_JoinsAndIsIdempotent(t *testing.T) {
	a := newTestAdaptor(t)

	// First emit lazily starts the dispatcher goroutine.
	a.emitConsensusPhase("open")

	a.StopConsensusPhaseDispatcher()
	a.StopConsensusPhaseDispatcher() // idempotent — must not panic

	// Emit after stop must be a no-op, not a panic or a restart.
	a.emitConsensusPhase("accepted")

	a.consensusPhaseMu.Lock()
	stopped := a.consensusPhaseStop
	a.consensusPhaseMu.Unlock()
	if !stopped {
		t.Fatal("dispatcher must remain stopped after StopConsensusPhaseDispatcher")
	}
}

// TestAdaptor_StopConsensusPhaseDispatcher_NeverStarted confirms stopping a
// dispatcher that was never started (no emit happened) is a safe no-op.
func TestAdaptor_StopConsensusPhaseDispatcher_NeverStarted(t *testing.T) {
	a := newTestAdaptor(t)
	a.StopConsensusPhaseDispatcher() // no goroutine to join — must not hang or panic
}
