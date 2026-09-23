package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/require"
)

func newActiveRecoveryRound(t *testing.T, establish bool) (*Engine, *replayFrontierAdaptor, *fakeClock, *mockLedger) {
	t.Helper()
	base := newMockAdaptor()
	base.setTrusted([]consensus.NodeID{{0x21}, {0x22}})
	a := &replayFrontierAdaptor{mockAdaptor: base}
	clock := &fakeClock{t: base.Now()}
	cfg := DefaultConfig()
	cfg.ManualTick = true
	cfg.Clock = clock.now
	e := NewEngine(a, cfg)
	parent := base.lastLCL
	e.prevLedger = parent
	require.NoError(t, e.StartRound(consensus.RoundID{Seq: parent.Seq() + 1, ParentHash: parent.ID()}, true))
	if establish {
		e.mu.Lock()
		e.closeLedger()
		e.mu.Unlock()
		require.Equal(t, consensus.PhaseEstablish, e.Phase())
	}
	child := chainLedger(parent.Seq()+1, byte(parent.Seq()+1), parent.ID()[0])
	base.ledgers[child.ID()] = child // Held replay result, not the canonical LCL.
	a.network = child.Seq()
	stageQuorumValidatedLedger(e, base, child)
	return e, a, clock, child
}

func TestEngine_RecoveryHandoffDefersActiveChild(t *testing.T) {
	for _, establish := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "establish"}[establish], func(t *testing.T) {
			e, a, clock, child := newActiveRecoveryRound(t, establish)
			parent := e.prevLedger
			phase := e.Phase()
			clock.advance(2 * time.Second)

			// A ready/quorum-validated replay must neither promote the service
			// tip nor restart the still-live child round before it can finish.
			acceptable, err := e.CanAcceptLedger(child.ID())
			require.NoError(t, err)
			require.False(t, acceptable)
			for range 3 {
				result, err := e.TrySwitchToLedger(child.ID())
				require.NoError(t, err)
				require.Equal(t, consensus.LedgerSwitchBusy, result)
			}
			require.Equal(t, parent.ID(), e.prevLedger.ID())
			require.Equal(t, phase, e.Phase())
			require.Equal(t, consensus.ModeProposing, e.Mode())
			require.Empty(t, a.switchedLedgers)
			require.Zero(t, e.BuildingLedgerSeq(), "deferral must not resurrect the abandoned-round build marker")
		})
	}
}

func TestEngine_RecoveryHandoffDeferralIsBounded(t *testing.T) {
	e, a, clock, child := newActiveRecoveryRound(t, true)
	for range 3 {
		clock.advance(time.Second)
		result, err := e.TrySwitchToLedger(child.ID())
		require.NoError(t, err)
		require.Equal(t, consensus.LedgerSwitchBusy, result)
	}
	// Retries cannot reset the grace period. Even a still-proposing but stuck
	// round yields once the existing consensus soft deadline is exceeded.
	clock.advance(e.timing.LedgerMaxConsensus)
	acceptable, err := e.CanAcceptLedger(child.ID())
	require.NoError(t, err)
	require.True(t, acceptable)
	result, err := e.TrySwitchToLedger(child.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchAccepted, result)
	require.Equal(t, consensus.ModeSwitchedLedger, e.Mode())
	require.Len(t, a.switchedLedgers, 1)
}

func TestEngine_RecoveryHandoffGraceStartsAgainWhenIdleLedgerCloses(t *testing.T) {
	e, _, clock, child := newActiveRecoveryRound(t, false)
	clock.advance(time.Minute)
	// A long idle open interval must not erase the establish phase's chance
	// to reach its first acceptance heartbeat.
	e.mu.Lock()
	e.closeLedger()
	e.mu.Unlock()
	require.Equal(t, consensus.PhaseEstablish, e.Phase())
	clock.advance(time.Second)
	result, err := e.TrySwitchToLedger(child.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)
}

func TestEngine_RecoveryHandoffDoesNotBlockRealRecovery(t *testing.T) {
	for _, condition := range []string{"wrong_ledger", "demoted", "observing", "network_ahead", "incorrect_lcl"} {
		t.Run(condition, func(t *testing.T) {
			e, a, _, child := newActiveRecoveryRound(t, true)
			switch condition {
			case "wrong_ledger":
				e.setMode(consensus.ModeWrongLedger)
				e.wrongLedgerID = child.ID()
			case "demoted":
				a.SetOperatingMode(consensus.OpModeConnected)
			case "observing":
				e.setMode(consensus.ModeObserving)
			case "network_ahead":
				a.network = child.Seq() + 1
			case "incorrect_lcl":
				e.state.HaveCorrectLCL = false
			}
			result, err := e.TrySwitchToLedger(child.ID())
			require.NoError(t, err)
			require.Equal(t, consensus.LedgerSwitchAccepted, result)
			require.Equal(t, child.ID(), e.prevLedger.ID())
		})
	}
}

func TestEngine_RecoveryHandoffAllowsLocalFullValidation(t *testing.T) {
	e, a, clock, child := newActiveRecoveryRound(t, true)
	clock.advance(3 * time.Second)
	result, err := e.TrySwitchToLedger(child.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchBusy, result)

	// Drive the ordinary acceptance path after the recovery attempt. The
	// held replay result has not changed the round mode or its parent.
	e.mu.Lock()
	e.acceptLedger(consensus.ResultSuccess)
	e.mu.Unlock()
	require.NotEmpty(t, a.validationsBroadcast)
	validation := a.validationsBroadcast[0]
	require.Equal(t, child.ID(), validation.LedgerID)
	require.True(t, validation.Full)
	require.Empty(t, a.switchedLedgers)

	// Retrying the retained recovery completion is now an idempotent no-op,
	// not a recovery restart that makes the next validation partial.
	result, err = e.TrySwitchToLedger(child.ID())
	require.NoError(t, err)
	require.Equal(t, consensus.LedgerSwitchAccepted, result)
	require.Equal(t, consensus.ModeProposing, e.Mode())
	require.Empty(t, a.switchedLedgers)
}
