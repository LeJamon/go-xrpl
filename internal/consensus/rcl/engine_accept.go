package rcl

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
)

type ledgerAcceptWork struct {
	result           consensus.Result
	prevLedger       consensus.Ledger
	txSet            consensus.TxSet
	closeTime        time.Time
	closeTimeCorrect bool
	resolution       time.Duration
	disputedNoTxs    [][]byte
	roundTime        time.Duration
	roundDuration    time.Duration
}

// acceptLedger finalizes consensus and accepts the new ledger. Runs in
// every mode; only validation emission is mode-gated via isCompatible.
func (e *Engine) acceptLedger(result consensus.Result) {
	ownPostUnlockScope := e.deferPostUnlock == 0
	if ownPostUnlockScope {
		e.deferPostUnlock++
		defer func() {
			e.deferPostUnlock--
			pending := e.takePendingPostUnlockLocked()
			e.mu.Unlock()
			runPostUnlock(pending)
			e.mu.Lock()
		}()
	}

	if e.phase != consensus.PhaseEstablish {
		return
	}
	e.purgePendingTrustLocked()
	trusted := e.trustedPredicate()

	// Close-time consensus → determineCloseTime + effCloseTime; else a
	// deterministic parentClose+1s fallback (a local-clock fallback diverges
	// across nodes — #401).
	priorClose := e.prevLedger.CloseTime()
	resolution := e.currentCloseTimeResolution()
	var rawCloseTime, closeTime time.Time
	closeTimeCorrect := false
	var ctBranch string
	if e.closeTime.haveConsensus {
		// updateCloseTimePosition records the winner selected from the
		// current trusted positions. Use that snapshot through acceptance;
		// re-tallying CloseTimes.Peers here would resurrect stale or revised
		// initial votes (and is particularly harmful for observers).
		if e.closeTime.consensusCloseTimeSet {
			rawCloseTime = e.closeTime.consensusCloseTime
		} else {
			// Keep direct/manual acceptance callers compatible with the
			// historical helper path. Normal close-time-gated rounds always
			// set the snapshot before haveConsensus becomes true.
			rawCloseTime = e.determineCloseTime()
		}
		if rawCloseTime.IsZero() {
			closeTime = priorClose.Add(time.Second)
			ctBranch = "consensus-disagree"
		} else {
			closeTime = effCloseTime(rawCloseTime, resolution, priorClose)
			closeTimeCorrect = true
			ctBranch = "consensus"
		}
	} else {
		closeTime = priorClose.Add(time.Second)
		rawCloseTime = closeTime
		ctBranch = "fallback"
	}

	var ourPosCT int64
	var ourPosSeq uint32
	if e.state != nil && e.state.OurPosition != nil {
		ourPosCT = e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
		ourPosSeq = e.state.OurPosition.Position
	}
	slog.Info("close-time decision",
		"t", "consensus",
		"event", "accept-ct",
		"seq", e.prevLedger.Seq()+1,
		"mode", e.mode.String(),
		"have_ct_consensus", e.closeTime.haveConsensus,
		"ct_branch", ctBranch,
		"raw_ct_xrpl", rawCloseTime.Unix()-protocol.RippleEpochUnix,
		"eff_ct_xrpl", closeTime.Unix()-protocol.RippleEpochUnix,
		"prior_ct_xrpl", priorClose.Unix()-protocol.RippleEpochUnix,
		"our_pos_ct_xrpl", ourPosCT,
		"our_pos_seq", ourPosSeq,
		"self_ct_xrpl", e.state.CloseTimes.Self.Unix()-protocol.RippleEpochUnix,
		"resolution_s", int(resolution.Seconds()),
		"peer_ct_count", len(e.state.CloseTimes.Peers),
		"proposer_count", e.proposalTracker.CountTrusted(trusted),
	)

	var txSet consensus.TxSet
	if e.ourTxSet != nil {
		txSet = e.ourTxSet
	} else {
		// Find most popular among trusted
		txSetCounts := make(map[consensus.TxSetID]int)
		for nodeID, proposal := range e.proposalTracker.All() {
			if trusted(nodeID) {
				txSetCounts[proposal.TxSet]++
			}
		}

		bestID, _ := mostPopularTxSet(txSetCounts)

		var err error
		txSet, err = e.adaptor.GetTxSet(bestID)
		if err != nil {
			return
		}
	}

	// prevRoundTime must exclude LCL build time (rippled ConsensusParms.h:
	// "Does not include the time to build the LCL"); capture both durations
	// before the off-lock apply so the converge-percent divisor and abandon
	// clamp track convergence, not the apply.
	roundTime := e.now().Sub(e.roundStartTime)
	roundDuration := e.now().Sub(e.state.StartTime)
	// DisputedNoTxs returns detached blobs; work keeps that snapshot while
	// the ledger is built after e.mu is released.
	disputedNoTxs := e.disputeTracker.DisputedNoTxs()

	// Apply the LCL off e.mu, mirroring rippled onAccept→addJob(jtACCEPT)
	// ("no lock is held during this job"). Snapshot every build input and
	// freeze the round (PhaseAccepted + buildInProgress) under the lock first:
	// concurrent OnProposal/OnValidation/OnTxSet during the unlocked apply then
	// buffer for the NEXT round instead of mutating this one, and the consensus
	// goroutine parks its round-driving until the commit tail runs.
	work := ledgerAcceptWork{
		result:           result,
		prevLedger:       e.prevLedger,
		txSet:            txSet,
		closeTime:        closeTime,
		closeTimeCorrect: closeTimeCorrect,
		resolution:       resolution,
		disputedNoTxs:    disputedNoTxs,
		roundTime:        roundTime,
		roundDuration:    roundDuration,
	}
	e.buildInProgress = true
	e.setPhase(consensus.PhaseAccepted)
	if e.acceptDeferrer != nil {
		complete := sync.OnceFunc(func() {
			e.completeDeferredLedgerAccept(work)
		})
		if e.acceptDeferrer.DeferLedgerAccept(complete) {
			return
		}
	}

	e.mu.Unlock()
	newLedger, err := e.buildAcceptedLedger(work)
	e.mu.Lock()
	e.commitAcceptedLedgerLocked(work, newLedger, err)
}

