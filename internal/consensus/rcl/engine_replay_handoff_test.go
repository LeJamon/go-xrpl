package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

type replayFrontierAdaptor struct {
	*mockAdaptor
	network uint32
}

func (a *replayFrontierAdaptor) NetworkValidatedLedgerSeq() uint32 { return a.network }

func TestEngine_ObserverAdoptsVerifiedSuccessorBeforeRebuilding(t *testing.T) {
	a := newMockAdaptor()
	a.opMode = consensus.OpModeTracking
	e := NewEngine(a, DefaultConfig())
	initial := a.lastLCL
	e.prevLedger = initial
	e.mode = consensus.ModeObserving
	validated := chainLedger(initial.Seq()+1, 105, initial.ID()[0])
	require.NoError(t, a.StoreLedger(validated))
	a.validatedLedgerHashOverride = validated.ID()

	e.checkLedger()

	require.Equal(t, validated.ID(), e.prevLedger.ID())
	require.Equal(t, consensus.ModeSwitchedLedger, e.mode)
	require.Equal(t, validated.Seq()+1, e.state.Round.Seq)
}

func TestEngine_ObserverWaitsForReplayWhenQuorumIsAhead(t *testing.T) {
	base := newMockAdaptor()
	base.opMode = consensus.OpModeTracking
	a := &replayFrontierAdaptor{mockAdaptor: base, network: base.lastLCL.Seq() + 10}
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	e.prevLedger = base.lastLCL
	round := consensus.RoundID{Seq: base.lastLCL.Seq() + 1, ParentHash: base.lastLCL.ID()}
	require.NoError(t, e.StartRound(round, false))
	e.phase = consensus.PhaseEstablish
	e.roundStartTime = a.Now().Add(-time.Minute)
	base.buildLedgerHook = func() { t.Fatal("observer must not build a private branch during replay") }

	for range 3 {
		e.TimerEntry()
	}
	require.Equal(t, consensus.PhaseEstablish, e.phase)
	require.Equal(t, round, e.state.Round)
}
