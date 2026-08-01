package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestAcceptLedgerWithoutTrustedPositionsDemotesOperatingModeAfterFullValidation(t *testing.T) {
	adaptor := newMockAdaptor()
	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	if err := engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	driveToEstablish(t, engine, adaptor)

	engine.mu.Lock()
	engine.acceptLedger(consensus.ResultSuccess)
	mode := engine.mode
	engine.mu.Unlock()

	if got := adaptor.GetOperatingMode(); got != consensus.OpModeConnected {
		t.Fatalf("operating mode = %v, want Connected after a round with no trusted positions", got)
	}
	if mode != consensus.ModeProposing {
		t.Fatalf("consensus mode = %v, want Proposing for the accepted round", mode)
	}

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("validations = %d, want one recovery validation", len(adaptor.validationsBroadcast))
	}
	if !adaptor.validationsBroadcast[0].Full {
		t.Fatal("validation must remain full for a round accepted while proposing")
	}
}

func TestFullValidationReflectsRoundModeOnly(t *testing.T) {
	adaptor := newMockAdaptor()
	peerA := consensus.NodeID{2}
	peerB := consensus.NodeID{3}
	adaptor.setTrusted([]consensus.NodeID{peerA, peerB})

	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	now := adaptor.Now()
	preferred := consensus.LedgerID{0xF3}
	for _, nodeID := range []consensus.NodeID{peerA, peerB} {
		if !engine.validationTracker.Add(&consensus.Validation{
			NodeID:    nodeID,
			LedgerID:  preferred,
			LedgerSeq: 103,
			Full:      true,
			SignTime:  now,
			SeenTime:  now,
		}) {
			t.Fatalf("failed to add trusted validation for %x", nodeID[:4])
		}
	}

	if err := engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := engine.Mode(); got != consensus.ModeObserving {
		t.Fatalf("mode = %v, want Observing while preferred frontier is ahead", got)
	}
	if got := adaptor.GetOperatingMode(); got != consensus.OpModeConnected {
		t.Fatalf("operating mode = %v, want Connected while preferred frontier is ahead", got)
	}

	adaptor.SetOperatingMode(consensus.OpModeFull)
	engine.mu.Lock()
	engine.setMode(consensus.ModeProposing)
	engine.sendValidation(&mockLedger{
		id:        consensus.LedgerID{101},
		seq:       101,
		parentID:  consensus.LedgerID{1},
		closeTime: now.Add(time.Second),
	})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("validations = %d, want one", len(adaptor.validationsBroadcast))
	}
	if !adaptor.validationsBroadcast[0].Full {
		t.Fatal("validation must be full when the accepted round is proposing")
	}
}

func TestAcceptLedgerPreferredFrontierMovedAheadUsesCapturedRoundMode(t *testing.T) {
	adaptor := newMockAdaptor()
	peerA := consensus.NodeID{2}
	peerB := consensus.NodeID{3}
	adaptor.setTrusted([]consensus.NodeID{peerA, peerB})

	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	driveToEstablish(t, engine, adaptor)

	now := adaptor.Now()
	for _, nodeID := range []consensus.NodeID{peerA, peerB} {
		if !engine.validationTracker.Add(&consensus.Validation{
			NodeID:    nodeID,
			LedgerID:  consensus.LedgerID{0xF4},
			LedgerSeq: 103,
			Full:      true,
			SignTime:  now,
			SeenTime:  now,
		}) {
			t.Fatalf("failed to add trusted validation for %x", nodeID[:4])
		}
	}

	engine.mu.Lock()
	engine.proposalTracker.Store(&consensus.Proposal{
		NodeID:         peerA,
		Round:          round,
		PreviousLedger: round.ParentHash,
		Timestamp:      now,
	})
	engine.acceptLedger(consensus.ResultSuccess)
	engine.mu.Unlock()

	if got := adaptor.GetOperatingMode(); got != consensus.OpModeFull {
		t.Fatalf("operating mode = %v, want Full with trusted positions in the accepted round", got)
	}
	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("validations = %d, want one", len(adaptor.validationsBroadcast))
	}
	if !adaptor.validationsBroadcast[0].Full {
		t.Fatal("an accepted proposing round must emit a full validation")
	}
}

