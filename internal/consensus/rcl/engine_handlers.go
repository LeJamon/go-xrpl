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
	e.purgePendingTrustLocked()

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
	// Trust callbacks do not take e.mu, so one may have queued a purge while
	// the trust gate was being evaluated. Apply it again immediately before
	// admitting this proposal, then re-check trust after the destructive purge.
	e.purgePendingTrustLocked()
	if !e.adaptor.IsTrusted(proposal.NodeID) {
		return nil
	}

	// Buffer for future playback, even between rounds.
	e.proposalTracker.bufferRecent(proposal)

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
	if e.proposalTracker.isDead(proposal.NodeID) {
		return nil
	}

	// Bow-out: a validator's final position sets ProposeSeq to seqLeave.
	// Erase its position, mark it dead, and un-vote it from every dispute —
	// otherwise the seqLeave position keeps voting forever.
	const seqLeave = uint32(0xFFFFFFFF)
	if proposal.Position == seqLeave {
		e.proposalTracker.markDead(proposal.NodeID)
		// Drop its dispute votes so they stop counting toward convergence.
		if e.disputeTracker != nil {
			e.disputeTracker.unVote(proposal.NodeID)
		}
		e.adaptor.RelayProposal(proposal, originPeer)
		return nil
	}

	// Drop non-increasing positions before counting close-time votes,
	// relaying, or updating disputes — otherwise a re-sent or equivocating
	// proposal at an already-seen ProposeSeq votes again.
	if !e.proposalTracker.store(proposal) {
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
	if e.ourTxSet != nil {
		if peerSet, ok := e.acquiredTxSets[proposal.TxSet]; ok {
			if !e.adaptor.IsTrusted(proposal.NodeID) {
				return nil
			}
			e.createDisputesAgainst(peerSet)
			if e.disputeTracker.updateDisputes(proposal.NodeID, peerSet) {
				e.peerUnchangedCounter = 0
			}
		}
	}

	return nil
}

// OnValidation is the synchronous compatibility path used by direct engine
// callers. The network router verifies validations on its worker queues and
// calls ProcessVerifiedValidation instead.
func (e *Engine) OnValidation(validation *consensus.Validation, originPeer uint64) error {
	if err := e.adaptor.VerifyValidation(validation); err != nil {
		return fmt.Errorf("invalid validation signature: %w", err)
	}

	disposition, err := e.ProcessVerifiedValidation(validation, consensus.ValidationOrigin{PeerID: originPeer})
	if err != nil {
		return err
	}
	if disposition.Relay {
		_ = e.adaptor.RelayValidation(validation, originPeer)
	}
	if disposition.Status == consensus.ValidationMultiple ||
		disposition.Status == consensus.ValidationConflicting {
		return &consensus.ByzantineValidationError{
			NodeID:  validation.NodeID,
			Reason:  disposition.Status.String(),
			Trusted: disposition.Trusted,
		}
	}
	return nil
}

// ProcessVerifiedValidation applies a signature-verified validation to local
// consensus state. It deliberately performs no network I/O; the router acts on
// the returned disposition after this method releases the engine lock.
func (e *Engine) ProcessVerifiedValidation(
	validation *consensus.Validation,
	origin consensus.ValidationOrigin,
) (consensus.ValidationDisposition, error) {
	e.mu.Lock()

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

	disposition := consensus.ValidationDisposition{
		Status:  consensus.ValidationUntracked,
		Tracked: tracked,
		Trusted: trusted,
		Relay: trusted ||
			origin.Cluster ||
			(e.relayPolicy != nil && e.relayPolicy.RelayUntrustedValidations()),
	}

	tracker := e.validationTracker
	deferFinality := tracked && tracker != nil
	if deferFinality {
		tracker.beginFinalityDeferral()
		disposition.Status = validationDispositionStatus(tracker.addStatusWithFinality(validation, false))
	}

	if disposition.AcquireEligible() {
		e.proposalTracker.setValidation(validation)
	}

	event := &consensus.ValidationReceivedEvent{
		Validation: validation,
		Trusted:    trusted,
		Timestamp:  e.adaptor.Now(),
	}

	e.mu.Unlock()
	if deferFinality {
		tracker.endFinalityDeferral()
	}
	e.eventBus.Publish(event)
	return disposition, nil
}

