package rcl

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

type replayCloseTimeAdaptor struct {
	*mockAdaptor
	adjusted consensus.CloseTimes
}

type synchronizedReplayTrustAdaptor struct {
	*mockAdaptor
	node        consensus.NodeID
	finalCheck  chan struct{}
	release     chan struct{}
	trustChecks int
}

func (a *synchronizedReplayTrustAdaptor) IsTrusted(nodeID consensus.NodeID) bool {
	trusted := a.mockAdaptor.IsTrusted(nodeID)
	if nodeID != a.node {
		return trusted
	}
	a.trustChecks++
	if a.trustChecks == 4 {
		close(a.finalCheck)
		<-a.release
		// The callback may revoke trust while Replay is blocked at its final
		// check. Returning true models that final check winning the race.
		return true
	}
	return trusted
}

func (a *replayCloseTimeAdaptor) AdjustCloseTime(closeTimes consensus.CloseTimes) {
	a.adjusted = consensus.CloseTimes{Self: closeTimes.Self, Peers: make(map[time.Time]int, len(closeTimes.Peers))}
	for closeTime, count := range closeTimes.Peers {
		a.adjusted.Peers[closeTime] = count
	}
}

func TestProposalTracker_ReplayFiltersUntrustedBeforeStore(t *testing.T) {
	pt := NewProposalTracker()
	prev := consensus.LedgerID{0xA1}
	trusted := consensus.NodeID{0x01}
	untrusted := consensus.NodeID{0x02}
	close := time.Unix(1_700_000_001, 0).UTC()

	pt.BufferRecent(&consensus.Proposal{NodeID: trusted, PreviousLedger: prev, CloseTime: close})
	pt.BufferRecent(&consensus.Proposal{NodeID: untrusted, PreviousLedger: prev, CloseTime: close})
	pt.Store(&consensus.Proposal{NodeID: untrusted, PreviousLedger: prev, Position: 4})
	trust := func(nodeID consensus.NodeID) bool { return nodeID == trusted }

	closeTimes, replayed, relay := pt.Replay(prev, trust)
	if replayed != 1 || len(relay) != 1 || len(closeTimes) != 1 {
		t.Fatalf("Replay = closeTimes %d, replayed %d, relay %d; want 1/1/1", len(closeTimes), replayed, len(relay))
	}
	if _, ok := pt.proposals[untrusted]; ok {
		t.Fatal("untrusted replay proposal was stored in current-round positions")
	}
}

func TestProposalTracker_ReplayRechecksTrustDuringStore(t *testing.T) {
	pt := NewProposalTracker()
	prev := consensus.LedgerID{0xA2}
	node := consensus.NodeID{0x03}
	pt.BufferRecent(&consensus.Proposal{NodeID: node, PreviousLedger: prev, Position: 0})

	checks := 0
	trust := func(got consensus.NodeID) bool {
		if got != node {
			return false
		}
		checks++
		// Replay checks the node once while pruning, once before selecting
		// its buffered proposal, once immediately before Store, and once
		// after Store. Revoke it only at the final check.
		return checks <= 2
	}

	_, replayed, relay := pt.Replay(prev, trust)
	if replayed != 0 || len(relay) != 0 {
		t.Fatalf("trust transition replayed=%d relay=%d; want 0/0", replayed, len(relay))
	}
	if _, ok := pt.proposals[node]; ok {
		t.Fatal("proposal became current-round state after trust was revoked")
	}
	if _, ok := pt.recentProposals[node]; ok {
		t.Fatal("proposal remained in recent history after trust was revoked")
	}
}