func (e *Engine) completeDeferredLedgerAccept(work ledgerAcceptWork) {
	newLedger, err := e.buildAcceptedLedger(work)

	e.mu.Lock()
	e.deferPostUnlock++
	e.commitAcceptedLedgerLocked(work, newLedger, err)
	e.deferPostUnlock--
	pending := e.takePendingPostUnlockLocked()
	e.mu.Unlock()
	runPostUnlock(pending)
}

func (e *Engine) buildAcceptedLedger(work ledgerAcceptWork) (consensus.Ledger, error) {
	newLedger, err := e.adaptor.BuildLedger(
		work.prevLedger,
		work.txSet,
		work.closeTime,
		work.closeTimeCorrect,
		work.disputedNoTxs,
	)
	if err != nil {
		return nil, err
	}
	if err := e.adaptor.ValidateLedger(newLedger); err != nil {
		return nil, err
	}
	if err := e.adaptor.StoreLedger(newLedger); err != nil {
		return nil, err
	}
	return newLedger, nil
}

// commitAcceptedLedgerLocked completes acceptance after the off-lock ledger
// application. Caller must hold e.mu.
func (e *Engine) commitAcceptedLedgerLocked(work ledgerAcceptWork, newLedger consensus.Ledger, err error) {
	e.purgePendingTrustLocked()
	e.buildInProgress = false

	if err != nil {
		// Build/validate/store failed off-lock; unwind to Establish so the next
		// heartbeat retries (matches the pre-offload early-return).
		e.setPhase(consensus.PhaseEstablish)
		e.processPendingRecoveryLedgerLocked()
		return
	}
	result := work.result
	prevLedger := work.prevLedger
	txSet := work.txSet
	closeTime := work.closeTime
	closeTimeCorrect := work.closeTimeCorrect
	resolution := work.resolution
	roundTime := work.roundTime
	roundDuration := work.roundDuration
	parentID := prevLedger.ID()
	parentClose := prevLedger.CloseTime()
	newID := newLedger.ID()
	txSetID := txSet.ID()
	slog.Info("ledger built",
		"t", "consensus",
		"event", "ledger-built",
		"seq", newLedger.Seq(),
		"hash", fmt.Sprintf("%x", newID[:8]),
		"parent_seq", prevLedger.Seq(),
		"parent_hash", fmt.Sprintf("%x", parentID[:8]),
		"parent_ct_xrpl", parentClose.Unix()-protocol.RippleEpochUnix,
		"close_time_xrpl", closeTime.Unix()-protocol.RippleEpochUnix,
		"close_time_correct", closeTimeCorrect,
		"resolution_s", int(resolution.Seconds()),
		"tx_set", fmt.Sprintf("%x", txSetID[:8]),
		"tx_count", txSet.Size(),
		"result", result.String(),
		"mode", e.mode.String(),
	)

	// Censorship detection: reconcile the txs we've been proposing against the
	// accepted set now that the LCL is built. Only meaningful with the correct
	// LCL and full consensus (a timed-out round proves nothing about exclusion).
	if e.state.HaveCorrectLCL && result == consensus.ResultSuccess {
		accepted := txSet.TxIDs()
		curr := newLedger.Seq()
		e.censorship.check(accepted, func(id consensus.TxID, seq uint32) bool {
			// Reached only for txs we proposed that stayed out of the accepted
			// set. Keep tracking them (never drop) so the wait accumulates as
			// they get re-proposed from the pending pool each round; txs we
			// genuinely stop proposing fall out via propose(), not here. Warn
			// each time persistent exclusion crosses the interval.
			if curr > seq && (curr-seq)%censorshipWarnInterval == 0 {
				slog.Warn("potential censorship: eligible tx not yet included",
					"t", "consensus",
					"event", "censorship-warn",
					"tx", fmt.Sprintf("%x", id[:8]),
					"tracked_since_seq", seq,
					"current_seq", curr,
					"waited", curr-seq,
				)
			}
			return false
		})
	}

	trustedProposers := e.proposalTracker.CountTrusted(e.trustedPredicate())
	e.eventBus.Publish(&consensus.ConsensusReachedEvent{
		Round:     e.state.Round,
		TxSet:     txSet.ID(),
		CloseTime: closeTime,
		Proposers: trustedProposers,
		Result:    result,
		// StartTime is wall-clock (see startRoundLocked); paired with e.now()
		// and captured before the off-lock build, like prevRoundTime.
		Duration:  roundDuration,
		Timestamp: e.adaptor.Now(),
	})

	isolated := trustedProposers == 0 && !e.adaptor.IsStandalone()
	if isolated {
		if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
			e.adaptor.SetOperatingMode(consensus.OpModeConnected)
		}
		slog.Info("leaving Full consensus participation",
			"t", "consensus",
			"event", "consensus-recovery",
			"seq", newLedger.Seq(),
			"trusted_proposers", trustedProposers,
		)
	}

	// Emission gate (rippled RCLConsensus.cpp:591-594):
	// validating && !consensusFail && canValidateSeq.
	//   consensusFail = MovedOn ONLY — Expired (hard timeout) still emits, and
	//     peers form quorum on the timeout-built ledger. Lumping Timeout in
	//     with MovedOn silently bowed us out of every timed-out round (#451).
	//   canValidateSeq prevents a second validation for an already-validated
	//     seq (a divergent close + reacquire would flag us Conflicting, #401).
	// Mode is intentionally NOT gated: rippled emits regardless of mode; the
	// Full flag (from mode==ModeProposing) controls whether peers count it
	// toward quorum. Partials in non-proposing modes keep us visible as a
	// liveness signal without affecting quorum; suppressing emission in
	// wrongLedger (the old behaviour) caused permanent quorum stalls (#451).
	// ResultFail is a go-xrpl sentinel mapping to the MovedOn suppress class.
	consensusFail := result == consensus.ResultMovedOn || result == consensus.ResultFail
	// blocked kills emission entirely: an amendment-blocked node builds
	// un-amended ledgers, and even a partial from it would misdirect peers.
	blocked := e.adaptor.IsAmendmentBlocked()
	// The round-scoped eligibility snapshot is independent of sync state, so
	// observing validators can emit partial validations.
	isValidator := e.validating.Load()
	canValidate := e.peekCanValidateSeqLocked(newLedger.Seq())
	// isCompatible suppresses emission when the build is on a side chain (not
	// just ahead of validated on the same chain). Replaces the coarse
	// wrongLedger-mode gate that blocked the ahead-but-compatible case (#451)
	// while still preventing side-chain emits (#401).
	compatible := e.isBuildCompatibleWithValidatedLocked(newLedger)
	if isValidator && !compatible {
		e.validating.Store(false)
	}
	willEmit := isValidator && !blocked && !consensusFail && canValidate && compatible

	newLedgerID := newLedger.ID()
	hashShort := fmt.Sprintf("%x", newLedgerID[:8])
	slog.Info("validation gate",
		"t", "consensus",
		"event", "validate-gate",
		"seq", newLedger.Seq(),
		"hash", hashShort,
		"result", result.String(),
		"is_validator", isValidator,
		"amendment_blocked", blocked,
		"consensus_fail", consensusFail,
		"wrong_lcl", e.mode == consensus.ModeWrongLedger,
		"compatible", compatible,
		"can_validate_seq", canValidate,
		"our_last_validated_seq", e.ourLastValidatedSeq,
		"max_disallowed_seq", e.adaptor.GetMaxDisallowedLedgerSeq(),
		"mode", e.mode.String(),
		"decision", emitDecision(willEmit, isValidator, blocked, consensusFail, canValidate, compatible),
	)

	if willEmit {
		e.sendValidation(newLedger)
	}

	validations := e.proposalTracker.ValidationsFor(newLedger.ID())

	e.buildingLedgerSeq.Store(0)
	e.adaptor.OnConsensusReached(newLedger, validations, roundTime)

	e.eventBus.Publish(&consensus.LedgerAcceptedEvent{
		LedgerID:    newLedger.ID(),
		LedgerSeq:   newLedger.Seq(),
		TxCount:     txSet.Size(),
		CloseTime:   closeTime,
		Validations: len(validations),
		Timestamp:   e.adaptor.Now(),
	})

	// Adjust our clock toward the network's close-time median unless consensus
	// moved on without us.
	if !consensusFail && (e.mode == consensus.ModeProposing || e.mode == consensus.ModeObserving) {
		e.adaptor.AdjustCloseTime(e.state.CloseTimes)
	}

	// Refresh the tracker's trusted set, quorum, and negative UNL each accept,
	// and advance the minSeq floor so far-stale validations are rejected at Add().
	if e.validationTracker != nil {
		quorum := e.refreshValidationConfigDeferredLocked()
		if newLedger.Seq() > 128 {
			// Keep a small history window so late validations for the
			// just-accepted ledger still count.
			e.validationTracker.SetMinSeq(newLedger.Seq() - 128)
		}
		// rippled feeds getCurrentNodeIDs() into updateTrusted, but its quorum
		// ignores the set — surface it for partial-outage visibility, not quorum.
		slog.Debug("live validator participation",
			"current", len(e.validationTracker.CurrentNodeIDs()),
			"quorum", quorum,
			"ledger_seq", newLedger.Seq())
	}

	// Track round time for convergePercent calculation
	e.prevRoundTime = roundTime

	// Track trusted proposer count for peer pressure in next round
	e.prevProposers = trustedProposers
	// Publish to the lock-free mirror for GetLastCloseInfo.
	e.storeLastCloseLocked()

	// Update state for next round
	e.prevLedger = newLedger
	e.acceptedLCL = consensus.LedgerID{}
	e.proposalTracker.ResetValidations()
	e.consensusCount++
	if e.processPendingRecoveryLedgerLocked() {
		return
	}

	// Phase is already PhaseAccepted (set before the off-lock apply).

	// Auto-advance only in Full mode; otherwise the router re-adopts until
	// caught up and checkAndStartRound takes over.
	if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
		// Preferred-LCL jump: retarget prev to a different preferred LCL we
		// hold locally to skip a handleWrongLedger detour; acquire via
		// handleWrongLedger when not cached.
		nextPrev := newLedger
		if e.validationTracker != nil {
			localID := newLedger.ID()
			candidateID, ok := e.validationPreferredForLedgerLocked(newLedger)
			if ok && candidateID != localID {
				if cached, err := e.adaptor.GetLedger(candidateID); err == nil && cached != nil {
					localBytes := localID
					slog.Info("preferred LCL differs; jumping prev to cached ledger",
						"t", "consensus",
						"event", "preferred-lcl-jump-cached",
						"local_seq", newLedger.Seq(),
						"local_hash", fmt.Sprintf("%x", localBytes[:8]),
						"preferred_seq", cached.Seq(),
						"preferred_hash", fmt.Sprintf("%x", candidateID[:8]),
					)
					nextPrev = cached
					e.prevLedger = cached
					e.acceptedLCL = newLedger.ID()
				} else {
					localBytes := localID
					slog.Info("preferred LCL differs; routing through handleWrongLedger (acquire)",
						"t", "consensus",
						"event", "preferred-lcl-jump-acquire",
						"local_seq", newLedger.Seq(),
						"local_hash", fmt.Sprintf("%x", localBytes[:8]),
						"preferred_hash", fmt.Sprintf("%x", candidateID[:8]),
					)
					e.handleWrongLedger(candidateID, nil)
					return
				}
			}
		}

		// Auto-advance.
		proposing := e.adaptor.IsValidator()
		nextRound := consensus.RoundID{
			Seq:        nextPrev.Seq() + 1,
			ParentHash: nextPrev.ID(),
		}
		e.startRoundLocked(nextRound, proposing, false)
	}
}

