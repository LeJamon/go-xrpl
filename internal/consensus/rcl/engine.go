// Package rcl implements the Ripple Consensus Ledger algorithm.
// This is the default consensus algorithm used by the XRP Ledger.
package rcl

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// Engine implements the RCL consensus algorithm.
type Engine struct {
	mu sync.RWMutex

	// Configuration
	timing     consensus.Timing
	thresholds consensus.Thresholds

	// Dependencies
	adaptor  consensus.Adaptor
	eventBus *consensus.EventBus

	// Current state
	mode       consensus.Mode
	phase      consensus.Phase
	state      *consensus.RoundState
	prevLedger consensus.Ledger

	// Proposal tracking
	proposals map[consensus.NodeID]*consensus.Proposal
	ourTxSet  consensus.TxSet
	converged bool

	// deadNodes tracks validators that have bowed out of the current
	// consensus round by sending a proposal with Position == seqLeave
	// (0xFFFFFFFF). Matches rippled's deadNodes_ set (Consensus.h:632).
	// Any further proposal from a dead node is dropped until the next
	// round clears the set (startRoundLocked), mirroring rippled's
	// Consensus.h:722.
	deadNodes map[consensus.NodeID]struct{}

	// Validation tracking
	validations map[consensus.NodeID]*consensus.Validation

	// validationTracker accumulates trusted validations across ledgers
	// and fires the fully-validated callback when quorum is reached.
	// This is what drives server_info.validated_ledger forward —
	// mirrors rippled's LedgerMaster::checkAccept quorum gate.
	validationTracker *ValidationTracker

	// Dispute tracking
	disputes map[consensus.TxID]*consensus.DisputedTx

	// Heartbeat ticker — single global timer matching rippled's ledgerGRANULARITY.
	heartbeat *time.Ticker

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Close time consensus
	haveCloseTimeConsensus  bool
	closeTimeAvalancheState avalancheState
	prevRoundTime           time.Duration
	roundStartTime          time.Time

	// Proposal buffering for cross-round playback.
	// Matches rippled's recentPeerPositions_ (Consensus.h:629).
	recentProposals map[consensus.NodeID][]*consensus.Proposal

	// Number of trusted proposers in the previous round.
	// Used by shouldCloseLedger() for peer pressure calculation.
	prevProposers int

	// wrongLedgerID tracks the ledger we're trying to acquire
	// while in ModeWrongLedger. Prevents spamming handleWrongLedger.
	wrongLedgerID consensus.LedgerID

	// lastSignTime is the monotonic floor for emitted validation
	// SignTime. If the adaptor clock regresses (NTP step, leap-second
	// correction, VM pause/resume), sendValidation bumps SignTime to
	// lastSignTime + 1s so peers never see a non-monotonic sequence of
	// validations from the same node. Matches rippled's
	// RCLConsensus::Adaptor::lastValidationTime_ (RCLConsensus.cpp:825-828).
	// Protected by e.mu (same lock as sendValidation's other state).
	lastSignTime time.Time

	// Stats
	roundCount     uint64
	consensusCount uint64
}

// avalancheState tracks the close time voting threshold escalation.
// Matches rippled's avalanche cutoffs in ConsensusParms.h.
type avalancheState int

const (
	avalancheInit  avalancheState = iota // 50% threshold
	avalancheMid                         // 65% threshold
	avalancheLate                        // 70% threshold
	avalancheStuck                       // 95% threshold
)

// Config holds RCL engine configuration.
type Config struct {
	Timing     consensus.Timing
	Thresholds consensus.Thresholds
}

// DefaultConfig returns the default RCL configuration.
func DefaultConfig() Config {
	return Config{
		Timing:     consensus.DefaultTiming(),
		Thresholds: consensus.DefaultThresholds(),
	}
}

// NewEngine creates a new RCL consensus engine.
func NewEngine(adaptor consensus.Adaptor, config Config) *Engine {
	return &Engine{
		timing:          config.Timing,
		thresholds:      config.Thresholds,
		adaptor:         adaptor,
		eventBus:        consensus.NewEventBus(100),
		mode:            consensus.ModeObserving,
		phase:           consensus.PhaseAccepted,
		proposals:       make(map[consensus.NodeID]*consensus.Proposal),
		validations:     make(map[consensus.NodeID]*consensus.Validation),
		disputes:        make(map[consensus.TxID]*consensus.DisputedTx),
		recentProposals: make(map[consensus.NodeID][]*consensus.Proposal),
		deadNodes:       make(map[consensus.NodeID]struct{}),
	}
}

// Start begins the consensus engine.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.ctx, e.cancel = context.WithCancel(ctx)
	e.eventBus.Start()

	// Get initial ledger state
	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return fmt.Errorf("failed to get last closed ledger: %w", err)
	}
	e.prevLedger = ledger

	// Wire the validation tracker: trusted set + quorum come from the adaptor,
	// and its callback drives the adaptor's fully-validated hook which in turn
	// flips the ledger service's validated_ledger pointer.
	e.validationTracker = NewValidationTracker(e.adaptor.GetQuorum(), 5*time.Minute)
	e.validationTracker.SetTrusted(e.adaptor.GetTrustedValidators())
	// Use the adaptor's network-adjusted clock for freshness checks.
	// Rippled's Validations::isCurrent uses app_.timeKeeper().closeTime()
	// — matching here avoids rejecting our own just-signed validation
	// by the accumulated close-time offset on a skewed node.
	e.validationTracker.SetNow(e.adaptor.Now)
	e.validationTracker.SetFullyValidatedCallback(func(ledgerID consensus.LedgerID, seq uint32) {
		e.adaptor.OnLedgerFullyValidated(ledgerID, seq)
	})

	// Start the main loop
	e.wg.Add(1)
	go e.run()

	return nil
}

// Stop gracefully shuts down the consensus engine.
func (e *Engine) Stop() error {
	e.cancel()
	e.wg.Wait()
	e.eventBus.Stop()
	return nil
}

// StartRound begins a new consensus round.
func (e *Engine) StartRound(round consensus.RoundID, proposing bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startRoundLocked(round, proposing, false)
}