func TestProposalTracker_ReplayPurgesUntrustedBeforeRetrust(t *testing.T) {
	pt := NewProposalTracker()
	prev := consensus.LedgerID{0xA5}
	node := consensus.NodeID{0x04}
	proposal := &consensus.Proposal{NodeID: node, PreviousLedger: prev, Position: 0}
	pt.BufferRecent(proposal)

	trusted := false
	pt.Replay(prev, func(consensus.NodeID) bool { return trusted })
	trusted = true
	_, replayed, relay := pt.Replay(prev, func(consensus.NodeID) bool { return trusted })
	if replayed != 0 || len(relay) != 0 {
		t.Fatalf("re-trusted purged proposal replayed=%d relay=%d; want 0/0", replayed, len(relay))
	}
	if _, ok := pt.recentProposals[node]; ok {
		t.Fatal("untrusted recent proposal survived purge and could be resurrected")
	}
}

func TestProposalTracker_ReplaySeqLeaveUnvotesAndAllowsFreshRejoin(t *testing.T) {
	pt := NewProposalTracker()
	prev := consensus.LedgerID{0xA6}
	node := consensus.NodeID{0x05}
	txID := consensus.TxSetID{0x06}
	pt.Store(&consensus.Proposal{NodeID: node, PreviousLedger: prev, Position: 3, TxSet: txID})
	const seqLeave = uint32(0xFFFFFFFF)
	bowOut := &consensus.Proposal{NodeID: node, PreviousLedger: prev, Position: seqLeave, TxSet: txID}
	pt.BufferRecent(bowOut)

	closeTimes, replayed, relay := pt.Replay(prev, func(consensus.NodeID) bool { return true })
	if len(closeTimes) != 0 || replayed != 0 || len(relay) != 1 || relay[0] != bowOut {
		t.Fatalf("seqLeave replay = closeTimes %d replayed %d relay %d; want 0/0/1", len(closeTimes), replayed, len(relay))
	}
	if !pt.IsDead(node) {
		t.Fatal("replayed seqLeave did not mark node dead")
	}
	if _, ok := pt.proposals[node]; ok {
		t.Fatal("replayed seqLeave left a current position")
	}

	// Dead markers are round-scoped. The consumed bow-out history must not
	// replay again and prevent a fresh position in the next round.
	pt.ResetRound()
	fresh := &consensus.Proposal{NodeID: node, PreviousLedger: prev, Position: 0, TxSet: txID}
	pt.BufferRecent(fresh)
	_, replayed, relay = pt.Replay(prev, func(consensus.NodeID) bool { return true })
	if replayed != 1 || len(relay) != 1 || relay[0] != fresh {
		t.Fatalf("fresh rejoin replayed=%d relay=%d; want 1/1", replayed, len(relay))
	}
	if got := pt.proposals[node]; got != fresh {
		t.Fatal("fresh proposal did not rejoin after round reset")
	}
}

func TestEngine_ReplayedSeqLeaveUnvotesDisputes(t *testing.T) {
	a := newMockAdaptor()
	node := consensus.NodeID{0x07}
	a.setTrusted([]consensus.NodeID{node})
	e := NewEngine(a, DefaultConfig())
	prev := consensus.LedgerID{0xA7}
	txID := consensus.TxID{0x08}
	e.disputeTracker.CreateDispute(txID, nil, true)
	e.disputeTracker.SetVote(txID, node, true)
	e.proposalTracker.BufferRecent(&consensus.Proposal{
		NodeID: node, PreviousLedger: prev, Position: 0xFFFFFFFF,
	})
	e.proposalTracker.Replay(prev, a.IsTrusted)
	e.unvoteDeadProposalsLocked()
	if votes := e.disputeTracker.Dispute(txID).Votes; len(votes) != 0 {
		t.Fatalf("replayed seqLeave retained dispute votes: %v", votes)
	}
}

