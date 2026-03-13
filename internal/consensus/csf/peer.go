package csf

import (
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// ProcessingDelays simulates internal peer processing delays.
// These model real-world delays between receiving a message and fully processing it.
type ProcessingDelays struct {
	// LedgerAccept is delay from consensus doAccept to accepting and issuing validation
	LedgerAccept SimDuration

	// RecvValidation is delay in processing validations from remote peers
	RecvValidation SimDuration

	// RecvProposal is delay in processing proposals
	RecvProposal SimDuration

	// RecvTxSet is delay in processing transaction sets
	RecvTxSet SimDuration

	// RecvTx is delay in processing individual transactions
	RecvTx SimDuration
}

// OnReceive returns the receive delay for a message type.
// Default is no delay.
func (d ProcessingDelays) OnReceive(msg interface{}) SimDuration {
	switch msg.(type) {
	case *Validation:
		return d.RecvValidation
	case *Proposal:
		return d.RecvProposal
	case *TxSet:
		return d.RecvTxSet
	case Tx:
		return d.RecvTx
	default:
		return 0
	}
}

// Position wraps a Proposal with additional metadata.
// For real consensus, this would add serialization and signing data.
type Position struct {
	proposal *Proposal
}

// NewPosition creates a position from a proposal.
func NewPosition(p *Proposal) *Position {
	return &Position{proposal: p}
}

// Proposal returns the underlying proposal.
func (p *Position) Proposal() *Proposal {
	return p.proposal
}

// Router handles message deduplication using sequence numbers.
// Messages are tagged with a sequence number by the origin node.
// Receivers ignore messages if they've already processed a newer sequence.
type Router struct {
	mu              sync.Mutex
	nextSeq         uint64
	lastObservedSeq map[PeerID]uint64
}

// NewRouter creates a new message router.
func NewRouter() *Router {
	return &Router{
		nextSeq:         1,
		lastObservedSeq: make(map[PeerID]uint64),
	}
}

// NextSeq returns and increments the next sequence number.
func (r *Router) NextSeq() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := r.nextSeq
	r.nextSeq++
	return seq
}

// ShouldProcess checks if a message from origin with seq should be processed.
// Returns true if this is a newer message than previously seen from origin.
func (r *Router) ShouldProcess(origin PeerID, seq uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastObservedSeq[origin] < seq {
		r.lastObservedSeq[origin] = seq
		return true
	}
	return false
}

// HasSeen checks if we've processed messages from origin up to seq.
func (r *Router) HasSeen(origin PeerID, seq uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastObservedSeq[origin] >= seq
}

// BroadcastMsg wraps a message for flooding across the network.
type BroadcastMsg struct {
	Seq    uint64
	Origin PeerID
}

// ValidationParms holds validation parameters.
type ValidationParms struct {
	// ValidationCurrentEarly is how early a validation is allowed
	ValidationCurrentEarly time.Duration
	// ValidationCurrentWall is the wall clock duration for current
	ValidationCurrentWall time.Duration
	// ValidationCurrentLocal is the local clock duration for current
	ValidationCurrentLocal time.Duration
	// ValidationSetExpires is when validation set expires
	ValidationSetExpires time.Duration
}

// DefaultValidationParms returns default validation parameters.
func DefaultValidationParms() ValidationParms {
	return ValidationParms{
		ValidationCurrentEarly: 3 * time.Minute,
		ValidationCurrentWall:  5 * time.Minute,
		ValidationCurrentLocal: 4 * time.Minute,
		ValidationSetExpires:   10 * time.Minute,
	}
}

// ConsensusParms holds consensus timing parameters matching rippled's.
type ConsensusParms struct {
	// ledgerIDLE_INTERVAL - How long to wait between rounds when idle
	LedgerIdleInterval time.Duration

	// ledgerGRANULARITY - How often to check for consensus/heartbeat
	LedgerGranularity time.Duration

	// ledgerMIN_CONSENSUS - Minimum time to remain in consensus
	LedgerMinConsensus time.Duration

	// ledgerMAX_CONSENSUS - Maximum time to remain in consensus
	LedgerMaxConsensus time.Duration

	// ledgerMIN_CLOSE - Minimum time before closing ledger
	LedgerMinClose time.Duration

	// ledgerMAX_CLOSE - Maximum time before closing ledger
	LedgerMaxClose time.Duration

	// proposeFRESHNESS - Max time a proposal is fresh
	ProposeFreshness time.Duration

	// proposeINTERVAL - How often to send proposals during establish
	ProposeInterval time.Duration

	// avMIN_CONSENSUS_TIME - Minimum time between validations
	MinConsensusTime time.Duration
}

