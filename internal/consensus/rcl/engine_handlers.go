package rcl

import (
	"fmt"
	"log/slog"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// OnProposal handles an incoming proposal. originPeer (0 = self) is
// excluded from the RelayProposal gossip forward.
func (e *Engine) OnProposal(proposal *consensus.Proposal, originPeer uint64) error {
	// Verify before taking e.mu: verification is pure, and doing it under the
	// write lock would serialize gossip-rate verifies behind round driving.
	if err := e.adaptor.VerifyProposal(proposal); err != nil {
		return fmt.Errorf("invalid proposal signature: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// A proposal carrying our own validator identity — a duplicate-key
	// misconfiguration (two nodes sharing our key) or our own proposal routed
	// back to us — must not be absorbed as a foreign position; that double-counts
	// our vote. Checked before the trust gate because we don't list our own key
	// in our trusted set, so an unrecognised self-keyed proposal would otherwise
	// be dropped just below as "untrusted", losing the misconfiguration signal.
	if e.adaptor.IsValidator() {
		if ourKey, err := e.adaptor.GetValidatorKey(); err == nil && proposal.NodeID == ourKey {
			slog.Error("dropping proposal signed with our own validator key",
				"t", "consensus",
				"event", "self-key-proposal",
				"peer", originPeer,
				"node", fmt.Sprintf("%x", proposal.NodeID[:6]))
			return nil
		}
	}

	// Drop untrusted proposals: buffering them would let throwaway keypairs
	// grow the tracker unboundedly and feed phantom proposers into
	// convergence counts.
	if !e.adaptor.IsTrusted(proposal.NodeID) {
		return nil
	}

	// Buffer for future playback, even between rounds.
	e.proposalTracker.BufferRecent(proposal)

	// Between rounds (accepted phase) only buffer, don't process.
	if e.phase == consensus.PhaseAccepted {
		return nil
	}

	// Reject proposals on a different previous ledger.
	if e.prevLedger != nil && proposal.PreviousLedger != e.prevLedger.ID() {
		return nil
	}

	// Ignore already-dead nodes. Must precede the bow-out arm: otherwise a
	// dead node could re-insert itself by re-sending seqLeave.
	if e.proposalTracker.IsDead(proposal.NodeID) {
		return nil
	}

	// Bow-out: a validator's final position sets ProposeSeq to seqLeave.
	// Erase its position, mark it dead, and un-vote it from every dispute —
	// otherwise the seqLeave position keeps voting forever.
	const seqLeave = uint32(0xFFFFFFFF)
	if proposal.Position == seqLeave {
		e.proposalTracker.MarkDead(proposal.NodeID)
		// Drop its dispute votes so they stop counting toward convergence.
		if e.disputeTracker != nil {
			e.disputeTracker.UnVote(proposal.NodeID)
		}
		return nil
	}

	// Drop non-increasing positions before counting close-time votes,
	// relaying, or updating disputes — otherwise a re-sent or equivocating
	// proposal at an already-seen ProposeSeq votes again.
	if !e.proposalTracker.Store(proposal) {
		return nil
	}

	// Record close time only from initial (Position == 0) proposals.
	if proposal.Position == 0 {
		e.state.CloseTimes.Peers[proposal.CloseTime]++
	}

	e.eventBus.Publish(&consensus.ProposalReceivedEvent{
		Proposal:  proposal,
		Trusted:   true,
		Timestamp: e.adaptor.Now(),
	})

	e.adaptor.RelayProposal(proposal, originPeer)

	{
		var ourTxSet consensus.TxSetID
		ourTxLen := -1
		if e.ourTxSet != nil {
			ourTxSet = e.ourTxSet.ID()
			ourTxLen = e.ourTxSet.Size()
		}
		_, peerCacheHit := e.acquiredTxSets[proposal.TxSet]
		if !peerCacheHit {
			if cached, _ := e.adaptor.GetTxSet(proposal.TxSet); cached != nil {
				peerCacheHit = true
			}
		}
		slog.Info("proposal received",
			"t", "consensus",
			"event", "propose-recv",
			"seq", proposal.Round.Seq,
			"peer", originPeer,
			"node", fmt.Sprintf("%x", proposal.NodeID[:6]),
			"pos_seq", proposal.Position,
			"peer_txset", fmt.Sprintf("%x", proposal.TxSet[:8]),
			"our_txset", fmt.Sprintf("%x", ourTxSet[:8]),
			"our_tx_count", ourTxLen,
			"peer_txset_cache_hit", peerCacheHit,
			"diff", proposal.TxSet != ourTxSet,
		)
	}

	// If the adaptor already has the tx set, cache it for dispute wiring;
	// else request it.
	if peerSet, err := e.adaptor.GetTxSet(proposal.TxSet); err == nil && peerSet != nil {
		if _, already := e.acquiredTxSets[proposal.TxSet]; !already {
			e.acquiredTxSets[proposal.TxSet] = peerSet
		}
	} else {
		e.adaptor.RequestTxSet(proposal.TxSet)
	}

	// If we hold the peer's tx set, run create/update-disputes for this
	// position (self-originated sets were already seeded in closeLedger).
	if e.ourTxSet != nil && proposal.TxSet != e.ourTxSet.ID() {
		if peerSet, ok := e.acquiredTxSets[proposal.TxSet]; ok {
			e.createDisputesAgainst(peerSet)
			if e.disputeTracker.UpdateDisputes(proposal.NodeID, peerSet) {
				e.peerUnchangedCounter = 0
			}
		}
	}

	if e.phase == consensus.PhaseEstablish {
		e.checkConvergence()
	}

	return nil
}

// OnValidation handles an incoming validation. originPeer (0 = self) is
// excluded from the RelayValidation gossip forward.
func (e *Engine) OnValidation(validation *consensus.Validation, originPeer uint64) error {
	// Verify before taking e.mu — see OnProposal.
	if err := e.adaptor.VerifyValidation(validation); err != nil {
		return fmt.Errorf("invalid validation signature: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	trusted := e.adaptor.IsTrusted(validation.NodeID)

	// Track listed-but-untrusted signers too: a validator published by a
	// configured list publisher but below the trust threshold gets its
	// validations stored (untrusted — quorum and trie filter on the trusted
	// set at read time), so a later trust change promotes what was already
	// seen instead of waiting one validation interval for a fresh one.
	// Publisher lists bound the key space, so this can't grow unboundedly.
	tracked := trusted
	if !tracked && e.listedOracle != nil {
		tracked = e.listedOracle.IsListed(validation.NodeID)
	}

	// Operator [relay_validations] stance: "all" (the default, matching
	// rippled) also forwards verified, current validations signed outside
	// our UNL, so peers with a different UNL that do trust the signer still
	// receive them.
	relay := trusted || (e.relayPolicy != nil && e.relayPolicy.RelayUntrustedValidations())

	// Feed the tracker — the gate that advances validated_ledger once a
	// quorum of trusted FULL validations accumulates (partials steer the trie
	// but don't count). Listed-but-untrusted keys are tracked too so a later
	// trust change promotes what was already seen; untrusted-and-unlisted keys
	// are dropped so the byNode map can't grow unboundedly.
	//
	// AddStatus doubles as the Byzantine detector: a validator must not sign
	// two ledgers (or re-sign differently) for one seq, even a seq its tip has
	// already superseded. On conflicting/multiple the validation is kept out
	// of quorum/trie but STILL relayed under the relay policy (peers should
	// observe it too) and no one is charged; the returned error only tells the
	// router to skip the catch-up acquire, not to penalise the relaying peer.
	if tracked && e.validationTracker != nil {
		switch status := e.validationTracker.AddStatus(validation); status {
		case ValStatusConflicting, ValStatusMultiple:
			if relay {
				e.adaptor.RelayValidation(validation, originPeer)
			}
			return &consensus.ByzantineValidationError{NodeID: validation.NodeID, Reason: status.String(), Trusted: trusted}
		}
	}

	// Round-scoped bookkeeping stays trusted-only.
	if trusted {
		e.proposalTracker.SetValidation(validation)
	}

	e.eventBus.Publish(&consensus.ValidationReceivedEvent{
		Validation: validation,
		Trusted:    trusted,
		Timestamp:  e.adaptor.Now(),
	})

	if relay {
		e.adaptor.RelayValidation(validation, originPeer)
	}

	return nil
}

// OnTxSet handles receiving a transaction set we requested.
func (e *Engine) OnTxSet(id consensus.TxSetID, txs [][]byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	txSet, err := e.adaptor.BuildTxSet(txs)
	if err != nil {
		return fmt.Errorf("failed to build tx set: %w", err)
	}

	if txSet.ID() != id {
		return fmt.Errorf("tx set ID mismatch: expected %x, got %x", id, txSet.ID())
	}

	// Cache for dispute wiring. A late tx set retroactively populates any
	// dispute whose tx it contains for some peer.
	if _, already := e.acquiredTxSets[id]; !already {
		e.acquiredTxSets[id] = txSet
		if e.ourTxSet != nil && id != e.ourTxSet.ID() {
			e.createDisputesAgainst(txSet)
			for nodeID, p := range e.proposalTracker.All() {
				if p.TxSet == id {
					if e.disputeTracker.UpdateDisputes(nodeID, txSet) {
						e.peerUnchangedCounter = 0
					}
				}
			}
		}
	}

	if e.phase == consensus.PhaseEstablish {
		e.checkConvergence()
	}

	return nil
}

// createDisputesAgainst creates a DisputedTx for every tx in only one
// side of the symmetric difference between a peer's set and ours,
// back-filling per-peer votes for each. Caller must hold e.mu.
func (e *Engine) createDisputesAgainst(peerTxSet consensus.TxSet) {
	if e.ourTxSet == nil || peerTxSet == nil {
		return
	}
	id := peerTxSet.ID()
	if _, seen := e.comparesTxSets[id]; seen {
		return
	}
	e.comparesTxSets[id] = struct{}{}

	if id == e.ourTxSet.ID() {
		return
	}

	ourIDs := e.ourTxSet.TxIDs()
	peerIDs := peerTxSet.TxIDs()

	ours := make(map[consensus.TxID]struct{}, len(ourIDs))
	for _, txID := range ourIDs {
		ours[txID] = struct{}{}
	}
	peers := make(map[consensus.TxID]struct{}, len(peerIDs))
	for _, txID := range peerIDs {
		peers[txID] = struct{}{}
	}

	// txs only in our set: seed ourVote=true and peer-vote=false.
	ourBlobs := e.ourTxSet.Txs()
	for idx, txID := range ourIDs {
		if _, also := peers[txID]; also {
			continue
		}
		if e.disputeTracker.Has(txID) {
			continue
		}
		var blob []byte
		if idx < len(ourBlobs) {
			blob = ourBlobs[idx]
		}
		dispute := e.disputeTracker.CreateDispute(txID, blob, true)
		e.seedDisputeVotes(dispute.TxID)
	}

	// txs only in peer's set: seed ourVote=false.
	peerBlobs := peerTxSet.Txs()
	for idx, txID := range peerIDs {
		if _, also := ours[txID]; also {
			continue
		}
		if e.disputeTracker.Has(txID) {
			continue
		}
		var blob []byte
		if idx < len(peerBlobs) {
			blob = peerBlobs[idx]
		}
		dispute := e.disputeTracker.CreateDispute(txID, blob, false)
		e.seedDisputeVotes(dispute.TxID)
	}
}

// seedDisputeVotes records each known peer's vote on a new dispute from
// its acquired tx set. Caller must hold e.mu.
func (e *Engine) seedDisputeVotes(txID consensus.TxID) {
	for nodeID, p := range e.proposalTracker.All() {
		peerSet, ok := e.acquiredTxSets[p.TxSet]
		if !ok {
			continue
		}
		if e.disputeTracker.SetVote(txID, nodeID, peerSet.Contains(txID)) {
			e.peerUnchangedCounter = 0
		}
	}
}

// OnLedger handles receiving a ledger we were missing.
func (e *Engine) OnLedger(id consensus.LedgerID, ledger []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Don't move prevLedger / start a round while an off-lock LCL build is in
	// flight; the commit tail owns round state until it completes.
	if e.buildInProgress {
		return nil
	}

	// If we were on wrong ledger, check if this helps
	if e.mode == consensus.ModeWrongLedger {
		l, err := e.adaptor.GetLedger(id)
		if err == nil && l != nil {
			// Never regress on out-of-order acquisition arrivals — EXCEPT for
			// the hash checkLedger explicitly pinned: the network-preferred
			// ledger may sit at a lower seq on a different chain than a
			// self-built run-ahead (Validations.h:892-895), and rippled's
			// switchLastClosedLedger takes that rewind.
			if e.prevLedger != nil && l.Seq() <= e.prevLedger.Seq() && id != e.wrongLedgerID {
				return nil
			}
			// Advance prevLedger to the FURTHEST locally-available closed
			// ledger that chains forward from l by parent hash, not one per
			// acquisition: under load a one-at-a-time catch-up stays
			// perpetually behind (#724). Only the build-on LCL moves (the
			// validated tip still moves solely via quorum); the ParentID chain
			// check prevents adopting a sibling fork, and an overshoot
			// self-corrects next round via checkLedger.
			for {
				next, nerr := e.adaptor.GetLedgerBySeq(l.Seq() + 1)
				if nerr != nil || next == nil || next.ParentID() != l.ID() {
					break
				}
				l = next
			}
			if !e.canSwitchToLedgerLocked(l) {
				return nil
			}
			lID := l.ID()
			slog.Info("Acquired missing ledger, restarting round",
				"seq", l.Seq(), "hash", fmt.Sprintf("%x", lID[:8]))
			e.prevLedger = l
			e.wrongLedgerID = consensus.LedgerID{}
			e.wrongLedgerAcquireFailures = 0
			if e.state != nil {
				e.state.HaveCorrectLCL = true
			}
			nextRound := consensus.RoundID{
				Seq:        l.Seq() + 1,
				ParentHash: l.ID(),
			}
			// recovering=true drops a would-be proposer to switchedLedger for
			// one round, suppressing emission; the next round promotes back
			// normally.
			proposing := e.adaptor.IsValidator() &&
				e.adaptor.GetOperatingMode() == consensus.OpModeFull
			e.startRoundLocked(nextRound, proposing, true)
		}
	}

	return nil
}

// parentValidations returns the trusted validations recorded for id, fed
// to GenerateFlagLedgerPseudoTxs for fee/amendment vote tallying. Callers
// pass prevLedger.ParentID(). Nil when the tracker isn't wired.
func (e *Engine) parentValidations(id consensus.LedgerID) []*consensus.Validation {
	if e.validationTracker == nil {
		return nil
	}
	return e.validationTracker.GetTrustedValidations(id)
}
