package rcl

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// recentProposalsPerNode caps the per-node cross-round playback buffer —
// recent positions kept for replay, bounded against gossip floods.
const recentProposalsPerNode = 10

// proposalTracker owns a round's peer-signal state: each trusted node's
// current position, the nodes that bowed out, the cross-round playback
// buffer, and the validations gathered for the accepted ledger. Not
// independently synchronized: every method runs under the Engine's e.mu.
type proposalTracker struct {
	// each trusted node's current-round position; reset at round start, removed on bow-out
	proposals map[consensus.NodeID]*consensus.Proposal

	// validators that bowed out (ProposeSeq == seqLeave); reset at round start so they can rejoin
	deadNodes map[consensus.NodeID]struct{}

	// up to recentProposalsPerNode per node for cross-round playback; NOT reset at round start
	recentProposals map[consensus.NodeID][]*consensus.Proposal

	// latest validation per trusted node, attached to the accepted ledger; reset on accept
	validations map[consensus.NodeID]*consensus.Validation
}

func newProposalTracker() *proposalTracker {
	return &proposalTracker{
		proposals:       make(map[consensus.NodeID]*consensus.Proposal),
		deadNodes:       make(map[consensus.NodeID]struct{}),
		recentProposals: make(map[consensus.NodeID][]*consensus.Proposal),
		validations:     make(map[consensus.NodeID]*consensus.Validation),
	}
}

// resetRound clears per-round positions and dead nodes at round start; it
// leaves recentProposals and validations (different lifecycles).
func (pt *proposalTracker) resetRound() {
	pt.proposals = make(map[consensus.NodeID]*consensus.Proposal)
	pt.deadNodes = make(map[consensus.NodeID]struct{})
}

// resetProposals clears current-round positions only (wrong-ledger switch
// keeps the dead-node set).
func (pt *proposalTracker) resetProposals() {
	pt.proposals = make(map[consensus.NodeID]*consensus.Proposal)
}

func (pt *proposalTracker) count() int {
	return len(pt.proposals)
}

// all returns a detached snapshot of the current-round positions. Callers may
// inspect or mutate the returned map and proposals without changing tracker
// state.
func (pt *proposalTracker) all() map[consensus.NodeID]*consensus.Proposal {
	out := make(map[consensus.NodeID]*consensus.Proposal, len(pt.proposals))
	for nodeID, proposal := range pt.proposals {
		out[nodeID] = cloneProposal(proposal)
	}
	return out
}

// store records a proposal as its node's position and reports whether it did.
// A proposal that does not advance the node's ProposeSeq — a re-send or a
// same-seq equivocation — is dropped.
func (pt *proposalTracker) store(p *consensus.Proposal) bool {
	existing, exists := pt.proposals[p.NodeID]
	if exists && p.Position <= existing.Position {
		return false
	}
	pt.proposals[p.NodeID] = cloneProposal(p)
	return true
}

func (pt *proposalTracker) countTrusted(trusted func(consensus.NodeID) bool) int {
	n := 0
	for nodeID := range pt.proposals {
		if trusted(nodeID) {
			n++
		}
	}
	return n
}

// pruneUntrusted permanently removes current-round and buffered positions
// whose validators are no longer trusted and returns the removed current
// node IDs. Replay and consensus callers invoke this immediately before using
// the current position set so a trust change cannot leave a stale vote in
// close-time or dispute tallies, then resurrect it if trust is restored.
func (pt *proposalTracker) pruneUntrusted(trusted func(consensus.NodeID) bool) []consensus.NodeID {
	var removed []consensus.NodeID
	for nodeID := range pt.proposals {
		if trusted(nodeID) {
			continue
		}
		pt.purgeNode(nodeID)
		removed = append(removed, nodeID)
	}
	for nodeID := range pt.recentProposals {
		if !trusted(nodeID) {
			pt.purgeNode(nodeID)
		}
	}
	return removed
}

// purgeNode permanently removes all current and buffered positions for node.
// It intentionally leaves the round's dead-node marker untouched: a replayed
// bow-out still has live-equivalent dead semantics even if its position data
// is later purged.
func (pt *proposalTracker) purgeNode(nodeID consensus.NodeID) {
	delete(pt.proposals, nodeID)
	delete(pt.recentProposals, nodeID)
}

// markDead removes a node's position and records it as bowed out for the round.
func (pt *proposalTracker) markDead(nodeID consensus.NodeID) {
	delete(pt.proposals, nodeID)
	pt.deadNodes[nodeID] = struct{}{}
}

func (pt *proposalTracker) isDead(nodeID consensus.NodeID) bool {
	_, dead := pt.deadNodes[nodeID]
	return dead
}

func (pt *proposalTracker) deadNodeCount() int {
	return len(pt.deadNodes)
}

// deadNodeIDs returns the bowed-out node IDs in map order.
func (pt *proposalTracker) deadNodeIDs() []consensus.NodeID {
	ids := make([]consensus.NodeID, 0, len(pt.deadNodes))
	for nodeID := range pt.deadNodes {
		ids = append(ids, nodeID)
	}
	return ids
}

