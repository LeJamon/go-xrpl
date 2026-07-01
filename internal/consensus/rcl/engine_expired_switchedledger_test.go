package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// expireRoundSetup drives an engine into a PhaseEstablish round that is past
// the 120s abandon ceiling and the per-avalanche minimum dwell, with four
// disagreeing trusted peers so checkConsensusState resolves to Expired. The
// peers' close-time votes are split 2/2 far beyond the resolution (and our
// own vote is a third value) so updateCloseTimePosition cannot re-derive
// close-time consensus — a persistent CT split.
func expireRoundSetup(t *testing.T, adaptor *mockAdaptor, engine *Engine, round consensus.RoundID, nidBase byte) {
	t.Helper()
	engine.setPhase(consensus.PhaseEstablish)
	engine.roundStartTime = time.Now().Add(-121 * time.Second)
	engine.prevRoundTime = 60 * time.Second
	engine.establishCounter = len(engine.parms.AvalancheCutoffs)*engine.parms.MinRounds + 1
	base := time.Now().Truncate(time.Second)
	engine.state.OurPosition = &consensus.Proposal{
		Round: round, Position: 1, TxSet: consensus.TxSetID{0xAA}, CloseTime: base.Add(200 * time.Second),
	}
	for i := 1; i <= 4; i++ {
		nid := consensus.NodeID{nidBase + byte(i)}
		adaptor.trusted[nid] = true
		ct := base
		if i%2 == 0 {
			ct = base.Add(100 * time.Second)
		}
		engine.proposalTracker.proposals[nid] = &consensus.Proposal{
			Round: round, NodeID: nid, Position: 1,
			TxSet: consensus.TxSetID{nidBase + 0x10 + byte(i)}, CloseTime: ct,
		}
	}
}

// An Expired round WITHOUT close-time consensus must not accept: rippled's
// haveConsensus bows the proposer out (leaveConsensus, Consensus.h:1784) but
// phaseEstablish then returns on !haveCloseTimeConsensus_ (Consensus.h:1406),
// leaving the round in Establish for checkLedger to resync. No ledger may be
// fabricated with a close time no peer agreed on.
func TestConsensus_Expired_NoCloseTimeConsensus_WaitsForResync(t *testing.T) {
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
	if engine.mode != consensus.ModeProposing {
		engine.mu.Unlock()
		t.Fatalf("setup: expected ModeProposing after StartRound, got %v", engine.mode)
	}
	expireRoundSetup(t, adaptor, engine, round, 0x30)
	engine.closeTime.haveConsensus = false

	engine.checkConvergence()

	phaseAfter := engine.phase
	modeAfter := engine.mode
	resyncArmed := engine.adaptor.Now().Before(engine.degradedResyncUntil)
	engine.mu.Unlock()

	if phaseAfter != consensus.PhaseEstablish {
		t.Errorf("expired round without CT consensus must stay in Establish (resync via checkLedger), got %v", phaseAfter)
	}
	if modeAfter != consensus.ModeObserving {
		t.Errorf("expired proposer must bow out to ModeObserving (rippled leaveConsensus), got %v", modeAfter)
	}
	if resyncArmed {
		t.Error("round expiry must not force a degraded resync")
	}

	select {
	case ev := <-subscriber.events:
		if cre, ok := ev.(*consensus.ConsensusReachedEvent); ok {
			t.Errorf("expired round without CT consensus must not accept, got result %v", cre.Result)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

// An Expired round WITH close-time consensus accepts the majority position
// (ResultAbandoned), matching rippled where haveConsensus returns true for
// Expired and phaseEstablish proceeds to onAccept.
func TestConsensus_Expired_WithCloseTimeConsensus_AcceptsAbandoned(t *testing.T) {
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
	expireRoundSetup(t, adaptor, engine, round, 0x40)
	engine.closeTime.haveConsensus = true

	engine.checkConvergence()

	phaseAfter := engine.phase
	engine.mu.Unlock()

	if phaseAfter == consensus.PhaseEstablish {
		t.Error("expired round with CT consensus must accept and leave Establish")
	}

	// leaveConsensus bows out to Observing at least transiently before the
	// next round re-promotes.
	adaptor.mu.RLock()
	sawObserving := false
	for _, m := range adaptor.modeChanges {
		if m == consensus.ModeObserving {
			sawObserving = true
		}
	}
	adaptor.mu.RUnlock()
	if !sawObserving {
		t.Error("expired proposing round must bow out to ModeObserving (rippled leaveConsensus)")
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
		t.Error("expired round with CT consensus must accept the majority position (ResultAbandoned)")
	}
}