func validationDispositionStatus(status valStatus) consensus.ValidationStatus {
	switch status {
	case valStatusCurrent:
		return consensus.ValidationCurrent
	case valStatusStale:
		return consensus.ValidationStale
	case valStatusBadSeq:
		return consensus.ValidationBadSeq
	case valStatusMultiple:
		return consensus.ValidationMultiple
	case valStatusConflicting:
		return consensus.ValidationConflicting
	default:
		return consensus.ValidationUntracked
	}
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
	if e.phase == consensus.PhaseAccepted {
		return nil
	}
	// Building a set can overlap a trust callback. Apply removals before the
	// set can seed any dispute state, then use one trust epoch for the peer
	// back-fill below.
	e.purgePendingTrustLocked()
	trusted := e.trustedPredicate()

	// Cache for dispute wiring. A late tx set retroactively populates any
	// dispute whose tx it contains for some peer.
	if _, already := e.acquiredTxSets[id]; !already {
		e.acquiredTxSets[id] = txSet
		if e.ourTxSet != nil && id != e.ourTxSet.ID() {
			e.createDisputesAgainst(txSet)
			for nodeID, p := range e.proposalTracker.all() {
				if !trusted(nodeID) {
					continue
				}
				if p.TxSet == id {
					if e.disputeTracker.updateDisputes(nodeID, txSet) {
						e.peerUnchangedCounter = 0
					}
				}
			}
		}
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
		if e.disputeTracker.has(txID) {
			continue
		}
		var blob []byte
		if idx < len(ourBlobs) {
			blob = ourBlobs[idx]
		}
		dispute := e.disputeTracker.createDispute(txID, blob, true)
		e.seedDisputeVotes(dispute.TxID)
	}

	// txs only in peer's set: seed ourVote=false.
	peerBlobs := peerTxSet.Txs()
	for idx, txID := range peerIDs {
		if _, also := ours[txID]; also {
			continue
		}
		if e.disputeTracker.has(txID) {
			continue
		}
		var blob []byte
		if idx < len(peerBlobs) {
			blob = peerBlobs[idx]
		}
		dispute := e.disputeTracker.createDispute(txID, blob, false)
		e.seedDisputeVotes(dispute.TxID)
	}
}

// seedDisputeVotes records each known peer's vote on a new dispute from
// its acquired tx set. Caller must hold e.mu.
func (e *Engine) seedDisputeVotes(txID consensus.TxID) {
	trusted := e.trustedPredicate()
	for nodeID, p := range e.proposalTracker.all() {
		if !trusted(nodeID) {
			continue
		}
		peerSet, ok := e.acquiredTxSets[p.TxSet]
		if !ok {
			continue
		}
		if e.disputeTracker.setVote(txID, nodeID, peerSet.Contains(txID)) {
			e.peerUnchangedCounter = 0
		}
	}
}

// TrySwitchToLedger synchronously evaluates and adopts a locally-held ledger
// selected by consensus recovery, validation, or the current network view.
func (e *Engine) TrySwitchToLedger(id consensus.LedgerID) (consensus.LedgerSwitchResult, error) {
	e.mu.Lock()
	e.deferPostUnlock++
	defer func() {
		e.deferPostUnlock--
		pending := e.takePendingPostUnlockLocked()
		e.mu.Unlock()
		runPostUnlock(pending)
	}()

	exactRecoveryTarget := e.mode == consensus.ModeWrongLedger && id == e.wrongLedgerID
	networkPreferred := e.prevLedger != nil && e.getNetworkLedger() == id

	l, err := e.adaptor.GetLedger(id)
	if err != nil {
		return consensus.LedgerSwitchIrrelevant, err
	}
	if l == nil {
		return consensus.LedgerSwitchIrrelevant, nil
	}
	validatedCandidate := e.adaptor.GetValidatedLedgerHash() == id ||
		e.isQuorumValidatedCandidateLocked(l)
	if !exactRecoveryTarget && !validatedCandidate && !networkPreferred {
		return consensus.LedgerSwitchIrrelevant, nil
	}
	if e.buildInProgress {
		return consensus.LedgerSwitchBusy, nil
	}
	if !e.switchToLedgerLocked(id, l) {
		return consensus.LedgerSwitchRejected, nil
	}
	return consensus.LedgerSwitchAccepted, nil
}

