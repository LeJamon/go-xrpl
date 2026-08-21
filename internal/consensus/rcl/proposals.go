package rcl

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// mostPopularTxSet returns the tx set with the highest count. Ties are
// broken deterministically by keeping the lexicographically smallest
// TxSetID, so that Go's randomized map-iteration order can never seed a
// fork or a replay mismatch. rippled defines no ordering here, so the
// choice of "smallest" is arbitrary; all that matters is that every node
// applies the same one. Returns the zero TxSetID and 0 for an empty map.
func mostPopularTxSet(counts map[consensus.TxSetID]int) (consensus.TxSetID, int) {
	var bestID consensus.TxSetID
	bestCount := -1
	for id, count := range counts {
		if count > bestCount || (count == bestCount && bytes.Compare(id[:], bestID[:]) < 0) {
			bestID = id
			bestCount = count
		}
	}
	if bestCount < 0 {
		bestCount = 0
	}
	return bestID, bestCount
}

// disputeTracker tracks disputed transactions and their per-peer
// votes during a consensus round. Mirrors the role of rippled's
// Result::disputes map plus the DisputedTx<> mutation API
// (rippled/src/xrpld/consensus/DisputedTx.h).
type disputeTracker struct {
	disputes map[consensus.TxID]*consensus.DisputedTx
}

// The owning Engine protects all access with e.mu; the tracker deliberately
// has no second lock.
func newDisputeTracker() *disputeTracker {
	return &disputeTracker{
		disputes: make(map[consensus.TxID]*consensus.DisputedTx),
	}
}

// createDispute registers a new disputed transaction. If the dispute
// already exists, the existing entry is returned unchanged. OurVote
// is seeded from the caller (it should be set to whether the tx is
// in our current proposed tx set).
//
// Matches the construction arm of rippled's createDisputes
// (Consensus.h:1867-1884).
func (dt *disputeTracker) createDispute(txID consensus.TxID, tx []byte, ourVote bool) *consensus.DisputedTx {
	if existing, exists := dt.disputes[txID]; exists {
		return existing
	}

	dispute := &consensus.DisputedTx{
		TxID:           txID,
		Tx:             append([]byte(nil), tx...),
		OurVote:        ourVote,
		Votes:          make(map[consensus.NodeID]bool),
		AvalancheState: consensus.AvalancheInit,
	}
	dt.disputes[txID] = dispute
	return dispute
}

// setVote records a peer's yes/no vote on a disputed transaction.
// Returns true iff the vote was newly inserted OR changed from a
// previous value; returns false if the peer already held this exact
// vote (no count adjustment needed).
//
// The returned bool matches rippled's DisputedTx::setVote contract:
// callers use it to reset peerUnchangedCounter_ (Consensus.h:1879,
// 1906) to detect rounds where some peer is still actively updating.
func (dt *disputeTracker) setVote(txID consensus.TxID, peerID consensus.NodeID, yes bool) bool {
	dispute, exists := dt.disputes[txID]
	if !exists {
		return false
	}
	return updateVoteCount(dispute, peerID, yes)
}