// startRoundLocked is the lock-free inner implementation of StartRound.
// Caller must hold e.mu.
//
// recovering indicates this round is entered immediately after
// handleWrongLedger or OnLedger adoption — rippled calls this the
// "switchedLedger" mode. In that mode the node acts like an observer
// for one round (no proposal, no validation emission) even if it's a
// full-configured validator. Mirrors rippled's Consensus.h:1107 which
// forces ConsensusMode::switchedLedger after a successful LCL switch,
// and Consensus.h:1457 which only emits a proposal when mode equals
// proposing. The suppression is intentional: a node that just
// swapped its prior-ledger pointer hasn't yet built a coherent view
// of the new round's tx-set, and emitting a stale proposal/validation
// would poison the network's convergence.
func (e *Engine) startRoundLocked(round consensus.RoundID, proposing, recovering bool) error {
	// Determine mode. After a wrongLedger recovery we enter switchedLedger
	// for exactly one round — not proposing, not validating — even though
	// we'd otherwise be ModeProposing. The NEXT startRoundLocked call
	// (via auto-advance in acceptLedger) gets the normal treatment.
	switch {
	case recovering && e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull:
		e.setMode(consensus.ModeSwitchedLedger)
	case proposing && e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull:
		e.setMode(consensus.ModeProposing)
	default:
		e.setMode(consensus.ModeObserving)
	}

	// Initialize round state.
	// StartTime must be wall-clock (time.Now) because shouldCloseLedger
	// reads it via time.Since at engine.go:873 — same class of bug as the
	// roundStartTime fix below, and the same rationale applies. PhaseStart
	// stays on adaptor.Now because its consumer (checkConvergence) reads
	// it via adaptor.Now().Sub(), which keeps the offset-adjusted pair
	// balanced.
	e.state = &consensus.RoundState{
		Round:          round,
		Mode:           e.mode,
		Phase:          consensus.PhaseOpen,
		Proposals:      make(map[consensus.NodeID]*consensus.Proposal),
		Disputed:       make(map[consensus.TxID]*consensus.DisputedTx),
		CloseTimes:     consensus.CloseTimes{Peers: make(map[time.Time]int)},
		StartTime:      time.Now(),
		PhaseStart:     e.adaptor.Now(),
		HaveCorrectLCL: true,
	}

	// Reset tracking maps
	e.proposals = make(map[consensus.NodeID]*consensus.Proposal)
	e.disputes = make(map[consensus.TxID]*consensus.DisputedTx)
	// deadNodes is scoped to a single consensus round — a validator that
	// bowed out of the prior round is free to rejoin in the new one.
	// Matches rippled's Consensus.h:722 (startRoundInternal clears
	// deadNodes_ alongside currPeerPositions_).
	e.deadNodes = make(map[consensus.NodeID]struct{})
	e.converged = false
	e.ourTxSet = nil
	e.haveCloseTimeConsensus = false
	e.closeTimeAvalancheState = avalancheInit
	// Internal duration metric — use the wall clock. Do NOT use
	// adaptor.Now() here: adaptor.Now returns time.Now().Add(closeOffset),
	// where closeOffset drifts as AdjustCloseTime pulls us toward the
	// network's average close time. The consumers of roundStartTime
	// measure elapsed wall time via time.Since (prevRoundTime,
	// phaseEstablish timeout, convergePercent weighting). Mixing
	// offset-adjusted captures with wall-clock-subtracted reads
	// produces -closeOffset as the measured duration — exactly the
	// negative-converge-time artifact visible in server_info.last_close.
	e.roundStartTime = time.Now()

	// Set phase
	e.setPhase(consensus.PhaseOpen)

	// Emit event
	e.eventBus.Publish(&consensus.RoundStartedEvent{
		Round:     round,
		Mode:      e.mode,
		Timestamp: e.adaptor.Now(),
	})

	// Replay buffered proposals matching this round's prevLedger.
	// Matches rippled's playbackProposals() (Consensus.h:1151).
	if e.prevLedger != nil && len(e.recentProposals) > 0 {
		prevID := e.prevLedger.ID()
		replayed := 0
		for nodeID, positions := range e.recentProposals {
			for _, p := range positions {
				if p.PreviousLedger == prevID {
					trusted := e.adaptor.IsTrusted(nodeID)
					existing, exists := e.proposals[nodeID]
					if !exists || p.Position > existing.Position {
						e.proposals[nodeID] = p
					}
					if p.Position == 0 && trusted {
						e.state.CloseTimes.Peers[p.CloseTime]++
					}
					if trusted {
						replayed++
					}
				}
			}
		}

		// Peer pressure: if more than half of previous proposers have
		// already closed, consider closing immediately — but still go
		// through shouldCloseLedger() to enforce timing constraints.
		// Matches rippled's startRoundInternal() (Consensus.h:732-738)
		// which calls timerEntry() → phaseOpen() → shouldCloseLedger().
		if replayed > e.prevProposers/2 {
			if e.shouldCloseLedger() {
				e.closeLedger()
				// Don't call checkConvergence() here — the establish
				// timer will evaluate it after fresh proposals arrive
				// with correct close times. Accepting immediately with
				// only replayed close times causes hash mismatches.
			}
		}
	}

	e.roundCount++
	return nil
}

// OnProposal handles an incoming proposal from a peer. originPeer is
// the overlay peer that delivered the message (0 for self-originated).
// Passed through to RelayProposal so we can exclude the originator from
// the gossip forward.
func (e *Engine) OnProposal(proposal *consensus.Proposal, originPeer uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Verify signature first (before buffering).
	if err := e.adaptor.VerifyProposal(proposal); err != nil {
		return fmt.Errorf("invalid proposal signature: %w", err)
	}

	// Always buffer proposals for future playback, even between rounds.
	// Matches rippled's recentPeerPositions_ (Consensus.h:754): cap at
	// 10 positions per node. The earlier 5-entry cap drifted from
	// rippled's value and would truncate a trusted validator's
	// cross-round trail under sustained gossip.
	positions := e.recentProposals[proposal.NodeID]
	if len(positions) >= 10 {
		positions = positions[1:] // drop oldest
	}
	e.recentProposals[proposal.NodeID] = append(positions, proposal)

	// During accepted phase (between rounds), only buffer — don't process.
	// Matches rippled Consensus.h:769-770.
	if e.phase == consensus.PhaseAccepted {
		return nil
	}

	// Reject proposals referencing a different previous ledger.
	// Matches rippled Consensus.h:776-781.
	if e.prevLedger != nil && proposal.PreviousLedger != e.prevLedger.ID() {
		return nil
	}

	// Ignore proposals from nodes already marked dead this round. This
	// guard must come before the bow-out arm below: rippled's
	// Consensus.h:785-789 drops the message outright before it ever
	// reaches the position-update code, so a node that's already dead
	// cannot keep re-inserting itself by repeatedly sending seqLeave.
	if _, dead := e.deadNodes[proposal.NodeID]; dead {
		return nil
	}

	// isBowOut: a validator bowing out of consensus sets ProposeSeq to
	// seqLeave (0xFFFFFFFF) on its final position so peers know to stop
	// counting it for the rest of the round. Mirrors rippled's
	// ConsensusProposal.h:68,154-156 and the handling in
	// Consensus.h:804-817: erase the current position, record the node
	// as dead, and (when E2/per-tx dispute tracking lands) un-vote its
	// contribution from every active dispute. Without this gate the
	// final seqLeave position would persist in e.proposals and keep
	// "voting" forever, skewing convergence and tie-break logic.
	const seqLeave = uint32(0xFFFFFFFF)
	if proposal.Position == seqLeave {
		delete(e.proposals, proposal.NodeID)
		e.deadNodes[proposal.NodeID] = struct{}{}
		// TODO(E2 per-tx dispute tracking): iterate e.disputes here and
		// unvote proposal.NodeID from each entry, matching rippled's
		// Consensus.h:809-811 (for each dispute: it.second.unVote(peerID)).
		// The current DisputedTx carries aggregate Yays/Nays counts only,
		// so there's nothing to unvote yet; adding the per-node vote set
		// is the E2 task.
		return nil
	}

	// Check if from trusted validator
	trusted := e.adaptor.IsTrusted(proposal.NodeID)

	// Store proposal
	existing, exists := e.proposals[proposal.NodeID]
	if !exists || proposal.Position > existing.Position {
		e.proposals[proposal.NodeID] = proposal
	}

	// Record close time only from initial proposals (Position == 0),
	// matching rippled's rawCloseTimes_.peers tracking (Consensus.h:825-830).
	if proposal.Position == 0 && trusted {
		e.state.CloseTimes.Peers[proposal.CloseTime]++
	}

	// Emit event
	e.eventBus.Publish(&consensus.ProposalReceivedEvent{
		Proposal:  proposal,
		Trusted:   trusted,
		Timestamp: e.adaptor.Now(),
	})

	// Relay to other peers, excluding the originating peer. Untrusted
	// proposals are not relayed to limit gossip amplification of spam —
	// matches rippled's relay-only-trusted heuristic.
	if trusted {
		e.adaptor.RelayProposal(proposal, originPeer)
	}

	// Check if we need the transaction set
	if _, err := e.adaptor.GetTxSet(proposal.TxSet); err != nil {
		e.adaptor.RequestTxSet(proposal.TxSet)
	}

	// If in establish phase, check for convergence
	if e.phase == consensus.PhaseEstablish {
		e.checkConvergence()
	}

	return nil
}