// DefaultConsensusParms returns default consensus parameters.
func DefaultConsensusParms() ConsensusParms {
	return ConsensusParms{
		LedgerIdleInterval: 15 * time.Second,
		LedgerGranularity:  10 * time.Millisecond,
		LedgerMinConsensus: 1950 * time.Millisecond,
		LedgerMaxConsensus: 10 * time.Second,
		LedgerMinClose:     2 * time.Second,
		LedgerMaxClose:     10 * time.Second,
		ProposeFreshness:   20 * time.Second,
		ProposeInterval:    250 * time.Millisecond,
		MinConsensusTime:   5 * time.Second,
	}
}

// Validations tracks validations received and manages validation state.
type Validations struct {
	mu                 sync.RWMutex
	parms              ValidationParms
	byLedger           map[consensus.LedgerID]map[PeerID]*Validation
	byNode             map[PeerID]*Validation
	lastValidatedSeq   map[PeerID]uint32
	trustedValidations map[consensus.LedgerID]int
	staleCount         int
}

// NewValidations creates a new validation tracker.
func NewValidations(parms ValidationParms) *Validations {
	return &Validations{
		parms:              parms,
		byLedger:           make(map[consensus.LedgerID]map[PeerID]*Validation),
		byNode:             make(map[PeerID]*Validation),
		lastValidatedSeq:   make(map[PeerID]uint32),
		trustedValidations: make(map[consensus.LedgerID]int),
	}
}

// Add adds a validation from the given node.
func (v *Validations) Add(nodeID PeerID, val *Validation) ValStatus {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Check if stale (older sequence than we've seen from this node)
	if existing, ok := v.byNode[nodeID]; ok {
		if val.Seq <= existing.Seq {
			v.staleCount++
			return ValStatusStale
		}
	}

	// Store by node
	v.byNode[nodeID] = val
	v.lastValidatedSeq[nodeID] = val.Seq

	// Store by ledger
	if v.byLedger[val.LedgerID] == nil {
		v.byLedger[val.LedgerID] = make(map[PeerID]*Validation)
	}
	v.byLedger[val.LedgerID][nodeID] = val

	// Track trusted count
	if val.Trusted {
		v.trustedValidations[val.LedgerID]++
	}

	return ValStatusCurrent
}

// NumTrustedForLedger returns the count of trusted validations for a ledger.
func (v *Validations) NumTrustedForLedger(ledgerID consensus.LedgerID) int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.trustedValidations[ledgerID]
}

// GetNodesAfter returns count of nodes that have validated a ledger after the given one.
func (v *Validations) GetNodesAfter(prevLedger *Ledger, prevLedgerID consensus.LedgerID) int {
	v.mu.RLock()
	defer v.mu.RUnlock()

	count := 0
	for _, seq := range v.lastValidatedSeq {
		if seq > prevLedger.Seq() {
			count++
		}
	}
	return count
}

// CanValidateSeq checks if we can send a validation for the given sequence.
func (v *Validations) CanValidateSeq(seq uint32) bool {
	// For simulation, always allow
	return true
}

// GetPreferred returns the preferred ledger based on validations.
func (v *Validations) GetPreferred(currentLedger *Ledger, earliestSeq uint32) consensus.LedgerID {
	v.mu.RLock()
	defer v.mu.RUnlock()

	// Find ledger with most trusted validations at or after earliestSeq
	var bestID consensus.LedgerID
	bestCount := 0

	for ledgerID, count := range v.trustedValidations {
		if count > bestCount {
			// In real impl, would check sequence >= earliestSeq
			bestID = ledgerID
			bestCount = count
		}
	}

	if bestCount == 0 {
		return currentLedger.ID()
	}
	return bestID
}

// Laggards returns count of trusted validators that haven't validated seq yet.
func (v *Validations) Laggards(seq uint32, trusted map[PeerID]bool) int {
	v.mu.RLock()
	defer v.mu.RUnlock()

	count := 0
	for nodeID := range trusted {
		if lastSeq, ok := v.lastValidatedSeq[nodeID]; !ok || lastSeq < seq {
			count++
		}
	}
	return count
}

// Expire removes old validations.
func (v *Validations) Expire() {
	// For simulation, we don't expire
}

// ValStatus represents the result of adding a validation.
type ValStatus int

const (
	ValStatusCurrent ValStatus = iota
	ValStatusStale
	ValStatusBadSeq
)

