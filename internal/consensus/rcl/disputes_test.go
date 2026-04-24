package rcl

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// makeTxID returns a deterministic TxID seeded from a single byte so
// tests can refer to disputed txs as "tx A", "tx B", etc.
func makeTxID(seed byte) consensus.TxID {
	var id consensus.TxID
	id[0] = seed
	return id
}

// makeTxSetID returns a deterministic TxSetID seeded from a single byte.
func makeTxSetID(seed byte) consensus.TxSetID {
	var id consensus.TxSetID
	id[0] = seed
	return id
}

// buildMockTxSet constructs a mockTxSet that correctly answers TxIDs
// and Contains for an arbitrary set of tx hashes. Used by dispute-
// integration tests that rely on TxSet membership behaving like a
// real tx set (unlike the legacy Contains-always-false mock).
func buildMockTxSet(id consensus.TxSetID, txIDs ...consensus.TxID) *mockTxSet {
	contains := make(map[consensus.TxID]bool, len(txIDs))
	blobs := make([][]byte, 0, len(txIDs))
	ids := make([]consensus.TxID, 0, len(txIDs))
	for _, txID := range txIDs {
		contains[txID] = true
		blob := make([]byte, 32)
		copy(blob, txID[:])
		blobs = append(blobs, blob)
		ids = append(ids, txID)
	}
	return &mockTxSet{
		id:          id,
		txs:         blobs,
		txIDs:       ids,
		containsTxs: contains,
	}
}