// OnValidation handles an incoming validation from a peer. originPeer
// is the overlay peer that delivered the message (0 for self-originated).
// Passed through to RelayValidation so we can exclude the originator
// from the gossip forward.
func (e *Engine) OnValidation(validation *consensus.Validation, originPeer uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Verify signature
	if err := e.adaptor.VerifyValidation(validation); err != nil {
		return fmt.Errorf("invalid validation signature: %w", err)
	}

	// Check if from trusted validator
	trusted := e.adaptor.IsTrusted(validation.NodeID)

	// Store validation. Also cap to trusted-only to bound memory under
	// adversarial validator spam — an untrusted key can send us
	// arbitrary validations and the map would grow unbounded.
	if trusted {
		e.validations[validation.NodeID] = validation
	}

	// Feed into the tracker — this is the gate that advances
	// server_info.validated_ledger once quorum of trusted validations
	// accumulates for a given ledger. Trust-gate here as well: the
	// tracker filters by trusted at quorum-count time, but without
	// this gate a byNode entry gets created for every untrusted
	// validator the network gossips, wasting memory on keys that
	// can never contribute to quorum. Rippled's LedgerMaster.cpp:886
	// filters on both Full and trusted before Add.
	if trusted && e.validationTracker != nil {
		e.validationTracker.Add(validation)
	}

	// Emit event
	e.eventBus.Publish(&consensus.ValidationReceivedEvent{
		Validation: validation,
		Trusted:    trusted,
		Timestamp:  e.adaptor.Now(),
	})

	// Relay trusted validations to other peers, excluding the origin.
	// Untrusted validations are dropped from the gossip forward for the
	// same spam-amplification reason as OnProposal. Mirrors rippled's
	// OverlayImpl::relay behavior for TMValidation.
	if trusted {
		e.adaptor.RelayValidation(validation, originPeer)
	}

	return nil
}

// OnTxSet handles receiving a transaction set we requested.
func (e *Engine) OnTxSet(id consensus.TxSetID, txs [][]byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Build and store the transaction set
	txSet, err := e.adaptor.BuildTxSet(txs)
	if err != nil {
		return fmt.Errorf("failed to build tx set: %w", err)
	}

	// Verify the ID matches
	if txSet.ID() != id {
		return fmt.Errorf("tx set ID mismatch: expected %x, got %x", id, txSet.ID())
	}

	// If in establish phase, check for convergence
	if e.phase == consensus.PhaseEstablish {
		e.checkConvergence()
	}

	return nil
}

// OnLedger handles receiving a ledger we were missing.
func (e *Engine) OnLedger(id consensus.LedgerID, ledger []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// If we were on wrong ledger, check if this helps
	if e.mode == consensus.ModeWrongLedger {
		l, err := e.adaptor.GetLedger(id)
		if err == nil && l != nil {
			lID := l.ID()
			slog.Info("Acquired missing ledger, restarting round",
				"seq", l.Seq(), "hash", fmt.Sprintf("%x", lID[:8]))
			e.prevLedger = l
			e.state.HaveCorrectLCL = true
			nextRound := consensus.RoundID{
				Seq:        l.Seq() + 1,
				ParentHash: l.ID(),
			}
			// Re-enter consensus with recovering=true. A trusted validator
			// in OpModeFull would normally be ModeProposing; recovering=true
			// drops it to ModeSwitchedLedger for one round. closeLedger
			// and acceptLedger both gate on mode==ModeProposing, so we
			// suppress emission exactly the way rippled does after a
			// wrongLedger recovery (Consensus.h:1107,1457). On the next
			// round (via acceptLedger auto-advance) the engine promotes
			// back to ModeProposing normally.
			proposing := e.adaptor.IsValidator() &&
				e.adaptor.GetOperatingMode() == consensus.OpModeFull
			e.startRoundLocked(nextRound, proposing, true)
		}
	}

	return nil
}

// State returns the current consensus state.
func (e *Engine) State() *consensus.RoundState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

// Mode returns the current operating mode.
func (e *Engine) Mode() consensus.Mode {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// Phase returns the current consensus phase.
func (e *Engine) Phase() consensus.Phase {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.phase
}

// IsProposing returns true if we're actively proposing.
func (e *Engine) IsProposing() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode == consensus.ModeProposing
}

// Timing returns the consensus timing parameters.
func (e *Engine) Timing() consensus.Timing {
	return e.timing
}

// GetLastCloseInfo returns the proposer count and convergence time from the last consensus round.
func (e *Engine) GetLastCloseInfo() (proposers int, convergeTime time.Duration) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.prevProposers, e.prevRoundTime
}

// Subscribe adds an event subscriber.
func (e *Engine) Subscribe(sub consensus.EventSubscriber) {
	e.eventBus.Subscribe(sub)
}

// Events returns the event channel for direct consumption.
func (e *Engine) Events() <-chan consensus.Event {
	return e.eventBus.Events()
}

// run is the main consensus loop driven by a single global heartbeat,
// matching rippled's processHeartbeatTimer → timerEntry pattern.
func (e *Engine) run() {
	defer e.wg.Done()

	interval := time.Second
	if e.timing.LedgerMinClose < interval {
		interval = e.timing.LedgerMinClose
	}
	e.heartbeat = time.NewTicker(interval)
	defer e.heartbeat.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-e.heartbeat.C:
			e.timerEntry()
		}
	}
}

// timerEntry is the single heartbeat dispatch, matching rippled's
// Consensus::timerEntry() (Consensus.h:859-888). Called every
// ledgerGRANULARITY (1s) and dispatches based on current phase.
func (e *Engine) timerEntry() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.adaptor.GetOperatingMode() != consensus.OpModeFull {
		return
	}

	// Check we're on the correct ledger (matches rippled's checkLedger).
	if e.phase != consensus.PhaseAccepted {
		e.checkLedger()
	}

	switch e.phase {
	case consensus.PhaseOpen:
		e.phaseOpen()
	case consensus.PhaseEstablish:
		e.phaseEstablish()
	case consensus.PhaseAccepted:
		e.checkAndStartRoundInner()
		// After starting a new round, immediately evaluate the new phase
		// in the same heartbeat tick. Matches rippled's startRoundInternal
		// which calls timerEntry() when peer pressure is detected.
		if e.phase == consensus.PhaseOpen {
			e.phaseOpen()
		}
	}
}

// checkAndStartRoundInner checks if we should start a new round.
// This serves as a fallback in case the auto-advance in acceptLedger
// didn't trigger (e.g., first round after startup).
// Caller must hold e.mu.
func (e *Engine) checkAndStartRoundInner() {
	// Only start if in accepted phase and not on wrong ledger
	if e.phase != consensus.PhaseAccepted {
		return
	}
	if e.mode == consensus.ModeWrongLedger {
		return
	}

	// Get current ledger
	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return
	}

	// Check if we have buffered proposals for this ledger.
	// If so, start immediately (peer pressure will close the open phase right away).
	// Otherwise, wait for the idle interval as before.
	ledgerID := ledger.ID()
	hasBufferedProposals := false
	for _, positions := range e.recentProposals {
		for _, p := range positions {
			if p.PreviousLedger == ledgerID {
				hasBufferedProposals = true
				break
			}
		}
		if hasBufferedProposals {
			break
		}
	}

	if !hasBufferedProposals {
		timeSinceClose := e.adaptor.Now().Sub(ledger.CloseTime())
		if timeSinceClose < e.timing.LedgerIdleInterval {
			return
		}
	}

	// Determine if we should propose
	proposing := e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull

	// Update prevLedger to the current LCL — it may have been changed
	// by an InboundLedger adoption since the last round.
	e.prevLedger = ledger

	// Start the round. Not a recovery — this is the normal idle-timeout
	// kick after acceptance; startRoundLocked picks ModeProposing for a
	// trusted validator in OpModeFull.
	round := consensus.RoundID{
		Seq:        ledger.Seq() + 1,
		ParentHash: ledger.ID(),
	}
	e.startRoundLocked(round, proposing, false)
}

