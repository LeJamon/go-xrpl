// Package rcl implements the Ripple Consensus Ledger algorithm.
// This is the default consensus algorithm used by the XRP Ledger.
package rcl

import (
	"context"
	"fmt"
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

	// Stats
	roundCount     uint64
	consensusCount uint64
}

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
		timing:      config.Timing,
		thresholds:  config.Thresholds,
		adaptor:     adaptor,
		eventBus:    consensus.NewEventBus(100),
		mode:        consensus.ModeObserving,
		phase:       consensus.PhaseAccepted,
		proposals:   make(map[consensus.NodeID]*consensus.Proposal),
		validations: make(map[consensus.NodeID]*consensus.Validation),
		disputes:    make(map[consensus.TxID]*consensus.DisputedTx),
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

	e.roundCount++
	return nil
}

// OnProposal handles an incoming proposal from a peer.
func (e *Engine) OnProposal(proposal *consensus.Proposal) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Verify signature
	if err := e.adaptor.VerifyProposal(proposal); err != nil {
		return fmt.Errorf("invalid proposal signature: %w", err)
	}

	// Check if from trusted validator
	trusted := e.adaptor.IsTrusted(proposal.NodeID)

	// Store proposal
	existing, exists := e.proposals[proposal.NodeID]
	if !exists || proposal.Position > existing.Position {
		e.proposals[proposal.NodeID] = proposal
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
		// Try to get the ledger
		l, err := e.adaptor.GetLedger(id)
		if err == nil && l != nil {
			e.prevLedger = l
			e.state.HaveCorrectLCL = true
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
			// Check operating mode
			if e.adaptor.GetOperatingMode() == consensus.OpModeFull {
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

	// Only start if in accepted phase
	if e.phase != consensus.PhaseAccepted {
		return
	}

	// Get current ledger
	ledger, err := e.adaptor.GetLastClosedLedger()
	if err != nil {
		return
	}

	// Check if it's time for a new round
	timeSinceClose := e.adaptor.Now().Sub(ledger.CloseTime())
	if timeSinceClose < e.timing.LedgerIdleInterval {
		return
	}

	// Determine if we should propose
	proposing := e.adaptor.IsValidator() && e.adaptor.GetOperatingMode() == consensus.OpModeFull

	// Start the round
	round := consensus.RoundID{
		Seq:        ledger.Seq() + 1,
		ParentHash: ledger.ID(),
	}
	e.startRoundLocked(round, proposing)
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

// startCloseTimer starts the timer for closing the ledger.
func (e *Engine) startCloseTimer() {
	if e.closeTimer != nil {
		e.closeTimer.Stop()
	}

	e.closeTimer = time.AfterFunc(e.timing.LedgerMinClose, func() {
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

	e.eventBus.Publish(&consensus.TimerFiredEvent{
		Timer:     consensus.TimerLedgerClose,
		Round:     e.state.Round,
		Timestamp: e.adaptor.Now(),
	})

	// Close the ledger and move to establish phase
	e.closeLedger()
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

// roundCloseTime calculates the close time for this round.
func (e *Engine) roundCloseTime() time.Time {
	now := e.adaptor.Now()
	resolution := e.adaptor.CloseTimeResolution()

	// Round to the nearest resolution
	rounded := now.Truncate(resolution)
	if now.Sub(rounded) > resolution/2 {
		rounded = rounded.Add(resolution)
	}

	return rounded
}

// startTimeoutTimer starts the timeout timer for the establish phase.
func (e *Engine) startTimeoutTimer() {
	if e.timeoutTimer != nil {
		e.timeoutTimer.Stop()
	}

	e.timeoutTimer = time.AfterFunc(e.timing.LedgerMaxClose, func() {
		e.onTimeoutTimer()
	})
}

// onTimeoutTimer handles the timeout timer firing.
func (e *Engine) onTimeoutTimer() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.phase != consensus.PhaseEstablish {
		return
	}

	e.eventBus.Publish(&consensus.TimerFiredEvent{
		Timer:     consensus.TimerRoundTimeout,
		Round:     e.state.Round,
		Timestamp: e.adaptor.Now(),
	})

	// Force consensus with what we have
	e.acceptLedger(consensus.ResultTimeout)
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

			// Check if we have consensus
			if count >= (trustedProposals*e.thresholds.MaxConsensusPct)/100 {
				e.acceptLedger(consensus.ResultSuccess)
			}
			return
		}
	}

	// Update our position if proposing and not converged
	if e.mode == consensus.ModeProposing && e.state.OurPosition != nil {
		e.updatePosition()
	}
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
	closeTime := e.determineCloseTime()
	resolution := e.adaptor.CloseTimeResolution()
	closeTime = effCloseTime(closeTime, resolution, e.prevLedger.CloseTime())

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

// determineCloseTime determines the consensus close time.
func (e *Engine) determineCloseTime() time.Time {
	// Collect close times from trusted proposals
	for nodeID, proposal := range e.proposals {
		if e.adaptor.IsTrusted(nodeID) {
			e.state.CloseTimes.Peers[proposal.CloseTime]++
		}
	}

	// Find most popular close time
	var bestTime time.Time
	bestCount := 0
	for t, count := range e.state.CloseTimes.Peers {
		if count > bestCount {
			bestTime = t
			bestCount = count
		}
	}

	// If no consensus on time, use our time
	if bestCount == 0 {
		return e.state.CloseTimes.Self
	}

	return bestTime
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