// updateCloseTimePosition tallies close-time votes, applies avalanche
// thresholds, and bumps our proposal's close time to consensus.
func (e *Engine) updateCloseTimePosition() {
	e.purgePendingTrustLocked()
	trusted := e.trustedPredicate()
	resolution := e.currentCloseTimeResolution()

	// Tally close-time votes from trusted proposals, rounded via roundCloseTime.
	closeTimeVotes := make(map[time.Time]int)
	participants := 0
	for nodeID, proposal := range e.proposalTracker.All() {
		if trusted(nodeID) {
			rounded := roundCloseTime(proposal.CloseTime, resolution)
			closeTimeVotes[rounded]++
			participants++
		}
	}

	if participants == 0 {
		e.closeTime.haveConsensus = true // trivially
		if e.state.OurPosition != nil {
			e.closeTime.consensusCloseTime = roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		} else {
			e.closeTime.consensusCloseTime = roundCloseTime(e.state.CloseTimes.Self, resolution)
		}
		e.closeTime.consensusCloseTimeSet = true
		return
	}

	// Add our own vote if proposing
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		ourRounded := roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		closeTimeVotes[ourRounded]++
		participants++
	}

	neededWeight := e.closeTime.neededWeight(e.convergePercent(), e.parms)
	threshVote := participantsNeeded(participants, neededWeight)
	threshConsensus := participantsNeeded(participants, 75) // avCT_CONSENSUS_PCT

	consensusCloseTime, winningVotes, haveWinner := mostVotedAscending(closeTimeVotes, threshVote)
	e.closeTime.consensusCloseTime = consensusCloseTime
	e.closeTime.consensusCloseTimeSet = haveWinner
	e.closeTime.haveConsensus = haveWinner && winningVotes >= threshConsensus

	votesSummary := summarizeCloseTimeVotes(closeTimeVotes)
	var consensusCT int64
	if !consensusCloseTime.IsZero() {
		consensusCT = consensusCloseTime.Unix() - protocol.RippleEpochUnix
	}
	var ourPosCT int64
	var ourPosSeq uint32
	if e.state.OurPosition != nil {
		ourPosCT = e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
		ourPosSeq = e.state.OurPosition.Position
	}
	slog.Debug("close-time avalanche",
		"t", "consensus",
		"event", "ct-avalanche",
		"seq", e.state.Round.Seq,
		"mode", e.mode.String(),
		"converge_pct", e.convergePercent(),
		"avalanche_state", e.closeTime.stateName(),
		"needed_weight", neededWeight,
		"thresh_vote", threshVote,
		"thresh_consensus", threshConsensus,
		"participants", participants,
		"have_consensus", e.closeTime.haveConsensus,
		"consensus_ct_xrpl", consensusCT,
		"our_pos_ct_xrpl", ourPosCT,
		"our_pos_seq", ourPosSeq,
		"votes", votesSummary,
	)

	// Update our proposal if close time changed
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		ourRounded := roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		if consensusCloseTime != ourRounded {
			oldCT := e.state.OurPosition.CloseTime.Unix() - protocol.RippleEpochUnix
			e.state.OurPosition.CloseTime = consensusCloseTime
			e.state.OurPosition.Position++
			e.state.OurPosition.Timestamp = e.adaptor.Now()
			if err := e.adaptor.SignProposal(e.state.OurPosition); err == nil {
				e.enqueueProposalBroadcastLocked(e.state.OurPosition)
			}
			slog.Info("our close-time bumped",
				"t", "consensus",
				"event", "ct-bump",
				"seq", e.state.Round.Seq,
				"old_ct_xrpl", oldCT,
				"new_ct_xrpl", consensusCT,
				"new_pos_seq", e.state.OurPosition.Position,
			)
		}
	}
}