func TestEngine_OnProposalSeqLeaveRelaysAndUnvotes(t *testing.T) {
	a := newMockAdaptor()
	node := consensus.NodeID{0x0A}
	a.setTrusted([]consensus.NodeID{node})
	e := NewEngine(a, DefaultConfig())
	round := consensus.RoundID{Seq: 101, ParentHash: a.lastLCL.ID()}
	if err := e.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	txID := consensus.TxID{0x0B}
	e.disputeTracker.CreateDispute(txID, nil, true)
	e.disputeTracker.SetVote(txID, node, true)
	bowOut := &consensus.Proposal{
		Round: round, NodeID: node, Position: 0xFFFFFFFF,
		TxSet: consensus.TxSetID{0x0C}, PreviousLedger: round.ParentHash,
	}
	if err := e.OnProposal(bowOut, 7); err != nil {
		t.Fatalf("OnProposal(seqLeave): %v", err)
	}

	a.mu.RLock()
	relayed := append([]*consensus.Proposal(nil), a.proposalsRelayed...)
	a.mu.RUnlock()
	if len(relayed) != 1 || relayed[0] != bowOut {
		t.Fatalf("seqLeave relay = %v, want exactly bow-out proposal", relayed)
	}
	if !e.proposalTracker.IsDead(node) {
		t.Fatal("seqLeave did not mark validator dead")
	}
	if votes := e.disputeTracker.Dispute(txID).Votes; len(votes) != 0 {
		t.Fatalf("seqLeave retained dispute vote: %v", votes)
	}
}

func TestEngine_TrustCallbackPurgesBeforeRetrustedProposal(t *testing.T) {
	a := newMockAdaptor()
	node := consensus.NodeID{0x0D}
	a.setTrusted([]consensus.NodeID{node})
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: a.lastLCL.ID()}
	if err := e.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	old := &consensus.Proposal{
		Round: round, NodeID: node, Position: 0, TxSet: consensus.TxSetID{0x0E},
		PreviousLedger: round.ParentHash, CloseTime: a.Now(), Timestamp: a.Now(),
	}
	txID := consensus.TxID{0x0F}
	e.mu.Lock()
	e.proposalTracker.Store(old)
	e.proposalTracker.BufferRecent(old)
	e.disputeTracker.CreateDispute(txID, nil, true)
	e.disputeTracker.SetVote(txID, node, true)
	e.mu.Unlock()

	a.setTrusted(nil)
	a.notifyTrustChanged()
	a.setTrusted([]consensus.NodeID{node})
	a.notifyTrustChanged()

	fresh := &consensus.Proposal{
		Round: round, NodeID: node, Position: 1, TxSet: consensus.TxSetID{0x10},
		PreviousLedger: round.ParentHash, CloseTime: a.Now(), Timestamp: a.Now(),
	}
	if err := e.OnProposal(fresh, 0); err != nil {
		t.Fatalf("OnProposal(retrusted): %v", err)
	}
	e.mu.RLock()
	current := e.proposalTracker.All()[node]
	recent := e.proposalTracker.recentProposals[node]
	votes := e.disputeTracker.Dispute(txID).Votes
	e.mu.RUnlock()
	if current != fresh {
		t.Fatalf("current proposal = %p, want fresh %p", current, fresh)
	}
	if len(recent) != 1 || recent[0] != fresh {
		t.Fatalf("recent proposals after re-trust = %v, want only fresh proposal", recent)
	}
	if len(votes) != 0 {
		t.Fatalf("stale dispute vote survived trust purge: %v", votes)
	}
}

