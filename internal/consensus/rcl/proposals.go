package rcl

import (
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// ProposalTracker tracks proposals during a consensus round.
type ProposalTracker struct {
	mu sync.RWMutex

	// round is the current round being tracked
	round consensus.RoundID

	// proposals maps node ID to their current proposal
	proposals map[consensus.NodeID]*consensus.Proposal

	// byTxSet maps tx set ID to nodes proposing it
	byTxSet map[consensus.TxSetID]map[consensus.NodeID]bool

	// trusted is the set of trusted validators
	trusted map[consensus.NodeID]bool

	// freshness is how long proposals are considered fresh
	freshness time.Duration
}

// NewProposalTracker creates a new proposal tracker.
func NewProposalTracker(freshness time.Duration) *ProposalTracker {
	return &ProposalTracker{
		proposals: make(map[consensus.NodeID]*consensus.Proposal),
		byTxSet:   make(map[consensus.TxSetID]map[consensus.NodeID]bool),
		trusted:   make(map[consensus.NodeID]bool),
		freshness: freshness,
	}
}

// SetRound sets the current round being tracked.
func (pt *ProposalTracker) SetRound(round consensus.RoundID) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	pt.round = round
	pt.proposals = make(map[consensus.NodeID]*consensus.Proposal)
	pt.byTxSet = make(map[consensus.TxSetID]map[consensus.NodeID]bool)
}

// SetTrusted updates the set of trusted validators.
func (pt *ProposalTracker) SetTrusted(nodes []consensus.NodeID) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	pt.trusted = make(map[consensus.NodeID]bool)
	for _, node := range nodes {
		pt.trusted[node] = true
	}
}

// Add adds or updates a proposal.
// Returns true if this is a new or updated proposal.
func (pt *ProposalTracker) Add(proposal *consensus.Proposal) bool {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Check if for current round
	if proposal.Round != pt.round {
		return false
	}

	// Check if newer than existing
	existing, hasExisting := pt.proposals[proposal.NodeID]
	if hasExisting {
		if proposal.Position <= existing.Position {
			return false // Not newer
		}

		// Remove from old tx set tracking
		if nodes, exists := pt.byTxSet[existing.TxSet]; exists {
			delete(nodes, proposal.NodeID)
			if len(nodes) == 0 {
				delete(pt.byTxSet, existing.TxSet)
			}
		}
	}

	// Store proposal
	pt.proposals[proposal.NodeID] = proposal

	// Add to tx set tracking
	nodes, exists := pt.byTxSet[proposal.TxSet]
	if !exists {
		nodes = make(map[consensus.NodeID]bool)
		pt.byTxSet[proposal.TxSet] = nodes
	}
	nodes[proposal.NodeID] = true

	return true
}

// Get returns the proposal from a specific node.
func (pt *ProposalTracker) Get(nodeID consensus.NodeID) *consensus.Proposal {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return pt.proposals[nodeID]
}

// GetAll returns all current proposals.
func (pt *ProposalTracker) GetAll() []*consensus.Proposal {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make([]*consensus.Proposal, 0, len(pt.proposals))
	for _, p := range pt.proposals {
		result = append(result, p)
	}
	return result
}

// GetTrusted returns proposals from trusted validators.
func (pt *ProposalTracker) GetTrusted() []*consensus.Proposal {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	var result []*consensus.Proposal
	for nodeID, p := range pt.proposals {
		if pt.trusted[nodeID] {
			result = append(result, p)
		}
	}
	return result
}

// GetForTxSet returns nodes proposing a specific tx set.
func (pt *ProposalTracker) GetForTxSet(txSetID consensus.TxSetID) []consensus.NodeID {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	nodes, exists := pt.byTxSet[txSetID]
	if !exists {
		return nil
	}

	result := make([]consensus.NodeID, 0, len(nodes))
	for nodeID := range nodes {
		result = append(result, nodeID)
	}
	return result
}

// GetTrustedForTxSet returns trusted nodes proposing a specific tx set.
func (pt *ProposalTracker) GetTrustedForTxSet(txSetID consensus.TxSetID) []consensus.NodeID {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	nodes, exists := pt.byTxSet[txSetID]
	if !exists {
		return nil
	}

	var result []consensus.NodeID
	for nodeID := range nodes {
		if pt.trusted[nodeID] {
			result = append(result, nodeID)
		}
	}
	return result
}

// TxSetCounts returns the count of proposals for each tx set.
func (pt *ProposalTracker) TxSetCounts() map[consensus.TxSetID]int {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make(map[consensus.TxSetID]int)
	for txSetID, nodes := range pt.byTxSet {
		result[txSetID] = len(nodes)
	}
	return result
}