// convergePercent returns establish-phase progress as a percentage of the
// previous round time (min 5s).
func (e *Engine) convergePercent() int {
	elapsed := e.now().Sub(e.roundStartTime)
	prevRound := max(e.prevRoundTime, avMinConsensusTime)
	return int(elapsed * 100 / prevRound)
}

func (e *Engine) determineCloseTime() time.Time {
	if e.closeTime != nil && e.closeTime.consensusCloseTimeSet {
		return e.closeTime.consensusCloseTime
	}

	// Our position is already rounded by updateCloseTimePosition.
	if e.state.OurPosition != nil {
		return e.state.OurPosition.CloseTime
	}

	resolution := e.currentCloseTimeResolution()

	// Observers: CloseTimes.Peers holds raw times; round before voting to
	// match rippled's asCloseTime.
	if len(e.state.CloseTimes.Peers) > 0 {
		roundedVotes := make(map[time.Time]int)
		for t, count := range e.state.CloseTimes.Peers {
			rounded := roundCloseTime(t, resolution)
			roundedVotes[rounded] += count
		}

		// Largest time on a tie, matching the proposing path — a different
		// pick would fork.
		bestTime, bestCount, _ := mostVotedAscending(roundedVotes, 0)
		if bestCount > 0 {
			return bestTime
		}
	}

	return roundCloseTime(e.state.CloseTimes.Self, resolution)
}