func TestEngine_OnTxSetPurgesQueuedTrustBeforeDisputes(t *testing.T) {
	a := newMockAdaptor()
	node := consensus.NodeID{0x14}
	a.setTrusted([]consensus.NodeID{node})
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()
	round := consensus.RoundID{Seq: 101, ParentHash: a.lastLCL.ID()}
	if err := e.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	txID := consensus.TxID{0x15}
	txBlob := make([]byte, len(txID))
	copy(txBlob, txID[:])
	peerSet, err := a.BuildTxSet([][]byte{txBlob})
	if err != nil {
		t.Fatalf("BuildTxSet: %v", err)
	}
	e.mu.Lock()
	e.ourTxSet = buildMockTxSet(consensus.TxSetID{0x16})
	old := &consensus.Proposal{NodeID: node, Position: 0, TxSet: peerSet.ID(), PreviousLedger: round.ParentHash}
	e.proposalTracker.Store(old)
	e.proposalTracker.BufferRecent(old)
	e.disputeTracker.CreateDispute(txID, nil, true)
	e.disputeTracker.SetVote(txID, node, true)
	e.mu.Unlock()

	a.setTrusted(nil)
	a.notifyTrustChanged()
	a.setTrusted([]consensus.NodeID{node})
	a.notifyTrustChanged()

	if err := e.OnTxSet(peerSet.ID(), [][]byte{txBlob}); err != nil {
		t.Fatalf("OnTxSet: %v", err)
	}
	e.mu.RLock()
	_, current := e.proposalTracker.All()[node]
	_, recent := e.proposalTracker.recentProposals[node]
	votes := e.disputeTracker.Dispute(txID).Votes
	e.mu.RUnlock()
	if current || recent {
		t.Fatal("queued trust purge did not remove current and recent proposal state")
	}
	if len(votes) != 0 {
		t.Fatalf("queued trust purge left dispute votes: %v", votes)
	}
}

func TestEngine_TimerEntryPurgesQueuedTrustBeforeDispatch(t *testing.T) {
	a := newMockAdaptor()
	node := consensus.NodeID{0x17}
	a.setTrusted([]consensus.NodeID{node})
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()
	round := consensus.RoundID{Seq: 101, ParentHash: a.lastLCL.ID()}
	if err := e.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	txID := consensus.TxID{0x18}
	old := &consensus.Proposal{NodeID: node, Position: 0, TxSet: consensus.TxSetID{0x19}, PreviousLedger: round.ParentHash}
	e.mu.Lock()
	e.proposalTracker.Store(old)
	e.proposalTracker.BufferRecent(old)
	e.disputeTracker.CreateDispute(txID, nil, true)
	e.disputeTracker.SetVote(txID, node, true)
	e.mu.Unlock()
	a.setTrusted(nil)
	a.notifyTrustChanged()
	a.setTrusted([]consensus.NodeID{node})
	a.notifyTrustChanged()
	// The disconnected early return makes this assertion isolate the central
	// timer-entry purge rather than a later phaseEstablish purge.
	a.mu.Lock()
	a.opMode = consensus.OpModeDisconnected
	a.mu.Unlock()
	e.TimerEntry()
	e.mu.RLock()
	_, current := e.proposalTracker.All()[node]
	_, recent := e.proposalTracker.recentProposals[node]
	votes := e.disputeTracker.Dispute(txID).Votes
	e.mu.RUnlock()
	if current || recent {
		t.Fatal("timer-entry purge did not remove current and recent proposal state")
	}
	if len(votes) != 0 {
		t.Fatalf("timer-entry purge left dispute votes: %v", votes)
	}
}

