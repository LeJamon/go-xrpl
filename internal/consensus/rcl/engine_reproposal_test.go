package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// A proposing node whose position hasn't changed but has gone stale (older
// than ProposeInterval) must re-broadcast it with a bumped seq, so peers
// don't prune it at ProposeFreshness during a long round (rippled
// Consensus.h:1636-1642). Before the fix, updatePosition returned without
// emitting when no dispute flipped, and the position went silent.
func TestEngine_UpdatePosition_FreshnessRepropose(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	engine.setMode(consensus.ModeProposing)
	engine.disputeTracker = NewDisputeTracker()
	set := buildMockTxSet(consensus.TxSetID{0x5E})
	engine.ourTxSet = set
	// Position last emitted well beyond ProposeInterval ago.
	engine.state.OurPosition = &consensus.Proposal{
		Round:     round,
		Position:  1,
		TxSet:     set.ID(),
		Timestamp: engine.adaptor.Now().Add(-engine.timing.ProposeInterval - time.Second),
	}
	adaptor.mu.Lock()
	adaptor.proposalsBroadcast = nil
	adaptor.mu.Unlock()

	engine.updatePosition()
	newSeq := engine.state.OurPosition.Position
	engine.mu.Unlock()

	adaptor.mu.RLock()
	n := len(adaptor.proposalsBroadcast)
	adaptor.mu.RUnlock()

	if n != 1 {
		t.Fatalf("stale unchanged position must be re-broadcast once, got %d", n)
	}
	if newSeq != 2 {
		t.Fatalf("re-proposal must bump the position seq to 2, got %d", newSeq)
	}
}

// A fresh position (updated within ProposeInterval) must NOT be re-broadcast.
func TestEngine_UpdatePosition_FreshPositionNoRepropose(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	engine.setMode(consensus.ModeProposing)
	engine.disputeTracker = NewDisputeTracker()
	set := buildMockTxSet(consensus.TxSetID{0x5E})
	engine.ourTxSet = set
	engine.state.OurPosition = &consensus.Proposal{
		Round:     round,
		Position:  1,
		TxSet:     set.ID(),
		Timestamp: engine.adaptor.Now(), // fresh
	}
	adaptor.mu.Lock()
	adaptor.proposalsBroadcast = nil
	adaptor.mu.Unlock()

	engine.updatePosition()
	engine.mu.Unlock()

	adaptor.mu.RLock()
	n := len(adaptor.proposalsBroadcast)
	adaptor.mu.RUnlock()
	if n != 0 {
		t.Fatalf("a fresh unchanged position must not be re-broadcast, got %d", n)
	}
}

// leaveConsensusLocked must broadcast a seqLeave bow-out so peers drop our
// position immediately (rippled Consensus.h:1807-1810), instead of leaving
// them to prune it at ProposeFreshness.
func TestEngine_LeaveConsensus_BroadcastsBowOut(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	engine.setMode(consensus.ModeProposing)
	set := buildMockTxSet(consensus.TxSetID{0x5E})
	engine.state.OurPosition = &consensus.Proposal{
		Round: round, Position: 3, TxSet: set.ID(), Timestamp: engine.adaptor.Now(),
	}
	adaptor.mu.Lock()
	adaptor.proposalsBroadcast = nil
	adaptor.mu.Unlock()

	engine.leaveConsensusLocked()
	modeAfter := engine.mode
	engine.mu.Unlock()

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.proposalsBroadcast) != 1 {
		t.Fatalf("leaveConsensus must broadcast one bow-out, got %d", len(adaptor.proposalsBroadcast))
	}
	if got := adaptor.proposalsBroadcast[0].Position; got != 0xFFFFFFFF {
		t.Fatalf("bow-out must carry seqLeave (0xFFFFFFFF), got %d", got)
	}
	if modeAfter != consensus.ModeObserving {
		t.Fatalf("leaveConsensus must drop to Observing, got %v", modeAfter)
	}
}