// peekCanValidateSeqLocked is the non-mutating SeqEnforcer predicate.
// The restart floor never idle-expires: the pre-restart persisted tip stays
// disallowed for the process lifetime. Caller holds e.mu read.
func (e *Engine) peekCanValidateSeqLocked(seq uint32) bool {
	floor := e.ourLastValidatedSeq
	if !e.ourLastValidatedTime.IsZero() &&
		e.adaptor.Now().Sub(e.ourLastValidatedTime) > validationSetExpires {
		floor = 0
	}
	if d := e.adaptor.GetMaxDisallowedLedgerSeq(); floor < d {
		floor = d
	}
	return seq > floor
}

// tryAdvanceValidatedSeqLocked is the mutating SeqEnforcer: idle-reset
// then reject-or-bump. The floor commits before signing so a sign failure
// still consumes the seq. Caller holds e.mu write.
func (e *Engine) tryAdvanceValidatedSeqLocked(seq uint32) bool {
	now := e.adaptor.Now()
	if !e.ourLastValidatedTime.IsZero() &&
		now.Sub(e.ourLastValidatedTime) > validationSetExpires {
		e.ourLastValidatedSeq = 0
	}
	if seq <= e.ourLastValidatedSeq || seq <= e.adaptor.GetMaxDisallowedLedgerSeq() {
		return false
	}
	e.ourLastValidatedSeq = seq
	e.ourLastValidatedTime = now
	return true
}