func TestEngine_CommitAcceptedLedgerPurgesQueuedTrust(t *testing.T) {
	a := newMockAdaptor()
	a.opMode = consensus.OpModeConnected
	node := consensus.NodeID{0x1A}
	a.setTrusted([]consensus.NodeID{node})
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()
	round := consensus.RoundID{Seq: 101, ParentHash: a.lastLCL.ID()}
	if err := e.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	txID := consensus.TxID{0x1B}
	old := &consensus.Proposal{NodeID: node, Position: 0, TxSet: consensus.TxSetID{0x1C}, PreviousLedger: round.ParentHash}
	e.mu.Lock()
	e.proposalTracker.Store(old)
	e.proposalTracker.BufferRecent(old)
	e.disputeTracker.CreateDispute(txID, nil, true)
	e.disputeTracker.SetVote(txID, node, true)
	e.mu.Unlock()
	a.setTrusted(nil)
	a.notifyTrustChanged()
	a.setTrusted([]consensus.NodeID{node})
	a.notifyTrustChanged()

	parent := e.prevLedger
	txSet := buildMockTxSet(consensus.TxSetID{0x1D})
	newLedger := &mockLedger{
		id:        consensus.LedgerID{0x1E},
		seq:       parent.Seq() + 1,
		parentID:  parent.ID(),
		closeTime: parent.CloseTime().Add(time.Second),
		txSetID:   txSet.ID(),
	}
	work := ledgerAcceptWork{
		result:           consensus.ResultSuccess,
		prevLedger:       parent,
		txSet:            txSet,
		closeTime:        newLedger.CloseTime(),
		closeTimeCorrect: true,
		resolution:       time.Second,
		roundTime:        time.Second,
		roundDuration:    time.Second,
	}
	e.mu.Lock()
	e.phase = consensus.PhaseAccepted
	e.buildInProgress = true
	e.commitAcceptedLedgerLocked(work, newLedger, nil)
	e.mu.Unlock()

	e.mu.RLock()
	_, current := e.proposalTracker.All()[node]
	_, recent := e.proposalTracker.recentProposals[node]
	votes := e.disputeTracker.Dispute(txID).Votes
	e.mu.RUnlock()
	if current || recent {
		t.Fatal("commit purge did not remove current and recent proposal state")
	}
	if len(votes) != 0 {
		t.Fatalf("commit purge left dispute votes: %v", votes)
	}
}

func TestReplayCloseTimesRecheckTrustAfterReplay(t *testing.T) {
	a := newMockAdaptor()
	node := consensus.NodeID{0x11}
	a.setTrusted([]consensus.NodeID{node})
	e := NewEngine(a, DefaultConfig())
	parent := a.lastLCL
	e.prevLedger = parent
	e.state = &roundState{CloseTimes: consensus.CloseTimes{Peers: make(map[time.Time]int)}}
	closeTime := parent.CloseTime().Add(4 * time.Second)
	e.proposalTracker.BufferRecent(&consensus.Proposal{
		NodeID: node, PreviousLedger: parent.ID(), Position: 0, CloseTime: closeTime,
	})
	votes, replayed, _ := e.proposalTracker.Replay(parent.ID(), a.IsTrusted)
	if replayed != 1 || len(votes) != 1 || votes[0].NodeID != node {
		t.Fatalf("Replay = votes %v, replayed %d; want one node-associated vote", votes, replayed)
	}

	// The validator can lose trust after Replay's final check. The caller must
	// filter the node-associated vote before it reaches CloseTimes.Peers.
	a.setTrusted(nil)
	e.mu.Lock()
	e.appendReplayCloseTimesLocked(votes)
	e.mu.Unlock()
	if len(e.state.CloseTimes.Peers) != 0 {
		t.Fatalf("close-time votes after trust transition = %v, want empty", e.state.CloseTimes.Peers)
	}
}

func TestReplayCloseTimesTrustCallbackLinearized(t *testing.T) {
	node := consensus.NodeID{0x1F}
	base := newMockAdaptor()
	base.setTrusted([]consensus.NodeID{node})
	a := &synchronizedReplayTrustAdaptor{
		mockAdaptor: base,
		node:        node,
		finalCheck:  make(chan struct{}),
		release:     make(chan struct{}),
	}
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(a, config)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()
	parent := base.lastLCL
	closeTime := parent.CloseTime().Add(4 * time.Second)
	e.state = &roundState{CloseTimes: consensus.CloseTimes{Peers: make(map[time.Time]int)}}
	e.proposalTracker.BufferRecent(&consensus.Proposal{
		NodeID: node, PreviousLedger: parent.ID(), Position: 0, CloseTime: closeTime,
	})

	type replayResult struct {
		votes    []ReplayCloseTime
		replayed int
	}
	resultCh := make(chan replayResult, 1)
	go func() {
		votes, replayed, _ := e.proposalTracker.Replay(parent.ID(), a.IsTrusted)
		resultCh <- replayResult{votes: votes, replayed: replayed}
	}()
	select {
	case <-a.finalCheck:
	case <-time.After(time.Second):
		t.Fatal("Replay did not reach its final trust check")
	}
	base.setTrusted(nil)
	a.notifyTrustChanged()
	close(a.release)
	result := <-resultCh
	if result.replayed != 1 || len(result.votes) != 1 {
		t.Fatalf("Replay = votes %v, replayed %d; want one vote after final-check race", result.votes, result.replayed)
	}

	e.mu.Lock()
	e.appendReplayCloseTimesLocked(result.votes)
	e.mu.Unlock()
	if len(e.state.CloseTimes.Peers) != 0 {
		t.Fatalf("close-time vote survived callback-linearized purge: %v", e.state.CloseTimes.Peers)
	}
	if _, ok := e.proposalTracker.All()[node]; ok {
		t.Fatal("callback-linearized purge left current proposal state")
	}
	if _, ok := e.proposalTracker.recentProposals[node]; ok {
		t.Fatal("callback-linearized purge left recent proposal state")
	}
}