// Peer represents a single peer in the consensus simulation.
// This is the main work-horse of the simulation framework and implements
// the callbacks required by the Consensus algorithm.
type Peer struct {
	mu sync.RWMutex

	// Identity
	ID  PeerID
	Key PeerID // Signing key (same as ID in simulation)

	// External references
	oracle     *LedgerOracle
	scheduler  *Scheduler
	net        *BasicNetwork
	trustGraph *TrustGraph
	collectors *Collectors

	// Ledger state
	lastClosedLedger     *Ledger
	fullyValidatedLedger *Ledger
	ledgers              map[consensus.LedgerID]*Ledger

	// Transaction state
	openTxs *TxSet

	// Validations
	validations *Validations

	// Proposals tracking
	peerPositions map[consensus.LedgerID][]*Proposal
	txSets        map[consensus.TxSetID]*TxSet

	// Acquiring state
	acquiringLedgers map[consensus.LedgerID]SimTime
	acquiringTxSets  map[consensus.TxSetID]SimTime

	// Round tracking
	completedLedgers int
	targetLedgers    int // Stop after this many ledgers
	prevProposers    int
	prevRoundTime    time.Duration

	// Quorum
	quorum int

	// Configuration
	clockSkew      time.Duration
	delays         ProcessingDelays
	runAsValidator bool
	consensusParms ConsensusParms

	// Message routing
	router *Router

	// Consensus state (simplified - in full impl would use actual consensus engine)
	phase             consensus.Phase
	mode              consensus.Mode
	currentRound      consensus.RoundID
	ourPosition       *Proposal
	proposalNum       uint32
	roundStartTime    time.Time
	phaseStartTime    time.Time
	converged         bool
	receivedProposals map[PeerID]*Proposal

	// Transaction injections for byzantine failure testing
	txInjections map[uint32]Tx
}

// NewPeer creates a new simulated peer.
func NewPeer(
	id PeerID,
	scheduler *Scheduler,
	oracle *LedgerOracle,
	net *BasicNetwork,
	trustGraph *TrustGraph,
	collectors *Collectors,
) *Peer {
	genesis := MakeGenesis()

	p := &Peer{
		ID:                   id,
		Key:                  id,
		oracle:               oracle,
		scheduler:            scheduler,
		net:                  net,
		trustGraph:           trustGraph,
		collectors:           collectors,
		lastClosedLedger:     genesis,
		fullyValidatedLedger: genesis,
		ledgers:              make(map[consensus.LedgerID]*Ledger),
		openTxs:              NewTxSet(),
		validations:          NewValidations(DefaultValidationParms()),
		peerPositions:        make(map[consensus.LedgerID][]*Proposal),
		txSets:               make(map[consensus.TxSetID]*TxSet),
		acquiringLedgers:     make(map[consensus.LedgerID]SimTime),
		acquiringTxSets:      make(map[consensus.TxSetID]SimTime),
		completedLedgers:     0,
		targetLedgers:        1<<31 - 1, // Max int
		runAsValidator:       true,
		consensusParms:       DefaultConsensusParms(),
		router:               NewRouter(),
		phase:                consensus.PhaseAccepted,
		mode:                 consensus.ModeObserving,
		receivedProposals:    make(map[PeerID]*Proposal),
		txInjections:         make(map[uint32]Tx),
	}

	// All peers start from genesis
	p.ledgers[genesis.ID()] = genesis

	// Nodes always trust themselves
	trustGraph.Trust(id, id)

	return p
}

// Schedule schedules a callback after the given duration.
// If duration is 0, executes immediately.
func (p *Peer) Schedule(when SimDuration, what func()) {
	if when == 0 {
		what()
	} else {
		p.scheduler.In(when, what)
	}
}

// Issue dispatches an event to collectors.
func (p *Peer) Issue(event Event) {
	p.collectors.On(p.ID, p.scheduler.Now(), event)
}

// Now returns the peer's current time (with clock skew).
func (p *Peer) Now() time.Time {
	// We want the generated time to be well past epoch to ensure
	// subtractions are positive.
	baseTime := time.Duration(p.scheduler.Now())
	return time.Unix(0, int64(baseTime+86400*time.Second+p.clockSkew))
}

// NowSim returns the current simulated time (without skew).
func (p *Peer) NowSim() SimTime {
	return p.scheduler.Now()
}

// Parms returns the consensus parameters.
func (p *Peer) Parms() ConsensusParms {
	return p.consensusParms
}

// SetParms sets the consensus parameters.
func (p *Peer) SetParms(parms ConsensusParms) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consensusParms = parms
}

// SetClockSkew sets the peer's clock skew.
func (p *Peer) SetClockSkew(skew time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clockSkew = skew
}

// SetDelays sets the processing delays.
func (p *Peer) SetDelays(delays ProcessingDelays) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delays = delays
}

// SetRunAsValidator sets whether this peer runs as a validator.
func (p *Peer) SetRunAsValidator(val bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runAsValidator = val
}

// SetTargetLedgers sets the number of ledgers to complete before stopping.
func (p *Peer) SetTargetLedgers(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.targetLedgers = n
}

// TargetLedgers returns the target number of ledgers.
func (p *Peer) TargetLedgers() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.targetLedgers
}

// -----------------------------------------------------------------------------
// Trust and Network members

// Trust extends trust to another peer.
func (p *Peer) Trust(other *Peer) {
	p.trustGraph.Trust(p.ID, other.ID)
}

