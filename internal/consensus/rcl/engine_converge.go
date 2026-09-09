package rcl

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// phaseEstablish re-evaluates convergence each heartbeat. Caller must hold e.mu.
// A nil stage sink disables nested timer diagnostics.
func (e *Engine) phaseEstablish(stages *[]slowHeartbeatStage) {
	roundTime := e.now().Sub(e.roundStartTime)

	// Snapshot round time and converge percent each tick (before pause/accept)
	// so consensus_info reports meaningful values between rounds.
	e.currentRoundTime = roundTime
	e.lastConvergePercent = e.convergePercent()

	e.establishCounter++
	e.peerUnchangedCounter++

	// Give everyone a chance to take an initial position: rippled
	// phaseEstablish returns before updateOurPositions until roundTime
	// reaches ledgerMIN_CONSENSUS (Consensus.h:1393-1400). Updating our
	// position or tallying close-time votes earlier diverges from the peer
	// majority's timing. Counters above still advance each tick.
	if roundTime < e.timing.LedgerMinConsensus {
		return
	}

	// Prune stale peer proposals every tick regardless of our own mode
	// (rippled updateOurPositions prunes unconditionally, Consensus.h:
	// 1509-1528): a bowed-out observer waiting at the close-time gate must
	// not have its tally diluted forever by a silent peer's stale vote.
	e.recordHeartbeatStageLocked(stages, "proposal-pruning", func() {
		e.pruneUntrustedProposalsLocked()
		e.pruneStaleProposalsLocked()
	})

	e.recordHeartbeatStageLocked(stages, "dispute-position-update", func() {
		e.updatePosition()
	})
	e.recordHeartbeatStageLocked(stages, "close-time-consensus", func() {
		e.updateCloseTimePosition()
	})

	// Keep positions fresh while a quorum-blocking share of validators
	// lags. The pause gates acceptance, not proposal maintenance.
	paused := false
	e.recordHeartbeatStageLocked(stages, "pause-validation", func() {
		paused = e.shouldPause(roundTime)
	})
	if paused {
		return
	}

	e.recordHeartbeatStageLocked(stages, "convergence", func() {
		e.checkConvergence(stages)
	})
}

// pruneUntrustedProposalsLocked removes current positions that lost trust
// since they were received or replayed and revokes their dispute votes.
// Caller must hold e.mu.
func (e *Engine) pruneUntrustedProposalsLocked() {
	e.purgePendingTrustLocked()
	trusted := e.trustedPredicate()
	for _, nodeID := range e.proposalTracker.pruneUntrusted(trusted) {
		if e.disputeTracker != nil {
			e.disputeTracker.unVote(nodeID)
		}
	}
	e.purgePendingTrustLocked()
}

// unvoteDeadProposalsLocked applies replayed bow-outs to the dispute tracker.
// ProposalTracker owns the dead/current maps; the engine owns dispute votes.
// Caller must hold e.mu.
func (e *Engine) unvoteDeadProposalsLocked() {
	if e.disputeTracker == nil {
		return
	}
	for _, nodeID := range e.proposalTracker.deadNodeIDs() {
		e.disputeTracker.unVote(nodeID)
	}
}

// pruneStaleProposalsLocked drops peer proposals older than the freshness
// window and revokes their dispute votes. Caller must hold e.mu.
func (e *Engine) pruneStaleProposalsLocked() {
	cutoff := e.adaptor.Now().Add(-e.timing.ProposeFreshness)
	for _, nodeID := range e.proposalTracker.pruneStale(cutoff) {
		if e.disputeTracker != nil {
			e.disputeTracker.unVote(nodeID)
		}
	}
}