func TestTrustedPredicateSnapshotSurvivesReplacement(t *testing.T) {
	base := newMockAdaptor()
	first := consensus.NodeID{0x30}
	second := consensus.NodeID{0x31}
	replacement := consensus.NodeID{0x32}
	base.setTrusted([]consensus.NodeID{first, second})
	config := DefaultConfig()
	config.ManualTick = true
	e := NewEngine(base, config)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()

	// Capture the view used by one multi-node decision, then replace the UNL
	// before evaluating any entries. The captured predicate must remain a
	// coherent pre-transition view; a fresh predicate must see the replacement.
	before := e.trustedPredicate()
	changed := make(chan struct{})
	go func() {
		base.setTrusted([]consensus.NodeID{second, replacement})
		base.notifyTrustChanged()
		close(changed)
	}()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("trust replacement callback did not complete")
	}
	if !before(first) || !before(second) || before(replacement) {
		t.Fatalf("captured trust view mixed replacement epoch: first=%t second=%t replacement=%t", before(first), before(second), before(replacement))
	}

	after := e.trustedPredicate()
	if after(first) || !after(second) || !after(replacement) {
		t.Fatalf("fresh trust view missed replacement: first=%t second=%t replacement=%t", after(first), after(second), after(replacement))
	}
}

func TestReplayPreservesInitialCloseTimeWithSeqLeave(t *testing.T) {
	base := time.Unix(1_700_003_000, 0).UTC()
	parent := &mockLedger{id: consensus.LedgerID{0x20}, seq: 100, closeTime: base}
	baseAdaptor := newMockAdaptor()
	baseAdaptor.validator = false
	baseAdaptor.opMode = consensus.OpModeConnected
	baseAdaptor.lastLCL = parent
	baseAdaptor.ledgers[parent.ID()] = parent
	node := consensus.NodeID{0x21}
	baseAdaptor.setTrusted([]consensus.NodeID{node})
	a := &replayCloseTimeAdaptor{mockAdaptor: baseAdaptor}
	e := NewEngine(a, DefaultConfig())
	e.prevLedger = parent
	initial := base.Add(4 * time.Second)
	e.proposalTracker.BufferRecent(&consensus.Proposal{
		NodeID: node, PreviousLedger: parent.ID(), Position: 0, CloseTime: initial,
	})
	e.proposalTracker.BufferRecent(&consensus.Proposal{
		NodeID: node, PreviousLedger: parent.ID(), Position: 0xFFFFFFFF,
	})
	if err := e.StartRound(consensus.RoundID{Seq: 101, ParentHash: parent.ID()}, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := e.state.CloseTimes.Peers[initial]; got != 1 {
		t.Fatalf("initial close-time count with seqLeave = %d, want 1", got)
	}
	if e.proposalTracker.Count() != 0 || !e.proposalTracker.IsDead(node) {
		t.Fatal("seqLeave should remove final current position while preserving raw initial history")
	}

	// This is the observer clock-adjustment input; no final current proposal is
	// required for a replayed initial vote to remain in raw history.
	a.AdjustCloseTime(e.state.CloseTimes)
	if got := a.adjusted.Peers[initial]; got != 1 {
		t.Fatalf("adjusted initial close-time count with seqLeave = %d, want 1", got)
	}
}

func TestObserverAcceptanceUsesCloseTimeGateWinner(t *testing.T) {
	a := newMockAdaptor()
	a.validator = false
	a.opMode = consensus.OpModeConnected
	parentClose := time.Unix(1_700_000_000, 0).UTC()
	parent := &mockLedger{id: consensus.LedgerID{0xA3}, seq: 100, closeTime: parentClose}
	a.lastLCL = parent
	a.ledgers[parent.ID()] = parent

	peerA := consensus.NodeID{0x11}
	peerB := consensus.NodeID{0x12}
	peerC := consensus.NodeID{0x13}
	a.setTrusted([]consensus.NodeID{peerA, peerB, peerC})
	e := NewEngine(a, DefaultConfig())
	round := consensus.RoundID{Seq: 101, ParentHash: parent.ID()}
	closeA := parentClose.Add(4 * time.Second)
	closeB := parentClose.Add(8 * time.Second)
	e.state = &roundState{
		Round:      round,
		CloseTimes: consensus.CloseTimes{Peers: map[time.Time]int{closeA: 100}},
		StartTime:  a.Now(),
	}
	e.prevLedger = parent
	e.phase = consensus.PhaseEstablish
	e.mode = consensus.ModeObserving
	e.ourTxSet = buildMockTxSet(consensus.TxSetID{0xA4})
	e.acquiredTxSets[e.ourTxSet.ID()] = e.ourTxSet

	e.proposalTracker.Store(&consensus.Proposal{
		NodeID: peerA, Position: 0, CloseTime: closeA, Timestamp: a.Now().Add(-time.Minute),
	})
	e.proposalTracker.Store(&consensus.Proposal{NodeID: peerB, Position: 0, CloseTime: closeB})
	e.proposalTracker.Store(&consensus.Proposal{NodeID: peerC, Position: 0, CloseTime: closeB})
	e.pruneStaleProposalsLocked()
	e.updateCloseTimePosition()
	if !e.closeTime.haveConsensus {
		t.Fatal("close-time gate did not reach consensus")
	}
	if !e.closeTime.consensusCloseTime.Equal(closeB) {
		t.Fatalf("gate winner = %v, want %v", e.closeTime.consensusCloseTime, closeB)
	}

	// Acceptance must use the retained winner, not the stale initial-vote
	// history. This is the observer path: there is no OurPosition to supply a
	// second copy of the winning close time.
	e.mu.Lock()
	e.acceptLedger(consensus.ResultSuccess)
	e.mu.Unlock()
	if got := a.lastLCL.CloseTime(); !got.Equal(closeB) {
		t.Fatalf("accepted close time = %v, want gate winner %v", got, closeB)
	}
}

func TestReplayPreservesInitialCloseTimesForClockAdjustment(t *testing.T) {
	base := time.Unix(1_700_001_000, 0).UTC()
	parent := &mockLedger{id: consensus.LedgerID{0xA8}, seq: 100, closeTime: base}
	baseAdaptor := newMockAdaptor()
	baseAdaptor.validator = false
	baseAdaptor.opMode = consensus.OpModeConnected
	baseAdaptor.lastLCL = parent
	baseAdaptor.ledgers[parent.ID()] = parent
	node := consensus.NodeID{0x09}
	baseAdaptor.setTrusted([]consensus.NodeID{node})
	a := &replayCloseTimeAdaptor{mockAdaptor: baseAdaptor}
	e := NewEngine(a, DefaultConfig())
	e.prevLedger = parent
	initial := base.Add(4 * time.Second)
	revision := base.Add(8 * time.Second)
	e.proposalTracker.BufferRecent(&consensus.Proposal{
		NodeID: node, PreviousLedger: parent.ID(), Position: 0, CloseTime: initial,
	})
	e.proposalTracker.BufferRecent(&consensus.Proposal{
		NodeID: node, PreviousLedger: parent.ID(), Position: 1, CloseTime: revision,
	})

	if err := e.StartRound(consensus.RoundID{Seq: 101, ParentHash: parent.ID()}, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	e.mu.Lock()
	// The replayed revision is the current position and wins acceptance;
	// CloseTimes.Peers remains the append-only initial-vote history.
	e.updateCloseTimePosition()
	if got := e.state.CloseTimes.Peers[initial]; got != 1 {
		t.Fatalf("initial replay close-time count = %d, want 1", got)
	}
	if got := e.state.CloseTimes.Peers[revision]; got != 0 {
		t.Fatalf("revised close-time count = %d, want 0", got)
	}
	e.mu.Unlock()

	// Acceptance invokes AdjustCloseTime with the preserved raw history while
	// BuildLedger uses the current-position winner.
	e.mu.Lock()
	e.acceptLedger(consensus.ResultSuccess)
	e.mu.Unlock()
	if got := a.adjusted.Peers[initial]; got != 1 {
		t.Fatalf("adjusted initial close-time count = %d, want 1", got)
	}
	if !a.lastLCL.CloseTime().Equal(revision) {
		t.Fatalf("accepted close time = %v, want revised winner %v", a.lastLCL.CloseTime(), revision)
	}
}

func TestAcceptLedgerManualConsensusFallsBackToDetermineCloseTime(t *testing.T) {
	a := newMockAdaptor()
	parentClose := time.Unix(1_700_002_000, 0).UTC()
	parent := &mockLedger{id: consensus.LedgerID{0xA9}, seq: 100, closeTime: parentClose}
	a.lastLCL = parent
	a.ledgers[parent.ID()] = parent
	e := NewEngine(a, DefaultConfig())
	e.prevLedger = parent
	e.phase = consensus.PhaseEstablish
	e.state = &roundState{
		Round:      consensus.RoundID{Seq: 101, ParentHash: parent.ID()},
		CloseTimes: consensus.CloseTimes{Peers: map[time.Time]int{}},
		StartTime:  a.Now(),
	}
	e.ourTxSet = buildMockTxSet(consensus.TxSetID{0xAA})
	e.acquiredTxSets[e.ourTxSet.ID()] = e.ourTxSet
	chosen := parentClose.Add(5 * time.Second)
	e.state.CloseTimes.Peers[chosen] = 1
	e.closeTime.haveConsensus = true
	// Simulate a manually-forced consensus flag without a stored gate winner.
	e.closeTime.consensusCloseTime = time.Time{}
	e.closeTime.consensusCloseTimeSet = false

	e.mu.Lock()
	e.acceptLedger(consensus.ResultSuccess)
	e.mu.Unlock()
	if got := a.lastLCL.CloseTime(); !got.Equal(chosen) {
		t.Fatalf("manual consensus fallback close time = %v, want %v", got, chosen)
	}
	if !e.closeTime.consensusCloseTime.IsZero() || e.closeTime.consensusCloseTimeSet {
		t.Fatal("close-time snapshot was not reset/left unset for manual fallback")
	}
}

func TestProposalTracker_PruneUntrustedRevokesCurrentPosition(t *testing.T) {
	pt := NewProposalTracker()
	trusted := consensus.NodeID{0x21}
	untrusted := consensus.NodeID{0x22}
	pt.Store(&consensus.Proposal{NodeID: trusted})
	pt.Store(&consensus.Proposal{NodeID: untrusted})
	removed := pt.PruneUntrusted(func(nodeID consensus.NodeID) bool { return nodeID == trusted })
	if len(removed) != 1 || removed[0] != untrusted {
		t.Fatalf("removed = %v, want [%v]", removed, untrusted)
	}
	if pt.Count() != 1 {
		t.Fatalf("current positions = %d, want 1", pt.Count())
	}
}