// Untrust revokes trust from another peer.
func (p *Peer) Untrust(other *Peer) {
	p.trustGraph.Untrust(p.ID, other.ID)
}

// Trusts checks whether we trust another peer.
func (p *Peer) Trusts(other *Peer) bool {
	return p.trustGraph.Trusts(p.ID, other.ID)
}

// TrustsPeerID checks whether we trust a peer by ID.
func (p *Peer) TrustsPeerID(id PeerID) bool {
	for _, trusted := range p.trustGraph.TrustedPeers(p.ID) {
		if trusted == id {
			return true
		}
	}
	return false
}

// Connect creates a network connection to another peer.
func (p *Peer) Connect(other *Peer, delay SimDuration) bool {
	return p.net.Connect(p.ID, other.ID, delay)
}

// Disconnect removes a network connection.
func (p *Peer) Disconnect(other *Peer) bool {
	return p.net.Disconnect(p.ID, other.ID)
}

// -----------------------------------------------------------------------------
// Consensus callback members

// AcquireLedger attempts to acquire a ledger by ID.
func (p *Peer) AcquireLedger(ledgerID consensus.LedgerID) *Ledger {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if we already have it
	if ledger, ok := p.ledgers[ledgerID]; ok {
		return ledger
	}

	// No peers means we can't get it
	peers := p.net.Peers(p.ID)
	if len(peers) == 0 {
		return nil
	}

	// Don't retry if already acquiring and not timed out
	if timeout, ok := p.acquiringLedgers[ledgerID]; ok {
		if p.scheduler.Now() < timeout {
			return nil
		}
	}

	// Send request to all peers
	minDelay := 10 * time.Second
	for _, peerID := range peers {
		if delay, ok := p.net.GetDelay(p.ID, peerID); ok {
			if delay < minDelay {
				minDelay = delay
			}
			// Send ledger request
			p.net.Send(p.ID, peerID, func() {
				// This runs on the receiving peer's context
				// In real impl, the peer would look up and send back
			})
		}
	}

	p.acquiringLedgers[ledgerID] = p.scheduler.Now() + SimTime(2*minDelay)
	return nil
}

// AcquireTxSet attempts to acquire a transaction set by ID.
func (p *Peer) AcquireTxSet(setID consensus.TxSetID) *TxSet {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if we already have it
	if txSet, ok := p.txSets[setID]; ok {
		return txSet
	}

	// No peers means we can't get it
	peers := p.net.Peers(p.ID)
	if len(peers) == 0 {
		return nil
	}

	// Don't retry if already acquiring and not timed out
	if timeout, ok := p.acquiringTxSets[setID]; ok {
		if p.scheduler.Now() < timeout {
			return nil
		}
	}

	// Send request to all peers
	minDelay := 10 * time.Second
	for _, peerID := range peers {
		if delay, ok := p.net.GetDelay(p.ID, peerID); ok {
			if delay < minDelay {
				minDelay = delay
			}
		}
	}

	p.acquiringTxSets[setID] = p.scheduler.Now() + SimTime(2*minDelay)
	return nil
}

// HasOpenTransactions returns true if there are pending transactions.
func (p *Peer) HasOpenTransactions() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.openTxs.Size() > 0
}

// ProposersValidated returns count of trusted validators that have validated the ledger.
func (p *Peer) ProposersValidated(prevLedger consensus.LedgerID) int {
	return p.validations.NumTrustedForLedger(prevLedger)
}

// ProposersFinished returns count of proposers that have finished with the given ledger.
func (p *Peer) ProposersFinished(prevLedger *Ledger, prevLedgerID consensus.LedgerID) int {
	return p.validations.GetNodesAfter(prevLedger, prevLedgerID)
}

// OnClose is called when consensus closes the ledger.
// Returns the initial result with our position.
func (p *Peer) OnClose(prevLedger *Ledger, closeTime time.Time, mode consensus.Mode) (*TxSet, *Proposal) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.Issue(CloseLedgerEvent{
		Ledger:    prevLedger,
		PriorSeq:  prevLedger.Seq(),
		Proposers: len(p.receivedProposals),
	})

	// Create our position from open transactions
	txSet := p.openTxs.Clone()
	proposal := &Proposal{
		PrevLedger: prevLedger.ID(),
		Position:   txSet,
		CloseTime:  closeTime,
		Time:       p.scheduler.Now(),
		NodeID:     p.ID,
		PropNum:    0,
	}

	return txSet, proposal
}

// Note: OnAccept, doAccept, and OnForceAccept are no longer used as the Peer
// now uses built-in consensus logic in acceptLedgerLocked.

// EarliestAllowedSeq returns the earliest sequence for ledger selection.
func (p *Peer) EarliestAllowedSeq() uint32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.fullyValidatedLedger.Seq()
}

