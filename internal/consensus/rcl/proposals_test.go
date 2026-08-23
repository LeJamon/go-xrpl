package rcl

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestDisputeTracker_CreateAndVote(t *testing.T) {
	dt := newDisputeTracker()

	txID := consensus.TxID{1}
	tx := []byte("test tx")

	// Create dispute. Yays/Nays count peer votes only; our stance
	// lives on OurVote, matching rippled's DisputedTx constructor.
	dispute := dt.createDispute(txID, tx, true)
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
	if !dt.setVote(txID, peerA, true) {
		t.Error("new peer vote should report changed")
	}
	if !dt.setVote(txID, peerB, true) {
		t.Error("new peer vote should report changed")
	}
	if !dt.setVote(txID, peerC, true) {
		t.Error("new peer vote should report changed")
	}
	if !dt.setVote(txID, peerD, false) {
		t.Error("new peer vote should report changed")
	}

	dispute = dt.dispute(txID)
	if dispute.Yays != 3 || dispute.Nays != 1 {
		t.Errorf("Expected 3 yays, 1 nay; got %d/%d", dispute.Yays, dispute.Nays)
	}

	// Re-asserting the same vote is a no-op and reports unchanged.
	if dt.setVote(txID, peerA, true) {
		t.Error("re-asserting same vote should report unchanged")
	}

	// Flipping an existing vote swaps one count and reports changed.
	if !dt.setVote(txID, peerA, false) {
		t.Error("flipped vote should report changed")
	}
	dispute = dt.dispute(txID)
	if dispute.Yays != 2 || dispute.Nays != 2 {
		t.Errorf("After flip expected 2/2; got %d/%d", dispute.Yays, dispute.Nays)
	}
}

func TestDisputeTracker_UnVote(t *testing.T) {
	dt := newDisputeTracker()

	tx1 := consensus.TxID{1}
	tx2 := consensus.TxID{2}
	dt.createDispute(tx1, []byte("tx1"), true)
	dt.createDispute(tx2, []byte("tx2"), false)

	peerX := consensus.NodeID{0xA}
	peerY := consensus.NodeID{0xB}

	dt.setVote(tx1, peerX, true)
	dt.setVote(tx1, peerY, true)
	dt.setVote(tx2, peerX, false)

	// Before: tx1 has 2 yays, tx2 has 1 nay.
	if d := dt.dispute(tx1); d.Yays != 2 {
		t.Fatalf("tx1 pre-unvote yays = %d, want 2", d.Yays)
	}
	if d := dt.dispute(tx2); d.Nays != 1 {
		t.Fatalf("tx2 pre-unvote nays = %d, want 1", d.Nays)
	}

	// UnVote removes peerX from every dispute but not peerY.
	dt.unVote(peerX)

	tx1Disp := dt.dispute(tx1)
	if tx1Disp.Yays != 1 {
		t.Errorf("tx1 post-unvote yays = %d, want 1", tx1Disp.Yays)
	}
	if _, has := tx1Disp.Votes[peerX]; has {
		t.Error("peerX should be gone from tx1 votes")
	}
	if _, has := tx1Disp.Votes[peerY]; !has {
		t.Error("peerY should remain in tx1 votes")
	}

	tx2Disp := dt.dispute(tx2)
	if tx2Disp.Nays != 0 {
		t.Errorf("tx2 post-unvote nays = %d, want 0", tx2Disp.Nays)
	}

	// UnVote for a peer that never voted is a no-op.
	dt.unVote(consensus.NodeID{0xFE})
	if d := dt.dispute(tx1); d.Yays != 1 {
		t.Errorf("unknown-peer unvote mutated tx1; yays = %d", d.Yays)
	}
}

func TestDisputeTracker_UpdateDisputes(t *testing.T) {
	dt := newDisputeTracker()

	tx1 := consensus.TxID{1}
	tx2 := consensus.TxID{2}
	dt.createDispute(tx1, []byte("tx1"), true)
	dt.createDispute(tx2, []byte("tx2"), false)

	peerID := consensus.NodeID{0xA}

	// Peer's tx set contains tx1 but not tx2. UpdateDisputes should
	// yield Yays=1 on tx1 and Nays=1 on tx2.
	peerTxSet := &mockTxSet{
		id:          consensus.TxSetID{1},
		containsTxs: map[consensus.TxID]bool{tx1: true},
	}

	if !dt.updateDisputes(peerID, peerTxSet) {
		t.Error("first UpdateDisputes should report changes")
	}
	if d := dt.dispute(tx1); d.Yays != 1 || d.Nays != 0 {
		t.Errorf("tx1 after UpdateDisputes = %d/%d, want 1/0", d.Yays, d.Nays)
	}
	if d := dt.dispute(tx2); d.Yays != 0 || d.Nays != 1 {
		t.Errorf("tx2 after UpdateDisputes = %d/%d, want 0/1", d.Yays, d.Nays)
	}

	// Calling again with the same set is a no-op.
	if dt.updateDisputes(peerID, peerTxSet) {
		t.Error("repeat UpdateDisputes should report no changes")
	}
}