// TestConsensus_OverlappingDisjointProposals_Converges drives the
// overlapping-proposal case from issue #266: peer positions diverge
// in the symmetric difference, and the engine must dispute both
// outliers and vote them into the final tx set once the avalanche
// threshold is cleared.
//
// To actually cross the 50% init threshold (rippled's minCONSENSUS_PCT
// at init — see ConsensusParms.h:146), the weight
// (yays*100 + ourVote*100) / (yays + nays + 1) must be strictly
// greater than 50. The natural rippled-world condition for that is
// "more than half of peers hold the disputed tx" — which happens
// when a supermajority of the network observes both outliers (e.g.
// peers proposing {A,B,C,D}).
//
// Acceptance criterion: issue #266 — "peers propose {A,B,C} and
// {A,B,D}; engine disputes C and D, votes both in after threshold
// ramps."
func TestConsensus_OverlappingDisjointProposals_Converges(t *testing.T) {
	adaptor := newMockAdaptor()

	selfNode := adaptor.nodeID
	// Two peers each hold ABC / ABD (the canonical issue statement);
	// plus three peers with ABCD to tip both disputes past 50%.
	peerABC1 := consensus.NodeID{0xA1}
	peerABC2 := consensus.NodeID{0xA2}
	peerABD1 := consensus.NodeID{0xA3}
	peerABD2 := consensus.NodeID{0xA4}
	peerABCD1 := consensus.NodeID{0xA5}
	peerABCD2 := consensus.NodeID{0xA6}
	peerABCD3 := consensus.NodeID{0xA7}
	adaptor.setTrusted([]consensus.NodeID{
		selfNode, peerABC1, peerABC2, peerABD1, peerABD2,
		peerABCD1, peerABCD2, peerABCD3,
	})

	txA := makeTxID(0xA)
	txB := makeTxID(0xB)
	txC := makeTxID(0xC)
	txD := makeTxID(0xD)

	setAB := buildMockTxSet(makeTxSetID(0x30), txA, txB)
	setABC := buildMockTxSet(makeTxSetID(0x31), txA, txB, txC)
	setABD := buildMockTxSet(makeTxSetID(0x32), txA, txB, txD)
	setABCD := buildMockTxSet(makeTxSetID(0x33), txA, txB, txC, txD)

	for _, ts := range []*mockTxSet{setAB, setABC, setABD, setABCD} {
		adaptor.txSets[ts.ID()] = ts
	}

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)
	engine.parms = consensus.DefaultConsensusParms()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	engine.mu.Lock()
	engine.ourTxSet = setAB
	engine.acquiredTxSets[setAB.ID()] = setAB
	engine.acquiredTxSets[setABC.ID()] = setABC
	engine.acquiredTxSets[setABD.ID()] = setABD
	engine.acquiredTxSets[setABCD.ID()] = setABCD
	engine.state.OurPosition = &consensus.Proposal{
		Round:     round,
		NodeID:    selfNode,
		Position:  0,
		TxSet:     setAB.ID(),
		CloseTime: adaptor.Now(),
	}
	engine.setPhase(consensus.PhaseEstablish)
	engine.mu.Unlock()

	now := adaptor.Now()
	proposals := []struct {
		node consensus.NodeID
		set  *mockTxSet
	}{
		{peerABC1, setABC},
		{peerABC2, setABC},
		{peerABD1, setABD},
		{peerABD2, setABD},
		{peerABCD1, setABCD},
		{peerABCD2, setABCD},
		{peerABCD3, setABCD},
	}
	for _, p := range proposals {
		prop := &consensus.Proposal{
			Round:          round,
			NodeID:         p.node,
			Position:       0,
			TxSet:          p.set.ID(),
			CloseTime:      now,
			PreviousLedger: consensus.LedgerID{1},
			Timestamp:      now,
		}
		if err := engine.OnProposal(prop, 0); err != nil {
			t.Fatalf("OnProposal(%x): %v", p.node, err)
		}
	}

	engine.mu.RLock()
	dC := engine.disputeTracker.GetDispute(txC)
	dD := engine.disputeTracker.GetDispute(txD)
	engine.mu.RUnlock()
	if dC == nil {
		t.Fatal("expected dispute for tx C after feeding peer proposals")
	}
	if dD == nil {
		t.Fatal("expected dispute for tx D after feeding peer proposals")
	}
	// C: ABC1, ABC2, ABCD1/2/3 YES (5); ABD1, ABD2 NO (2).
	// D: ABD1, ABD2, ABCD1/2/3 YES (5); ABC1, ABC2 NO (2).
	if dC.Yays != 5 || dC.Nays != 2 {
		t.Errorf("dispute C peer tally = %d/%d, want 5/2", dC.Yays, dC.Nays)
	}
	if dD.Yays != 5 || dD.Nays != 2 {
		t.Errorf("dispute D peer tally = %d/%d, want 5/2", dD.Yays, dD.Nays)
	}
	if dC.OurVote || dD.OurVote {
		t.Errorf("our initial vote on each dispute should be false (txs not in our set)")
	}

	// Drive updatePosition. Weight = (5*100 + 0)/(5+2+1) = 62.5 > 50
	// at AvalancheInit — both disputes flip to yes in a single call.
	engine.mu.Lock()
	engine.updatePosition()
	engine.mu.Unlock()

	engine.mu.RLock()
	dC = engine.disputeTracker.GetDispute(txC)
	dD = engine.disputeTracker.GetDispute(txD)
	outSet := engine.ourTxSet
	engine.mu.RUnlock()

	if !dC.OurVote {
		t.Errorf("after avalanche ramp, ourVote on tx C should be true")
	}
	if !dD.OurVote {
		t.Errorf("after avalanche ramp, ourVote on tx D should be true")
	}
	if outSet == nil {
		t.Fatal("ourTxSet should not be nil after updatePosition")
	}
	if !outSet.Contains(txA) || !outSet.Contains(txB) ||
		!outSet.Contains(txC) || !outSet.Contains(txD) {
		t.Errorf("final tx set should include A,B,C,D; got A=%v B=%v C=%v D=%v",
			outSet.Contains(txA), outSet.Contains(txB),
			outSet.Contains(txC), outSet.Contains(txD))
	}
}