// pruneStale removes positions older than cutoff and returns their node IDs so
// the caller can unvote them from disputes. Zero-timestamp positions are kept.
func (pt *proposalTracker) pruneStale(cutoff time.Time) []consensus.NodeID {
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

// bufferRecent appends to the node's playback buffer, capped at recentProposalsPerNode (oldest dropped).
func (pt *proposalTracker) bufferRecent(p *consensus.Proposal) {
	positions := pt.recentProposals[p.NodeID]
	if len(positions) >= recentProposalsPerNode {
		positions = positions[1:]
	}
	pt.recentProposals[p.NodeID] = append(positions, cloneProposal(p))
}

func (pt *proposalTracker) hasBufferedFor(prevID consensus.LedgerID) bool {
	for _, positions := range pt.recentProposals {
		for _, p := range positions {
			if p.PreviousLedger == prevID {
				return true
			}
		}
	}
	return false
}

// latestFresh returns each trusted node's newest buffered proposal timestamped
// within freshness of now. Buffers are in arrival order, so it scans newest-first.
func (pt *proposalTracker) latestFresh(trusted func(consensus.NodeID) bool, now time.Time, freshness time.Duration) map[consensus.NodeID]*consensus.Proposal {
	out := make(map[consensus.NodeID]*consensus.Proposal)
	for nodeID, positions := range pt.recentProposals {
		if !trusted(nodeID) {
			continue
		}
		for i := len(positions) - 1; i >= 0; i-- {
			if now.Sub(positions[i].Timestamp) > freshness {
				continue
			}
			out[nodeID] = cloneProposal(positions[i])
			break
		}
	}
	return out
}

// replayCloseTime is an initial close-time vote replayed from a trusted
// validator. The node identity lets callers re-check trust immediately before
// recording the vote, after replay has returned.
type replayCloseTime struct {
	NodeID    consensus.NodeID
	CloseTime time.Time
}

// replay upserts buffered proposals for prevID into current-round positions
// (monotonic) and returns the node-associated close-time votes to record — one
// per stored Position==0 trusted proposal — the count of trusted proposals
// replayed, and the proposals whose position was (re-)stored, so the caller can
// re-share them to peers that missed them on this ledger. A replayed seqLeave
// removes the current position, marks the node dead, and is returned for relay
// without counting as a proposer. Buffered duplicates at a non-increasing
// ProposeSeq are dropped: not counted, not relayed.
func (pt *proposalTracker) replay(prevID consensus.LedgerID, trusted func(consensus.NodeID) bool) (closeTimes []replayCloseTime, trustedReplayed int, relay []*consensus.Proposal) {
	// A trust transition can occur between rounds, leaving an old current
	// position behind while its validator's buffered entries are replayed.
	// Remove it before replay can expose the current set to callers.
	pt.pruneUntrusted(trusted)
	for nodeID, positions := range pt.recentProposals {
		if !trusted(nodeID) {
			pt.purgeNode(nodeID)
			continue
		}
		for _, p := range positions {
			if p.PreviousLedger != prevID {
				continue
			}
			// Re-check immediately before storing: the trust view may have
			// changed while replaying another validator's buffered positions.
			if !trusted(nodeID) {
				pt.purgeNode(nodeID)
				break
			}
			// Replay a bow-out through the same current/dead/unvote state
			// transition as a live proposal. Preserve it in recent history so
			// repeated playback for this ledger remains terminal.
			const seqLeave = uint32(0xFFFFFFFF)
			if p.Position == seqLeave {
				if pt.isDead(nodeID) {
					break
				}
				pt.markDead(nodeID)
				if !trusted(nodeID) {
					pt.purgeNode(nodeID)
					break
				}
				relay = append(relay, cloneProposal(p))
				break
			}
			if pt.isDead(nodeID) {
				break
			}
			if !pt.store(p) {
				continue
			}
			// A trust transition during Store must not leave a current-round
			// position that can be relayed or tallied by the caller.
			if !trusted(nodeID) {
				pt.purgeNode(nodeID)
				break
			}
			relay = append(relay, cloneProposal(p))
			if p.Position == 0 {
				closeTimes = append(closeTimes, replayCloseTime{NodeID: nodeID, CloseTime: p.CloseTime})
			}
			trustedReplayed++
		}
	}
	return closeTimes, trustedReplayed, relay
}

func (pt *proposalTracker) setValidation(v *consensus.Validation) {
	pt.validations[v.NodeID] = cloneProposalValidation(v)
}

func (pt *proposalTracker) validationsFor(ledgerID consensus.LedgerID) []*consensus.Validation {
	var out []*consensus.Validation
	for _, v := range pt.validations {
		if v.LedgerID == ledgerID {
			out = append(out, cloneProposalValidation(v))
		}
	}
	return out
}

func (pt *proposalTracker) resetValidations() {
	pt.validations = make(map[consensus.NodeID]*consensus.Validation)
}

func cloneProposal(p *consensus.Proposal) *consensus.Proposal {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Signature = append([]byte(nil), p.Signature...)
	return &copy
}

func cloneProposalValidation(v *consensus.Validation) *consensus.Validation {
	if v == nil {
		return nil
	}
	copy := *v
	copy.Signature = append([]byte(nil), v.Signature...)
	copy.Amendments = append([][32]byte(nil), v.Amendments...)
	copy.SigningData = append([]byte(nil), v.SigningData...)
	copy.Raw = append([]byte(nil), v.Raw...)
	return &copy
}
