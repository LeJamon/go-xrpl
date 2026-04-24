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

	// Create dispute. Yays/Nays count peer votes only; our stance
	// lives on OurVote, matching rippled's DisputedTx constructor.
	dispute := dt.CreateDispute(txID, tx, true)
	if dispute == nil {
		t.Fatal("Dispute should be created")
	}
	if dispute.Yays != 0 || dispute.Nays != 0 {
		t.Errorf("Peer counts should start at 0/0; got %d/%d", dispute.Yays, dispute.Nays)
	}
	if !dispute.OurVote {
		t.Error("OurVote should track the seeded stance")
	}

	peerA := consensus.NodeID{0xA}
	peerB := consensus.NodeID{0xB}
	peerC := consensus.NodeID{0xC}
	peerD := consensus.NodeID{0xD}

	// Three peers vote yes, one no.
	if !dt.SetVote(txID, peerA, true) {
		t.Error("new peer vote should report changed")
	}
	if !dt.SetVote(txID, peerB, true) {
		t.Error("new peer vote should report changed")
	}
	if !dt.SetVote(txID, peerC, true) {
		t.Error("new peer vote should report changed")
	}
	if !dt.SetVote(txID, peerD, false) {
		t.Error("new peer vote should report changed")
	}

	dispute = dt.GetDispute(txID)
	if dispute.Yays != 3 || dispute.Nays != 1 {
		t.Errorf("Expected 3 yays, 1 nay; got %d/%d", dispute.Yays, dispute.Nays)
	}

	// Re-asserting the same vote is a no-op and reports unchanged.
	if dt.SetVote(txID, peerA, true) {
		t.Error("re-asserting same vote should report unchanged")
	}

	// Flipping an existing vote swaps one count and reports changed.
	if !dt.SetVote(txID, peerA, false) {
		t.Error("flipped vote should report changed")
	}
	dispute = dt.GetDispute(txID)
	if dispute.Yays != 2 || dispute.Nays != 2 {
		t.Errorf("After flip expected 2/2; got %d/%d", dispute.Yays, dispute.Nays)
	}
}

func TestDisputeTracker_UnVote(t *testing.T) {
	dt := NewDisputeTracker()

	tx1 := consensus.TxID{1}
	tx2 := consensus.TxID{2}
	dt.CreateDispute(tx1, []byte("tx1"), true)
	dt.CreateDispute(tx2, []byte("tx2"), false)

	peerX := consensus.NodeID{0xA}
	peerY := consensus.NodeID{0xB}

	dt.SetVote(tx1, peerX, true)
	dt.SetVote(tx1, peerY, true)
	dt.SetVote(tx2, peerX, false)

	// Before: tx1 has 2 yays, tx2 has 1 nay.
	if d := dt.GetDispute(tx1); d.Yays != 2 {
		t.Fatalf("tx1 pre-unvote yays = %d, want 2", d.Yays)
	}
	if d := dt.GetDispute(tx2); d.Nays != 1 {
		t.Fatalf("tx2 pre-unvote nays = %d, want 1", d.Nays)
	}

	// UnVote removes peerX from every dispute but not peerY.
	dt.UnVote(peerX)

	tx1Disp := dt.GetDispute(tx1)
	if tx1Disp.Yays != 1 {
		t.Errorf("tx1 post-unvote yays = %d, want 1", tx1Disp.Yays)
	}
	if _, has := tx1Disp.Votes[peerX]; has {
		t.Error("peerX should be gone from tx1 votes")
	}
	if _, has := tx1Disp.Votes[peerY]; !has {
		t.Error("peerY should remain in tx1 votes")
	}

	tx2Disp := dt.GetDispute(tx2)
	if tx2Disp.Nays != 0 {
		t.Errorf("tx2 post-unvote nays = %d, want 0", tx2Disp.Nays)
	}

	// UnVote for a peer that never voted is a no-op.
	dt.UnVote(consensus.NodeID{0xFE})
	if d := dt.GetDispute(tx1); d.Yays != 1 {
		t.Errorf("unknown-peer unvote mutated tx1; yays = %d", d.Yays)
	}
}

