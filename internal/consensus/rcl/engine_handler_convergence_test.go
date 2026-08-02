package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func timerDrivenConvergenceEngine(t *testing.T, txSet *mockTxSet) (*Engine, *mockAdaptor, consensus.NodeID) {
	t.Helper()

	adaptor := newMockAdaptor()
	peer := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{peer})
	adaptor.quorum = 1
	adaptor.txSets[txSet.ID()] = txSet

	config := DefaultConfig()
	config.ManualTick = true
	engine := NewEngine(adaptor, config)
	if err := engine.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	lastLCL, err := adaptor.GetLastClosedLedger()
	if err != nil {
		t.Fatalf("GetLastClosedLedger: %v", err)
	}
	round := consensus.RoundID{Seq: lastLCL.Seq() + 1, ParentHash: lastLCL.ID()}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	engine.mu.Lock()
	engine.prevLedger = lastLCL
	engine.roundStartTime = adaptor.Now().Add(-config.Timing.LedgerMinConsensus - time.Second)
	engine.setMode(consensus.ModeProposing)
	engine.setPhase(consensus.PhaseEstablish)
	engine.ourTxSet = txSet
	engine.acquiredTxSets[txSet.ID()] = txSet
	engine.disputeTracker = newDisputeTracker()
	engine.closeTime.haveConsensus = true
	engine.state.OurPosition = &consensus.Proposal{
		Round:          round,
		NodeID:         adaptor.nodeID,
		TxSet:          txSet.ID(),
		PreviousLedger: lastLCL.ID(),
		CloseTime:      adaptor.Now(),
		Timestamp:      adaptor.Now(),
	}
	engine.mu.Unlock()

	return engine, adaptor, peer
}

func adaptorLCLSeq(t *testing.T, adaptor *mockAdaptor) uint32 {
	t.Helper()
	ledger, err := adaptor.GetLastClosedLedger()
	if err != nil {
		t.Fatalf("GetLastClosedLedger: %v", err)
	}
	return ledger.Seq()
}

func TestEngine_OnProposalDoesNotConvergeBeforeTimer(t *testing.T) {
	txSet := buildMockTxSet(consensus.TxSetID{0xA1})
	engine, adaptor, peer := timerDrivenConvergenceEngine(t, txSet)
	proposal := &consensus.Proposal{
		Round:          engine.state.Round,
		NodeID:         peer,
		TxSet:          txSet.ID(),
		PreviousLedger: adaptor.lastLCL.ID(),
		CloseTime:      adaptor.Now(),
		Timestamp:      adaptor.Now(),
	}

	if err := engine.OnProposal(proposal, 1); err != nil {
		t.Fatalf("OnProposal: %v", err)
	}
	if got := engine.Phase(); got != consensus.PhaseEstablish {
		t.Fatalf("phase after proposal = %v, want Establish until TimerEntry", got)
	}
	if got := adaptorLCLSeq(t, adaptor); got != 100 {
		t.Fatalf("LCL sequence after proposal = %d, want 100 until TimerEntry", got)
	}

	engine.TimerEntry()
	if got := adaptorLCLSeq(t, adaptor); got != 101 {
		t.Fatalf("LCL sequence after TimerEntry = %d, want 101", got)
	}
}

func TestEngine_OnTxSetDoesNotConvergeBeforeTimer(t *testing.T) {
	tx := make([]byte, 32)
	tx[0] = 1
	txSet := &mockTxSet{
		id:          consensus.TxSetID{1},
		txs:         [][]byte{tx},
		txIDs:       []consensus.TxID{{1}},
		containsTxs: map[consensus.TxID]bool{{1}: true},
	}
	engine, adaptor, peer := timerDrivenConvergenceEngine(t, txSet)
	engine.mu.Lock()
	engine.proposalTracker.Store(&consensus.Proposal{
		Round:          engine.state.Round,
		NodeID:         peer,
		TxSet:          txSet.ID(),
		PreviousLedger: engine.prevLedger.ID(),
	})
	delete(engine.acquiredTxSets, txSet.ID())
	engine.mu.Unlock()

	if err := engine.OnTxSet(txSet.ID(), txSet.Txs()); err != nil {
		t.Fatalf("OnTxSet: %v", err)
	}
	if got := engine.Phase(); got != consensus.PhaseEstablish {
		t.Fatalf("phase after tx set = %v, want Establish until TimerEntry", got)
	}
	if got := adaptorLCLSeq(t, adaptor); got != 100 {
		t.Fatalf("LCL sequence after tx set = %d, want 100 until TimerEntry", got)
	}

	engine.TimerEntry()
	if got := adaptorLCLSeq(t, adaptor); got != 101 {
		t.Fatalf("LCL sequence after TimerEntry = %d, want 101", got)
	}
}