// shouldPause returns true when the establish phase should suspend for one
// heartbeat: our prev LCL has run past the fully-validated tip and a
// quorum-blocking share of trusted validators is lagging or offline. A
// paused round skips acceptLedger, so the local closed_ledger doesn't
// drift further past validated (#451). Clears once the round exceeds
// LedgerMaxConsensus or peers catch up. Caller must hold e.mu.
func (e *Engine) shouldPause(roundTime time.Duration) bool {
	if e.prevLedger == nil {
		return false
	}
	// Early-out: not a validator, no validation history, nothing ahead, or
	// past the hard timeout. Skipping with no prior validation lets bootstrap
	// rounds run — pause guards ongoing drift, not startup.
	if !e.adaptor.IsValidator() {
		return false
	}
	if e.ourLastValidatedSeq == 0 {
		return false
	}
	if e.timing.LedgerMaxConsensus > 0 && roundTime > e.timing.LedgerMaxConsensus {
		return false
	}

	prevSeq := e.prevLedger.Seq()
	validatedSeq := e.validatedSeqLocked()
	if validatedSeq >= prevSeq {
		return false
	}
	ahead := prevSeq - validatedSeq
	if ahead == 0 {
		return false
	}

	trusted, quorum := e.adaptor.GetTrustedValidatorsAndQuorum()
	totalValidators := len(trusted)
	if totalValidators == 0 {
		return false
	}
	if quorum == 0 {
		return false
	}

	laggards, offline := e.countLaggardsAndOfflineLocked(prevSeq, trusted)
	if laggards == 0 {
		return false
	}

	// Phase-progressive threshold: each ledger we're ahead cycles through 5
	// phases of increasing strictness — phase 0 pauses on a single laggard,
	// maxPausePhase pauses unconditionally.
	const maxPausePhase = 4
	phase := int(ahead-1) % (maxPausePhase + 1)

	switch phase {
	case 0:
		// Pause when laggards+offline exceed quorum slack.
		if laggards+offline > totalValidators-quorum {
			return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
		}
	case maxPausePhase:
		// No tolerance — strictest phase.
		return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
	default:
		// Intermediate: require the non-laggard ratio to clear quorum + a
		// linear share of slack.
		nonLaggards := float64(totalValidators - laggards - offline)
		quorumRatio := float64(quorum) / float64(totalValidators)
		allowedDissent := 1.0 - quorumRatio
		phaseFactor := float64(phase) / float64(maxPausePhase)
		if nonLaggards/float64(totalValidators) < quorumRatio+(allowedDissent*phaseFactor) {
			return logPauseLocked(e, ahead, laggards, offline, totalValidators, quorum, phase)
		}
	}
	return false
}

// validatedSeqLocked returns the most-recently fully-validated seq (0 if
// none), from the adaptor's validated hash+ledger. Caller must hold e.mu.
func (e *Engine) validatedSeqLocked() uint32 {
	vh := e.adaptor.GetValidatedLedgerHash()
	if vh == (consensus.LedgerID{}) {
		return 0
	}
	vl, err := e.adaptor.GetLedger(vh)
	if err != nil || vl == nil {
		return 0
	}
	return vl.Seq()
}

// isBuildCompatibleWithValidatedLocked reports whether the built ledger
// has the validated tip on its ancestry (rippled's areCompatible). Three
// branches by validatedSeq vs builtSeq: walk the higher back to the lower
// via ParentID and compare, or compare hashes at equal seq. Missing
// intermediate ancestors → true (compatible), matching rippled's
// nullopt-hashOfSeq rule. Caller must hold e.mu.
func (e *Engine) isBuildCompatibleWithValidatedLocked(built consensus.Ledger) bool {
	if built == nil {
		return true
	}
	vh := e.adaptor.GetValidatedLedgerHash()
	if vh == (consensus.LedgerID{}) {
		return true
	}
	vl, err := e.adaptor.GetLedger(vh)
	if err != nil || vl == nil {
		return true
	}
	validatedSeq := vl.Seq()
	builtSeq := built.Seq()

	if validatedSeq == builtSeq {
		return built.ID() == vh
	}

	if validatedSeq < builtSeq {
		current := built
		// Walk built back to validatedSeq via parents (first hop is
		// prevLedger, always known; deeper hops may miss → compatible per
		// rippled).
		for current != nil && current.Seq() > validatedSeq {
			parent, err := e.adaptor.GetLedger(current.ParentID())
			if err != nil || parent == nil {
				return true
			}
			current = parent
		}
		if current == nil || current.Seq() != validatedSeq {
			return true
		}
		return current.ID() == vh
	}

	// validatedSeq > builtSeq: walk validated back to builtSeq.
	current := vl
	for current != nil && current.Seq() > builtSeq {
		parent, err := e.adaptor.GetLedger(current.ParentID())
		if err != nil || parent == nil {
			return true
		}
		current = parent
	}
	if current == nil || current.Seq() != builtSeq {
		return true
	}
	return current.ID() == built.ID()
}

