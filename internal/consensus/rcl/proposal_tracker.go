package rcl

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// recentProposalsPerNode caps the cross-round playback buffer per node.
// Matches rippled's recentPeerPositions_ retention (Consensus.h:754): a
// trusted validator's most recent positions are kept so a freshly started
// round can replay gossip that arrived during the accepted phase, while
// the cap bounds memory under sustained gossip.
const recentProposalsPerNode = 10

// ProposalTracker owns the round-scoped peer-signal state of a consensus
// round: each trusted node's current position, the set of nodes that have
// bowed out, the cross-round proposal buffer used for playback, and the
// validations gathered for the round's accepted ledger.
//
// It is the third state cluster the Engine delegates to, alongside
// ValidationTracker and DisputeTracker. Unlike those two it is NOT
// independently synchronized: every method is called with the Engine's
// e.mu held (read or write as appropriate) — the same lock that already
// serialized these maps when they were inline Engine fields. Holding e.mu
// is the caller's responsibility.
type ProposalTracker struct {
	// proposals holds each trusted node's current-round position. Reset at
	// round start; a node that bows out is removed.
	proposals map[consensus.NodeID]*consensus.Proposal

	// deadNodes records validators that bowed out of the current round by
	// sending a position with ProposeSeq == seqLeave. Reset at round start
	// (a node that bowed out of the prior round may rejoin the new one).
	deadNodes map[consensus.NodeID]struct{}

	// recentProposals buffers up to recentProposalsPerNode positions per
	// node for cross-round playback. NOT reset at round start — it carries
	// gossip across rounds.
	recentProposals map[consensus.NodeID][]*consensus.Proposal

	// validations holds the latest validation per trusted node for the
	// current round, gathered to attach to the accepted ledger. Reset when
	// a ledger is accepted.
	validations map[consensus.NodeID]*consensus.Validation
}

// NewProposalTracker creates an empty proposal tracker.
func NewProposalTracker() *ProposalTracker {
	return &ProposalTracker{
		proposals:       make(map[consensus.NodeID]*consensus.Proposal),
		deadNodes:       make(map[consensus.NodeID]struct{}),
		recentProposals: make(map[consensus.NodeID][]*consensus.Proposal),
		validations:     make(map[consensus.NodeID]*consensus.Validation),
	}
}

// ResetRound clears the per-round position and dead-node state at the
// start of a new consensus round. recentProposals and validations have
// different lifecycles and are left intact.
func (pt *ProposalTracker) ResetRound() {
	pt.proposals = make(map[consensus.NodeID]*consensus.Proposal)
	pt.deadNodes = make(map[consensus.NodeID]struct{})
}

// ResetProposals clears only the current-round positions. Used on a
// wrong-ledger switch, which keeps the dead-node set.
func (pt *ProposalTracker) ResetProposals() {
	pt.proposals = make(map[consensus.NodeID]*consensus.Proposal)
}

// Count returns the number of current-round positions.
func (pt *ProposalTracker) Count() int {
	return len(pt.proposals)
}

// All returns the current-round positions for iteration. Callers range it
// read-only; mutations go through Store / MarkDead / PruneStale.
func (pt *ProposalTracker) All() map[consensus.NodeID]*consensus.Proposal {
	return pt.proposals
}

// Store records proposal as its node's position when it is newer than any
// stored one (higher ProposeSeq), matching rippled's monotonic position
// update.
func (pt *ProposalTracker) Store(p *consensus.Proposal) {
	existing, exists := pt.proposals[p.NodeID]
	if !exists || p.Position > existing.Position {
		pt.proposals[p.NodeID] = p
	}
}

// CountTrusted returns how many current-round positions come from nodes
// for which trusted reports true.
func (pt *ProposalTracker) CountTrusted(trusted func(consensus.NodeID) bool) int {
	n := 0
	for nodeID := range pt.proposals {
		if trusted(nodeID) {
			n++
		}
	}
	return n
}

// MarkDead removes a node's current position and records it as bowed out
// for the rest of the round.
func (pt *ProposalTracker) MarkDead(nodeID consensus.NodeID) {
	delete(pt.proposals, nodeID)
	pt.deadNodes[nodeID] = struct{}{}
}

// IsDead reports whether a node has bowed out this round.
func (pt *ProposalTracker) IsDead(nodeID consensus.NodeID) bool {
	_, dead := pt.deadNodes[nodeID]
	return dead
}

// DeadNodeCount returns the number of bowed-out nodes.
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

// PruneStale removes every current position last refreshed before cutoff
// (a peer that stopped proposing within the round) and returns the removed
// node IDs so the caller can unvote them from active disputes. Positions
// with a zero timestamp are never pruned.
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

// BufferRecent appends a proposal to its node's cross-round playback
// buffer, capped at recentProposalsPerNode (oldest dropped).
func (pt *ProposalTracker) BufferRecent(p *consensus.Proposal) {
	positions := pt.recentProposals[p.NodeID]
	if len(positions) >= recentProposalsPerNode {
		positions = positions[1:]
	}
	pt.recentProposals[p.NodeID] = append(positions, p)
}

// HasBufferedFor reports whether any buffered proposal references prevID
// as its previous ledger.
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

// LatestFresh returns, for each node whose newest buffered proposal was
// timestamped within freshness of now, that newest fresh proposal. trusted
// filters which nodes are considered. Buffer slices are in arrival order,
// so the scan runs newest-first and stops at the first fresh entry.
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

// Replay upserts every buffered proposal whose PreviousLedger == prevID
// into the current-round positions (monotonic by ProposeSeq) and returns
// the close-time votes that should be recorded — one per Position==0
// proposal from a trusted node — together with the count of trusted
// proposals replayed. Matches rippled's playbackProposals.
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

// SetValidation records a validation as its node's latest for the round.
func (pt *ProposalTracker) SetValidation(v *consensus.Validation) {
	pt.validations[v.NodeID] = v
}

// ValidationsFor returns the validations gathered this round for ledgerID.
func (pt *ProposalTracker) ValidationsFor(ledgerID consensus.LedgerID) []*consensus.Validation {
	var out []*consensus.Validation
	for _, v := range pt.validations {
		if v.LedgerID == ledgerID {
			out = append(out, v)
		}
	}
	return out
}

// ResetValidations clears the round's gathered validations after a ledger
// is accepted.
func (pt *ProposalTracker) ResetValidations() {
	pt.validations = make(map[consensus.NodeID]*consensus.Validation)
}
