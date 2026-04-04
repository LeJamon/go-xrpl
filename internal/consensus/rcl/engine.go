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

	// Validation tracking
	validations map[consensus.NodeID]*consensus.Validation

	// Dispute tracking
	disputes map[consensus.TxID]*consensus.DisputedTx

	// Timers
	closeTimer   *time.Timer
	timeoutTimer *time.Timer

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
	return e.startRoundLocked(round, proposing)
}

// startRoundLocked is the lock-free inner implementation of StartRound.
// Caller must hold e.mu.
func (e *Engine) startRoundLocked(round consensus.RoundID, proposing bool) error {
	// Determine mode
	if proposing && e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull {
		e.setMode(consensus.ModeProposing)
	} else {
		e.setMode(consensus.ModeObserving)
	}

	// Initialize round state
	e.state = &consensus.RoundState{
		Round:          round,
		Mode:           e.mode,
		Phase:          consensus.PhaseOpen,
		Proposals:      make(map[consensus.NodeID]*consensus.Proposal),
		Disputed:       make(map[consensus.TxID]*consensus.DisputedTx),
		CloseTimes:     consensus.CloseTimes{Peers: make(map[time.Time]int)},
		StartTime:      e.adaptor.Now(),
		PhaseStart:     e.adaptor.Now(),
		HaveCorrectLCL: true,
	}

	// Reset tracking maps
	e.proposals = make(map[consensus.NodeID]*consensus.Proposal)
	e.disputes = make(map[consensus.TxID]*consensus.DisputedTx)
	e.converged = false
	e.ourTxSet = nil
	e.haveCloseTimeConsensus = false
	e.closeTimeAvalancheState = avalancheInit
	e.roundStartTime = e.adaptor.Now()

	// Set phase
	e.setPhase(consensus.PhaseOpen)

	// Emit event
	e.eventBus.Publish(&consensus.RoundStartedEvent{
		Round:     round,
		Mode:      e.mode,
		Timestamp: e.adaptor.Now(),
	})

	// Start close timer
	e.startCloseTimer()

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
				if e.closeTimer != nil {
					e.closeTimer.Stop()
				}
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

// OnProposal handles an incoming proposal from a peer.
func (e *Engine) OnProposal(proposal *consensus.Proposal) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Verify signature first (before buffering).
	if err := e.adaptor.VerifyProposal(proposal); err != nil {
		return fmt.Errorf("invalid proposal signature: %w", err)
	}

	// Always buffer proposals for future playback, even between rounds.
	// Matches rippled's recentPeerPositions_ (Consensus.h:629).
	// Keep max 5 per node to limit memory.
	positions := e.recentProposals[proposal.NodeID]
	if len(positions) >= 5 {
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

	// Relay to other peers
	if trusted {
		e.adaptor.RelayProposal(proposal)
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

// OnValidation handles an incoming validation from a peer.
func (e *Engine) OnValidation(validation *consensus.Validation) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Verify signature
	if err := e.adaptor.VerifyValidation(validation); err != nil {
		return fmt.Errorf("invalid validation signature: %w", err)
	}

	// Check if from trusted validator
	trusted := e.adaptor.IsTrusted(validation.NodeID)

	// Store validation
	e.validations[validation.NodeID] = validation

	// Emit event
	e.eventBus.Publish(&consensus.ValidationReceivedEvent{
		Validation: validation,
		Trusted:    trusted,
		Timestamp:  e.adaptor.Now(),
	})

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
			e.startRoundLocked(nextRound, false)
			e.setMode(consensus.ModeSwitchedLedger)
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

// run is the main consensus loop.
func (e *Engine) run() {
	defer e.wg.Done()

	for {
		select {
		case <-e.ctx.Done():
			return
		default:
			if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
				e.checkLedgerLocked() // Detect wrong chain (like rippled's timerEntry)
				e.checkAndStartRound()
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// checkAndStartRound checks if we should start a new round.
// This serves as a fallback in case the auto-advance in acceptLedger
// didn't trigger (e.g., first round after startup).
func (e *Engine) checkAndStartRound() {
	e.mu.Lock()
	defer e.mu.Unlock()

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

	// Start the round
	round := consensus.RoundID{
		Seq:        ledger.Seq() + 1,
		ParentHash: ledger.ID(),
	}
	e.startRoundLocked(round, proposing)
}

// checkLedgerLocked acquires the lock and calls checkLedger.
// Only runs during active consensus phases (Open/Establish).
// Matches rippled's timerEntry() calling checkLedger() on every tick.
func (e *Engine) checkLedgerLocked() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.phase == consensus.PhaseAccepted {
		return
	}
	e.checkLedger()
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
		// Found — restart the round with the correct ledger
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
		e.startRoundLocked(nextRound, false) // observe first round after switch
		e.setMode(consensus.ModeSwitchedLedger)
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
		// Stop timers to prevent closing/accepting while on wrong chain
		if e.closeTimer != nil {
			e.closeTimer.Stop()
		}
		if e.timeoutTimer != nil {
			e.timeoutTimer.Stop()
		}
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
	// Matches rippled's adaptor_.proposersValidated(prevLedgerID_).
	proposersValidated := 0
	if e.prevLedger != nil {
		prevID := e.prevLedger.ID()
		for nodeID, v := range e.validations {
			if e.adaptor.IsTrusted(nodeID) && v.LedgerID == prevID {
				proposersValidated++
			}
		}
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

// startCloseTimer starts the timer for evaluating ledger close.
// Uses min(LedgerMinClose, 1s) matching rippled's heartbeat timer that calls
// timerEntry() → phaseOpen() → shouldCloseLedger(). This ensures peer
// pressure (proposersClosed + proposersValidated) is evaluated early,
// while shouldCloseLedger() still enforces LedgerMinClose for non-peer-
// pressure closes.
func (e *Engine) startCloseTimer() {
	if e.closeTimer != nil {
		e.closeTimer.Stop()
	}

	interval := time.Second
	if e.timing.LedgerMinClose < interval {
		interval = e.timing.LedgerMinClose
	}

	e.closeTimer = time.AfterFunc(interval, func() {
		e.onCloseTimer()
	})
}

// onCloseTimer handles the close timer firing.
func (e *Engine) onCloseTimer() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.phase != consensus.PhaseOpen {
		return
	}

	if e.shouldCloseLedger() {
		e.eventBus.Publish(&consensus.TimerFiredEvent{
			Timer:     consensus.TimerLedgerClose,
			Round:     e.state.Round,
			Timestamp: e.adaptor.Now(),
		})

		// Close the ledger and move to establish phase
		e.closeLedger()
	} else {
		// Reschedule check in 100ms
		e.closeTimer = time.AfterFunc(100*time.Millisecond, func() {
			e.onCloseTimer()
		})
	}
}

// closeLedger transitions from open to establish phase.
func (e *Engine) closeLedger() {
	// Build our transaction set from pending transactions
	txs := e.adaptor.GetPendingTxs()
	txSet, err := e.adaptor.BuildTxSet(txs)
	if err != nil {
		// TODO: handle error
		return
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

	// Start timeout timer
	e.startTimeoutTimer()
}

// startTimeoutTimer starts timers for the establish phase:
// a periodic heartbeat (matching rippled's timerEntry calling
// phaseEstablish) and a hard timeout at LedgerMaxClose.
func (e *Engine) startTimeoutTimer() {
	if e.timeoutTimer != nil {
		e.timeoutTimer.Stop()
	}

	// Periodic heartbeat to re-evaluate convergence during establish phase.
	// Matches rippled's timerEntry() → phaseEstablish() on every heartbeat.
	// Use min(LedgerMinClose, 1s) so tests with fast timing still work.
	interval := time.Second
	if e.timing.LedgerMinClose < interval {
		interval = e.timing.LedgerMinClose
	}
	e.timeoutTimer = time.AfterFunc(interval, func() {
		e.onEstablishTimer()
	})
}

// onEstablishTimer periodically re-evaluates convergence during establish phase.
func (e *Engine) onEstablishTimer() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.phase != consensus.PhaseEstablish {
		return
	}

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

	// Reschedule if still in establish phase
	if e.phase == consensus.PhaseEstablish {
		interval := time.Second
		if e.timing.LedgerMinClose < interval {
			interval = e.timing.LedgerMinClose
		}
		e.timeoutTimer = time.AfterFunc(interval, func() {
			e.onEstablishTimer()
		})
	}
}

// checkConvergence checks if proposals have converged.
func (e *Engine) checkConvergence() {
	if e.phase != consensus.PhaseEstablish {
		return
	}

	// Count proposals for each tx set
	txSetCounts := make(map[consensus.TxSetID]int)
	trustedProposals := 0

	for nodeID, proposal := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			txSetCounts[proposal.TxSet]++
			trustedProposals++
		}
	}

	// Check if any tx set has enough support
	quorum := e.adaptor.GetQuorum()
	threshold := (trustedProposals * e.thresholds.MinConsensusPct) / 100

	if threshold < quorum {
		threshold = quorum
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
		Duration:  e.adaptor.Now().Sub(e.state.StartTime),
		Timestamp: e.adaptor.Now(),
	})

	// If validator, send validation
	if e.adaptor.IsValidator() {
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
		proposing := e.adaptor.IsValidator()
		nextRound := consensus.RoundID{
			Seq:        newLedger.Seq() + 1,
			ParentHash: newLedger.ID(),
		}
		e.startRoundLocked(nextRound, proposing)
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

// sendValidation creates and broadcasts a validation.
func (e *Engine) sendValidation(ledger consensus.Ledger) {
	nodeID, err := e.adaptor.GetValidatorKey()
	if err != nil {
		return
	}

	validation := &consensus.Validation{
		LedgerID:  ledger.ID(),
		LedgerSeq: ledger.Seq(),
		NodeID:    nodeID,
		SignTime:  e.adaptor.Now(),
		SeenTime:  e.adaptor.Now(),
		Full:      true,
	}

	if err := e.adaptor.SignValidation(validation); err != nil {
		return
	}

	e.adaptor.BroadcastValidation(validation)
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