// canSwitchToLedgerLocked applies rippled's pre-switch sanity checks to an
// acquired candidate LCL (NetworkOPs.cpp:1953-1962): plausible seq/close time
// via canBeCurrent, and validated-chain ancestry via areCompatible. Peer-LCL
// votes are counted ungated, so this is the safety that keeps a bogus
// gossiped hash from being adopted. Caller must hold e.mu.
func (e *Engine) canSwitchToLedgerLocked(candidate consensus.Ledger) bool {
	if candidate == nil {
		return false
	}
	if !e.canBeCurrentLocked(candidate) {
		return false
	}
	if e.isBuildCompatibleWithValidatedLocked(candidate) {
		return true
	}
	return e.canReplaceFastLoadProvisionalLocked(candidate)
}

type fastLoadProvisionalReporter interface {
	IsFastLoadProvisional() bool
}

func (e *Engine) canReplaceFastLoadProvisionalLocked(candidate consensus.Ledger) bool {
	reporter, ok := e.adaptor.(fastLoadProvisionalReporter)
	if !ok || !reporter.IsFastLoadProvisional() {
		return false
	}
	validatedID := e.adaptor.GetValidatedLedgerHash()
	if validatedID == (consensus.LedgerID{}) || candidate.ID() == validatedID {
		return false
	}
	validated, err := e.adaptor.GetLedger(validatedID)
	if err != nil || validated == nil || candidate.Seq() != validated.Seq() {
		return false
	}
	return e.isQuorumValidatedCandidateLocked(candidate)
}

// canBeCurrentLocked mirrors rippled LedgerMaster::canBeCurrent
// (LedgerMaster.cpp:341-407): never rewind behind the validated tip, close
// time within 5 minutes of network time, and seq at most
// validated+10+elapsed/2 ahead. Caller must hold e.mu.
func (e *Engine) canBeCurrentLocked(candidate consensus.Ledger) bool {
	now := e.adaptor.Now()
	var validated consensus.Ledger
	if vh := e.adaptor.GetValidatedLedgerHash(); vh != (consensus.LedgerID{}) {
		if vl, err := e.adaptor.GetLedger(vh); err == nil {
			validated = vl
		}
	}
	if validated != nil && candidate.Seq() < validated.Seq() {
		return false
	}
	if validated != nil || candidate.Seq() > 10 {
		closeTime := candidate.CloseTime()
		if reporter, ok := candidate.(parentCloseTimeReporter); ok &&
			!reporter.ParentCloseTime().IsZero() {
			closeTime = reporter.ParentCloseTime()
		}
		gap := now.Sub(closeTime)
		if gap < 0 {
			gap = -gap
		}
		if gap > 5*time.Minute {
			return false
		}
	}
	if validated != nil {
		maxSeq := validated.Seq() + 10
		validatedCloseTime := validated.CloseTime()
		if reporter, ok := validated.(parentCloseTimeReporter); ok &&
			!reporter.ParentCloseTime().IsZero() {
			validatedCloseTime = reporter.ParentCloseTime()
		}
		if elapsed := now.Sub(validatedCloseTime); elapsed > 0 {
			maxSeq += uint32(elapsed / (2 * time.Second))
		}
		if candidate.Seq() > maxSeq {
			return false
		}
	}
	return true
}