// updateVoteCount records peerID's yes/no vote on dispute, adjusting
// the Yays/Nays tallies. Returns true iff the vote was newly inserted
// or changed from a previous value. Caller must hold the owning Engine's e.mu.
func updateVoteCount(dispute *consensus.DisputedTx, peerID consensus.NodeID, yes bool) bool {
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

// unVote removes a peer's contribution from every active dispute.
// Called when the peer bows out of the round (isBowOut), when its
// last-known proposal ages past proposeFRESHNESS, or when it is
// otherwise forcibly removed from currPeerPositions_.
//
// Matches rippled's bow-out loop at Consensus.h:807-811 and the
// stale-proposal loop at Consensus.h:1517-1520.
func (dt *disputeTracker) unVote(peerID consensus.NodeID) {
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

// updateDisputes records a peer's position across every active
// dispute: for each dispute, the peer votes YES iff the disputed tx
// appears in peerTxSet, else NO. Returns true iff any vote changed.
//
// Matches rippled's updateDisputes (Consensus.h:1892-1908).
func (dt *disputeTracker) updateDisputes(peerID consensus.NodeID, peerTxSet consensus.TxSet) bool {
	if peerTxSet == nil {
		return false
	}
	changed := false
	for txID, dispute := range dt.disputes {
		if updateVoteCount(dispute, peerID, peerTxSet.Contains(txID)) {
			changed = true
		}
	}
	return changed
}

// updateOurVote re-evaluates our vote on every dispute given the
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
func (dt *disputeTracker) updateOurVote(percentTime int, proposing bool, parms consensus.ConsensusParms) []consensus.TxID {
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
			// Observer mode: recognise the majority position
			// without contributing our own weight (mirrors
			// rippled's non-proposing branch in
			// LedgerConsensus::updateOurPositions).
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

// allStalled reports whether every active dispute is stalled per
// DisputedTx.Stalled. An empty dispute set returns false — rippled
// gates the stalled bit on disputes being non-empty (Consensus.h:1718).
//
// Matches rippled's std::ranges::all_of stalled check at
// Consensus.h:1720-1728.
func (dt *disputeTracker) allStalled(parms consensus.ConsensusParms, proposing bool, peersUnchanged int) bool {
	if len(dt.disputes) == 0 {
		return false
	}
	for _, dispute := range dt.disputes {
		if !disputeStalled(dispute, parms, proposing, peersUnchanged) {
			return false
		}
	}
	return true
}

// disputeStalled mirrors rippled's DisputedTx::stalled (DisputedTx.h:88-149).
// A dispute is stalled when all of the following hold:
//   - it has reached the terminal avalanche cutoff (the next state loops
//     back to itself) and has been there for at least MinRounds;
//   - if we are proposing, our own vote has been stable for at least
//     MinRounds;
//   - either our peers have not changed votes for StalledRounds OR
//     (we are proposing AND our own vote has been stable for
//     StalledRounds) — i.e. at least one side has stopped moving;
//   - the tally is >MinConsensusPct one-sided (overwhelming yes or no
//     support), so further flipping is unlikely.
func disputeStalled(d *consensus.DisputedTx, parms consensus.ConsensusParms, proposing bool, peersUnchanged int) bool {
	currentCutoff := parms.AvalancheCutoff(d.AvalancheState)
	nextCutoff := parms.AvalancheCutoff(currentCutoff.Next)

	if nextCutoff.ConsensusTime > currentCutoff.ConsensusTime ||
		d.AvalancheCounter < parms.MinRounds {
		return false
	}
	if proposing && d.CurrentVoteCounter < parms.MinRounds {
		return false
	}
	// Stalled only when at least one side has frozen for StalledRounds.
	// While peers AND (when proposing) we ourselves are both still
	// flipping votes, the dispute is still moving.
	if peersUnchanged < parms.StalledRounds &&
		proposing && d.CurrentVoteCounter < parms.StalledRounds {
		return false
	}

	ownYes := 0
	selfTotal := 0
	if proposing {
		selfTotal = 1
		if d.OurVote {
			ownYes = 1
		}
	}
	support := (d.Yays + ownYes) * 100
	total := d.Nays + d.Yays + selfTotal
	if total == 0 {
		return false
	}
	weight := support / total
	return weight > parms.MinConsensusPct || weight < (100-parms.MinConsensusPct)
}

// dispute returns a disputed transaction.
func (dt *disputeTracker) dispute(txID consensus.TxID) *consensus.DisputedTx {
	return dt.disputes[txID]
}

// has reports whether a dispute exists for the given TxID.
func (dt *disputeTracker) has(txID consensus.TxID) bool {
	_, ok := dt.disputes[txID]
	return ok
}

// disputedNoTxs returns detached raw blobs of every dispute we currently vote
// NO on — txs peers proposed that end up excluded from the consensus set.
func (dt *disputeTracker) disputedNoTxs() [][]byte {
	var txs [][]byte
	for _, d := range dt.disputes {
		if !d.OurVote {
			txs = append(txs, append([]byte(nil), d.Tx...))
		}
	}
	return txs
}

// all returns detached snapshots of all disputed transactions.
func (dt *disputeTracker) all() []*consensus.DisputedTx {
	result := make([]*consensus.DisputedTx, 0, len(dt.disputes))
	for _, d := range dt.disputes {
		result = append(result, cloneDisputedTx(d))
	}
	return result
}

// count returns the number of disputes.
func (dt *disputeTracker) count() int {
	return len(dt.disputes)
}

func cloneDisputedTx(d *consensus.DisputedTx) *consensus.DisputedTx {
	if d == nil {
		return nil
	}
	copy := *d
	copy.Tx = append([]byte(nil), d.Tx...)
	copy.Votes = make(map[consensus.NodeID]bool, len(d.Votes))
	for nodeID, vote := range d.Votes {
		copy.Votes[nodeID] = vote
	}
	return &copy
}