// checkLedger verifies we are on the correct ledger by comparing our
// prevLedger against what the network prefers (from proposal counting).
// If we're on the wrong chain, calls handleWrongLedger to switch.
// Matches rippled's checkLedger() (Consensus.h:1118-1147).
func (e *Engine) checkLedger() {
	if e.prevLedger == nil {
		return
	}
	ourID := e.prevLedger.ID()
	netLgr := e.getNetworkLedger()
	if netLgr != ourID {
		// If the network proposals reference our parent, we just completed
		// the round they're still working on — we're ahead, not wrong.
		// Wait for the network to catch up rather than switching back.
		if netLgr == e.prevLedger.ParentID() {
			return
		}

		// Switch preference: pick whichever ledger has MORE trusted
		// validation support, not strictly fully-validated. Rippled
		// uses vals.getPreferred() (RCLConsensus.cpp:301) which walks a
		// LedgerTrie and returns the ledger with the most validation
		// support on its ancestor chain; our approximation compares
		// the flat trusted-count at each exact hash.
		//
		// The OLD behavior — "only switch if netLgr is fully validated"
		// — could strand a catch-up node on the wrong branch. Example:
		// 2-of-3 trusted validators back the peer branch, but neither
		// has crossed quorum yet because our OWN validation for the
		// same seq is on the other branch. The new rule lets us switch
		// as soon as the PEER branch has MORE support than ours —
		// including the case where we have zero support for ours
		// (which is the common case when we're on a stale branch to
		// begin with).
		//
		// Safety gate: require at least ONE trusted validation on the
		// peer branch. Otherwise we'd flip on nothing but proposals,
		// reintroducing the proposals-only thrash the old gate was
		// installed to prevent.
		if e.validationTracker != nil {
			netSupport := e.validationTracker.GetTrustedSupport(netLgr)
			ourSupport := e.validationTracker.GetTrustedSupport(ourID)
			if netSupport == 0 || netSupport <= ourSupport {
				return
			}
		}

		// Already targeting this ledger — don't spam
		if e.mode == consensus.ModeWrongLedger && e.wrongLedgerID == netLgr {
			return
		}
		slog.Warn("Consensus view changed",
			"phase", e.phase,
			"mode", e.mode,
			"our", fmt.Sprintf("%x", ourID[:8]),
			"net", fmt.Sprintf("%x", netLgr[:8]),
		)
		e.handleWrongLedger(netLgr)
	}
}

// getNetworkLedger determines what ledger the network is working on
// by counting recent proposals from trusted validators.
// Returns the most popular prevLedger ID if a majority of trusted
// proposers agree on a different ledger than ours.
// Simplified substitute for rippled's getPrevLedger() + LedgerTrie.
func (e *Engine) getNetworkLedger() consensus.LedgerID {
	if e.prevLedger == nil {
		return consensus.LedgerID{}
	}
	ourID := e.prevLedger.ID()
	freshness := e.timing.ProposeFreshness
	now := e.adaptor.Now()

	// For each trusted node, take the most recent fresh proposal
	type vote struct {
		prevLedger consensus.LedgerID
	}
	votes := make(map[consensus.NodeID]vote)
	for nodeID, positions := range e.recentProposals {
		if !e.adaptor.IsTrusted(nodeID) {
			continue
		}
		// Find the most recent fresh proposal from this node
		var best *consensus.Proposal
		for _, p := range positions {
			if now.Sub(p.Timestamp) > freshness {
				continue // stale
			}
			if best == nil || p.Timestamp.After(best.Timestamp) {
				best = p
			}
		}
		if best != nil {
			votes[nodeID] = vote{prevLedger: best.PreviousLedger}
		}
	}

	// Include our own position as a vote too. checkConvergence already
	// counts self when tallying tx-set agreement; for consistency, the
	// network-ledger preferred-prevLedger vote should work the same way.
	// Without this, a 3-validator UNL's majority threshold (>len/2) is
	// computed over peers only — two peers disagreeing with us will
	// flip our LCL even though a fair vote would include our own
	// position and produce a 2-2 tie (no switch).
	if e.state != nil && e.state.OurPosition != nil {
		pos := e.state.OurPosition
		if now.Sub(pos.Timestamp) <= freshness {
			if key, err := e.adaptor.GetValidatorKey(); err == nil {
				votes[key] = vote{prevLedger: pos.PreviousLedger}
			}
		}
	}

	// Build the set of hashes already voted for via trusted proposals.
	// Peer-LCL votes for those SAME hashes are redundant and — worse —
	// would double-count a validator that happens to also be connected
	// as a peer (its proposal vote + its peerLCL synthetic vote). We
	// skip them below to match rippled's LedgerTrie which folds votes
	// per ledger, not per signaling channel.
	proposalHashes := make(map[consensus.LedgerID]struct{}, len(votes))
	for _, v := range votes {
		proposalHashes[v.prevLedger] = struct{}{}
	}

	// Fold in peer-reported LCLs from statusChange. A peer that has
	// advanced its LCL but hasn't yet gossipped a proposal to us still
	// contributes a signal about where the network is. We key these on
	// a synthetic NodeID derived from the hash so a single peer's
	// reported LCL counts as one vote regardless of its actual
	// validator pubkey (which we don't know from the status message).
	// The vote set remains deduped by NodeID; and we drop peer-LCL
	// votes whose hash ALREADY has a trusted-proposer vote so a
	// trusted validator connected as a peer isn't counted twice.
	for i, h := range e.adaptor.PeerReportedLedgers() {
		if _, already := proposalHashes[h]; already {
			continue
		}
		var synthKey consensus.NodeID
		// Real validator pubkeys are compressed secp256k1 (0x02/0x03
		// prefix) or ed25519-tagged (0xED). 0xFF is unused by XRPL
		// public-key encoding so synthetic entries can't collide
		// with a real validator key.
		synthKey[0] = 0xFF
		synthKey[1] = byte(i >> 8)
		synthKey[2] = byte(i)
		// Fill the rest with the ledger hash so different reported
		// LCLs from the same ordinal slot stay distinguishable.
		copy(synthKey[3:], h[:30])
		votes[synthKey] = vote{prevLedger: h}
	}

	if len(votes) == 0 {
		return ourID
	}

	// Count votes per prevLedger
	counts := make(map[consensus.LedgerID]int)
	for _, v := range votes {
		counts[v.prevLedger]++
	}

	// Find the most popular
	var bestID consensus.LedgerID
	bestCount := 0
	for id, count := range counts {
		if count > bestCount {
			bestID = id
			bestCount = count
		}
	}

	// Only switch if majority of voters agree AND it's different from ours
	if bestID != ourID && bestCount > len(votes)/2 {
		return bestID
	}
	return ourID
}

// handleWrongLedger switches the engine to the network's preferred ledger.
// Matches rippled's handleWrongLedger() (Consensus.h:1062-1113).
func (e *Engine) handleWrongLedger(netLedgerID consensus.LedgerID) {
	// Step 1: Stop proposing (like rippled's leaveConsensus)
	if e.mode == consensus.ModeProposing {
		e.setMode(consensus.ModeObserving)
	}

	// Step 2: Clear consensus state and replay for new ledger
	// (only if this is a new target ledger)
	if e.prevLedger == nil || netLedgerID != e.prevLedger.ID() {
		e.proposals = make(map[consensus.NodeID]*consensus.Proposal)
		e.disputes = make(map[consensus.TxID]*consensus.DisputedTx)
		e.converged = false
		e.haveCloseTimeConsensus = false
		if e.state != nil {
			e.state.CloseTimes.Peers = make(map[time.Time]int)
		}

		// Replay proposals matching the new ledger
		for nodeID, positions := range e.recentProposals {
			for _, p := range positions {
				if p.PreviousLedger == netLedgerID {
					trusted := e.adaptor.IsTrusted(nodeID)
					existing, exists := e.proposals[nodeID]
					if !exists || p.Position > existing.Position {
						e.proposals[nodeID] = p
					}
					if p.Position == 0 && trusted && e.state != nil {
						e.state.CloseTimes.Peers[p.CloseTime]++
					}
				}
			}
		}
	}

	// Step 3: Try to acquire the correct ledger.
	// First try by hash, then check if the adaptor's LCL has already been
	// updated (e.g., by inbound ledger adoption in the router).
	newLedger, err := e.adaptor.GetLedger(netLedgerID)
	if err != nil || newLedger == nil {
		if lcl, lclErr := e.adaptor.GetLastClosedLedger(); lclErr == nil && lcl != nil && lcl.ID() == netLedgerID {
			newLedger = lcl
			err = nil
		}
	}
	if err == nil && newLedger != nil {
		// Found — restart the round with the correct ledger AND flag
		// recovering=true so the engine enters ModeSwitchedLedger for
		// exactly one round. That mirrors rippled (Consensus.h:1107): a
		// node that just swapped its prior-ledger pointer suppresses its
		// own proposal and validation for the current round to avoid
		// poisoning convergence with stale-view gossip. On the NEXT
		// round (via acceptLedger auto-advance) a trusted validator is
		// promoted back to ModeProposing normally — so we still get
		// full participation, just not on the recovery round itself.
		slog.Info("Switching to network ledger",
			"seq", newLedger.Seq(),
			"hash", fmt.Sprintf("%x", netLedgerID[:8]),
		)
		e.prevLedger = newLedger
		e.wrongLedgerID = consensus.LedgerID{}
		if e.state != nil {
			e.state.HaveCorrectLCL = true
		}
		nextRound := consensus.RoundID{
			Seq:        newLedger.Seq() + 1,
			ParentHash: newLedger.ID(),
		}
		proposing := e.adaptor.IsValidator() &&
			e.adaptor.GetOperatingMode() == consensus.OpModeFull
		e.startRoundLocked(nextRound, proposing, true)
	} else {
		// Not found — enter wrong ledger mode and request from peers
		slog.Info("Cannot acquire network ledger, entering wrongLedger mode",
			"hash", fmt.Sprintf("%x", netLedgerID[:8]),
		)
		if e.state != nil {
			e.state.HaveCorrectLCL = false
		}
		e.wrongLedgerID = netLedgerID
		e.setMode(consensus.ModeWrongLedger)
		e.adaptor.RequestLedger(netLedgerID)
	}
}