// validationLaggardFreshness (20s): a validation older than this counts
// the peer as offline, not a laggard. Shorter than the 3m/5m isCurrent
// windows — laggard accounting wants "validated in the last interval".
const validationLaggardFreshness = 20 * time.Second

// countLaggardsAndOfflineLocked partitions trusted validators (except us)
// by their latest fresh validation: laggards have a fresh validation at
// seq < prevSeq (haven't advanced past our prev); offline have none or
// only a stale one. seq >= prevSeq counts as neither. Caller must hold e.mu.
func (e *Engine) countLaggardsAndOfflineLocked(prevSeq uint32, trusted []consensus.NodeID) (laggards, offline int) {
	if e.validationTracker == nil {
		return 0, 0
	}
	self, _ := e.adaptor.GetValidatorKey()
	now := e.adaptor.Now()
	for _, k := range trusted {
		if k == self {
			continue
		}
		v := e.validationTracker.latestValidation(k)
		if v == nil {
			offline++
			continue
		}
		seen := v.SeenTime
		if seen.IsZero() {
			seen = v.SignTime
		}
		if !seen.IsZero() && now.Sub(seen) > validationLaggardFreshness {
			offline++
			continue
		}
		if v.LedgerSeq < prevSeq {
			laggards++
		}
	}
	return laggards, offline
}

// logPauseLocked emits the pause telemetry and returns true so callers can
// `return logPauseLocked(...)`.
func logPauseLocked(e *Engine, ahead uint32, laggards, offline, totalValidators, quorum int, phase int) bool {
	seq := uint32(0)
	if e.prevLedger != nil {
		seq = e.prevLedger.Seq()
	}
	slog.Info("consensus pause — ahead of validated, peers lagging",
		"t", "consensus",
		"event", "consensus-pause",
		"working_seq", seq,
		"ahead", ahead,
		"validators", totalValidators,
		"laggards", laggards,
		"offline", offline,
		"quorum", quorum,
		"phase", phase,
	)
	return true
}

// abandonDeadlineExceeded reports whether the round passed the
// clamp(prevRoundTime*factor, LedgerMaxConsensus, LedgerAbandonConsensus)
// hard deadline. Caller must hold e.mu.
func (e *Engine) abandonDeadlineExceeded(roundTime time.Duration) bool {
	lo := e.timing.LedgerMaxConsensus
	hi := e.timing.LedgerAbandonConsensus
	if hi <= 0 {
		return false
	}
	// clamp(factor×prev, lo, hi); factor 0 disables scaling → absolute ceiling.
	var deadline time.Duration
	if e.timing.LedgerAbandonConsensusFactor > 0 && e.prevRoundTime > 0 {
		deadline = e.prevRoundTime * time.Duration(e.timing.LedgerAbandonConsensusFactor)
	} else {
		deadline = hi
	}
	if lo > 0 && deadline < lo {
		deadline = lo
	}
	if deadline > hi {
		deadline = hi
	}
	return roundTime > deadline
}

// consensusState is checkConsensusState's decision: No, MovedOn, Expired,
// Yes (same ordinal layout as rippled's ConsensusState).
type consensusState int

const (
	consensusStateNo consensusState = iota
	consensusStateMovedOn
	consensusStateExpired
	consensusStateYes
)

