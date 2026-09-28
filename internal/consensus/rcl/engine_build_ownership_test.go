package rcl

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

func TestEngine_AbandonedEstablishDoesNotReserveBuild(t *testing.T) {
	base := newMockAdaptor()
	a := &replayFrontierAdaptor{mockAdaptor: base}
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	e.prevLedger = base.lastLCL
	round := consensus.RoundID{Seq: base.lastLCL.Seq() + 1, ParentHash: base.lastLCL.ID()}
	require.NoError(t, e.StartRound(round, true))
	require.Equal(t, consensus.ModeProposing, e.Mode())

	// Use the real close path, not a fixture that writes the build marker.
	e.mu.Lock()
	e.closeLedger()
	e.mu.Unlock()
	require.Equal(t, consensus.PhaseEstablish, e.Phase())

	// A short ingress stall lets quorum advance two ledgers. The observer
	// intentionally parks this round so replay can take over; it must not
	// also tell the router that it owns the successor replay needs.
	a.network = base.lastLCL.Seq() + 2
	base.SetOperatingMode(consensus.OpModeConnected)
	base.buildLedgerHook = func() { t.Error("abandoned round must not build") }
	for range 3 {
		e.TimerEntry()
	}
	require.Equal(t, consensus.ModeObserving, e.Mode())
	require.Equal(t, consensus.PhaseEstablish, e.Phase())
	require.Zero(t, e.BuildingLedgerSeq(), "a parked consensus round must not suppress successor acquisition")
}

func TestEngine_DeferredAcceptRetainsBuildOwnership(t *testing.T) {
	base := newMockAdaptor()
	a := &deferredAcceptAdaptor{mockAdaptor: base}
	e := startDeferredAccept(t, a)
	seq := base.lastLCL.Seq() + 1
	require.Equal(t, seq, e.BuildingLedgerSeq(), "a queued accept job already owns the build")
	base.SetOperatingMode(consensus.OpModeConnected)
	e.TimerEntry()
	require.Equal(t, seq, e.BuildingLedgerSeq(), "demotion must not release a real accept job")
	complete := a.completion(t)
	complete()
	require.Zero(t, e.BuildingLedgerSeq())
	complete()
	require.Zero(t, e.BuildingLedgerSeq(), "duplicate completion must not resurrect ownership")
}
