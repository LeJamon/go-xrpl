package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestPhaseEstablish_PauseStillRefreshesProposal(t *testing.T) {
	adaptor := newMockAdaptor()
	peerA := consensus.NodeID{2}
	peerB := consensus.NodeID{3}
	adaptor.setTrusted([]consensus.NodeID{adaptor.nodeID, peerA, peerB})
	adaptor.quorum = 3

	validatedID := consensus.LedgerID{9}
	validated := &mockLedger{id: validatedID, seq: 9, closeTime: adaptor.Now()}
	adaptor.ledgers[validatedID] = validated
	adaptor.validatedLedgerHashOverride = validatedID

	config := DefaultConfig()
	config.ManualTick = true
	config.Timing.LedgerMinConsensus = 100 * time.Millisecond
	config.Timing.LedgerMaxConsensus = 2 * time.Second
	config.Timing.ProposeInterval = 50 * time.Millisecond
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	prev := &mockLedger{id: consensus.LedgerID{12}, seq: 12, closeTime: adaptor.Now()}
	round := consensus.RoundID{Seq: 13, ParentHash: prev.ID()}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	now := adaptor.Now()
	engine.mu.Lock()
	engine.prevLedger = prev
	engine.ourLastValidatedSeq = validated.Seq()
	engine.roundStartTime = now.Add(-500 * time.Millisecond)
	engine.setMode(consensus.ModeProposing)
	engine.setPhase(consensus.PhaseEstablish)
	engine.disputeTracker = newDisputeTracker()
	engine.ourTxSet = buildMockTxSet(consensus.TxSetID{0xA1})
	engine.state.OurPosition = &consensus.Proposal{
		Round:          round,
		NodeID:         adaptor.nodeID,
		Position:       1,
		TxSet:          engine.ourTxSet.ID(),
		CloseTime:      now,
		PreviousLedger: prev.ID(),
		Timestamp:      now.Add(-config.Timing.ProposeInterval),
	}
	engine.validationTracker.setTrusted([]consensus.NodeID{adaptor.nodeID, peerA, peerB})
	for _, nodeID := range []consensus.NodeID{peerA, peerB} {
		if !engine.validationTracker.Add(&consensus.Validation{
			NodeID:    nodeID,
			LedgerID:  validated.ID(),
			LedgerSeq: validated.Seq(),
			Full:      true,
			SignTime:  now,
			SeenTime:  now,
		}) {
			engine.mu.Unlock()
			t.Fatalf("failed to add laggard validation from %x", nodeID[:4])
		}
	}
	if !engine.shouldPause(500 * time.Millisecond) {
		engine.mu.Unlock()
		t.Fatal("setup did not trigger the laggard pause")
	}

	engine.phaseEstablish(nil)
	phase := engine.phase
	position := engine.state.OurPosition.Position
	timestamp := engine.state.OurPosition.Timestamp
	engine.mu.Unlock()

	if phase != consensus.PhaseEstablish {
		t.Fatalf("paused round phase = %v, want Establish", phase)
	}
	if position != 2 {
		t.Fatalf("paused round proposal position = %d, want freshness re-proposal at 2", position)
	}
	if !timestamp.Equal(now) {
		t.Fatalf("paused round proposal timestamp = %v, want %v", timestamp, now)
	}
	adaptor.mu.RLock()
	broadcasts := len(adaptor.proposalsBroadcast)
	adaptor.mu.RUnlock()
	if broadcasts != 1 {
		t.Fatalf("paused round proposal broadcasts = %d, want 1", broadcasts)
	}
}
