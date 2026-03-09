package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

func TestProposalTracker_Add(t *testing.T) {
	pt := NewProposalTracker(20 * time.Second)

	round := consensus.RoundID{Seq: 100}
	pt.SetRound(round)

	node1 := consensus.NodeID{1}
	txSet1 := consensus.TxSetID{1}

	proposal := &consensus.Proposal{
		Round:     round,
		NodeID:    node1,
		Position:  0,
		TxSet:     txSet1,
		Timestamp: time.Now(),
	}

	// Add proposal
	if !pt.Add(proposal) {
		t.Error("First proposal should be added")
	}

	// Count should be 1
	if pt.Count() != 1 {
		t.Errorf("Expected 1 proposal, got %d", pt.Count())
	}

	// Adding same position should return false
	if pt.Add(proposal) {
		t.Error("Same position proposal should not be added")
	}
}

func TestProposalTracker_UpdatePosition(t *testing.T) {
	pt := NewProposalTracker(20 * time.Second)

	round := consensus.RoundID{Seq: 100}
	pt.SetRound(round)

	node1 := consensus.NodeID{1}
	txSet1 := consensus.TxSetID{1}
	txSet2 := consensus.TxSetID{2}

	// Initial proposal for txSet1
	pt.Add(&consensus.Proposal{
		Round:     round,
		NodeID:    node1,
		Position:  0,
		TxSet:     txSet1,
		Timestamp: time.Now(),
	})

	// Update to txSet2
	pt.Add(&consensus.Proposal{
		Round:     round,
		NodeID:    node1,
		Position:  1,
		TxSet:     txSet2,
		Timestamp: time.Now(),
	})

	// Should have 1 proposal
	if pt.Count() != 1 {
		t.Errorf("Expected 1 proposal, got %d", pt.Count())
	}

	// Current proposal should be for txSet2
	p := pt.Get(node1)
	if p.TxSet != txSet2 {
		t.Error("Proposal should be updated to txSet2")
	}

	// TxSet1 should have 0 supporters
	if len(pt.GetForTxSet(txSet1)) != 0 {
		t.Error("TxSet1 should have no supporters")
	}

	// TxSet2 should have 1 supporter
	if len(pt.GetForTxSet(txSet2)) != 1 {
		t.Error("TxSet2 should have 1 supporter")
	}
}

func TestProposalTracker_TrustedCounts(t *testing.T) {
	pt := NewProposalTracker(20 * time.Second)

	round := consensus.RoundID{Seq: 100}
	pt.SetRound(round)

	nodes := []consensus.NodeID{{1}, {2}, {3}, {4}}
	pt.SetTrusted(nodes[:3]) // First 3 are trusted

	txSet1 := consensus.TxSetID{1}
	txSet2 := consensus.TxSetID{2}

	// Add proposals
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[0], Position: 0, TxSet: txSet1})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[1], Position: 0, TxSet: txSet1})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[2], Position: 0, TxSet: txSet2})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[3], Position: 0, TxSet: txSet2}) // Untrusted

	// Total count should be 4
	if pt.Count() != 4 {
		t.Errorf("Expected 4 proposals, got %d", pt.Count())
	}

	// Trusted count should be 3
	if pt.TrustedCount() != 3 {
		t.Errorf("Expected 3 trusted proposals, got %d", pt.TrustedCount())
	}

	// TxSet counts
	counts := pt.TrustedTxSetCounts()
	if counts[txSet1] != 2 {
		t.Errorf("Expected 2 trusted for txSet1, got %d", counts[txSet1])
	}
	if counts[txSet2] != 1 {
		t.Errorf("Expected 1 trusted for txSet2, got %d", counts[txSet2])
	}
}

func TestProposalTracker_WinningTxSet(t *testing.T) {
	pt := NewProposalTracker(20 * time.Second)

	round := consensus.RoundID{Seq: 100}
	pt.SetRound(round)

	nodes := []consensus.NodeID{{1}, {2}, {3}, {4}, {5}}
	pt.SetTrusted(nodes)

	txSet1 := consensus.TxSetID{1}
	txSet2 := consensus.TxSetID{2}

	// Add proposals: 3 for txSet1, 2 for txSet2
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[0], Position: 0, TxSet: txSet1})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[1], Position: 0, TxSet: txSet1})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[2], Position: 0, TxSet: txSet1})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[3], Position: 0, TxSet: txSet2})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[4], Position: 0, TxSet: txSet2})

	// Winning should be txSet1 with 3
	winningID, winningCount := pt.GetWinningTxSet()
	if winningID != txSet1 {
		t.Error("Winning tx set should be txSet1")
	}
	if winningCount != 3 {
		t.Errorf("Winning count should be 3, got %d", winningCount)
	}
}