// sendValidation builds and broadcasts a validation. Tracker callbacks execute
// after e.mu is released, either through the caller's deferred scope or by a
// direct unlock around tracking. The Full flag (set from mode==ModeProposing)
// is what makes peers count it toward quorum; partials don't count.
func (e *Engine) sendValidation(ledger consensus.Ledger) {
	// SeqEnforcer guard + bump; defensive so direct test callers can't bypass.
	if !e.tryAdvanceValidatedSeqLocked(ledger.Seq()) {
		return
	}

	nodeID, err := e.adaptor.GetValidatorKey()
	if err != nil {
		return
	}

	full := e.mode == consensus.ModeProposing

	// SignTime is a UINT32 count of XRPL epoch seconds on the wire. Normalize
	// before signing and tracking so a validation relayed back by a peer is
	// identical to the local copy; retaining adaptor-clock nanoseconds locally
	// would make the tracker misclassify that echo as a conflicting validation.
	// Keep the normalized time under a monotonic floor: a regressing adaptor
	// clock is bumped to lastSignTime+1s so peers never see stale SignTimes.
	signTime := time.Unix(e.adaptor.Now().Unix(), 0).UTC()
	if !e.lastSignTime.IsZero() && !signTime.After(e.lastSignTime) {
		signTime = e.lastSignTime.Add(1 * time.Second)
	}
	e.lastSignTime = signTime

	validation := &consensus.Validation{
		LedgerID:  ledger.ID(),
		LedgerSeq: ledger.Seq(),
		NodeID:    nodeID,
		SignTime:  signTime,
		SeenTime:  signTime,
		Full:      full,
		// load_fee (sfLoadFee); zero = no load info, serializer omits it.
		LoadFee: e.adaptor.GetLoadFee(),
	}

	// Cookie / ServerVersion are HardenedValidations-only: pre-HV peers reject
	// validations carrying them (their sig preimage omits the fields). Cookie
	// on every HV validation; ServerVersion only on voting ledgers.
	if e.adaptor.IsFeatureEnabled("HardenedValidations") {
		cookie := e.adaptor.GetCookie()
		if cookie == 0 {
			slog.Warn("sendValidation: cookie is zero under HardenedValidations — adaptor must generate one at boot; emitting without cookie")
		}
		validation.Cookie = cookie

		if protocol.IsVotingLedger(ledger.Seq()) {
			serverVersion := e.adaptor.GetServerVersion()
			if serverVersion == 0 {
				slog.Warn("sendValidation: serverVersion is zero on voting ledger under HardenedValidations — adaptor must advertise a build tag; emitting without serverVersion")
			}
			validation.ServerVersion = serverVersion
		}
	}

	// Fee + amendment votes only on voting (flag) ledgers; emitting every
	// ledger inflates bandwidth ~256× and confuses peer aggregators.
	if protocol.IsVotingLedger(ledger.Seq()) {
		if fv := e.adaptor.GetFeeVote(ledger); fv.HasBaseFee() || fv.HasReserveBase() || fv.HasReserveIncrement() {
			if fv.PostXRPFees {
				if fv.HasBaseFee() {
					validation.SetBaseFeeDrops(drops.XRPAmount(fv.BaseFee))
				}
				if fv.HasReserveBase() {
					validation.SetReserveBaseDrops(drops.XRPAmount(fv.ReserveBase))
				}
				if fv.HasReserveIncrement() {
					validation.SetReserveIncrementDrops(drops.XRPAmount(fv.ReserveIncrement))
				}
			} else {
				if fv.HasBaseFee() {
					validation.SetBaseFee(fv.BaseFee)
				}
				if fv.HasReserveBase() {
					validation.SetReserveBase(uint32(fv.ReserveBase))
				}
				if fv.HasReserveIncrement() {
					validation.SetReserveIncrement(uint32(fv.ReserveIncrement))
				}
			}
		}

		// Amendment vote (flag ledgers only); nil when there's no vote to cast.
		validation.Amendments = e.adaptor.GetAmendmentVote()
	}

	// Tie to the converged tx-set so peers tie-break concurrent same-seq
	// ledgers; only set when we produced a proposal (observers omit it).
	if e.ourTxSet != nil {
		setID := e.ourTxSet.ID()
		copy(validation.ConsensusHash[:], setID[:])
	}

	// ValidatedHash is HardenedValidations-only (pre-HV peers reject it as
	// malformed). Skip when we haven't crossed quorum (zero hash).
	if e.adaptor.IsFeatureEnabled("HardenedValidations") {
		if vh := e.adaptor.GetValidatedLedgerHash(); vh != (consensus.LedgerID{}) {
			copy(validation.ValidatedHash[:], vh[:])
		}
	}

	if err := e.adaptor.SignValidation(validation); err != nil {
		slog.Warn("validation sign failed",
			"t", "consensus",
			"event", "validate-sign-fail",
			"seq", ledger.Seq(),
			"error", err,
		)
		return
	}

	ledgerID := ledger.ID()
	slog.Info("validation emitted",
		"t", "consensus",
		"event", "validate-emit",
		"seq", ledger.Seq(),
		"hash", fmt.Sprintf("%x", ledgerID[:8]),
		"full", full,
		"sign_time_xrpl", signTime.Unix()-protocol.RippleEpochUnix,
	)

	// Feed our own validation into the tracker. Partials steer our trie but
	// don't count toward quorum (Full filter); a 1-validator standalone is
	// always proposing, so Full crosses immediately.
	if e.validationTracker != nil {
		tracker := e.validationTracker
		tracker.beginFinalityDeferral()
		tracker.addStatus(validation, false)
		if e.deferPostUnlock == 0 {
			e.mu.Unlock()
			tracker.endFinalityDeferral()
			e.mu.Lock()
		} else {
			e.pendingPostUnlock = append(e.pendingPostUnlock, func() {
				tracker.endFinalityDeferral()
			})
		}
	}

	e.enqueueValidationBroadcastLocked(validation)
}