// setMode changes the consensus mode.
func (e *Engine) setMode(newMode consensus.Mode) {
	if e.mode == newMode {
		return
	}

	oldMode := e.mode
	e.mode = newMode

	e.eventBus.Publish(&consensus.ModeChangedEvent{
		OldMode:   oldMode,
		NewMode:   newMode,
		Timestamp: e.adaptor.Now(),
	})

	e.adaptor.OnModeChange(oldMode, newMode)
}

// setPhase changes the consensus phase.
func (e *Engine) setPhase(newPhase consensus.Phase) {
	if e.phase == newPhase {
		return
	}

	oldPhase := e.phase
	e.phase = newPhase
	if e.state != nil {
		e.state.Phase = newPhase
		e.state.PhaseStart = e.adaptor.Now()
	}

	e.eventBus.Publish(&consensus.PhaseChangedEvent{
		Round:     e.state.Round,
		OldPhase:  oldPhase,
		NewPhase:  newPhase,
		Timestamp: e.adaptor.Now(),
	})

	e.adaptor.OnPhaseChange(oldPhase, newPhase)
}

// shouldCloseLedger checks whether the ledger should be closed now.
// Matches rippled's shouldCloseLedger() (Consensus.cpp:27-103).
func (e *Engine) shouldCloseLedger() bool {
	if e.prevLedger == nil {
		return false
	}
	openTime := time.Since(e.state.StartTime)
	timeSincePrevClose := e.adaptor.Now().Sub(e.prevLedger.CloseTime())

	// Sanity check: if timeSincePrevClose or prevRoundTime are unreasonable,
	// just close (matches rippled lines 52-64).
	if e.prevRoundTime < 0 || e.prevRoundTime > 10*time.Minute ||
		timeSincePrevClose > 10*time.Minute {
		return true
	}

	// Count how many trusted peers have already closed (sent proposals)
	proposersClosed := 0
	for nodeID := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			proposersClosed++
		}
	}

	// Count trusted validators that have validated our previous ledger.
	// Reads the PERSISTENT validation tracker (not the round-scoped
	// e.validations, which is reset at round start and so always zero
	// at the beginning of a round before any current-round validations
	// arrive). Matches rippled's adaptor_.proposersValidated() at
	// RCLConsensus.cpp:281 which reads the persistent Validations
	// store. Fixes the pre-R5.9 behavior where early-close peer
	// pressure from validations was invisible until mid-round.
	proposersValidated := 0
	if e.prevLedger != nil && e.validationTracker != nil {
		proposersValidated = e.validationTracker.ProposersValidated(e.prevLedger.ID())
	}

	// Peer pressure: if more than half of previous round's proposers
	// have already closed or validated, close immediately (matches rippled lines 67-73).
	if (proposersClosed + proposersValidated) > e.prevProposers/2 {
		return true
	}

	// No transactions: only close at the idle interval.
	// Matches rippled lines 75-80.
	anyTransactions := len(e.adaptor.GetPendingTxs()) > 0
	if !anyTransactions {
		return timeSincePrevClose >= e.timing.LedgerIdleInterval
	}

	// Preserve minimum ledger open time (matches rippled lines 83-88).
	if openTime < e.timing.LedgerMinClose {
		return false
	}

	// Don't close faster than half the previous round time,
	// so slower validators can keep up (matches rippled lines 93-98).
	if openTime < e.prevRoundTime/2 {
		return false
	}

	return true
}

// phaseOpen evaluates whether to close the ledger during the open phase.
// Called by timerEntry on each heartbeat. Matches rippled's phaseOpen()
// (Consensus.h:1168-1239).
// Caller must hold e.mu.
func (e *Engine) phaseOpen() {
	if e.shouldCloseLedger() {
		e.eventBus.Publish(&consensus.TimerFiredEvent{
			Timer:     consensus.TimerLedgerClose,
			Round:     e.state.Round,
			Timestamp: e.adaptor.Now(),
		})
		e.closeLedger()
	}
}

// closeLedger transitions from open to establish phase.
// Reference: rippled Consensus.h closeLedger() (~line 1434)
func (e *Engine) closeLedger() {
	// Build our transaction set from pending transactions
	txs := e.adaptor.GetPendingTxs()
	txSet, err := e.adaptor.BuildTxSet(txs)
	if err != nil {
		slog.Error("Failed to build tx set, falling back to empty set",
			"t", "Consensus",
			"round", e.state.Round,
			"pending_txs", len(txs),
			"err", err,
		)

		// Fall back to an empty tx set so consensus can still advance.
		txSet, err = e.adaptor.BuildTxSet(nil)
		if err != nil {
			slog.Error("Failed to build empty tx set, cannot close ledger",
				"t", "Consensus",
				"round", e.state.Round,
				"err", err,
			)
			e.setMode(consensus.ModeObserving)
			return
		}
	}
	e.ourTxSet = txSet

	// Use raw now — rippled sets rawCloseTimes_.self = now_ (Consensus.h:1441).
	// Rounding only happens later via effCloseTime() at acceptance.
	closeTime := e.adaptor.Now()
	e.state.CloseTimes.Self = closeTime

	// If proposing, create and broadcast our proposal
	if e.mode == consensus.ModeProposing {
		nodeID, err := e.adaptor.GetValidatorKey()
		if err == nil {
			proposal := &consensus.Proposal{
				Round:          e.state.Round,
				NodeID:         nodeID,
				Position:       0,
				TxSet:          txSet.ID(),
				CloseTime:      closeTime,
				PreviousLedger: e.prevLedger.ID(),
				Timestamp:      e.adaptor.Now(),
			}

			if err := e.adaptor.SignProposal(proposal); err == nil {
				e.state.OurPosition = proposal
				e.adaptor.BroadcastProposal(proposal)
			}
		}
	}

	// Move to establish phase
	e.setPhase(consensus.PhaseEstablish)
}

// phaseEstablish re-evaluates convergence during the establish phase.
// Called by timerEntry on each heartbeat. Matches rippled's phaseEstablish()
// (Consensus.h:1366-1430).
// Caller must hold e.mu.
func (e *Engine) phaseEstablish() {
	roundTime := time.Since(e.roundStartTime)

	// Hard timeout: force accept after LedgerMaxClose
	if roundTime >= e.timing.LedgerMaxClose {
		e.eventBus.Publish(&consensus.TimerFiredEvent{
			Timer:     consensus.TimerRoundTimeout,
			Round:     e.state.Round,
			Timestamp: e.adaptor.Now(),
		})
		e.acceptLedger(consensus.ResultTimeout)
		return
	}

	// Update positions and check convergence
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		e.updatePosition()
	}
	e.updateCloseTimePosition()
	e.checkConvergence()
}