// GetPrevLedger determines the previous ledger to build on.
func (p *Peer) GetPrevLedger(ledgerID consensus.LedgerID, ledger *Ledger, mode consensus.Mode) consensus.LedgerID {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Only switch if past genesis
	if ledger.Seq() == 0 {
		return ledgerID
	}

	netLgr := p.validations.GetPreferred(ledger, p.fullyValidatedLedger.Seq())

	if netLgr != ledgerID {
		p.Issue(WrongPrevLedgerEvent{
			WrongLedger:   ledger,
			CorrectLedger: p.ledgers[netLgr],
		})
	}

	return netLgr
}

// Propose broadcasts our position.
func (p *Peer) Propose(pos *Proposal) {
	p.share(pos)
}

// share broadcasts a message to all connected peers.
func (p *Peer) share(msg interface{}) {
	seq := p.router.NextSeq()
	origin := p.ID

	peers := p.net.Peers(p.ID)
	for _, peerID := range peers {
		if peerID != origin {
			targetPeer := p.findPeer(peerID)
			if targetPeer != nil {
				delay, _ := p.net.GetDelay(p.ID, peerID)
				// Copy message for closure based on type
				switch m := msg.(type) {
				case Tx:
					txCopy := m
					seqCopy := seq
					originCopy := origin
					p.scheduler.In(delay, func() {
						targetPeer.handleTxFromPeer(txCopy, seqCopy, originCopy)
					})
				case *Proposal:
					propCopy := *m
					seqCopy := seq
					originCopy := origin
					p.scheduler.In(delay, func() {
						targetPeer.handleProposalFromPeer(&propCopy, seqCopy, originCopy)
					})
				case *TxSet:
					txsCopy := m.Clone()
					seqCopy := seq
					originCopy := origin
					p.scheduler.In(delay, func() {
						targetPeer.handleTxSetFromPeer(txsCopy, seqCopy, originCopy)
					})
				case *Validation:
					valCopy := *m
					seqCopy := seq
					originCopy := origin
					p.scheduler.In(delay, func() {
						targetPeer.handleValidationFromPeer(&valCopy, seqCopy, originCopy)
					})
				}
			}
		}
	}
}

// handleTx processes an incoming transaction.
func (p *Peer) handleTx(tx Tx) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Ignore if already in last closed ledger
	if p.lastClosedLedger.Txs().Contains(tx) {
		return false
	}

	// Add to open transactions if new
	if p.openTxs.Contains(tx) {
		return false
	}

	p.openTxs.Insert(tx)
	return true
}

// -----------------------------------------------------------------------------
// Transaction submission

// Submit submits a transaction to the peer.
func (p *Peer) Submit(tx Tx) {
	p.Issue(submitTxEvent{Tx: tx})
	if p.handleTx(tx) {
		p.share(tx)
	}
}

type submitTxEvent struct {
	Tx Tx
}

func (submitTxEvent) isEvent() {}

// -----------------------------------------------------------------------------
// Simulation driver members

// TimerEntry is the heartbeat timer callback.
func (p *Peer) TimerEntry() {
	p.mu.Lock()
	completed := p.completedLedgers
	target := p.targetLedgers
	phase := p.phase
	phaseStart := p.phaseStartTime
	p.mu.Unlock()

	now := p.Now()

	// Check phase transitions
	switch phase {
	case consensus.PhaseOpen:
		// Check if we should close the ledger
		timeSincePhaseStart := now.Sub(phaseStart)
		if timeSincePhaseStart >= p.consensusParms.LedgerMinClose {
			p.closeLedger()
		}

	case consensus.PhaseEstablish:
		// Check if we have consensus
		p.checkConsensus()
	}

	// Only reschedule if not completed
	if completed < target {
		p.scheduler.In(p.consensusParms.LedgerGranularity, func() {
			p.TimerEntry()
		})
	}
}

// closeLedger transitions from open to establish phase.
func (p *Peer) closeLedger() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.phase != consensus.PhaseOpen {
		return
	}

	p.Issue(CloseLedgerEvent{
		Ledger:    p.lastClosedLedger,
		PriorSeq:  p.lastClosedLedger.Seq(),
		Proposers: len(p.receivedProposals),
	})

	// Create our position from open transactions
	txSet := p.openTxs.Clone()

	// Round close time to resolution to help peers converge on same time
	closeTime := p.Now()
	resolution := p.consensusParms.LedgerGranularity
	if resolution > 0 {
		closeNanos := closeTime.UnixNano()
		roundedNanos := (closeNanos / int64(resolution)) * int64(resolution)
		closeTime = time.Unix(0, roundedNanos)
	}

	p.ourPosition = &Proposal{
		PrevLedger: p.lastClosedLedger.ID(),
		Position:   txSet,
		CloseTime:  closeTime,
		Time:       p.scheduler.Now(),
		NodeID:     p.ID,
		PropNum:    p.proposalNum,
	}
	p.proposalNum++

	// Store our tx set
	p.txSets[txSet.ID()] = txSet

	// Broadcast our proposal if we're a validator
	if p.runAsValidator {
		p.broadcastProposal(p.ourPosition)
	}

	p.phase = consensus.PhaseEstablish
	p.phaseStartTime = p.Now()
}