// TrustedTxSetCounts returns the count of trusted proposals for each tx set.
func (pt *ProposalTracker) TrustedTxSetCounts() map[consensus.TxSetID]int {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make(map[consensus.TxSetID]int)
	for txSetID, nodes := range pt.byTxSet {
		count := 0
		for nodeID := range nodes {
			if pt.trusted[nodeID] {
				count++
			}
		}
		if count > 0 {
			result[txSetID] = count
		}
	}
	return result
}

// GetWinningTxSet returns the tx set with the most trusted support.
func (pt *ProposalTracker) GetWinningTxSet() (consensus.TxSetID, int) {
	counts := pt.TrustedTxSetCounts()

	var bestID consensus.TxSetID
	bestCount := 0

	for txSetID, count := range counts {
		if count > bestCount {
			bestID = txSetID
			bestCount = count
		}
	}

	return bestID, bestCount
}

// Count returns the total number of proposals.
func (pt *ProposalTracker) Count() int {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return len(pt.proposals)
}

// TrustedCount returns the number of proposals from trusted validators.
func (pt *ProposalTracker) TrustedCount() int {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	count := 0
	for nodeID := range pt.proposals {
		if pt.trusted[nodeID] {
			count++
		}
	}
	return count
}

// HasConverged returns true if proposals have converged to a single tx set.
func (pt *ProposalTracker) HasConverged(threshold float64) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	trustedCount := 0
	for nodeID := range pt.proposals {
		if pt.trusted[nodeID] {
			trustedCount++
		}
	}

	if trustedCount == 0 {
		return false
	}

	_, bestCount := pt.GetWinningTxSet()
	return float64(bestCount)/float64(trustedCount) >= threshold
}

// Clear removes all proposals.
func (pt *ProposalTracker) Clear() {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	pt.proposals = make(map[consensus.NodeID]*consensus.Proposal)
	pt.byTxSet = make(map[consensus.TxSetID]map[consensus.NodeID]bool)
}

// DisputeTracker tracks disputed transactions and their per-peer
// votes during a consensus round. Mirrors the role of rippled's
// Result::disputes map plus the DisputedTx<> mutation API
// (rippled/src/xrpld/consensus/DisputedTx.h).
type DisputeTracker struct {
	mu sync.RWMutex

	disputes map[consensus.TxID]*consensus.DisputedTx
}

// NewDisputeTracker creates a new dispute tracker.
func NewDisputeTracker() *DisputeTracker {
	return &DisputeTracker{
		disputes: make(map[consensus.TxID]*consensus.DisputedTx),
	}
}

// CreateDispute registers a new disputed transaction. If the dispute
// already exists, the existing entry is returned unchanged. OurVote
// is seeded from the caller (it should be set to whether the tx is
// in our current proposed tx set).
//
// Matches the construction arm of rippled's createDisputes
// (Consensus.h:1867-1884).
func (dt *DisputeTracker) CreateDispute(txID consensus.TxID, tx []byte, ourVote bool) *consensus.DisputedTx {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if existing, exists := dt.disputes[txID]; exists {
		return existing
	}

	dispute := &consensus.DisputedTx{
		TxID:           txID,
		Tx:             tx,
		OurVote:        ourVote,
		Votes:          make(map[consensus.NodeID]bool),
		AvalancheState: consensus.AvalancheInit,
	}
	dt.disputes[txID] = dispute
	return dispute
}

// SetVote records a peer's yes/no vote on a disputed transaction.
// Returns true iff the vote was newly inserted OR changed from a
// previous value; returns false if the peer already held this exact
// vote (no count adjustment needed).
//
// The returned bool matches rippled's DisputedTx::setVote contract:
// callers use it to reset peerUnchangedCounter_ (Consensus.h:1879,
// 1906) to detect rounds where some peer is still actively updating.
func (dt *DisputeTracker) SetVote(txID consensus.TxID, peerID consensus.NodeID, yes bool) bool {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	dispute, exists := dt.disputes[txID]
	if !exists {
		return false
	}

	prev, had := dispute.Votes[peerID]
	switch {
	case !had:
		dispute.Votes[peerID] = yes
		if yes {
			dispute.Yays++
		} else {
			dispute.Nays++
		}
		return true
	case prev == yes:
		return false
	case yes:
		dispute.Votes[peerID] = true
		dispute.Nays--
		dispute.Yays++
		return true
	default:
		dispute.Votes[peerID] = false
		dispute.Yays--
		dispute.Nays++
		return true
	}
}