// checkConvergence drives the accept gate (rippled's
// phaseEstablish→haveConsensus→checkConsensus flow): compute consensusState,
// then dispatch.
// Every accept path — Yes, MovedOn, and Expired — sits behind the
// close-time gate, exactly as in rippled where haveConsensus returns true
// for all three and phaseEstablish then returns on !haveCloseTimeConsensus_
// (Consensus.h:1406-1411). No→retry next heartbeat.
func (e *Engine) checkConvergence(stages *[]slowHeartbeatStage) {
	if e.phase != consensus.PhaseEstablish {
		return
	}

	// Gate out wrongLedger: rippled makes this structurally unreachable
	// (result_ null), but go-xrpl's observer fallback in countAgreement would
	// otherwise accept on peer-peer agreement, walk prev past validated, and
	// re-enter wrongLedger every round — a permanent stall (iter27/iter28).
	if e.mode == consensus.ModeWrongLedger {
		return
	}

	roundTime := e.now().Sub(e.roundStartTime)
	agree, disagree := e.countAgreement()
	total := agree + disagree

	state := e.checkConsensusState(roundTime, agree, total)

	if state == consensusStateNo {
		return
	}

	// The Expired hard-timeout bows a proposer out (rippled leaveConsensus
	// inside haveConsensus, Consensus.h:1784) once past the per-avalanche
	// minimum dwell (Consensus.h:1765). Acceptance still sits behind the
	// close-time gate below: an expired round without close-time consensus
	// stays in Establish and recovers only via checkLedger resyncing it onto
	// the network's ledger.
	if state == consensusStateExpired {
		minimumCounter := e.parms.AvalancheCutoffCount() * e.parms.MinRounds
		if e.establishCounter < minimumCounter {
			slog.Warn("consensus expired but inside retry window — continuing",
				"t", "consensus",
				"event", "expired-retry",
				"round", e.state.Round,
				"establish_counter", e.establishCounter,
				"minimum_counter", minimumCounter,
				"round_time", roundTime,
			)
			return
		}
		if !e.roundExpiredReported {
			e.roundExpiredReported = true
			slog.Warn("consensus taken too long, abandoning round",
				"t", "consensus",
				"event", "expired",
				"round", e.state.Round,
				"round_time", roundTime,
				"prev_round_time", e.prevRoundTime,
				"max_consensus", e.timing.LedgerMaxConsensus,
				"abandon_consensus", e.timing.LedgerAbandonConsensus,
			)
			e.eventBus.Publish(&consensus.TimerFiredEvent{
				Timer:     consensus.TimerRoundTimeout,
				Round:     e.state.Round,
				Timestamp: e.adaptor.Now(),
			})
		}
		e.leaveConsensusLocked()
	}

	// Close-time consensus gates every accept path. Re-try once here in case
	// the caller (OnProposal/OnTxSet) skipped phaseEstablish.
	if !e.closeTime.haveConsensus {
		e.recordHeartbeatStageLocked(stages, "close-time-consensus", func() {
			e.updateCloseTimePosition()
		})
		if !e.closeTime.haveConsensus {
			return
		}
	}

	switch state {
	case consensusStateYes:
		e.recordHeartbeatStageLocked(stages, "accept", func() {
			e.acceptLedger(consensus.ResultSuccess)
		})
	case consensusStateMovedOn:
		finished := 0
		if e.validationTracker != nil && e.prevLedger != nil {
			finished = e.validationTracker.proposersFinished(e.prevLedger)
		}
		slog.Info("consensus moved on, accepting",
			"t", "consensus",
			"event", "moved-on",
			"seq", e.state.Round.Seq,
			"finished", finished,
			"current_proposers", total,
			"prev_proposers", e.prevProposers,
			"round_time_ms", roundTime.Milliseconds(),
		)
		e.recordHeartbeatStageLocked(stages, "accept", func() {
			e.acceptLedger(consensus.ResultMovedOn)
		})
	case consensusStateExpired:
		e.recordHeartbeatStageLocked(stages, "accept", func() {
			e.acceptLedger(consensus.ResultAbandoned)
		})
	}
}