// checkConvergence checks if proposals have converged.
func (e *Engine) checkConvergence() {
	if e.phase != consensus.PhaseEstablish {
		return
	}

	// Minimum time in establish phase before accepting consensus.
	// Matches rippled's checkConsensus(): currentAgreeTime <= ledgerMIN_CONSENSUS.
	if e.adaptor.Now().Sub(e.state.PhaseStart) <= e.timing.LedgerMinConsensus {
		return
	}

	// Count proposals for each tx set. We include ourselves as a
	// participant when proposing, mirroring rippled's checkConsensus
	// (agreeing/total both include count_self). Without this, a 3-node
	// UNL whose threshold uses quorum=3 can never converge: peers
	// contribute at most 2 trusted proposals, the winning set never
	// reaches 3, and every round hits LedgerMaxClose (10s timeout).
	txSetCounts := make(map[consensus.TxSetID]int)
	trustedProposals := 0

	for nodeID, proposal := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			txSetCounts[proposal.TxSet]++
			trustedProposals++
		}
	}

	// Add our own position to the count if we're a proposing validator.
	if e.mode == consensus.ModeProposing && e.ourTxSet != nil {
		txSetCounts[e.ourTxSet.ID()]++
		trustedProposals++
	}

	// Round-level convergence is a percentage of participants, not a
	// validation-quorum check. Rippled's LedgerMaster::checkAccept and
	// round consensus are separate gates — conflating them here pinned
	// the threshold at validation-quorum (3) even when that exceeded
	// the available participants. Use pure percentage of participants.
	threshold := (trustedProposals*e.thresholds.MinConsensusPct + 99) / 100
	if threshold < 1 {
		threshold = 1
	}

	for txSetID, count := range txSetCounts {
		if count >= threshold {
			e.converged = true
			e.state.Converged = true

			// If it's not our tx set, we should adopt it
			if e.ourTxSet == nil || e.ourTxSet.ID() != txSetID {
				// Request the winning tx set if we don't have it
				e.adaptor.RequestTxSet(txSetID)
			}

			// Check if we have TX consensus
			if count >= (trustedProposals*e.thresholds.MaxConsensusPct)/100 {
				// Also need close time consensus before accepting
				// (matching rippled Consensus.h:1406-1411)
				if !e.haveCloseTimeConsensus {
					// Update close time position — this may establish CT consensus.
					e.updateCloseTimePosition()
					if !e.haveCloseTimeConsensus {
						return // Keep going until CT consensus is reached
					}
				}
				e.acceptLedger(consensus.ResultSuccess)
			}
			return
		}
	}

	// Update our position (tx set + close time) if proposing and not converged
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		e.updatePosition()
	}

	// Always update close time position during establish phase
	e.updateCloseTimePosition()
}

// updatePosition updates our proposal position based on peer proposals.
func (e *Engine) updatePosition() {
	// Find the most popular tx set among trusted validators
	txSetCounts := make(map[consensus.TxSetID]int)
	for nodeID, proposal := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			txSetCounts[proposal.TxSet]++
		}
	}

	var bestTxSet consensus.TxSetID
	bestCount := 0
	for txSetID, count := range txSetCounts {
		if count > bestCount {
			bestTxSet = txSetID
			bestCount = count
		}
	}

	// If the best is different from ours, consider changing
	if e.ourTxSet != nil && bestTxSet != e.ourTxSet.ID() && bestCount > len(e.proposals)/2 {
		// Adopt the popular tx set
		txSet, err := e.adaptor.GetTxSet(bestTxSet)
		if err == nil {
			e.ourTxSet = txSet

			// Broadcast new position
			nodeID, _ := e.adaptor.GetValidatorKey()
			proposal := &consensus.Proposal{
				Round:          e.state.Round,
				NodeID:         nodeID,
				Position:       e.state.OurPosition.Position + 1,
				TxSet:          txSet.ID(),
				CloseTime:      e.state.OurPosition.CloseTime,
				PreviousLedger: e.prevLedger.ID(),
				Timestamp:      e.adaptor.Now(),
			}

			if err := e.adaptor.SignProposal(proposal); err == nil {
				e.state.OurPosition = proposal
				e.adaptor.BroadcastProposal(proposal)
			}
		}
	}
}

// acceptLedger finalizes consensus and accepts the new ledger.
func (e *Engine) acceptLedger(result consensus.Result) {
	if e.phase != consensus.PhaseEstablish {
		return
	}

	// Determine winning close time and apply effCloseTime
	rawCloseTime := e.determineCloseTime()
	resolution := e.adaptor.CloseTimeResolution()
	priorClose := e.prevLedger.CloseTime()
	closeTime := effCloseTime(rawCloseTime, resolution, priorClose)

	slog.Debug("acceptLedger close time",
		"seq", e.prevLedger.Seq()+1,
		"mode", e.mode,
		"raw_ct", rawCloseTime.Unix()-946684800,
		"eff_ct", closeTime.Unix()-946684800,
		"prior_ct", priorClose.Unix()-946684800,
		"resolution", resolution,
		"proposers", len(e.proposals),
		"has_position", e.state.OurPosition != nil,
		"ct_consensus", e.haveCloseTimeConsensus,
	)

	// Get the agreed transaction set
	var txSet consensus.TxSet
	if e.ourTxSet != nil {
		txSet = e.ourTxSet
	} else {
		// Find most popular among trusted
		txSetCounts := make(map[consensus.TxSetID]int)
		for nodeID, proposal := range e.proposals {
			if e.adaptor.IsTrusted(nodeID) {
				txSetCounts[proposal.TxSet]++
			}
		}

		var bestID consensus.TxSetID
		bestCount := 0
		for id, count := range txSetCounts {
			if count > bestCount {
				bestID = id
				bestCount = count
			}
		}

		var err error
		txSet, err = e.adaptor.GetTxSet(bestID)
		if err != nil {
			return
		}
	}

	// Build the new ledger
	newLedger, err := e.adaptor.BuildLedger(e.prevLedger, txSet, closeTime)
	if err != nil {
		return
	}

	// Validate and store
	if err := e.adaptor.ValidateLedger(newLedger); err != nil {
		return
	}

	if err := e.adaptor.StoreLedger(newLedger); err != nil {
		return
	}

	// Emit consensus reached event
	e.eventBus.Publish(&consensus.ConsensusReachedEvent{
		Round:     e.state.Round,
		TxSet:     txSet.ID(),
		CloseTime: closeTime,
		Proposers: len(e.proposals),
		Result:    result,
		// StartTime is wall-clock (see startRoundLocked); use time.Since
		// to keep the pair balanced rather than mixing offset-adjusted
		// adaptor.Now() against it.
		Duration:  time.Since(e.state.StartTime),
		Timestamp: e.adaptor.Now(),
	})

	// Emit our validation whenever we're a validator AND we're not on
	// a confirmed-wrong ledger. Rippled RCLConsensus.cpp:587-594 calls
	// validate(built, result.txns, proposing) whenever validating_ is
	// true AND !consensusFail AND canValidateSeq — regardless of mode.
	// The `proposing` flag is passed INTO validate() and only controls
	// whether vfFullValidation is set inside the validation, not
	// whether the validation is sent at all.
	//
	// So switchedLedger rounds emit a PARTIAL validation (Full=false):
	// they attest "I saw this ledger close" without claiming "I drove
	// the tx-set". Rippled peers rely on partial validations as an
	// early liveness signal before full quorum materializes. My prior
	// blanket suppression was stricter than rippled and left recovery
	// rounds invisible to the network.
	if e.adaptor.IsValidator() && e.mode != consensus.ModeWrongLedger {
		e.sendValidation(newLedger)
	}

	// Collect validations
	var validations []*consensus.Validation
	for _, v := range e.validations {
		if v.LedgerID == newLedger.ID() {
			validations = append(validations, v)
		}
	}

	// Notify adaptor
	e.adaptor.OnConsensusReached(newLedger, validations)

	// Emit ledger accepted event
	e.eventBus.Publish(&consensus.LedgerAcceptedEvent{
		LedgerID:    newLedger.ID(),
		LedgerSeq:   newLedger.Seq(),
		TxCount:     txSet.Size(),
		CloseTime:   closeTime,
		Validations: len(validations),
		Timestamp:   e.adaptor.Now(),
	})

	// Adjust our clock toward the network's close time average.
	// Matches rippled's adjustCloseTime() in RCLConsensus.cpp:694-732.
	if e.mode == consensus.ModeProposing || e.mode == consensus.ModeObserving {
		e.adaptor.AdjustCloseTime(e.state.CloseTimes)
	}

	// Refresh the ValidationTracker's trusted set on every accept.
	// Amendments and negative-UNL updates can mutate the UNL across
	// ledger boundaries; re-pulling both from the adaptor keeps the
	// tracker in sync without requiring callers to invalidate by hand.
	// Also advance the minSeq floor so far-stale validations get
	// rejected at the Add() gate rather than being filtered out in
	// checkFullValidation every pass.
	if e.validationTracker != nil {
		e.validationTracker.SetTrusted(e.adaptor.GetTrustedValidators())
		e.validationTracker.SetQuorum(e.adaptor.GetQuorum())
		// Pull the negative-UNL from the just-accepted ledger so
		// validations from temporarily-disabled validators are excluded
		// from quorum. Rippled's checkAccept consults the same SLE per
		// ledger. Without this call, SetNegativeUNL is unreachable from
		// production code and the negUNL filter is dead.
		e.validationTracker.SetNegativeUNL(e.adaptor.GetNegativeUNL())
		if newLedger.Seq() > 128 {
			// Keep a small history window so late validations for the
			// just-accepted ledger still count.
			e.validationTracker.SetMinSeq(newLedger.Seq() - 128)
		}
	}

	// Track round time for convergePercent calculation
	e.prevRoundTime = time.Since(e.roundStartTime)

	// Track trusted proposer count for peer pressure in next round
	trustedCount := 0
	for nodeID := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			trustedCount++
		}
	}
	e.prevProposers = trustedCount

	// Update state for next round
	e.prevLedger = newLedger
	e.validations = make(map[consensus.NodeID]*consensus.Validation)
	e.consensusCount++

	// Move to accepted phase
	e.setPhase(consensus.PhaseAccepted)

	// Only auto-advance to the next round if we're in Full mode.
	// If not Full, the router will keep re-adopting until caught up,
	// then transition to Full, at which point checkAndStartRound kicks in.
	if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
		// Auto-advance to the next round after a successful accept. Not
		// a recovery — if the previous round WAS a switchedLedger round,
		// this advancement is exactly where we promote back to
		// ModeProposing (recovering=false means startRoundLocked picks
		// Proposing for a trusted validator in OpModeFull).
		proposing := e.adaptor.IsValidator()
		nextRound := consensus.RoundID{
			Seq:        newLedger.Seq() + 1,
			ParentHash: newLedger.ID(),
		}
		e.startRoundLocked(nextRound, proposing, false)
	}
}

