package rcl

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// Issue #421: a tracker must surface non-zero last_close.proposers once
// a round completes with trusted peer proposals on hand.
func TestEngine_TrackerReportsProposers_ExplicitStartRound(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = false
	adaptor.opMode = consensus.OpModeFull

	trusted := []consensus.NodeID{{0x10}, {0x11}, {0x12}}
	adaptor.setTrusted(trusted)
	adaptor.quorum = 2

	config := DefaultConfig()
	config.Timing.LedgerMinClose = 5 * time.Millisecond
	config.Timing.LedgerMaxClose = 200 * time.Millisecond
	config.Timing.LedgerMinConsensus = 5 * time.Millisecond
	config.Timing.LedgerMaxConsensus = 200 * time.Millisecond
	config.Timing.LedgerAbandonConsensus = 1 * time.Second
	config.Timing.LedgerIdleInterval = 10 * time.Millisecond
	config.Timing.ProposeFreshness = 5 * time.Second

	engine := NewEngine(adaptor, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	parent, _ := adaptor.GetLastClosedLedger()
	round := consensus.RoundID{Seq: parent.Seq() + 1, ParentHash: parent.ID()}
	if err := engine.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	now := adaptor.Now()
	for _, nodeID := range trusted {
		p := &consensus.Proposal{
			Round:          round,
			NodeID:         nodeID,
			Position:       0,
			TxSet:          consensus.TxSetID{1},
			CloseTime:      now,
			PreviousLedger: parent.ID(),
			Timestamp:      now,
		}
		if err := engine.OnProposal(p, 1); err != nil {
			t.Fatalf("OnProposal(%x): %v", nodeID[:1], err)
		}
	}

	// Wait long enough for the heartbeat to drive phaseOpen ->
	// phaseEstablish -> acceptLedger.
	deadline := time.Now().Add(3 * time.Second)
	var proposers int
	var convergeTime time.Duration
	for time.Now().Before(deadline) {
		proposers, convergeTime = engine.GetLastCloseInfo()
		if proposers > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if proposers == 0 {
		t.Fatalf("expected GetLastCloseInfo to report a non-zero "+
			"proposer count after a round with 3 trusted proposals; "+
			"got proposers=%d convergeTime=%v (phase=%v mode=%v)",
			proposers, convergeTime, engine.Phase(), engine.Mode())
	}
	if proposers != len(trusted) {
		t.Errorf("expected proposers=%d, got %d", len(trusted), proposers)
	}
}

// Proposals arrive BEFORE StartRound (peer positions buffered between
// rounds). The heartbeat must auto-start the round, replay the buffered
// positions, and surface a non-zero proposer count on accept.
func TestEngine_TrackerReportsProposers_TimerDriven(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = false
	adaptor.opMode = consensus.OpModeFull

	trusted := []consensus.NodeID{{0x20}, {0x21}, {0x22}}
	adaptor.setTrusted(trusted)
	adaptor.quorum = 2

	config := DefaultConfig()
	config.Timing.LedgerMinClose = 5 * time.Millisecond
	config.Timing.LedgerMaxClose = 200 * time.Millisecond
	config.Timing.LedgerMinConsensus = 5 * time.Millisecond
	config.Timing.LedgerMaxConsensus = 200 * time.Millisecond
	config.Timing.LedgerAbandonConsensus = 1 * time.Second
	config.Timing.LedgerIdleInterval = 10 * time.Millisecond
	config.Timing.ProposeFreshness = 5 * time.Second

	engine := NewEngine(adaptor, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	// Engine starts in PhaseAccepted. Buffer proposals for the
	// upcoming round (next seq, prevLedger = adaptor's LCL).
	parent, _ := adaptor.GetLastClosedLedger()
	round := consensus.RoundID{Seq: parent.Seq() + 1, ParentHash: parent.ID()}
	now := adaptor.Now()
	for _, nodeID := range trusted {
		p := &consensus.Proposal{
			Round:          round,
			NodeID:         nodeID,
			Position:       0,
			TxSet:          consensus.TxSetID{1},
			CloseTime:      now,
			PreviousLedger: parent.ID(),
			Timestamp:      now,
		}
		if err := engine.OnProposal(p, 1); err != nil {
			t.Fatalf("OnProposal(%x): %v", nodeID[:1], err)
		}
	}

	// No explicit StartRound — the engine's heartbeat
	// (checkAndStartRoundInner) must start the round and consume
	// the buffered proposals.
	deadline := time.Now().Add(3 * time.Second)
	var proposers int
	for time.Now().Before(deadline) {
		proposers, _ = engine.GetLastCloseInfo()
		if proposers > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if proposers == 0 {
		t.Fatalf("expected GetLastCloseInfo to report a non-zero "+
			"proposer count for a timer-driven tracker; got %d "+
			"(phase=%v mode=%v)",
			proposers, engine.Phase(), engine.Mode())
	}
	if proposers != len(trusted) {
		t.Errorf("expected proposers=%d, got %d", len(trusted), proposers)
	}
}

// Adaptor LCL jumps ahead via inbound acquisition; engine restarts the
// round via handleWrongLedger. The recovery round must still report a
// non-zero proposer count.
func TestEngine_TrackerReportsProposers_AfterWrongLedger(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = false
	adaptor.opMode = consensus.OpModeFull

	trusted := []consensus.NodeID{{0x30}, {0x31}, {0x32}}
	adaptor.setTrusted(trusted)
	adaptor.quorum = 2

	config := DefaultConfig()
	config.Timing.LedgerMinClose = 5 * time.Millisecond
	config.Timing.LedgerMaxClose = 200 * time.Millisecond
	config.Timing.LedgerMinConsensus = 5 * time.Millisecond
	config.Timing.LedgerMaxConsensus = 200 * time.Millisecond
	config.Timing.LedgerAbandonConsensus = 1 * time.Second
	config.Timing.LedgerIdleInterval = 10 * time.Millisecond
	config.Timing.ProposeFreshness = 5 * time.Second

	engine := NewEngine(adaptor, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	// Inbound ledger has jumped the adaptor's LCL to a seq ahead of
	// the engine's prevLedger. Stash the new ledger in the adaptor's
	// store so OnLedger can find it.
	adaptor.mu.Lock()
	jumpedLedger := &mockLedger{
		id:        consensus.LedgerID{0x99},
		seq:       150,
		closeTime: adaptor.now.Add(-100 * time.Millisecond),
	}
	adaptor.ledgers[jumpedLedger.ID()] = jumpedLedger
	adaptor.lastLCL = jumpedLedger
	adaptor.mu.Unlock()

	// Buffer fresh proposals from trusted peers for the round AFTER
	// the jumped ledger (PreviousLedger = jumpedLedger.ID()), as a
	// real network would have if peers were already proposing for
	// the next slot.
	now := adaptor.Now()
	round := consensus.RoundID{Seq: jumpedLedger.Seq() + 1, ParentHash: jumpedLedger.ID()}
	for _, nodeID := range trusted {
		p := &consensus.Proposal{
			Round:          round,
			NodeID:         nodeID,
			Position:       0,
			TxSet:          consensus.TxSetID{1},
			CloseTime:      now,
			PreviousLedger: jumpedLedger.ID(),
			Timestamp:      now,
		}
		if err := engine.OnProposal(p, 1); err != nil {
			t.Fatalf("OnProposal(%x): %v", nodeID[:1], err)
		}
	}

	// Force the engine into wrongLedger and feed it the catch-up
	// ledger via OnLedger — the path the router takes after an
	// inbound acquisition lands. handleWrongLedger restarts the
	// round with recovering=true.
	if err := engine.OnLedger(jumpedLedger.ID(), nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var proposers int
	for time.Now().Before(deadline) {
		proposers, _ = engine.GetLastCloseInfo()
		if proposers > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if proposers == 0 {
		t.Fatalf("expected GetLastCloseInfo to report a non-zero "+
			"proposer count after wrongLedger recovery; got %d "+
			"(phase=%v mode=%v)",
			proposers, engine.Phase(), engine.Mode())
	}
}

// Tracker never reaches acceptLedger (stays in Tracking mode); the
// fallback must still produce a non-zero count from buffered trusted
// proposals. Untrusted proposals must not inflate the count.
func TestEngine_TrackerReportsProposers_NoAcceptFallback(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = false
	// Stay in Tracking (not Full) so timerEntry never drives a round
	// to completion — the engine cannot publish a non-zero
	// prevProposers via acceptLedger in this state.
	adaptor.opMode = consensus.OpModeTracking

	trusted := []consensus.NodeID{{0x40}, {0x41}, {0x42}}
	adaptor.setTrusted(trusted)
	adaptor.quorum = 2

	config := DefaultConfig()
	config.Timing.ProposeFreshness = 30 * time.Second

	engine := NewEngine(adaptor, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	parent, _ := adaptor.GetLastClosedLedger()
	round := consensus.RoundID{Seq: parent.Seq() + 1, ParentHash: parent.ID()}
	now := adaptor.Now()
	for _, nodeID := range trusted {
		p := &consensus.Proposal{
			Round:          round,
			NodeID:         nodeID,
			Position:       0,
			TxSet:          consensus.TxSetID{1},
			CloseTime:      now,
			PreviousLedger: parent.ID(),
			Timestamp:      now,
		}
		if err := engine.OnProposal(p, 1); err != nil {
			t.Fatalf("OnProposal(%x): %v", nodeID[:1], err)
		}
	}

	// No round started, no acceptLedger fired. The fallback must
	// still produce a non-zero count.
	proposers, _ := engine.GetLastCloseInfo()
	if proposers != len(trusted) {
		t.Fatalf("expected fallback proposers=%d, got %d "+
			"(phase=%v mode=%v op_mode=%v)",
			len(trusted), proposers, engine.Phase(), engine.Mode(),
			adaptor.GetOperatingMode())
	}

	// Untrusted proposals must not inflate the count.
	untrusted := &consensus.Proposal{
		Round:          round,
		NodeID:         consensus.NodeID{0xAA},
		Position:       0,
		TxSet:          consensus.TxSetID{1},
		CloseTime:      now,
		PreviousLedger: parent.ID(),
		Timestamp:      now,
	}
	if err := engine.OnProposal(untrusted, 1); err != nil {
		t.Fatalf("OnProposal(untrusted): %v", err)
	}
	proposers, _ = engine.GetLastCloseInfo()
	if proposers != len(trusted) {
		t.Fatalf("untrusted proposer must not be counted: got %d, want %d",
			proposers, len(trusted))
	}
}

// Proposals older than ProposeFreshness must drop out of the count.
func TestEngine_RecentTrustedProposerCount_StaleProposalsExcluded(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = false
	adaptor.opMode = consensus.OpModeTracking

	trusted := []consensus.NodeID{{0x50}, {0x51}}
	adaptor.setTrusted(trusted)
	adaptor.quorum = 2

	config := DefaultConfig()
	config.Timing.ProposeFreshness = 100 * time.Millisecond

	engine := NewEngine(adaptor, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	parent, _ := adaptor.GetLastClosedLedger()
	round := consensus.RoundID{Seq: parent.Seq() + 1, ParentHash: parent.ID()}

	staleTime := adaptor.Now().Add(-1 * time.Second)
	for _, nodeID := range trusted {
		p := &consensus.Proposal{
			Round:          round,
			NodeID:         nodeID,
			Position:       0,
			TxSet:          consensus.TxSetID{1},
			CloseTime:      staleTime,
			PreviousLedger: parent.ID(),
			Timestamp:      staleTime,
		}
		if err := engine.OnProposal(p, 1); err != nil {
			t.Fatalf("OnProposal: %v", err)
		}
	}

	proposers, _ := engine.GetLastCloseInfo()
	if proposers != 0 {
		t.Fatalf("stale proposals must be excluded from count: got %d, want 0",
			proposers)
	}
}