// TestConsensus_BowOut_UnVotesDisputes seeds a dispute that peer X
// has voted YES on, then drives a bow-out proposal from X. The
// dispute's Yay count must drop by one and X must be gone from the
// per-peer vote map.
//
// Acceptance criterion: issue #266 — "node X votes C=yes, bows out;
// C's Yay count decreases by one."
func TestConsensus_BowOut_UnVotesDisputes(t *testing.T) {
	adaptor := newMockAdaptor()
	selfNode := adaptor.nodeID
	bowingNode := consensus.NodeID{0x77}
	anotherNode := consensus.NodeID{0x88}
	adaptor.setTrusted([]consensus.NodeID{selfNode, bowingNode, anotherNode})

	txC := makeTxID(0xC)
	txD := makeTxID(0xD)

	setWithC := buildMockTxSet(makeTxSetID(0x41), txC)
	setWithD := buildMockTxSet(makeTxSetID(0x42), txD)
	setEmpty := buildMockTxSet(makeTxSetID(0x43))

	for _, ts := range []*mockTxSet{setWithC, setWithD, setEmpty} {
		adaptor.txSets[ts.ID()] = ts
	}

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	engine.mu.Lock()
	engine.ourTxSet = setEmpty
	engine.acquiredTxSets[setEmpty.ID()] = setEmpty
	engine.acquiredTxSets[setWithC.ID()] = setWithC
	engine.acquiredTxSets[setWithD.ID()] = setWithD
	engine.state.OurPosition = &consensus.Proposal{
		Round: round, NodeID: selfNode, TxSet: setEmpty.ID(),
	}
	engine.setPhase(consensus.PhaseEstablish)
	engine.mu.Unlock()

	now := adaptor.Now()
	if err := engine.OnProposal(&consensus.Proposal{
		Round: round, NodeID: bowingNode, Position: 0,
		TxSet: setWithC.ID(), CloseTime: now,
		PreviousLedger: consensus.LedgerID{1}, Timestamp: now,
	}, 0); err != nil {
		t.Fatalf("OnProposal bowingNode: %v", err)
	}
	if err := engine.OnProposal(&consensus.Proposal{
		Round: round, NodeID: anotherNode, Position: 0,
		TxSet: setWithD.ID(), CloseTime: now,
		PreviousLedger: consensus.LedgerID{1}, Timestamp: now,
	}, 0); err != nil {
		t.Fatalf("OnProposal anotherNode: %v", err)
	}

	engine.mu.RLock()
	preC := engine.disputeTracker.GetDispute(txC)
	preD := engine.disputeTracker.GetDispute(txD)
	engine.mu.RUnlock()
	if preC == nil {
		t.Fatal("expected dispute for tx C after bowingNode's proposal")
	}
	// bowingNode has C → YES on C, NO on D (if dispute D exists yet).
	// anotherNode has D → NO on C, YES on D.
	if preC.Yays != 1 || preC.Nays != 1 {
		t.Fatalf("pre-bow-out tx C tally = %d/%d, want 1/1", preC.Yays, preC.Nays)
	}
	if _, has := preC.Votes[bowingNode]; !has {
		t.Fatal("pre-bow-out: bowingNode should have a vote on tx C")
	}
	if preC.Votes[bowingNode] != true {
		t.Fatal("pre-bow-out: bowingNode should vote YES on tx C")
	}
	if preD != nil && (preD.Yays != 1 || preD.Nays != 1) {
		t.Fatalf("pre-bow-out tx D tally = %d/%d, want 1/1", preD.Yays, preD.Nays)
	}

	bowOut := &consensus.Proposal{
		Round: round, NodeID: bowingNode, Position: 0xFFFFFFFF,
		TxSet: setWithC.ID(), CloseTime: now,
		PreviousLedger: consensus.LedgerID{1}, Timestamp: now,
	}
	if err := engine.OnProposal(bowOut, 0); err != nil {
		t.Fatalf("bow-out OnProposal: %v", err)
	}

	engine.mu.RLock()
	postC := engine.disputeTracker.GetDispute(txC)
	postD := engine.disputeTracker.GetDispute(txD)
	_, stillInProposals := engine.proposals[bowingNode]
	_, isDead := engine.deadNodes[bowingNode]
	engine.mu.RUnlock()

	if stillInProposals {
		t.Error("bowingNode should be evicted from proposals")
	}
	if !isDead {
		t.Error("bowingNode should be recorded in deadNodes")
	}
	if postC.Yays != 0 {
		t.Errorf("tx C yays after bow-out = %d, want 0", postC.Yays)
	}
	if postC.Nays != 1 {
		t.Errorf("tx C nays after bow-out = %d, want 1", postC.Nays)
	}
	if _, has := postC.Votes[bowingNode]; has {
		t.Error("bowingNode should be gone from tx C votes map after unvote")
	}
	if postD != nil {
		if postD.Yays != 1 {
			t.Errorf("tx D yays after bow-out = %d, want 1 (anotherNode stays)", postD.Yays)
		}
		if postD.Nays != 0 {
			t.Errorf("tx D nays after bow-out = %d, want 0", postD.Nays)
		}
		if _, has := postD.Votes[bowingNode]; has {
			t.Error("bowingNode should be gone from tx D votes map after unvote")
		}
	}
}