// broadcastProposal sends a proposal to all peers.
func (p *Peer) broadcastProposal(prop *Proposal) {
	seq := p.router.NextSeq()
	origin := p.ID

	peers := p.net.Peers(p.ID)
	for _, peerID := range peers {
		if peerID != origin {
			targetPeer := p.findPeer(peerID)
			if targetPeer != nil {
				delay, _ := p.net.GetDelay(p.ID, peerID)
				propCopy := *prop
				p.scheduler.In(delay, func() {
					targetPeer.handleProposalFromPeer(&propCopy, seq, origin)
				})
			}
		}
	}
}

// findPeer finds a peer by ID (simplified - in real impl would use registry)
var peerRegistry = make(map[PeerID]*Peer)
var peerRegistryMu sync.Mutex

func (p *Peer) findPeer(id PeerID) *Peer {
	peerRegistryMu.Lock()
	defer peerRegistryMu.Unlock()
	return peerRegistry[id]
}

// RegisterPeer registers a peer in the global registry for message delivery.
func RegisterPeer(peer *Peer) {
	peerRegistryMu.Lock()
	defer peerRegistryMu.Unlock()
	peerRegistry[peer.ID] = peer
}

// UnregisterPeer removes a peer from the global registry.
func UnregisterPeer(peer *Peer) {
	peerRegistryMu.Lock()
	defer peerRegistryMu.Unlock()
	delete(peerRegistry, peer.ID)
}

// ClearPeerRegistry clears all peers from the registry.
func ClearPeerRegistry() {
	peerRegistryMu.Lock()
	defer peerRegistryMu.Unlock()
	peerRegistry = make(map[PeerID]*Peer)
}

// handleProposalFromPeer handles a proposal received from network.
func (p *Peer) handleProposalFromPeer(prop *Proposal, seq uint64, origin PeerID) {
	if !p.router.ShouldProcess(origin, seq) {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.Issue(ReceiveProposalEvent{Proposal: prop})

	// Store the proposal
	p.receivedProposals[prop.NodeID] = prop

	// Store the tx set if we don't have it
	if prop.Position != nil {
		if _, ok := p.txSets[prop.Position.ID()]; !ok {
			p.txSets[prop.Position.ID()] = prop.Position
		}
	}

	// If in establish phase, check consensus
	if p.phase == consensus.PhaseEstablish {
		p.checkConsensusLocked()
	}
}

// checkConsensus checks if we've reached consensus.
func (p *Peer) checkConsensus() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checkConsensusLocked()
}

// checkConsensusLocked checks consensus (caller must hold lock).
func (p *Peer) checkConsensusLocked() {
	if p.phase != consensus.PhaseEstablish {
		return
	}

	// Count proposals for each tx set from trusted validators
	txSetVotes := make(map[consensus.TxSetID]int)
	trustedCount := 0

	for nodeID, prop := range p.receivedProposals {
		if p.TrustsPeerID(nodeID) && prop.Position != nil {
			txSetVotes[prop.Position.ID()]++
			trustedCount++
		}
	}

	// Count our own vote
	if p.ourPosition != nil && p.ourPosition.Position != nil {
		txSetVotes[p.ourPosition.Position.ID()]++
		trustedCount++
	}

	// Use the UNL size for threshold, not just the count of proposals received
	// This ensures we wait for enough trusted validators to respond
	unlSize := p.trustGraph.UNLSize(p.ID)
	threshold := int(float64(unlSize) * 0.8)
	if threshold < 1 {
		threshold = 1
	}

	var winningTxSetID consensus.TxSetID
	var winningCount int

	for txSetID, count := range txSetVotes {
		if count > winningCount {
			winningTxSetID = txSetID
			winningCount = count
		}
	}

	// Check if enough time has passed or we have consensus
	timeSincePhaseStart := p.Now().Sub(p.phaseStartTime)

	// Need both: enough votes AND minimum consensus time has passed
	hasConsensus := winningCount >= threshold && timeSincePhaseStart >= p.consensusParms.LedgerMinConsensus
	timedOut := timeSincePhaseStart >= p.consensusParms.LedgerMaxConsensus

	if hasConsensus || timedOut {
		p.acceptLedgerLocked(winningTxSetID, trustedCount)
	}
}