func TestProposalTracker_Convergence(t *testing.T) {
	pt := NewProposalTracker(20 * time.Second)

	round := consensus.RoundID{Seq: 100}
	pt.SetRound(round)

	nodes := []consensus.NodeID{{1}, {2}, {3}, {4}, {5}}
	pt.SetTrusted(nodes)

	txSet1 := consensus.TxSetID{1}
	txSet2 := consensus.TxSetID{2}

	// Initially divergent: 2 vs 3
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[0], Position: 0, TxSet: txSet1})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[1], Position: 0, TxSet: txSet1})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[2], Position: 0, TxSet: txSet2})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[3], Position: 0, TxSet: txSet2})
	pt.Add(&consensus.Proposal{Round: round, NodeID: nodes[4], Position: 0, TxSet: txSet2})

	// Not converged at 80% threshold (3/5 = 60%)
	if pt.HasConverged(0.8) {
		t.Error("Should not be converged at 80%")
	}

	// Converged at 50% threshold
	if !pt.HasConverged(0.5) {
		t.Error("Should be converged at 50%")
	}
}

func TestProposalTracker_WrongRound(t *testing.T) {
	pt := NewProposalTracker(20 * time.Second)

	round := consensus.RoundID{Seq: 100}
	wrongRound := consensus.RoundID{Seq: 99}
	pt.SetRound(round)

	node1 := consensus.NodeID{1}
	txSet1 := consensus.TxSetID{1}

	// Proposal for wrong round should not be added
	if pt.Add(&consensus.Proposal{
		Round:    wrongRound,
		NodeID:   node1,
		Position: 0,
		TxSet:    txSet1,
	}) {
		t.Error("Proposal for wrong round should not be added")
	}

	if pt.Count() != 0 {
		t.Error("Count should be 0")
	}
}

func TestDisputeTracker_CreateAndVote(t *testing.T) {
	dt := NewDisputeTracker()

	txID := consensus.TxID{1}
	tx := []byte("test tx")

	// Create dispute
	dispute := dt.CreateDispute(txID, tx, true)
	if dispute == nil {
		t.Fatal("Dispute should be created")
	}

	if dispute.Yays != 1 || dispute.Nays != 0 {
		t.Errorf("Initial vote should be 1 yay, 0 nay; got %d/%d", dispute.Yays, dispute.Nays)
	}

	// Add votes
	dt.AddVote(txID, true)
	dt.AddVote(txID, true)
	dt.AddVote(txID, false)

	dispute = dt.GetDispute(txID)
	if dispute.Yays != 3 || dispute.Nays != 1 {
		t.Errorf("Expected 3 yays, 1 nay; got %d/%d", dispute.Yays, dispute.Nays)
	}
}

func TestDisputeTracker_Resolve(t *testing.T) {
	dt := NewDisputeTracker()

	tx1 := consensus.TxID{1}
	tx2 := consensus.TxID{2}

	// Create disputes
	dt.CreateDispute(tx1, []byte("tx1"), true)
	dt.CreateDispute(tx2, []byte("tx2"), false)

	// Add votes for tx1 (should be included)
	dt.AddVote(tx1, true)
	dt.AddVote(tx1, true)
	dt.AddVote(tx1, false)

	// Add votes for tx2 (should be excluded)
	dt.AddVote(tx2, false)
	dt.AddVote(tx2, false)
	dt.AddVote(tx2, true)

	// Resolve at 60% threshold
	include, exclude := dt.Resolve(0.6)

	// tx1 has 4 yays, 1 nay = 80% yay -> include
	foundTx1Include := false
	for _, id := range include {
		if id == tx1 {
			foundTx1Include = true
		}
	}
	if !foundTx1Include {
		t.Error("tx1 should be included")
	}

	// tx2 has 1 yay, 3 nay = 25% yay -> exclude
	foundTx2Exclude := false
	for _, id := range exclude {
		if id == tx2 {
			foundTx2Exclude = true
		}
	}
	if !foundTx2Exclude {
		t.Error("tx2 should be excluded")
	}
}

func TestDisputeTracker_UpdateOurVote(t *testing.T) {
	dt := NewDisputeTracker()

	txID := consensus.TxID{1}
	dt.CreateDispute(txID, []byte("tx"), true) // Initial: yay

	dispute := dt.GetDispute(txID)
	if dispute.Yays != 1 || dispute.Nays != 0 {
		t.Errorf("Initial should be 1/0, got %d/%d", dispute.Yays, dispute.Nays)
	}

	// Change our vote to nay
	dt.UpdateOurVote(txID, false)

	dispute = dt.GetDispute(txID)
	if dispute.Yays != 0 || dispute.Nays != 1 {
		t.Errorf("After update should be 0/1, got %d/%d", dispute.Yays, dispute.Nays)
	}

	if dispute.OurVote != false {
		t.Error("OurVote should be false")
	}
}