// TestConsensus_AvalancheThresholdRamp drives a single dispute
// through the four avalanche states (50 → 65 → 70 → 95) and asserts
// the required pct ConsensusParms.NeededWeight reports at each
// state. Uses a 3-yay/1-nay peer split with ourVote=true — weight
// is 80%, which holds steady through Init/Mid/Late but loses at
// Stuck (95%) so we can also confirm the flip-at-Stuck path.
//
// Acceptance criterion: issue #266 — "threshold rises 50 → 65 → 70 → 95
// as avalanche state advances."
func TestConsensus_AvalancheThresholdRamp(t *testing.T) {
	dt := NewDisputeTracker()
	parms := consensus.DefaultConsensusParms()

	txID := makeTxID(0xC)
	dt.CreateDispute(txID, []byte("tx"), true)
	// 3 peers vote YES, 1 NO. ourVote = true. Weight = (3*100+100)/(3+1+1) = 80.
	// This stays above Init/Mid/Late thresholds but below Stuck's 95%.
	for i, yes := range []bool{true, true, true, false} {
		var p consensus.NodeID
		p[0] = 0x10 + byte(i)
		dt.SetVote(txID, p, yes)
	}

	d := dt.GetDispute(txID)
	if d.AvalancheState != consensus.AvalancheInit {
		t.Fatalf("start AvalancheState = %v, want Init", d.AvalancheState)
	}
	if reqPct, _ := parms.NeededWeight(d.AvalancheState, 0, 0, parms.MinRounds); reqPct != 50 {
		t.Errorf("Init required pct = %d, want 50", reqPct)
	}

	// Call 1 at percentTime=0: MinRounds guard blocks advance.
	dt.UpdateOurVote(0, true, parms)
	if d.AvalancheState != consensus.AvalancheInit {
		t.Errorf("after 1 tick: state = %v, want Init (min-rounds guard)", d.AvalancheState)
	}
	if d.OurVote != true {
		t.Errorf("weight 80 > Init threshold 50: ourVote should stay true, got %v", d.OurVote)
	}

	// Call 2 at percentTime=60 with counter>=MinRounds: advance to Mid.
	dt.UpdateOurVote(60, true, parms)
	if d.AvalancheState != consensus.AvalancheMid {
		t.Errorf("after percentTime=60, 2nd tick: state = %v, want Mid", d.AvalancheState)
	}
	if reqPct, _ := parms.NeededWeight(d.AvalancheState, 60, d.AvalancheCounter, parms.MinRounds); reqPct != 65 {
		t.Errorf("Mid required pct = %d, want 65", reqPct)
	}

	// Stay in Mid for MinRounds before advancing. Current counter
	// resets to 0 on state change; drive two more ticks.
	dt.UpdateOurVote(60, true, parms) // counter=1, guard blocks
	dt.UpdateOurVote(90, true, parms) // counter=2, percentTime crosses 85 → advance to Late
	if d.AvalancheState != consensus.AvalancheLate {
		t.Errorf("after percentTime=90: state = %v, want Late", d.AvalancheState)
	}
	if reqPct, _ := parms.NeededWeight(d.AvalancheState, 90, d.AvalancheCounter, parms.MinRounds); reqPct != 70 {
		t.Errorf("Late required pct = %d, want 70", reqPct)
	}

	// Advance to Stuck.
	dt.UpdateOurVote(210, true, parms) // counter=1, guard blocks
	dt.UpdateOurVote(210, true, parms) // counter=2, crosses 200 → advance to Stuck
	if d.AvalancheState != consensus.AvalancheStuck {
		t.Errorf("after percentTime=210: state = %v, want Stuck", d.AvalancheState)
	}
	if reqPct, _ := parms.NeededWeight(d.AvalancheState, 210, d.AvalancheCounter, parms.MinRounds); reqPct != 95 {
		t.Errorf("Stuck required pct = %d, want 95", reqPct)
	}
	// At Stuck threshold 95, weight 80 no longer holds → our vote
	// should flip to false. Depending on exact round count the flip
	// happens on the entry-into-Stuck tick or the one after. Assert
	// it flipped within one more tick.
	if d.OurVote {
		dt.UpdateOurVote(210, true, parms)
	}
	if d.OurVote {
		t.Errorf("at Stuck (95%% threshold), weight 80%% should flip our vote to false; got OurVote=%v", d.OurVote)
	}
}