// updateCloseTimePosition counts close time votes from peer proposals,
// applies avalanche thresholds, and updates our proposal's close time
// to match the consensus. Matches rippled's updateOurPositions() close
// time logic (Consensus.h:1507-1634).
func (e *Engine) updateCloseTimePosition() {
	resolution := e.adaptor.CloseTimeResolution()

	// Count close time votes from current trusted proposals, rounding
	// each via roundCloseTime (matching rippled's asCloseTime).
	closeTimeVotes := make(map[time.Time]int)
	participants := 0
	for nodeID, proposal := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			rounded := roundCloseTime(proposal.CloseTime, resolution)
			closeTimeVotes[rounded]++
			participants++
		}
	}

	if participants == 0 {
		e.haveCloseTimeConsensus = true // trivially
		return
	}

	// Add our own vote if proposing
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		ourRounded := roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		closeTimeVotes[ourRounded]++
		participants++
	}

	// Determine threshold from avalanche state
	neededWeight := e.getCloseTimeNeededWeight()
	threshVote := participantsNeeded(participants, neededWeight)
	threshConsensus := participantsNeeded(participants, 75) // avCT_CONSENSUS_PCT

	// Find winning close time
	var consensusCloseTime time.Time
	e.haveCloseTimeConsensus = false
	for t, count := range closeTimeVotes {
		if count >= threshVote {
			consensusCloseTime = t
			threshVote = count // raise bar to pick the MOST popular
			if count >= threshConsensus {
				e.haveCloseTimeConsensus = true
			}
		}
	}

	// Update our proposal if close time changed
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil && !consensusCloseTime.IsZero() {
		ourRounded := roundCloseTime(e.state.OurPosition.CloseTime, resolution)
		if consensusCloseTime != ourRounded {
			e.state.OurPosition.CloseTime = consensusCloseTime
			e.state.OurPosition.Position++
			e.state.OurPosition.Timestamp = e.adaptor.Now()
			if err := e.adaptor.SignProposal(e.state.OurPosition); err == nil {
				e.adaptor.BroadcastProposal(e.state.OurPosition)
			}
		}
	}
}

// getCloseTimeNeededWeight returns the minimum vote percentage for close time
// based on the avalanche state machine. Matches rippled's getNeededWeight()
// in ConsensusParms.h:172-199.
func (e *Engine) getCloseTimeNeededWeight() int {
	pct := e.convergePercent()
	switch e.closeTimeAvalancheState {
	case avalancheInit:
		if pct >= 0 {
			e.closeTimeAvalancheState = avalancheMid
		}
		return 50
	case avalancheMid:
		if pct >= 50 {
			e.closeTimeAvalancheState = avalancheLate
		}
		return 65
	case avalancheLate:
		if pct >= 85 {
			e.closeTimeAvalancheState = avalancheStuck
		}
		return 70
	case avalancheStuck:
		return 95
	}
	return 50
}

// convergePercent returns how far through the establish phase we are,
// as a percentage of the previous round time (min 5s).
// Matches rippled's convergePercent_ calculation.
func (e *Engine) convergePercent() int {
	elapsed := time.Since(e.roundStartTime)
	prevRound := e.prevRoundTime
	if prevRound < 5*time.Second {
		prevRound = 5 * time.Second
	}
	return int(elapsed * 100 / prevRound)
}

// participantsNeeded computes the minimum number of participants required
// to meet a given percentage threshold. Matches rippled's participantsNeeded().
func participantsNeeded(participants, percent int) int {
	result := (participants*percent + percent/2) / 100
	if result == 0 {
		return 1
	}
	return result
}

// determineCloseTime returns the consensus close time.
// Uses the close time that was converged on by updateCloseTimePosition().
// If we have a consensus position with a non-zero close time, use it.
// For observers (no position), use the most popular peer close time
// ROUNDED to the current resolution — matching rippled where all nodes
// (proposers and observers) use rounded consensus values.
func (e *Engine) determineCloseTime() time.Time {
	// If we have a position (from updateCloseTimePosition convergence), use its close time.
	// This is already rounded by updateCloseTimePosition().
	if e.state.OurPosition != nil && !e.state.OurPosition.CloseTime.IsZero() {
		return e.state.OurPosition.CloseTime
	}

	resolution := e.adaptor.CloseTimeResolution()

	// For observers: use the most popular peer close time from proposals,
	// but ROUND it to the resolution before returning. CloseTimes.Peers
	// stores raw times; rippled rounds before voting (asCloseTime), so
	// we must round here to match.
	if len(e.state.CloseTimes.Peers) > 0 {
		// Vote on rounded times (matching rippled's updateOurPositions)
		roundedVotes := make(map[time.Time]int)
		for t, count := range e.state.CloseTimes.Peers {
			rounded := roundCloseTime(t, resolution)
			roundedVotes[rounded] += count
		}

		var bestTime time.Time
		bestCount := 0
		for t, count := range roundedVotes {
			if count > bestCount {
				bestTime = t
				bestCount = count
			}
		}
		if bestCount > 0 {
			return bestTime
		}
	}

	return roundCloseTime(e.state.CloseTimes.Self, resolution)
}

