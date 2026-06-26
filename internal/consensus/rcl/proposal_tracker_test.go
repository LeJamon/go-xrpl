package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// Task 2.4 (B4): tests for isBowOut (seqLeave == 0xFFFFFFFF) detection.
// Rippled reference: ConsensusProposal.h:68,154-156 and Consensus.h:804-817.
// A validator bowing out sets ProposeSeq to seqLeave so peers know to stop
// counting them for the remainder of the round. We must evict their current
// position and refuse further proposals until the next round clears the set.

// TestOnProposal_BowOutEvictsNode feeds a valid proposal from node X, then
// a seqLeave proposal from X, and asserts that the stored position for X is
// cleared. Mirrors rippled's Consensus.h:812-814 where peerPositions gets
// erase(peerID) on bow-out and the nodeID is inserted into deadNodes_.
func TestOnProposal_BowOutEvictsNode(t *testing.T) {
	adaptor := newMockAdaptor()
	bowingNode := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{bowingNode, {3}})

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	// Initial proposal — should be stored.
	first := &consensus.Proposal{
		Round:          round,
		NodeID:         bowingNode,
		Position:       0,
		TxSet:          consensus.TxSetID{1},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(first, 0); err != nil {
		t.Fatalf("first OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, stored := engine.proposalTracker.proposals[bowingNode]
	engine.mu.RUnlock()
	if !stored {
		t.Fatalf("precondition: first proposal from bowingNode should have been stored")
	}

	// Bow-out proposal — Position == seqLeave (0xFFFFFFFF).
	bowOut := &consensus.Proposal{
		Round:          round,
		NodeID:         bowingNode,
		Position:       0xFFFFFFFF,
		TxSet:          consensus.TxSetID{2},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(bowOut, 0); err != nil {
		t.Fatalf("bow-out OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, stillStored := engine.proposalTracker.proposals[bowingNode]
	_, dead := engine.proposalTracker.deadNodes[bowingNode]
	engine.mu.RUnlock()

	if stillStored {
		t.Errorf("expected bowed-out node %v to be evicted from proposals map", bowingNode)
	}
	if !dead {
		t.Errorf("expected bowed-out node %v to be recorded in deadNodes set", bowingNode)
	}
}

// TestOnProposal_DeadNodeLaterProposalIgnored verifies that once a node
// bows out, any subsequent proposal it sends in the same round is ignored.
// Matches rippled's Consensus.h:785-789 guard.
func TestOnProposal_DeadNodeLaterProposalIgnored(t *testing.T) {
	adaptor := newMockAdaptor()
	bowingNode := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{bowingNode, {3}})

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	bowOut := &consensus.Proposal{
		Round:          round,
		NodeID:         bowingNode,
		Position:       0xFFFFFFFF,
		TxSet:          consensus.TxSetID{2},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(bowOut, 0); err != nil {
		t.Fatalf("bow-out OnProposal: %v", err)
	}

	// A "normal" proposal after bow-out must be silently dropped.
	followUp := &consensus.Proposal{
		Round:          round,
		NodeID:         bowingNode,
		Position:       1,
		TxSet:          consensus.TxSetID{3},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(followUp, 0); err != nil {
		t.Fatalf("follow-up OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, stored := engine.proposalTracker.proposals[bowingNode]
	engine.mu.RUnlock()
	if stored {
		t.Errorf("expected follow-up proposal from dead node %v to be ignored, but it was stored", bowingNode)
	}
}

// TestStartRound_ClearsDeadNodes verifies that a new round clears the
// deadNodes set so a validator can rejoin consensus in the next round.
// Matches rippled's Consensus.h:722 (startRoundInternal clears deadNodes_).
func TestStartRound_ClearsDeadNodes(t *testing.T) {
	adaptor := newMockAdaptor()
	bowingNode := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{bowingNode, {3}})

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round1 := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round1, true); err != nil {
		t.Fatalf("StartRound round1: %v", err)
	}

	// Bow out in round 1.
	bowOut := &consensus.Proposal{
		Round:          round1,
		NodeID:         bowingNode,
		Position:       0xFFFFFFFF,
		TxSet:          consensus.TxSetID{2},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(bowOut, 0); err != nil {
		t.Fatalf("bow-out OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, deadAfterBow := engine.proposalTracker.deadNodes[bowingNode]
	engine.mu.RUnlock()
	if !deadAfterBow {
		t.Fatalf("precondition: bowingNode should be marked dead after bow-out")
	}

	// Start the next round — deadNodes must reset.
	round2 := consensus.RoundID{Seq: 102, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round2, true); err != nil {
		t.Fatalf("StartRound round2: %v", err)
	}

	engine.mu.RLock()
	_, stillDead := engine.proposalTracker.deadNodes[bowingNode]
	engine.mu.RUnlock()
	if stillDead {
		t.Fatalf("expected deadNodes to be cleared after StartRound, but %v is still marked dead", bowingNode)
	}

	// And a fresh proposal from the previously-bowed node must be accepted
	// again in the new round.
	rejoin := &consensus.Proposal{
		Round:          round2,
		NodeID:         bowingNode,
		Position:       0,
		TxSet:          consensus.TxSetID{5},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(rejoin, 0); err != nil {
		t.Fatalf("rejoin OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, stored := engine.proposalTracker.proposals[bowingNode]
	engine.mu.RUnlock()
	if !stored {
		t.Errorf("expected rejoined proposal from %v to be accepted in the new round", bowingNode)
	}
}

// alwaysTrusted is a trust predicate that accepts every node, used by the
// ProposalTracker unit tests below.
func alwaysTrusted(consensus.NodeID) bool { return true }

// TestProposalTracker_StoreMonotonic verifies Store keeps the position with
// the highest ProposeSeq and ignores a stale (lower-seq) one.
func TestProposalTracker_StoreMonotonic(t *testing.T) {
	pt := NewProposalTracker()
	node := consensus.NodeID{1}

	pt.Store(&consensus.Proposal{NodeID: node, Position: 5, TxSet: consensus.TxSetID{5}})
	pt.Store(&consensus.Proposal{NodeID: node, Position: 3, TxSet: consensus.TxSetID{3}})
	if got := pt.proposals[node].Position; got != 5 {
		t.Fatalf("stale proposal overwrote newer: Position = %d, want 5", got)
	}
	pt.Store(&consensus.Proposal{NodeID: node, Position: 7, TxSet: consensus.TxSetID{7}})
	if got := pt.proposals[node].Position; got != 7 {
		t.Fatalf("newer proposal not stored: Position = %d, want 7", got)
	}
	if pt.Count() != 1 {
		t.Fatalf("Count = %d, want 1", pt.Count())
	}
}

// TestProposalTracker_MarkDeadAndReset verifies MarkDead removes the node's
// position and records it dead, and ResetRound clears both maps.
func TestProposalTracker_MarkDeadAndReset(t *testing.T) {
	pt := NewProposalTracker()
	node := consensus.NodeID{1}
	pt.Store(&consensus.Proposal{NodeID: node, Position: 0})

	pt.MarkDead(node)
	if _, ok := pt.proposals[node]; ok {
		t.Error("MarkDead did not evict the node's position")
	}
	if !pt.IsDead(node) {
		t.Error("MarkDead did not record the node as dead")
	}

	pt.ResetRound()
	if pt.IsDead(node) {
		t.Error("ResetRound did not clear the dead-node set")
	}
	if pt.Count() != 0 {
		t.Errorf("ResetRound did not clear positions: Count = %d", pt.Count())
	}
}

// TestProposalTracker_BufferRecentCap verifies the per-node playback buffer
// caps at recentProposalsPerNode and drops the oldest entry.
func TestProposalTracker_BufferRecentCap(t *testing.T) {
	pt := NewProposalTracker()
	node := consensus.NodeID{1}
	for i := 0; i < recentProposalsPerNode+3; i++ {
		pt.BufferRecent(&consensus.Proposal{NodeID: node, Position: uint32(i)})
	}
	buf := pt.recentProposals[node]
	if len(buf) != recentProposalsPerNode {
		t.Fatalf("buffer size = %d, want %d", len(buf), recentProposalsPerNode)
	}
	// Oldest three (Position 0,1,2) dropped; newest entry retained.
	if buf[0].Position != 3 {
		t.Errorf("oldest entry not dropped: front Position = %d, want 3", buf[0].Position)
	}
	if buf[len(buf)-1].Position != uint32(recentProposalsPerNode+2) {
		t.Errorf("newest entry missing: back Position = %d, want %d",
			buf[len(buf)-1].Position, recentProposalsPerNode+2)
	}
}

// TestProposalTracker_Replay verifies Replay upserts buffered positions for
// the target ledger and returns close-time votes and the trusted count.
func TestProposalTracker_Replay(t *testing.T) {
	pt := NewProposalTracker()
	target := consensus.LedgerID{9}
	other := consensus.LedgerID{8}
	nodeA := consensus.NodeID{1}
	nodeB := consensus.NodeID{2}
	ct := time.Unix(1000, 0)

	pt.BufferRecent(&consensus.Proposal{NodeID: nodeA, Position: 0, PreviousLedger: target, CloseTime: ct})
	pt.BufferRecent(&consensus.Proposal{NodeID: nodeA, Position: 1, PreviousLedger: target, CloseTime: ct})
	pt.BufferRecent(&consensus.Proposal{NodeID: nodeB, Position: 0, PreviousLedger: other, CloseTime: ct})

	closeTimes, trustedReplayed := pt.Replay(target, alwaysTrusted)

	// nodeA contributes two proposals (Position 0 and 1) for the target.
	if trustedReplayed != 2 {
		t.Errorf("trustedReplayed = %d, want 2", trustedReplayed)
	}
	// Only the Position==0 proposal yields a close-time vote.
	if len(closeTimes) != 1 || !closeTimes[0].Equal(ct) {
		t.Errorf("closeTimes = %v, want one entry equal to %v", closeTimes, ct)
	}
	// nodeA upserted to its highest position; nodeB (other ledger) absent.
	if pt.proposals[nodeA].Position != 1 {
		t.Errorf("nodeA position = %d, want 1", pt.proposals[nodeA].Position)
	}
	if _, ok := pt.proposals[nodeB]; ok {
		t.Error("nodeB (different prev ledger) should not have been replayed")
	}
}

// TestProposalTracker_PruneStale verifies PruneStale removes positions older
// than the cutoff (with a non-zero timestamp) and keeps fresh / zero-ts ones.
func TestProposalTracker_PruneStale(t *testing.T) {
	pt := NewProposalTracker()
	now := time.Unix(2000, 0)
	cutoff := now.Add(-10 * time.Second)

	stale := consensus.NodeID{1}
	fresh := consensus.NodeID{2}
	zero := consensus.NodeID{3}
	pt.Store(&consensus.Proposal{NodeID: stale, Timestamp: now.Add(-30 * time.Second)})
	pt.Store(&consensus.Proposal{NodeID: fresh, Timestamp: now})
	pt.Store(&consensus.Proposal{NodeID: zero}) // zero timestamp — never pruned

	removed := pt.PruneStale(cutoff)
	if len(removed) != 1 || removed[0] != stale {
		t.Fatalf("removed = %v, want [%v]", removed, stale)
	}
	if _, ok := pt.proposals[stale]; ok {
		t.Error("stale position not pruned")
	}
	if _, ok := pt.proposals[fresh]; !ok {
		t.Error("fresh position wrongly pruned")
	}
	if _, ok := pt.proposals[zero]; !ok {
		t.Error("zero-timestamp position wrongly pruned")
	}
}

// TestProposalTracker_LatestFresh verifies LatestFresh returns the newest
// in-window position per trusted node and skips stale-only / untrusted ones.
func TestProposalTracker_LatestFresh(t *testing.T) {
	pt := NewProposalTracker()
	now := time.Unix(3000, 0)
	freshness := 10 * time.Second

	nodeA := consensus.NodeID{1}
	nodeB := consensus.NodeID{2} // untrusted
	nodeC := consensus.NodeID{3} // only stale entries

	// nodeA: an old then a recent entry — recent one wins.
	pt.BufferRecent(&consensus.Proposal{NodeID: nodeA, Position: 0, Timestamp: now.Add(-30 * time.Second)})
	pt.BufferRecent(&consensus.Proposal{NodeID: nodeA, Position: 1, Timestamp: now.Add(-1 * time.Second)})
	pt.BufferRecent(&consensus.Proposal{NodeID: nodeB, Position: 0, Timestamp: now})
	pt.BufferRecent(&consensus.Proposal{NodeID: nodeC, Position: 0, Timestamp: now.Add(-60 * time.Second)})

	trusted := func(n consensus.NodeID) bool { return n == nodeA || n == nodeC }
	out := pt.LatestFresh(trusted, now, freshness)

	if len(out) != 1 {
		t.Fatalf("LatestFresh returned %d entries, want 1", len(out))
	}
	got, ok := out[nodeA]
	if !ok {
		t.Fatal("expected nodeA in LatestFresh result")
	}
	if got.Position != 1 {
		t.Errorf("nodeA newest fresh Position = %d, want 1", got.Position)
	}
}

// TestProposalTracker_ValidationsForAndReset verifies round-validation
// collection by ledger and reset on accept.
func TestProposalTracker_ValidationsForAndReset(t *testing.T) {
	pt := NewProposalTracker()
	ledger := consensus.LedgerID{7}
	other := consensus.LedgerID{8}

	pt.SetValidation(&consensus.Validation{NodeID: consensus.NodeID{1}, LedgerID: ledger})
	pt.SetValidation(&consensus.Validation{NodeID: consensus.NodeID{2}, LedgerID: ledger})
	pt.SetValidation(&consensus.Validation{NodeID: consensus.NodeID{3}, LedgerID: other})

	if got := pt.ValidationsFor(ledger); len(got) != 2 {
		t.Errorf("ValidationsFor(%v) = %d entries, want 2", ledger, len(got))
	}
	pt.ResetValidations()
	if got := pt.ValidationsFor(ledger); len(got) != 0 {
		t.Errorf("ResetValidations did not clear: %d entries remain", len(got))
	}
}