func TestAcceptLedgerCurrentPreferredWithTrustedPositionEmitsFull(t *testing.T) {
	adaptor := newMockAdaptor()
	peerA := consensus.NodeID{2}
	peerB := consensus.NodeID{3}
	adaptor.setTrusted([]consensus.NodeID{peerA, peerB})

	preferred := &mockLedger{
		id:        consensus.LedgerID{101},
		seq:       101,
		parentID:  consensus.LedgerID{1},
		closeTime: adaptor.Now().Add(time.Second),
	}
	adaptor.mu.Lock()
	adaptor.ledgers[preferred.ID()] = preferred
	adaptor.mu.Unlock()

	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	now := adaptor.Now()
	for _, nodeID := range []consensus.NodeID{peerA, peerB} {
		if !engine.validationTracker.Add(&consensus.Validation{
			NodeID:    nodeID,
			LedgerID:  preferred.ID(),
			LedgerSeq: preferred.Seq(),
			Full:      true,
			SignTime:  now,
			SeenTime:  now,
		}) {
			t.Fatalf("failed to add trusted validation for %x", nodeID[:4])
		}
	}

	if err := engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := engine.Mode(); got != consensus.ModeProposing {
		t.Fatalf("mode = %v, want Proposing on the parent of the preferred ledger", got)
	}
	driveToEstablish(t, engine, adaptor)

	engine.mu.Lock()
	engine.proposalTracker.Store(&consensus.Proposal{
		NodeID:         peerA,
		Round:          consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}},
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      now,
	})
	engine.acceptLedger(consensus.ResultSuccess)
	engine.mu.Unlock()

	if got := adaptor.GetOperatingMode(); got != consensus.OpModeFull {
		t.Fatalf("operating mode = %v, want Full on the current preferred ledger", got)
	}
	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("validations = %d, want one", len(adaptor.validationsBroadcast))
	}
	if !adaptor.validationsBroadcast[0].Full {
		t.Fatal("a genuinely proposing validator on the current preferred ledger must emit a full validation")
	}
}

func TestAcceptLedgerKeepsPreferredDirectChildForNextRound(t *testing.T) {
	adaptor := newMockAdaptor()
	peerA := consensus.NodeID{2}
	peerB := consensus.NodeID{3}
	adaptor.setTrusted([]consensus.NodeID{peerA, peerB})

	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	driveToEstablish(t, engine, adaptor)

	now := adaptor.Now()
	directChild := &mockLedger{
		id:        consensus.LedgerID{102},
		seq:       102,
		parentID:  consensus.LedgerID{101},
		closeTime: now.Add(time.Second),
	}
	adaptor.mu.Lock()
	adaptor.ledgers[directChild.ID()] = directChild
	adaptor.mu.Unlock()
	for _, nodeID := range []consensus.NodeID{peerA, peerB} {
		if !engine.validationTracker.Add(&consensus.Validation{
			NodeID: nodeID, LedgerID: directChild.ID(), LedgerSeq: directChild.Seq(),
			Full: true, SignTime: now, SeenTime: now,
		}) {
			t.Fatalf("failed to add trusted validation for %x", nodeID[:4])
		}
	}

	engine.mu.Lock()
	engine.proposalTracker.Store(&consensus.Proposal{
		NodeID: peerA, Round: round, PreviousLedger: round.ParentHash, Timestamp: now,
	})
	engine.acceptLedger(consensus.ResultSuccess)
	nextRound := engine.state.Round
	engine.mu.Unlock()

	if nextRound.Seq != 102 || nextRound.ParentHash != (consensus.LedgerID{101}) {
		t.Fatalf("next round = %+v, want seq 102 on locally accepted ledger 101", nextRound)
	}
}