// roundCloseTime rounds to the nearest multiple of resolution (up at the
// midpoint). Truncates sub-second precision first so nanosecond-skewed
// validators round identically; does the modulo in XRPL-epoch space to
// match rippled byte-for-byte.
func roundCloseTime(closeTime time.Time, resolution time.Duration) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	resSec := int64(resolution.Seconds())
	if resSec <= 0 {
		return closeTime
	}
	xrplSec := closeTime.Unix() - protocol.RippleEpochUnix
	xrplSec += resSec / 2
	xrplSec -= xrplSec % resSec
	return time.Unix(xrplSec+protocol.RippleEpochUnix, 0).UTC()
}

// emitDecision labels which arm of the validation gate fired. wrongLedger
// is intentionally NOT a skip reason (rippled emits a partial there, #451).
func emitDecision(emit, isValidator, blocked, consensusFail, canValidate, compatible bool) string {
	if emit {
		return "emit"
	}
	if !isValidator {
		return "skip:not-validator"
	}
	if blocked {
		return "skip:amendment-blocked"
	}
	if consensusFail {
		return "skip:consensus-fail"
	}
	if !canValidate {
		return "skip:already-validated-seq"
	}
	if !compatible {
		return "skip:incompatible-with-validated"
	}
	return "skip:unknown"
}

// effCloseTime rounds to resolution, then floors at priorCloseTime + 1s.
func effCloseTime(closeTime time.Time, resolution time.Duration, priorCloseTime time.Time) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	rounded := roundCloseTime(closeTime, resolution)
	minTime := priorCloseTime.Add(time.Second)
	if rounded.Before(minTime) {
		return minTime
	}
	return rounded
}