// leaveConsensusLocked bows a proposer out of the round (rippled
// Consensus::leaveConsensus, Consensus.h:1800-1817): stop proposing so the
// next round doesn't count us, without touching phase or prevLedger.
// Idempotent. Caller holds e.mu.
func (e *Engine) leaveConsensusLocked() {
	if e.mode != consensus.ModeProposing {
		return
	}
	// Broadcast a bow-out (seqLeave) so peers immediately unVote and drop
	// our position instead of counting it for up to ProposeFreshness after
	// we've left (rippled leaveConsensus → position.bowOut + propose,
	// Consensus.h:1807-1810).
	if e.state != nil && e.state.OurPosition != nil && e.prevLedger != nil {
		nodeID, err := e.adaptor.GetValidatorKey()
		if err == nil {
			bow := &consensus.Proposal{
				Round:          e.state.Round,
				NodeID:         nodeID,
				Position:       0xFFFFFFFF, // seqLeave
				TxSet:          e.state.OurPosition.TxSet,
				CloseTime:      e.state.OurPosition.CloseTime,
				PreviousLedger: e.prevLedger.ID(),
				Timestamp:      e.adaptor.Now(),
			}
			if err := e.adaptor.SignProposal(bow); err == nil {
				e.state.OurPosition = bow
				e.enqueueProposalBroadcastLocked(bow)
			}
		}
	}
	e.setMode(consensus.ModeObserving)
}

// reproposeCurrentLocked re-emits our current position unchanged with a
// bumped seq and fresh timestamp (rippled's freshness re-proposal). Caller
// holds e.mu; caller guarantees OurPosition and prevLedger are non-nil.
func (e *Engine) reproposeCurrentLocked() {
	cur := e.state.OurPosition
	nodeID, err := e.adaptor.GetValidatorKey()
	if err != nil {
		return
	}
	proposal := &consensus.Proposal{
		Round:          e.state.Round,
		NodeID:         nodeID,
		Position:       cur.Position + 1,
		TxSet:          cur.TxSet,
		CloseTime:      cur.CloseTime,
		PreviousLedger: e.prevLedger.ID(),
		Timestamp:      e.adaptor.Now(),
	}
	if err := e.adaptor.SignProposal(proposal); err == nil {
		e.state.OurPosition = proposal
		e.enqueueProposalBroadcastLocked(proposal)
	}
}

// checkConsensusState mirrors rippled's checkConsensus, returning
// {No, Yes, MovedOn, Expired}. Priority order:
//
//  1. roundTime <= ledgerMIN_CONSENSUS                         → No
//  2. currentProposers < prevProposers*3/4 AND
//     roundTime < prevRoundTime + ledgerMIN_CONSENSUS          → No
//  3. checkConsensusReached(agree, ...)                        → Yes
//  4. checkConsensusReached(finished, ...)                     → MovedOn
//  5. roundTime > clamp(prevRoundTime*factor, MAX, ABANDON)    → Expired
//  6. else                                                     → No
//
// "stalled" requires haveCloseTimeConsensus and every dispute Stalled.
func (e *Engine) checkConsensusState(roundTime time.Duration, agree, currentProposers int) consensusState {
	if roundTime <= e.timing.LedgerMinConsensus {
		return consensusStateNo
	}

	// 3/4 prev-proposers pause: with fewer than 3/4 of last round's proposers
	// present, wait one more MIN_CONSENSUS past prevRoundTime for stragglers.
	// Skipped at prevProposers==0 so a 1-node soak can't freeze.
	if e.prevProposers > 0 && currentProposers < (e.prevProposers*3/4) {
		if roundTime < (e.prevRoundTime + e.timing.LedgerMinConsensus) {
			return consensusStateNo
		}
	}

	reachedMax := e.timing.LedgerMaxConsensus > 0 && roundTime > e.timing.LedgerMaxConsensus
	proposing := e.mode == consensus.ModeProposing

	// agree/currentProposers are PEER-only counts (rippled currPeerPositions_);
	// self joins only inside the Yes check via countSelf=proposing
	// (Consensus.cpp:153-158) — folding it into the shared counts skews the
	// 3/4-proposers pause and MovedOn denominator. stalled needs
	// haveCloseTimeConsensus and a non-empty dispute set all individually
	// stalled.
	stalled := false
	if e.closeTime.haveConsensus && e.disputeTracker != nil {
		stalled = e.disputeTracker.allStalled(e.parms, proposing, e.peerUnchangedCounter)
	}
	if checkConsensusReached(agree, currentProposers, proposing, e.thresholds.MinConsensusPct, reachedMax, stalled) {
		return consensusStateYes
	}

	// MovedOn denominator is current-round proposers (not prevProposers):
	// peers stop proposing for our round as they advance.
	if e.prevLedger != nil && e.validationTracker != nil {
		finished := e.validationTracker.proposersFinished(e.prevLedger)
		if checkConsensusReached(finished, currentProposers, false, e.thresholds.MinConsensusPct, reachedMax, false) {
			return consensusStateMovedOn
		}
	}

	if e.timing.LedgerAbandonConsensus > 0 && e.abandonDeadlineExceeded(roundTime) {
		return consensusStateExpired
	}

	return consensusStateNo
}

