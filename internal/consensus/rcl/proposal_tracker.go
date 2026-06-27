package rcl

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// recentProposalsPerNode caps the per-node cross-round playback buffer —
// recent positions kept for replay, bounded against gossip floods.
const recentProposalsPerNode = 10

// ProposalTracker owns a round's peer-signal state: each trusted node's
// current position, the nodes that bowed out, the cross-round playback
// buffer, and the validations gathered for the accepted ledger. Not
// independently synchronized: every method runs under the Engine's e.mu.
type ProposalTracker struct {
	// each trusted node's current-round position; reset at round start, removed on bow-out
	proposals map[consensus.NodeID]*consensus.Proposal

	// validators that bowed out (ProposeSeq == seqLeave); reset at round start so they can rejoin
	deadNodes map[consensus.NodeID]struct{}

	// up to recentProposalsPerNode per node for cross-round playback; NOT reset at round start
	recentProposals map[consensus.NodeID][]*consensus.Proposal

	// latest validation per trusted node, attached to the accepted ledger; reset on accept
	validations map[consensus.NodeID]*consensus.Validation
}

func NewProposalTracker() *ProposalTracker {
	return &ProposalTracker{
		proposals:       make(map[consensus.NodeID]*consensus.Proposal),
		deadNodes:       make(map[consensus.NodeID]struct{}),
		recentProposals: make(map[consensus.NodeID][]*consensus.Proposal),
		validations:     make(map[consensus.NodeID]*consensus.Validation),
	}
}

// ResetRound clears per-round positions and dead nodes at round start; it
// leaves recentProposals and validations (different lifecycles).
func (pt *ProposalTracker) ResetRound() {
	pt.proposals = make(map[consensus.NodeID]*consensus.Proposal)
	pt.deadNodes = make(map[consensus.NodeID]struct{})
}

// ResetProposals clears current-round positions only (wrong-ledger switch
// keeps the dead-node set).
func (pt *ProposalTracker) ResetProposals() {
	pt.proposals = make(map[consensus.NodeID]*consensus.Proposal)
}

// Count returns the number of current-round positions.
func (pt *ProposalTracker) Count() int {
	return len(pt.proposals)
}

// All returns the current-round positions for read-only iteration; mutate via
// Store/MarkDead/PruneStale.
func (pt *ProposalTracker) All() map[consensus.NodeID]*consensus.Proposal {
	return pt.proposals
}

// Store records a proposal as its node's position when newer (higher ProposeSeq).
func (pt *ProposalTracker) Store(p *consensus.Proposal) {
	existing, exists := pt.proposals[p.NodeID]
	if !exists || p.Position > existing.Position {
		pt.proposals[p.NodeID] = p
	}
}

func (pt *ProposalTracker) CountTrusted(trusted func(consensus.NodeID) bool) int {
	n := 0
	for nodeID := range pt.proposals {
		if trusted(nodeID) {
			n++
		}
	}
	return n
}

// MarkDead removes a node's position and records it as bowed out for the round.
func (pt *ProposalTracker) MarkDead(nodeID consensus.NodeID) {
	delete(pt.proposals, nodeID)
	pt.deadNodes[nodeID] = struct{}{}
}

func (pt *ProposalTracker) IsDead(nodeID consensus.NodeID) bool {
	_, dead := pt.deadNodes[nodeID]
	return dead
}

func (pt *ProposalTracker) DeadNodeCount() int {
	return len(pt.deadNodes)
}

// DeadNodeIDs returns the bowed-out node IDs in map order.
func (pt *ProposalTracker) DeadNodeIDs() []consensus.NodeID {
	ids := make([]consensus.NodeID, 0, len(pt.deadNodes))
	for nodeID := range pt.deadNodes {
		ids = append(ids, nodeID)
	}
	return ids
}

// PruneStale removes positions older than cutoff and returns their node IDs so
// the caller can unvote them from disputes. Zero-timestamp positions are kept.
func (pt *ProposalTracker) PruneStale(cutoff time.Time) []consensus.NodeID {
	var removed []consensus.NodeID
	for nodeID, p := range pt.proposals {
		if p.Timestamp.IsZero() {
			continue
		}
		if p.Timestamp.Before(cutoff) {
			delete(pt.proposals, nodeID)
			removed = append(removed, nodeID)
		}
	}
	return removed
}

// BufferRecent appends to the node's playback buffer, capped at recentProposalsPerNode (oldest dropped).
func (pt *ProposalTracker) BufferRecent(p *consensus.Proposal) {
	positions := pt.recentProposals[p.NodeID]
	if len(positions) >= recentProposalsPerNode {
		positions = positions[1:]
	}
	pt.recentProposals[p.NodeID] = append(positions, p)
}

// HasBufferedFor reports whether any buffered proposal has prevID as its previous ledger.
func (pt *ProposalTracker) HasBufferedFor(prevID consensus.LedgerID) bool {
	for _, positions := range pt.recentProposals {
		for _, p := range positions {
			if p.PreviousLedger == prevID {
				return true
			}
		}
	}
	return false
}

// LatestFresh returns each trusted node's newest buffered proposal timestamped
// within freshness of now. Buffers are in arrival order, so it scans newest-first.
func (pt *ProposalTracker) LatestFresh(trusted func(consensus.NodeID) bool, now time.Time, freshness time.Duration) map[consensus.NodeID]*consensus.Proposal {
	out := make(map[consensus.NodeID]*consensus.Proposal)
	for nodeID, positions := range pt.recentProposals {
		if !trusted(nodeID) {
			continue
		}
		for i := len(positions) - 1; i >= 0; i-- {
			if now.Sub(positions[i].Timestamp) > freshness {
				continue
			}
			out[nodeID] = positions[i]
			break
		}
	}
	return out
}

// Replay upserts buffered proposals for prevID into current-round positions
// (monotonic) and returns the close-time votes to record — one per Position==0
// trusted proposal — plus the count of trusted proposals replayed.
func (pt *ProposalTracker) Replay(prevID consensus.LedgerID, trusted func(consensus.NodeID) bool) (closeTimes []time.Time, trustedReplayed int) {
	for nodeID, positions := range pt.recentProposals {
		for _, p := range positions {
			if p.PreviousLedger != prevID {
				continue
			}
			isTrusted := trusted(nodeID)
			pt.Store(p)
			if p.Position == 0 && isTrusted {
				closeTimes = append(closeTimes, p.CloseTime)
			}
			if isTrusted {
				trustedReplayed++
			}
		}
	}
	return closeTimes, trustedReplayed
}

func (pt *ProposalTracker) SetValidation(v *consensus.Validation) {
	pt.validations[v.NodeID] = v
}

func (pt *ProposalTracker) ValidationsFor(ledgerID consensus.LedgerID) []*consensus.Validation {
	var out []*consensus.Validation
	for _, v := range pt.validations {
		if v.LedgerID == ledgerID {
			out = append(out, v)
		}
	}
	return out
}

func (pt *ProposalTracker) ResetValidations() {
	pt.validations = make(map[consensus.NodeID]*consensus.Validation)
}