func TestDisputeTracker_UpdateOurVote_AvalancheRamp(t *testing.T) {
	dt := newDisputeTracker()
	parms := consensus.DefaultConsensusParms()

	txID := consensus.TxID{1}
	// Seed ourVote = false so disputes with any yays are candidates
	// for flipping.
	dt.createDispute(txID, []byte("tx"), false)

	// Give the dispute 3 yays out of 4 peers = 75% support.
	peers := []consensus.NodeID{{1}, {2}, {3}, {4}}
	dt.setVote(txID, peers[0], true)
	dt.setVote(txID, peers[1], true)
	dt.setVote(txID, peers[2], true)
	dt.setVote(txID, peers[3], false)

	// At init (50% threshold), 75% agreement flips our vote.
	changed := dt.updateOurVote(0, true, parms)
	if len(changed) != 1 || changed[0] != txID {
		t.Fatalf("expected dispute to flip at init state; got %v", changed)
	}
	if d := dt.dispute(txID); !d.OurVote {
		t.Error("OurVote should now be true")
	}

	// Reset to the opposite stance so the next calls can flip again.
	// Build a fresh dispute with the same peer split so we can
	// observe state progression without the "already agree" shortcut.
	txID2 := consensus.TxID{2}
	dt.createDispute(txID2, []byte("tx2"), true)
	for _, p := range peers {
		dt.setVote(txID2, p, false)
	}

	// 4 no, 0 yes → under any threshold we should flip to false.
	changed = dt.updateOurVote(0, true, parms)
	if len(changed) != 1 || changed[0] != txID2 {
		t.Fatalf("expected unanimous opposition to flip our vote; got %v", changed)
	}
	d := dt.dispute(txID2)
	if d.OurVote {
		t.Error("OurVote should have flipped to false")
	}

	// Drive the avalanche state machine forward on a still-disputed
	// dispute. Create one with a 2/2 split: at the init 50% threshold,
	// weight=(2*100+100)/(2+2+1)=60 > 50 so we'd flip YES; to exercise
	// the ramp, start from a "yes, with nays>0" stance and check
	// state transitions via percentTime alone.
	rampID := consensus.TxID{3}
	dt.createDispute(rampID, []byte("tx3"), true)
	for _, p := range peers[:2] {
		dt.setVote(rampID, p, true)
	}
	for _, p := range peers[2:] {
		dt.setVote(rampID, p, false)
	}

	ramp := dt.dispute(rampID)
	if ramp.AvalancheState != consensus.AvalancheInit {
		t.Fatalf("ramp dispute should start at AvalancheInit; got %v", ramp.AvalancheState)
	}

	// avMIN_ROUNDS=2: first call stays in init (counter=1 < 2).
	dt.updateOurVote(60, true, parms)
	if ramp.AvalancheState != consensus.AvalancheInit {
		t.Errorf("after 1 round at 60%%, state = %v, want Init (min-rounds guard)", ramp.AvalancheState)
	}

	// Second call with percentTime>=50 and counter>=2: advance to Mid.
	dt.updateOurVote(60, true, parms)
	if ramp.AvalancheState != consensus.AvalancheMid {
		t.Errorf("after 2 rounds at 60%%, state = %v, want Mid", ramp.AvalancheState)
	}

	// Drive to Late (cutoff 85%).
	dt.updateOurVote(90, true, parms)
	dt.updateOurVote(90, true, parms)
	if ramp.AvalancheState != consensus.AvalancheLate {
		t.Errorf("after 90%% time, state = %v, want Late", ramp.AvalancheState)
	}

	// Drive to Stuck (cutoff 200%).
	dt.updateOurVote(210, true, parms)
	dt.updateOurVote(210, true, parms)
	if ramp.AvalancheState != consensus.AvalancheStuck {
		t.Errorf("after 210%% time, state = %v, want Stuck", ramp.AvalancheState)
	}
}

func TestDisputeTracker_AllReturnsDetachedSnapshots(t *testing.T) {
	dt := newDisputeTracker()
	txID := consensus.TxID{1}
	peer := consensus.NodeID{2}
	dt.createDispute(txID, []byte{1, 2}, true)
	dt.setVote(txID, peer, true)

	snapshot := dt.all()[0]
	snapshot.OurVote = false
	snapshot.Tx[0] = 9
	snapshot.Votes[peer] = false
	snapshot.Votes[consensus.NodeID{3}] = true

	stored := dt.dispute(txID)
	if !stored.OurVote || stored.Tx[0] != 1 || !stored.Votes[peer] || len(stored.Votes) != 1 {
		t.Fatalf("mutating dispute snapshot changed tracker state: %#v", stored)
	}
}
