package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestConsensus_ExpiredSwitchedLedger_ResyncsInsteadOfLivelock pins the #1161
// consensus livelock. A checkLedger view-change restarts the round in
// ModeSwitchedLedger (non-proposing), so it contributes no self close-time vote;
// under a persistent network close-time split closeTime.haveConsensus then stays
// false forever. The old checkConvergence gated EVERY non-No accept — including
// the Expired hard timeout — behind haveConsensus, so the round never accepted,
// never bowed out, and the per-close heartbeat froze until the watchdog
// fatal-aborted the node.
//
// The Expired hard-timeout path now runs BEFORE the close-time gate (rippled
// runs leaveConsensus inside haveConsensus, Consensus.h:1784, ahead of
// phaseEstablish's CT return, Consensus.h:1406). The round must terminate
// (ResultAbandoned closes a ledger so the heartbeat ticks) and a switched-ledger
// round must drop to a degraded resync so the node catches the validated tip
// instead of livelocking.
func TestConsensus_ExpiredSwitchedLedger_ResyncsInsteadOfLivelock(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	config.Timing.LedgerMaxConsensus = 15 * time.Second
	config.Timing.LedgerAbandonConsensus = 120 * time.Second
	config.Timing.LedgerAbandonConsensusFactor = 10

	engine := NewEngine(adaptor, config)
	subscriber := &testSubscriber{events: make(chan consensus.Event, 32)}
	engine.Subscribe(subscriber)

	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	engine.mu.Lock()

	// Model the view-change restart: pinned in ModeSwitchedLedger and never
	// reaching close-time consensus.
	engine.setMode(consensus.ModeSwitchedLedger)
	engine.setPhase(consensus.PhaseEstablish)
	engine.closeTime.haveConsensus = false

	// Past the 120s hard ceiling and past the per-avalanche minimum dwell.
	engine.roundStartTime = time.Now().Add(-121 * time.Second)
	engine.prevRoundTime = 60 * time.Second
	engine.establishCounter = len(engine.parms.AvalancheCutoffs)*engine.parms.MinRounds + 1

	// Disagreeing trusted peers so checkConsensus resolves to Expired, not Yes,
	// and so acceptLedger has a trusted tx set to build the abandoned ledger on.
	engine.state.OurPosition = &consensus.Proposal{Round: round, Position: 1, TxSet: consensus.TxSetID{0xAA}}
	for i := 1; i <= 4; i++ {
		nid := consensus.NodeID{byte(0x30 + i)}
		adaptor.trusted[nid] = true
		engine.proposalTracker.proposals[nid] = &consensus.Proposal{
			Round: round, NodeID: nid, Position: 1, TxSet: consensus.TxSetID{byte(0xD0 + i)},
		}
	}

	engine.checkConvergence()

	phaseAfter := engine.phase
	modeAfter := engine.mode
	opModeAfter := adaptor.GetOperatingMode()
	resyncArmed := engine.adaptor.Now().Before(engine.degradedResyncUntil)
	haveCT := engine.closeTime.haveConsensus
	engine.mu.Unlock()

	// Close-time consensus was false throughout: the old CT gate would have
	// returned early here (livelock). The guard makes the test non-vacuous.
	if haveCT {
		t.Fatalf("setup drifted: closeTime.haveConsensus must stay false to model the split")
	}
	if phaseAfter == consensus.PhaseEstablish {
		t.Errorf("expired switched-ledger round must terminate, still in Establish (livelock)")
	}
	if modeAfter != consensus.ModeObserving {
		t.Errorf("switched-ledger expiry must drop to ModeObserving via degraded resync, got %v", modeAfter)
	}
	if opModeAfter != consensus.OpModeTracking {
		t.Errorf("degraded resync must demote OpModeFull→Tracking, got %v", opModeAfter)
	}
	if !resyncArmed {
		t.Error("degraded-resync cooldown must be armed so the node resyncs to the validated tip")
	}

	sawAbandoned := false
	deadline := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-subscriber.events:
			if cre, ok := ev.(*consensus.ConsensusReachedEvent); ok && cre.Result == consensus.ResultAbandoned {
				sawAbandoned = true
			}
		case <-deadline:
			break drain
		}
	}
	if !sawAbandoned {
		t.Error("expired switched-ledger round must accept the majority position (ResultAbandoned) so a ledger closes and the heartbeat ticks")
	}
}

// TestConsensus_ExpiredProposing_BowsOutWithoutForcedDemotion confirms the
// normal proposer expiry is unchanged: a proposing validator that hits the hard
// timeout bows out to Observing (rippled leaveConsensus) and accepts the
// majority position, but is NOT force-demoted to a degraded resync (that path is
// reserved for a switched-ledger round checkStuckWrongLedger can't rescue). The
// per-close heartbeat still ticks because the round terminates.
func TestConsensus_ExpiredProposing_BowsOutWithoutForcedDemotion(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	config.Timing.LedgerMaxConsensus = 15 * time.Second
	config.Timing.LedgerAbandonConsensus = 120 * time.Second
	config.Timing.LedgerAbandonConsensusFactor = 10

	engine := NewEngine(adaptor, config)
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	if engine.mode != consensus.ModeProposing {
		engine.mu.Unlock()
		t.Fatalf("setup: expected ModeProposing after StartRound, got %v", engine.mode)
	}
	engine.setPhase(consensus.PhaseEstablish)
	engine.closeTime.haveConsensus = false
	engine.roundStartTime = time.Now().Add(-121 * time.Second)
	engine.prevRoundTime = 60 * time.Second
	engine.establishCounter = len(engine.parms.AvalancheCutoffs)*engine.parms.MinRounds + 1
	engine.state.OurPosition = &consensus.Proposal{Round: round, Position: 1, TxSet: consensus.TxSetID{0xAA}}
	for i := 1; i <= 4; i++ {
		nid := consensus.NodeID{byte(0x40 + i)}
		adaptor.trusted[nid] = true
		engine.proposalTracker.proposals[nid] = &consensus.Proposal{
			Round: round, NodeID: nid, Position: 1, TxSet: consensus.TxSetID{byte(0xE0 + i)},
		}
	}

	engine.checkConvergence()

	phaseAfter := engine.phase
	resyncArmed := engine.adaptor.Now().Before(engine.degradedResyncUntil)
	engine.mu.Unlock()

	if phaseAfter == consensus.PhaseEstablish {
		t.Errorf("expired proposing round must terminate, still in Establish")
	}
	// leaveConsensus bows out to Observing at least transiently.
	adaptor.mu.RLock()
	sawObserving := false
	for _, m := range adaptor.modeChanges {
		if m == consensus.ModeObserving {
			sawObserving = true
		}
	}
	adaptor.mu.RUnlock()
	if !sawObserving {
		t.Errorf("expired proposing round must bow out to ModeObserving (rippled leaveConsensus)")
	}
	// A proposer expiry must NOT arm the degraded resync — that's the
	// switched-ledger-only recovery. Force-demoting every timed-out proposer
	// would make nodes bail constantly under transient splits.
	if resyncArmed {
		t.Errorf("proposer expiry must not force a degraded resync (over-eager bailout)")
	}
}