// acceptLedgerLocked accepts the consensus result (caller must hold lock).
func (p *Peer) acceptLedgerLocked(winningTxSetID consensus.TxSetID, proposers int) {
	// Get the winning tx set
	var winningTxSet *TxSet
	if txSet, ok := p.txSets[winningTxSetID]; ok {
		winningTxSet = txSet
	} else if p.ourPosition != nil && p.ourPosition.Position != nil && p.ourPosition.Position.ID() == winningTxSetID {
		winningTxSet = p.ourPosition.Position
	} else {
		// Fallback to our position or empty set
		if p.ourPosition != nil && p.ourPosition.Position != nil {
			winningTxSet = p.ourPosition.Position
		} else {
			winningTxSet = NewTxSet()
		}
	}

	// Inject any test transactions
	acceptedTxs := p.injectTxs(p.lastClosedLedger, winningTxSet)

	// Determine close time
	closeTime := p.Now()
	if p.ourPosition != nil {
		closeTime = p.ourPosition.CloseTime
	}

	// Create new ledger
	newLedger := p.oracle.Accept(
		p.lastClosedLedger,
		acceptedTxs,
		closeTime,
		true,
		30*time.Second,
	)
	p.ledgers[newLedger.ID()] = newLedger

	p.Issue(AcceptLedgerEvent{Ledger: newLedger})
	p.prevProposers = proposers - 1 // Subtract self
	if p.prevProposers < 0 {
		p.prevProposers = 0
	}
	p.prevRoundTime = p.Now().Sub(p.roundStartTime)
	p.lastClosedLedger = newLedger

	// Remove accepted transactions from open set
	for _, tx := range acceptedTxs.Transactions() {
		p.openTxs.Remove(tx)
	}

	// Send validation if validator
	if p.runAsValidator && p.validations.CanValidateSeq(newLedger.Seq()) {
		val := &Validation{
			LedgerID: newLedger.ID(),
			Seq:      newLedger.Seq(),
			SignTime: p.Now(),
			SeenTime: p.Now(),
			NodeID:   p.ID,
			Key:      p.Key,
			Full:     true,
			Trusted:  true,
		}

		p.broadcastValidation(val)
		p.addTrustedValidationLocked(val)
	}

	p.checkFullyValidatedLocked(newLedger)

	// Start next round
	p.completedLedgers++
	if p.completedLedgers < p.targetLedgers {
		p.startRoundInternalLocked()
	} else {
		p.phase = consensus.PhaseAccepted
	}
}

// broadcastValidation sends a validation to all peers.
func (p *Peer) broadcastValidation(val *Validation) {
	seq := p.router.NextSeq()
	origin := p.ID

	peers := p.net.Peers(p.ID)
	for _, peerID := range peers {
		if peerID != origin {
			targetPeer := p.findPeer(peerID)
			if targetPeer != nil {
				delay, _ := p.net.GetDelay(p.ID, peerID)
				valCopy := *val
				p.scheduler.In(delay, func() {
					targetPeer.handleValidationFromPeer(&valCopy, seq, origin)
				})
			}
		}
	}
}