// checkConsensusReached: true when agreeing/total meets minPct. Empty set
// → true only past ledgerMAX_CONSENSUS (reachedMax, the alone-too-long
// carve-out); a stalled dispute set short-circuits to true.
func checkConsensusReached(agreeing, total int, countSelf bool, minPct int, reachedMax, stalled bool) bool {
	if total == 0 {
		// Alone for too long → consensus by default.
		return reachedMax
	}
	if stalled {
		return true
	}
	if countSelf {
		agreeing++
		total++
	}
	return (agreeing*100)/total >= minPct
}

// countAgreement returns PEER proposers whose position matches ours
// (agree) vs differs (disagree) — self excluded, like rippled's
// currPeerPositions_ tally (Consensus.h:1689-1707); the Yes check adds
// self via countSelf. Caller must hold e.mu.
func (e *Engine) countAgreement() (agree, disagree int) {
	e.purgePendingTrustLocked()
	trusted := e.trustedPredicate()
	var ourTxSet consensus.TxSetID
	haveOurs := false
	if e.state != nil && e.state.OurPosition != nil {
		ourTxSet = e.state.OurPosition.TxSet
		haveOurs = true
	} else if e.ourTxSet != nil {
		ourTxSet = e.ourTxSet.ID()
		haveOurs = true
	}
	if !haveOurs {
		// Observer without a position: count peer-peer agreement on the most
		// popular tx set so non-proposing nodes still get a convergence signal.
		counts := make(map[consensus.TxSetID]int)
		for nodeID, p := range e.proposalTracker.all() {
			if trusted(nodeID) {
				counts[p.TxSet]++
			}
		}
		var best int
		for _, c := range counts {
			if c > best {
				best = c
			}
		}
		agree = best
		for _, c := range counts {
			if c != best {
				disagree += c
			}
		}
		return agree, disagree
	}

	for nodeID, p := range e.proposalTracker.all() {
		if !trusted(nodeID) {
			continue
		}
		if p.TxSet == ourTxSet {
			agree++
		} else {
			disagree++
		}
	}
	return agree, disagree
}