// UnVote removes a peer's contribution from every active dispute.
// Called when the peer bows out of the round (isBowOut), when its
// last-known proposal ages past proposeFRESHNESS, or when it is
// otherwise forcibly removed from currPeerPositions_.
//
// Matches rippled's bow-out loop at Consensus.h:807-811 and the
// stale-proposal loop at Consensus.h:1517-1520.
func (dt *DisputeTracker) UnVote(peerID consensus.NodeID) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	for _, dispute := range dt.disputes {
		vote, had := dispute.Votes[peerID]
		if !had {
			continue
		}
		delete(dispute.Votes, peerID)
		if vote {
			dispute.Yays--
		} else {
			dispute.Nays--
		}
	}
}

// UpdateDisputes records a peer's position across every active
// dispute: for each dispute, the peer votes YES iff the disputed tx
// appears in peerTxSet, else NO. Returns true iff any vote changed.
//
// Matches rippled's updateDisputes (Consensus.h:1892-1908).
func (dt *DisputeTracker) UpdateDisputes(peerID consensus.NodeID, peerTxSet consensus.TxSet) bool {
	if peerTxSet == nil {
		return false
	}
	dt.mu.Lock()
	defer dt.mu.Unlock()

	changed := false
	for txID, dispute := range dt.disputes {
		yes := peerTxSet.Contains(txID)
		prev, had := dispute.Votes[peerID]
		switch {
		case !had:
			dispute.Votes[peerID] = yes
			if yes {
				dispute.Yays++
			} else {
				dispute.Nays++
			}
			changed = true
		case prev == yes:
			// no-op
		case yes:
			dispute.Votes[peerID] = true
			dispute.Nays--
			dispute.Yays++
			changed = true
		default:
			dispute.Votes[peerID] = false
			dispute.Yays--
			dispute.Nays++
			changed = true
		}
	}
	return changed
}

// UpdateOurVote re-evaluates our vote on every dispute given the
// current convergePercent and avalanche thresholds, mirroring
// rippled's DisputedTx::updateVote (DisputedTx.h:278-338) applied
// across all disputes as in updateOurPositions (Consensus.h:1536-1564).
//
// For each dispute:
//   - If we already agree with the peer tally (ourVote=yes && nays==0,
//     or ourVote=no && yays==0), skip.
//   - Advance the dispute's avalanche state if allowed by percentTime
//     and MinRounds.
//   - When proposing, compute weight = (yays*100 + (ourVote ? 100 : 0))
//     / (yays + nays + 1); flip our vote iff weight > requiredPct.
//   - When not proposing, flip iff yays > nays (observer rule — we
//     don't outweigh proposers, just recognize consensus).
//
// Returns the list of TxIDs whose OurVote flipped this call. The
// engine uses that list to rebuild the proposed tx set.
func (dt *DisputeTracker) UpdateOurVote(percentTime int, proposing bool, parms consensus.ConsensusParms) []consensus.TxID {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	var changed []consensus.TxID
	for txID, dispute := range dt.disputes {
		if dispute.OurVote && dispute.Nays == 0 {
			continue
		}
		if !dispute.OurVote && dispute.Yays == 0 {
			continue
		}

		dispute.AvalancheCounter++
		requiredPct, newState := parms.NeededWeight(
			dispute.AvalancheState,
			percentTime,
			dispute.AvalancheCounter,
			parms.MinRounds,
		)
		if newState != nil {
			dispute.AvalancheState = *newState
			dispute.AvalancheCounter = 0
		}

		var newVote bool
		if proposing {
			ownContribution := 0
			if dispute.OurVote {
				ownContribution = 100
			}
			weight := (dispute.Yays*100 + ownContribution) /
				(dispute.Yays + dispute.Nays + 1)
			newVote = weight > requiredPct
		} else {
			// Observer: just recognize the majority, don't try to
			// add our own weight.
			newVote = dispute.Yays > dispute.Nays
		}

		if newVote == dispute.OurVote {
			dispute.CurrentVoteCounter++
			continue
		}
		dispute.CurrentVoteCounter = 0
		dispute.OurVote = newVote
		changed = append(changed, txID)
	}
	return changed
}

// GetDispute returns a disputed transaction.
func (dt *DisputeTracker) GetDispute(txID consensus.TxID) *consensus.DisputedTx {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return dt.disputes[txID]
}

// Has reports whether a dispute exists for the given TxID.
func (dt *DisputeTracker) Has(txID consensus.TxID) bool {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	_, ok := dt.disputes[txID]
	return ok
}

// GetAll returns all disputed transactions.
func (dt *DisputeTracker) GetAll() []*consensus.DisputedTx {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	result := make([]*consensus.DisputedTx, 0, len(dt.disputes))
	for _, d := range dt.disputes {
		result = append(result, d)
	}
	return result
}

// Count returns the number of disputes.
func (dt *DisputeTracker) Count() int {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return len(dt.disputes)
}

// Clear removes all disputes.
func (dt *DisputeTracker) Clear() {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	dt.disputes = make(map[consensus.TxID]*consensus.DisputedTx)
}