func TestDisputeTracker_UpdateDisputes(t *testing.T) {
	dt := NewDisputeTracker()

	tx1 := consensus.TxID{1}
	tx2 := consensus.TxID{2}
	dt.CreateDispute(tx1, []byte("tx1"), true)
	dt.CreateDispute(tx2, []byte("tx2"), false)

	peerID := consensus.NodeID{0xA}

	// Peer's tx set contains tx1 but not tx2. UpdateDisputes should
	// yield Yays=1 on tx1 and Nays=1 on tx2.
	peerTxSet := &mockTxSet{
		id:          consensus.TxSetID{1},
		containsTxs: map[consensus.TxID]bool{tx1: true},
	}

	if !dt.UpdateDisputes(peerID, peerTxSet) {
		t.Error("first UpdateDisputes should report changes")
	}
	if d := dt.GetDispute(tx1); d.Yays != 1 || d.Nays != 0 {
		t.Errorf("tx1 after UpdateDisputes = %d/%d, want 1/0", d.Yays, d.Nays)
	}
	if d := dt.GetDispute(tx2); d.Yays != 0 || d.Nays != 1 {
		t.Errorf("tx2 after UpdateDisputes = %d/%d, want 0/1", d.Yays, d.Nays)
	}

	// Calling again with the same set is a no-op.
	if dt.UpdateDisputes(peerID, peerTxSet) {
		t.Error("repeat UpdateDisputes should report no changes")
	}
}

func TestDisputeTracker_UpdateOurVote_AvalancheRamp(t *testing.T) {
	dt := NewDisputeTracker()
	parms := consensus.DefaultConsensusParms()

	txID := consensus.TxID{1}
	// Seed ourVote = false so disputes with any yays are candidates
	// for flipping.
	dt.CreateDispute(txID, []byte("tx"), false)

	// Give the dispute 3 yays out of 4 peers = 75% support.
	peers := []consensus.NodeID{{1}, {2}, {3}, {4}}
	dt.SetVote(txID, peers[0], true)
	dt.SetVote(txID, peers[1], true)
	dt.SetVote(txID, peers[2], true)
	dt.SetVote(txID, peers[3], false)

	// At init (50% threshold), 75% agreement flips our vote.
	changed := dt.UpdateOurVote(0, true, parms)
	if len(changed) != 1 || changed[0] != txID {
		t.Fatalf("expected dispute to flip at init state; got %v", changed)
	}
	if d := dt.GetDispute(txID); !d.OurVote {
		t.Error("OurVote should now be true")
	}

	// Reset to the opposite stance so the next calls can flip again.
	// Build a fresh dispute with the same peer split so we can
	// observe state progression without the "already agree" shortcut.
	txID2 := consensus.TxID{2}
	dt.CreateDispute(txID2, []byte("tx2"), true)
	for _, p := range peers {
		dt.SetVote(txID2, p, false)
	}

	// 4 no, 0 yes → under any threshold we should flip to false.
	changed = dt.UpdateOurVote(0, true, parms)
	if len(changed) != 1 || changed[0] != txID2 {
		t.Fatalf("expected unanimous opposition to flip our vote; got %v", changed)
	}
	d := dt.GetDispute(txID2)
	if d.OurVote {
		t.Error("OurVote should have flipped to false")
	}

	// Drive the avalanche state machine forward on a still-disputed
	// dispute. Create one with a 2/2 split: at the init 50% threshold,
	// weight=(2*100+100)/(2+2+1)=60 > 50 so we'd flip YES; to exercise
	// the ramp, start from a "yes, with nays>0" stance and check
	// state transitions via percentTime alone.
	rampID := consensus.TxID{3}
	dt.CreateDispute(rampID, []byte("tx3"), true)
	for _, p := range peers[:2] {
		dt.SetVote(rampID, p, true)
	}
	for _, p := range peers[2:] {
		dt.SetVote(rampID, p, false)
	}

	ramp := dt.GetDispute(rampID)
	if ramp.AvalancheState != consensus.AvalancheInit {
		t.Fatalf("ramp dispute should start at AvalancheInit; got %v", ramp.AvalancheState)
	}

	// avMIN_ROUNDS=2: first call stays in init (counter=1 < 2).
	dt.UpdateOurVote(60, true, parms)
	if ramp.AvalancheState != consensus.AvalancheInit {
		t.Errorf("after 1 round at 60%%, state = %v, want Init (min-rounds guard)", ramp.AvalancheState)
	}

	// Second call with percentTime>=50 and counter>=2: advance to Mid.
	dt.UpdateOurVote(60, true, parms)
	if ramp.AvalancheState != consensus.AvalancheMid {
		t.Errorf("after 2 rounds at 60%%, state = %v, want Mid", ramp.AvalancheState)
	}

	// Drive to Late (cutoff 85%).
	dt.UpdateOurVote(90, true, parms)
	dt.UpdateOurVote(90, true, parms)
	if ramp.AvalancheState != consensus.AvalancheLate {
		t.Errorf("after 90%% time, state = %v, want Late", ramp.AvalancheState)
	}

	// Drive to Stuck (cutoff 200%).
	dt.UpdateOurVote(210, true, parms)
	dt.UpdateOurVote(210, true, parms)
	if ramp.AvalancheState != consensus.AvalancheStuck {
		t.Errorf("after 210%% time, state = %v, want Stuck", ramp.AvalancheState)
	}
}