// updatePosition runs the per-tx dispute re-vote and, if any vote flipped,
// rebuilds ourTxSet from the inclusion decisions and rebroadcasts our
// position. Caller must hold e.mu.
func (e *Engine) updatePosition() {
	e.purgePendingTrustLocked()
	if e.state == nil {
		return
	}

	if e.disputeTracker == nil || e.ourTxSet == nil {
		return
	}

	proposing := e.mode == consensus.ModeProposing
	disputeCount := e.disputeTracker.count()
	changed := e.disputeTracker.updateOurVote(e.convergePercent(), proposing, e.parms)

	if disputeCount > 0 || proposing {
		var ourSetID consensus.TxSetID
		ourSetSize := -1
		if e.ourTxSet != nil {
			ourSetID = e.ourTxSet.ID()
			ourSetSize = e.ourTxSet.Size()
		}
		slog.Info("update position",
			"t", "consensus",
			"event", "update-position",
			"seq", e.state.Round.Seq,
			"mode", e.mode.String(),
			"converge_pct", e.convergePercent(),
			"disputes", disputeCount,
			"flipped", len(changed),
			"our_txset", fmt.Sprintf("%x", ourSetID[:8]),
			"our_tx_count", ourSetSize,
			"acquired_txsets", len(e.acquiredTxSets),
			"peer_proposals", e.proposalTracker.count(),
		)
	}

	// Freshness re-proposal (rippled Consensus.h:1636-1642): when nothing
	// flipped but our position has gone stale (older than ProposeInterval),
	// re-emit it with a bumped seq and fresh timestamp so peers don't prune
	// it at ProposeFreshness during a long round — losing it would drop our
	// vote from every peer's tally exactly when convergence is hardest.
	if len(changed) == 0 {
		if proposing && e.state.OurPosition != nil && e.prevLedger != nil &&
			e.adaptor.Now().Sub(e.state.OurPosition.Timestamp) >= e.timing.ProposeInterval {
			e.reproposeCurrentLocked()
		}
		return
	}

	// Rebuild ourTxSet from the dispute decisions: add a tx on a yes vote,
	// drop it on a no vote.
	currentBlobs := e.ourTxSet.Txs()
	currentIDs := e.ourTxSet.TxIDs()
	idSet := make(map[consensus.TxID]int, len(currentIDs))
	for idx, id := range currentIDs {
		idSet[id] = idx
	}

	newBlobs := make([][]byte, 0, len(currentBlobs)+len(changed))
	keep := make(map[consensus.TxID]bool, len(currentIDs))
	for _, id := range currentIDs {
		keep[id] = true
	}
	for _, txID := range changed {
		dispute := e.disputeTracker.dispute(txID)
		if dispute == nil {
			continue
		}
		if dispute.OurVote {
			if !keep[txID] {
				keep[txID] = true
			}
		} else {
			keep[txID] = false
		}
	}
	// Preserve original order for txs we keep that were already in
	// ours, then append newly-voted-in disputes.
	for idx, id := range currentIDs {
		if keep[id] {
			newBlobs = append(newBlobs, currentBlobs[idx])
		}
	}
	for _, txID := range changed {
		if _, already := idSet[txID]; already {
			continue
		}
		if !keep[txID] {
			continue
		}
		dispute := e.disputeTracker.dispute(txID)
		if dispute == nil || dispute.Tx == nil {
			continue
		}
		newBlobs = append(newBlobs, dispute.Tx)
	}

	newTxSet, err := e.adaptor.BuildTxSet(newBlobs)
	if err != nil || newTxSet == nil {
		slog.Warn("updatePosition: failed to rebuild tx set after dispute re-vote",
			"err", err,
		)
		return
	}

	// No-op if the rebuild produced the same set.
	if newTxSet.ID() == e.ourTxSet.ID() {
		return
	}

	e.ourTxSet = newTxSet
	e.acquiredTxSets[newTxSet.ID()] = newTxSet
	// Emitting needs both OurPosition (for the seq bump) and prevLedger; a
	// test harness without Start() has prevLedger nil — still update ourTxSet,
	// just don't emit.
	if proposing && e.state.OurPosition != nil && e.prevLedger != nil {
		nodeID, _ := e.adaptor.GetValidatorKey()
		proposal := &consensus.Proposal{
			Round:          e.state.Round,
			NodeID:         nodeID,
			Position:       e.state.OurPosition.Position + 1,
			TxSet:          newTxSet.ID(),
			CloseTime:      e.state.OurPosition.CloseTime,
			PreviousLedger: e.prevLedger.ID(),
			Timestamp:      e.adaptor.Now(),
		}
		if err := e.adaptor.SignProposal(proposal); err == nil {
			e.state.OurPosition = proposal
			e.enqueueProposalBroadcastLocked(proposal)
		}
	}

	// Refresh per-peer votes for peers whose position matches the new set.
	trusted := e.trustedPredicate()
	for nodeID, p := range e.proposalTracker.all() {
		if !trusted(nodeID) {
			continue
		}
		if p.TxSet != newTxSet.ID() {
			continue
		}
		if e.disputeTracker.updateDisputes(nodeID, newTxSet) {
			e.peerUnchangedCounter = 0
		}
	}
}