// isVotingLedger reports whether a validation for this ledger should
// carry fee-vote and amendment-vote fields. Matches rippled
// Ledger.cpp:951-953: a flag ledger is one whose sequence is 1 less
// than a multiple of 256 (i.e., (seq+1) % 256 == 0). The validation
// for that ledger carries the vote for the next flag cycle.
func isVotingLedger(ledgerSeq uint32) bool {
	return (ledgerSeq+1)%256 == 0
}

// sendValidation creates and broadcasts a validation.
//
// The Full flag on the emitted validation reflects whether we were
// actively PROPOSING this round. Rippled sets vfFullValidation iff
// mode == proposing (RCLConsensus.cpp:849-851); switchedLedger and
// observing emit the same frame with the bit cleared (partial
// validation). Partial validations are accepted by peers but don't
// count toward quorum (LedgerMaster.cpp:886 filters Full=false out of
// the trusted count).
func (e *Engine) sendValidation(ledger consensus.Ledger) {
	nodeID, err := e.adaptor.GetValidatorKey()
	if err != nil {
		return
	}

	full := e.mode == consensus.ModeProposing

	// Compute SignTime under a monotonic floor. If the adaptor clock
	// regresses (NTP step, leap-second correction, VM pause/resume) the
	// emitted SignTime could be older than the prior validation from
	// this node, so peers would reject it as stale. Bump to
	// lastSignTime + 1s in that case to preserve monotonicity. Matches
	// rippled RCLConsensus.cpp:825-828. SeenTime mirrors SignTime (as
	// before) so the two remain equal on emission.
	signTime := e.adaptor.Now()
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
		// R6b.5b: emit local load_fee (sfLoadFee) — rippled
		// RCLConsensus.cpp:851 always populates this under
		// HardenedValidations. Zero means "no load info",
		// serializer omits the field.
		LoadFee: e.adaptor.GetLoadFee(),
	}

	// B1: sfCookie and sfServerVersion are scoped inside rippled's
	// `if (rules().enabled(featureHardenedValidations))` block at
	// RCLConsensus.cpp:853-867. Before HV is active (pre-2020 on
	// mainnet, any modern testnet/standalone on old rules) peers
	// reject validations that carry these fields because the preimage
	// they compute for signature verification omits them. sfCookie
	// emits on every HV-enabled validation; sfServerVersion emits
	// ONLY on voting ledgers within the same block (cpp:864-866 —
	// "Report our server version every flag ledger").
	if e.adaptor.IsFeatureEnabled("HardenedValidations") {
		cookie := e.adaptor.GetCookie()
		if cookie == 0 {
			slog.Warn("sendValidation: cookie is zero under HardenedValidations — adaptor must generate one at boot; emitting without cookie")
		}
		validation.Cookie = cookie

		if isVotingLedger(ledger.Seq()) {
			serverVersion := e.adaptor.GetServerVersion()
			if serverVersion == 0 {
				slog.Warn("sendValidation: serverVersion is zero on voting ledger under HardenedValidations — adaptor must advertise a build tag; emitting without serverVersion")
			}
			validation.ServerVersion = serverVersion
		}
	}

	// Fee vote + amendment vote emission is gated on isVotingLedger.
	// Rippled emits these ONLY on flag ledgers — the validation signed
	// for ledger seq covers the transition to seq+1; on the flag
	// boundary (seq+1)%256 == 0) we attach the vote for the next
	// flag-ledger cycle. Emitting on every ledger inflates bandwidth
	// ~256× and confuses peer aggregators that accept these fields
	// only on the expected boundary. Matches
	// Ledger.cpp:951-953 isVotingLedger + RCLConsensus.cpp:879.
	if isVotingLedger(ledger.Seq()) {
		// Fee vote: emit the AMOUNT triple under post-XRPFees rules, the
		// legacy UINT triple otherwise. Rippled's FeeVoteImpl.cpp:120-192
		// is a hard if/else on featureXRPFees; the adaptor's postXRPFees
		// flag mirrors that decision so the two paths never co-emit.
		// Zero values from the adaptor mean "no vote" and the serializer
		// omits the fields.
		if baseFee, reserveBase, reserveIncrement, postXRPFees := e.adaptor.GetFeeVote(); baseFee != 0 || reserveBase != 0 || reserveIncrement != 0 {
			if postXRPFees {
				validation.BaseFeeDrops = baseFee
				validation.ReserveBaseDrops = reserveBase
				validation.ReserveIncrementDrops = reserveIncrement
			} else {
				validation.BaseFee = baseFee
				validation.ReserveBase = uint32(reserveBase)
				validation.ReserveIncrement = uint32(reserveIncrement)
			}
		}

		// Amendment vote — populated alongside fee vote on flag
		// ledgers only. See R5.3. Adaptor returns nil when there is
		// no vote to cast (non-validators, empty stance, all
		// amendments already enabled).
		validation.Amendments = e.adaptor.GetAmendmentVote()
	}

	// Tie the validation to the tx-set we converged on, so peers can
	// tie-break between concurrent same-seq ledgers with different tx
	// sets. Rippled's STValidation always includes this when available;
	// we only have it when we actually produced a proposal this round
	// (observers that didn't propose can legitimately omit it).
	if e.ourTxSet != nil {
		setID := e.ourTxSet.ID()
		copy(validation.ConsensusHash[:], setID[:])
	}

	// Attach the most-recent fully-validated LCL hash we know about.
	// Rippled emits sfValidatedHash ONLY under featureHardenedValidations
	// (RCLConsensus.cpp:853). On mainnet that amendment has been active
	// since 2020 so this is always true; on testnet/standalone a node
	// running against pre-HardenedValidations rules must omit the field
	// or peers on the old rules reject the validation as malformed.
	// GetValidatedLedgerHash returns the zero LedgerID on a node that
	// hasn't yet crossed quorum — in that case we also skip emission.
	if e.adaptor.IsFeatureEnabled("HardenedValidations") {
		if vh := e.adaptor.GetValidatedLedgerHash(); vh != (consensus.LedgerID{}) {
			copy(validation.ValidatedHash[:], vh[:])
		}
	}

	if err := e.adaptor.SignValidation(validation); err != nil {
		return
	}

	e.adaptor.BroadcastValidation(validation)

	// Feed our own validation into the tracker. Only Full validations
	// are accepted by the tracker's Full-gate, so a partial
	// switchedLedger emission is automatically excluded from our own
	// quorum view — matching rippled's behavior where switchedLedger
	// partials don't count toward local checkAccept. In a
	// 1-validator standalone setup the Full path crosses the
	// threshold immediately and fires OnLedgerFullyValidated.
	if e.validationTracker != nil {
		e.validationTracker.Add(validation)
	}
}

// roundCloseTime rounds a close time to the nearest multiple of resolution.
// Rounds up if the close time is at the midpoint.
// Reference: rippled LedgerTiming.h roundCloseTime()
func roundCloseTime(closeTime time.Time, resolution time.Duration) time.Time {
	if closeTime.IsZero() {
		return closeTime
	}
	// Add half the resolution for rounding
	adjusted := closeTime.Add(resolution / 2)
	// Truncate to the nearest resolution boundary using Unix seconds
	epoch := adjusted.Unix()
	resSec := int64(resolution.Seconds())
	if resSec <= 0 {
		return closeTime
	}
	return time.Unix(epoch-(epoch%resSec), 0).UTC()
}

// effCloseTime calculates the effective ledger close time.
// After rounding to the close time resolution, ensures the result is
// at least 1 second after the prior ledger's close time.
// Reference: rippled LedgerTiming.h effCloseTime()
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