func (e *Engine) CanAcceptLedger(id consensus.LedgerID) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	l, err := e.adaptor.GetLedger(id)
	if err != nil || l == nil {
		return false, err
	}
	return e.canBeCurrentLocked(l), nil
}

// isQuorumValidatedCandidateLocked rechecks the live trusted-validation set
// without advancing the adaptor's accepted validated tip. The selected ledger
// still passes canSwitchToLedgerLocked before it can become current.
func (e *Engine) isQuorumValidatedCandidateLocked(l consensus.Ledger) bool {
	if l == nil || e.validationTracker == nil || e.adaptor.IsQuorumUnavailable() {
		return false
	}
	_, _, accepted := e.validationTracker.RecheckFullyValidated(l.ID(), l.Seq())
	return accepted
}

func (e *Engine) switchToLedgerLocked(id consensus.LedgerID, l consensus.Ledger) bool {
	// A validated-ledger completion can race with the normal consensus commit:
	// by the time the router hands the locally-held ledger back to us, it may
	// already be our current LCL. Treat that handoff as successfully satisfied.
	// Restarting it as a recovery round would unnecessarily enter
	// switchedLedger and turn the next validation into a partial validation.
	// Keep the WrongLedger case active: even an equal hash must restart that
	// pinned recovery state so consensus can leave WrongLedger safely.
	if e.mode != consensus.ModeWrongLedger && e.prevLedger != nil && e.prevLedger.ID() == l.ID() {
		return true
	}

	if !e.canSwitchToLedgerLocked(l) {
		if e.lastRefusedSwitch != id {
			e.lastRefusedSwitch = id
			validatedID := e.adaptor.GetValidatedLedgerHash()
			slog.Info("Refusing acquired recovery ledger",
				"t", "consensus",
				"event", "switch-refused",
				"seq", l.Seq(),
				"hash", fmt.Sprintf("%x", id[:8]),
				"validated_hash", fmt.Sprintf("%x", validatedID[:8]),
				"close_time", l.CloseTime(),
			)
		}
		return false
	}

	lID := l.ID()
	slog.Info("Acquired missing ledger, restarting round",
		"seq", l.Seq(), "hash", fmt.Sprintf("%x", lID[:8]))
	previousLedger := e.prevLedger
	previousWrongLedgerID := e.wrongLedgerID
	e.prevLedger = l
	nextRound := consensus.RoundID{Seq: l.Seq() + 1, ParentHash: l.ID()}
	proposing := e.adaptor.IsValidator() &&
		e.adaptor.GetOperatingMode() == consensus.OpModeFull
	if err := e.startRoundLocked(nextRound, proposing, true); err != nil {
		e.prevLedger = previousLedger
		e.wrongLedgerID = previousWrongLedgerID
		slog.Error("Failed to switch to acquired ledger",
			"seq", l.Seq(),
			"hash", fmt.Sprintf("%x", lID[:8]),
			"err", err,
		)
		return false
	}
	e.wrongLedgerID = consensus.LedgerID{}
	if e.state != nil {
		e.state.HaveCorrectLCL = true
	}
	return true
}

// parentValidations returns the trusted full validations recorded for the
// exact parent hash/sequence, fed to GenerateFlagLedgerPseudoTxs for
// fee/amendment vote tallying. Nil when the tracker isn't wired.
func (e *Engine) parentValidations(id consensus.LedgerID, seq uint32) []*consensus.Validation {
	if e.validationTracker == nil {
		return nil
	}
	return e.validationTracker.GetTrustedFullValidations(id, seq)
}
