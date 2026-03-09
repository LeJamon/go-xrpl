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

// DisputeTracker tracks disputed transactions during consensus.
type DisputeTracker struct {
	mu sync.RWMutex

	// disputes maps tx ID to dispute info
	disputes map[consensus.TxID]*consensus.DisputedTx

	// ourVotes tracks our votes on disputes
	ourVotes map[consensus.TxID]bool
}

// NewDisputeTracker creates a new dispute tracker.
func NewDisputeTracker() *DisputeTracker {
	return &DisputeTracker{
		disputes: make(map[consensus.TxID]*consensus.DisputedTx),
		ourVotes: make(map[consensus.TxID]bool),
	}
}

// CreateDispute creates a new disputed transaction.
func (dt *DisputeTracker) CreateDispute(txID consensus.TxID, tx []byte, ourVote bool) *consensus.DisputedTx {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if existing, exists := dt.disputes[txID]; exists {
		return existing
	}

	dispute := &consensus.DisputedTx{
		TxID:    txID,
		Tx:      tx,
		OurVote: ourVote,
		Yays:    0,
		Nays:    0,
	}

	if ourVote {
		dispute.Yays = 1
	} else {
		dispute.Nays = 1
	}

	dt.disputes[txID] = dispute
	dt.ourVotes[txID] = ourVote

	return dispute
}

// AddVote records a vote on a disputed transaction.
func (dt *DisputeTracker) AddVote(txID consensus.TxID, include bool) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	dispute, exists := dt.disputes[txID]
	if !exists {
		return
	}

	if include {
		dispute.Yays++
	} else {
		dispute.Nays++
	}
}

// GetDispute returns a disputed transaction.
func (dt *DisputeTracker) GetDispute(txID consensus.TxID) *consensus.DisputedTx {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	return dt.disputes[txID]
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

// Resolve determines which transactions should be included.
// Returns (include, exclude) lists.
func (dt *DisputeTracker) Resolve(threshold float64) ([]consensus.TxID, []consensus.TxID) {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	var include, exclude []consensus.TxID

	for txID, dispute := range dt.disputes {
		total := dispute.Yays + dispute.Nays
		if total == 0 {
			continue
		}

		if float64(dispute.Yays)/float64(total) >= threshold {
			include = append(include, txID)
		} else {
			exclude = append(exclude, txID)
		}
	}

	return include, exclude
}

// UpdateOurVote updates our vote on a dispute.
func (dt *DisputeTracker) UpdateOurVote(txID consensus.TxID, include bool) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	dispute, exists := dt.disputes[txID]
	if !exists {
		return
	}

	oldVote, hadVote := dt.ourVotes[txID]
	if hadVote && oldVote == include {
		return // No change
	}

	// Update vote counts
	if hadVote {
		if oldVote {
			dispute.Yays--
		} else {
			dispute.Nays--
		}
	}

	if include {
		dispute.Yays++
	} else {
		dispute.Nays++
	}

	dispute.OurVote = include
	dt.ourVotes[txID] = include
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
	dt.ourVotes = make(map[consensus.TxID]bool)
}