// handleValidationFromPeer handles a validation received from network.
func (p *Peer) handleValidationFromPeer(val *Validation, seq uint64, origin PeerID) {
	if !p.router.ShouldProcess(origin, seq) {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.Issue(ReceiveValidationEvent{Validation: val})

	if p.TrustsPeerID(val.NodeID) {
		p.addTrustedValidationLocked(val)
	}
}

// handleTxFromPeer handles a transaction received from network.
func (p *Peer) handleTxFromPeer(tx Tx, seq uint64, origin PeerID) {
	if !p.router.ShouldProcess(origin, seq) {
		return
	}

	p.mu.Lock()

	// Ignore if already in last closed ledger
	if p.lastClosedLedger.Txs().Contains(tx) {
		p.mu.Unlock()
		return
	}

	// Add to open transactions if new
	if p.openTxs.Contains(tx) {
		p.mu.Unlock()
		return
	}

	p.openTxs.Insert(tx)
	p.mu.Unlock()

	// Relay to other peers (flood the transaction)
	p.relayTx(tx, origin)
}

// relayTx relays a transaction to all connected peers except the origin.
func (p *Peer) relayTx(tx Tx, from PeerID) {
	seq := p.router.NextSeq()
	origin := p.ID

	peers := p.net.Peers(p.ID)
	for _, peerID := range peers {
		// Don't send back to the origin or to ourselves
		if peerID != from && peerID != origin {
			targetPeer := p.findPeer(peerID)
			if targetPeer != nil {
				delay, _ := p.net.GetDelay(p.ID, peerID)
				txCopy := tx
				seqCopy := seq
				originCopy := origin
				p.scheduler.In(delay, func() {
					targetPeer.handleTxFromPeer(txCopy, seqCopy, originCopy)
				})
			}
		}
	}
}

// handleTxSetFromPeer handles a transaction set received from network.
func (p *Peer) handleTxSetFromPeer(txs *TxSet, seq uint64, origin PeerID) {
	if !p.router.ShouldProcess(origin, seq) {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	id := txs.ID()
	if _, ok := p.txSets[id]; ok {
		return // Already have it
	}

	p.txSets[id] = txs
	delete(p.acquiringTxSets, id)
}

// addTrustedValidationLocked adds a trusted validation (caller must hold lock).
func (p *Peer) addTrustedValidationLocked(val *Validation) bool {
	val.Trusted = true
	val.SeenTime = p.Now()
	status := p.validations.Add(val.NodeID, val)

	if status == ValStatusStale {
		return false
	}

	if ledger, ok := p.ledgers[val.LedgerID]; ok {
		p.checkFullyValidatedLocked(ledger)
	}

	return true
}

// checkFullyValidatedLocked checks if a ledger can be deemed fully validated (caller must hold lock).
func (p *Peer) checkFullyValidatedLocked(ledger *Ledger) {
	if ledger.Seq() <= p.fullyValidatedLedger.Seq() {
		return
	}

	count := p.validations.NumTrustedForLedger(ledger.ID())
	numTrustedPeers := p.trustGraph.UNLSize(p.ID)
	p.quorum = int(float64(numTrustedPeers) * 0.8)
	if p.quorum < 1 {
		p.quorum = 1
	}

	if count >= p.quorum && ledger.IsAncestor(p.fullyValidatedLedger, p.oracle) {
		p.Issue(FullyValidateLedgerEvent{Ledger: ledger})
		p.fullyValidatedLedger = ledger
	}
}

func (p *Peer) startRoundInternalLocked() {
	p.Issue(StartRoundEvent{
		Ledger:   p.lastClosedLedger,
		Proposer: p.runAsValidator,
	})

	p.phase = consensus.PhaseOpen
	if p.runAsValidator {
		p.mode = consensus.ModeProposing
	} else {
		p.mode = consensus.ModeObserving
	}
	p.currentRound = consensus.RoundID{
		Seq:        p.lastClosedLedger.Seq() + 1,
		ParentHash: p.lastClosedLedger.ID(),
	}
	p.ourPosition = nil
	p.proposalNum = 0
	p.roundStartTime = p.Now()
	p.phaseStartTime = p.Now()
	p.converged = false
	p.receivedProposals = make(map[PeerID]*Proposal)
}

// StartRound begins the next consensus round.
func (p *Peer) StartRound() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.startRoundInternalLocked()
}

// Start begins the consensus process.
// This runs until targetLedgers is reached.
func (p *Peer) Start() {
	p.validations.Expire()
	p.scheduler.In(p.consensusParms.LedgerGranularity, func() {
		p.TimerEntry()
	})
	p.StartRound()
}

// PrevLedgerID returns the current previous ledger ID.
func (p *Peer) PrevLedgerID() consensus.LedgerID {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastClosedLedger.ID()
}

// LastClosedLedger returns the last closed ledger.
func (p *Peer) LastClosedLedger() *Ledger {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastClosedLedger
}

// FullyValidatedLedger returns the fully validated ledger.
func (p *Peer) FullyValidatedLedger() *Ledger {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.fullyValidatedLedger
}

// CompletedLedgers returns the count of completed ledgers.
func (p *Peer) CompletedLedgers() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.completedLedgers
}

// HaveValidated returns true if we've validated past genesis.
func (p *Peer) HaveValidated() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.fullyValidatedLedger.Seq() > 0
}

// GetValidLedgerIndex returns the earliest allowed sequence.
func (p *Peer) GetValidLedgerIndex() uint32 {
	return p.EarliestAllowedSeq()
}

// GetQuorumKeys returns the quorum and trusted keys.
func (p *Peer) GetQuorumKeys() (int, []PeerID) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.quorum, p.trustGraph.TrustedPeers(p.ID)
}

// Validator returns whether running as validator.
func (p *Peer) Validator() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.runAsValidator
}

// -----------------------------------------------------------------------------
// Byzantine failure testing

// InjectTx registers a transaction to inject at a specific sequence.
func (p *Peer) InjectTx(seq uint32, tx Tx) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.txInjections[seq] = tx
}

// injectTxs adds injected transactions to the accepted set.
func (p *Peer) injectTxs(prevLedger *Ledger, src *TxSet) *TxSet {
	tx, ok := p.txInjections[prevLedger.Seq()]
	if !ok {
		return src
	}

	result := src.Clone()
	result.Insert(tx)
	return result
}

// ConsensusResult represents the result of a consensus round.
type ConsensusResult struct {
	TxSet     *TxSet
	Position  *Proposal
	State     ConsensusState
	Proposers int
	RoundTime time.Duration
}

// ConsensusState represents the state of consensus.
type ConsensusState int

const (
	ConsensusStateNo ConsensusState = iota
	ConsensusStateMovedOn
	ConsensusStateYes
)
