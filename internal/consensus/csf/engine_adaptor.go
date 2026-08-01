package csf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/ledgertrie"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
)

var errNotFound = errors.New("csf: object not found")

type peerRegistry struct {
	mu     sync.RWMutex
	peers  map[PeerID]*Peer
	byNode map[consensus.NodeID]*Peer
}

func newPeerRegistry() *peerRegistry {
	return &peerRegistry{
		peers:  make(map[PeerID]*Peer),
		byNode: make(map[consensus.NodeID]*Peer),
	}
}

func (r *peerRegistry) add(peer *Peer) {
	r.mu.Lock()
	r.peers[peer.ID] = peer
	r.byNode[peer.nodeID] = peer
	r.mu.Unlock()
}

func (r *peerRegistry) get(id PeerID) *Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.peers[id]
}

func (r *peerRegistry) getNode(id consensus.NodeID) *Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byNode[id]
}

func nodeIDFor(id PeerID) consensus.NodeID {
	var node consensus.NodeID
	binary.BigEndian.PutUint32(node[:4], uint32(id)+1)
	return node
}

func wirePeerID(id PeerID) uint64 {
	return uint64(id) + 1
}

type collectorEvent struct {
	when  SimTime
	event Event
}

type Peer struct {
	ID PeerID

	nodeID     consensus.NodeID
	scheduler  *Scheduler
	oracle     *LedgerOracle
	network    *BasicNetwork
	trustGraph *TrustGraph
	collectors *Collectors
	registry   *peerRegistry

	lifecycleMu      sync.Mutex
	workMu           sync.Mutex
	engine           *rcl.Engine
	started          bool
	stopped          bool
	ticking          bool
	tickGen          uint64
	cancelTick       func()
	cancelAccept     func()
	validationGen    uint64
	nextValidation   uint64
	validationTimers map[uint64]func()
	timing           consensus.Timing

	collectorMu       sync.Mutex
	deferCollectors   int
	pendingCollectors []collectorEvent

	mu                sync.Mutex
	validator         bool
	standalone        bool
	targetLedgers     int
	completed         int
	clockSkew         time.Duration
	recvValDelay      SimDuration
	ledgerAcceptDelay SimDuration
	operatingMode     consensus.OperatingMode
	ledgers           map[consensus.LedgerID]*Ledger
	canonical         map[uint32]*Ledger
	lcl               *Ledger
	validated         consensus.LedgerID
	txSets            map[consensus.TxSetID]*TxSet
	openTxs           map[consensus.TxID][]byte
	injected          map[uint32]map[consensus.TxID][]byte
	seenTxs           map[consensus.TxID]struct{}
	seenProposals     map[[32]byte]struct{}
	seenValidation    map[[32]byte]struct{}
	relayHave         map[[32]byte]map[uint64]struct{}
	pendingValidated  map[consensus.LedgerID]uint32
	acquiringLedgers  map[consensus.LedgerID]SimTime
	acquiringTxSets   map[consensus.TxSetID]SimTime
	asyncErrors       []error
	trustChanged      func([]consensus.NodeID, int)
}

func newPeer(
	id PeerID,
	scheduler *Scheduler,
	oracle *LedgerOracle,
	network *BasicNetwork,
	trustGraph *TrustGraph,
	collectors *Collectors,
	registry *peerRegistry,
) *Peer {
	genesis := oracle.Genesis()
	peer := &Peer{
		ID:               id,
		nodeID:           nodeIDFor(id),
		scheduler:        scheduler,
		oracle:           oracle,
		network:          network,
		trustGraph:       trustGraph,
		collectors:       collectors,
		registry:         registry,
		timing:           consensus.DefaultTiming(),
		validator:        true,
		targetLedgers:    int(^uint(0) >> 1),
		operatingMode:    consensus.OpModeFull,
		ledgers:          map[consensus.LedgerID]*Ledger{genesis.ID(): genesis},
		canonical:        map[uint32]*Ledger{0: genesis},
		lcl:              genesis,
		validated:        genesis.ID(),
		txSets:           make(map[consensus.TxSetID]*TxSet),
		openTxs:          make(map[consensus.TxID][]byte),
		injected:         make(map[uint32]map[consensus.TxID][]byte),
		seenTxs:          make(map[consensus.TxID]struct{}),
		seenProposals:    make(map[[32]byte]struct{}),
		seenValidation:   make(map[[32]byte]struct{}),
		relayHave:        make(map[[32]byte]map[uint64]struct{}),
		pendingValidated: make(map[consensus.LedgerID]uint32),
		acquiringLedgers: make(map[consensus.LedgerID]SimTime),
		acquiringTxSets:  make(map[consensus.TxSetID]SimTime),
		validationTimers: make(map[uint64]func()),
	}
	trustGraph.Trust(id, id)
	return peer
}

func (p *Peer) SetParms(timing consensus.Timing) error {
	if timing.LedgerGranularity <= 0 {
		return errors.New("csf: ledger granularity must be positive")
	}
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	if p.started {
		return errors.New("csf: consensus timing cannot change after start")
	}
	p.timing = timing
	return nil
}

func (p *Peer) SetClockSkew(skew time.Duration) {
	p.mu.Lock()
	p.clockSkew = skew
	p.mu.Unlock()
}

func (p *Peer) SetRunAsValidator(validator bool) {
	p.mu.Lock()
	p.validator = validator
	p.mu.Unlock()
}

func (p *Peer) SetValidationReceiveDelay(delay SimDuration) error {
	if delay < 0 {
		return errors.New("csf: validation receive delay must not be negative")
	}
	p.mu.Lock()
	p.recvValDelay = delay
	p.mu.Unlock()
	return nil
}

func (p *Peer) SetLedgerAcceptDelay(delay SimDuration) error {
	if delay < 0 {
		return errors.New("csf: ledger accept delay must not be negative")
	}
	p.mu.Lock()
	p.ledgerAcceptDelay = delay
	p.mu.Unlock()
	return nil
}

func (p *Peer) SetTargetLedgers(target int) {
	p.mu.Lock()
	p.targetLedgers = target
	p.mu.Unlock()
}

func (p *Peer) TargetLedgers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.targetLedgers
}

func (p *Peer) CompletedLedgers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completed
}

func (p *Peer) targetReached() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completed >= p.targetLedgers
}

func (p *Peer) Trust(other *Peer) {
	p.trustGraph.Trust(p.ID, other.ID)
	p.notifyTrustChanged()
}

func (p *Peer) Untrust(other *Peer) {
	p.trustGraph.Untrust(p.ID, other.ID)
	p.notifyTrustChanged()
}

func (p *Peer) Trusts(other *Peer) bool {
	return p.trustGraph.Trusts(p.ID, other.ID)
}

func (p *Peer) Connect(other *Peer, delay SimDuration) bool {
	if p == other {
		return false
	}
	first, second := p, other
	if first.ID > second.ID {
		first, second = second, first
	}
	first.lifecycleMu.Lock()
	second.lifecycleMu.Lock()
	if p.stopped || other.stopped || !p.network.Connect(p.ID, other.ID, delay) {
		second.lifecycleMu.Unlock()
		first.lifecycleMu.Unlock()
		return false
	}
	second.lifecycleMu.Unlock()
	first.lifecycleMu.Unlock()
	p.invalidateAcquisitions()
	other.invalidateAcquisitions()
	p.retryPendingValidated()
	other.retryPendingValidated()
	return true
}

func (p *Peer) Disconnect(other *Peer) bool {
	if !p.network.Disconnect(p.ID, other.ID) {
		return false
	}
	p.invalidateAcquisitions()
	other.invalidateAcquisitions()
	return true
}

func (p *Peer) Start() error {
	p.workMu.Lock()
	p.deferCollectorEvents()
	defer func() {
		events := p.releaseCollectorEvents()
		p.workMu.Unlock()
		p.dispatchCollectorEvents(events)
	}()

	p.lifecycleMu.Lock()
	if p.stopped {
		p.lifecycleMu.Unlock()
		return errors.New("csf: peer is stopped")
	}
	restart := p.started && !p.ticking
	if !p.started {
		p.mu.Lock()
		p.standalone = len(p.network.Peers(p.ID)) == 0
		p.mu.Unlock()
		cfg := rcl.Config{
			Timing:     p.timing,
			Thresholds: consensus.DefaultThresholds(),
			Clock:      p.Now,
			ManualTick: true,
		}
		p.engine = rcl.NewEngine(p, cfg)
		p.engine.SetLedgerAncestryProvider(p)
		if err := p.engine.Start(context.Background()); err != nil {
			p.engine = nil
			p.lifecycleMu.Unlock()
			return err
		}
		lcl := p.LastClosedLedger()
		round := consensus.RoundID{Seq: lcl.Seq() + 1, ParentHash: lcl.ID()}
		if err := p.engine.StartRound(round, p.IsValidator()); err != nil {
			_ = p.engine.Stop()
			p.engine = nil
			p.lifecycleMu.Unlock()
			return err
		}
		p.started = true
	}
	engine := p.engine
	p.lifecycleMu.Unlock()

	if restart {
		if err := engine.RestartRound(p.IsValidator()); err != nil {
			return err
		}
	}

	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	if p.stopped {
		return errors.New("csf: peer is stopped")
	}
	if !p.ticking {
		p.tickGen++
		p.ticking = true
		p.scheduleTickLocked(p.tickGen)
	}
	return nil
}

func (p *Peer) scheduleTickLocked(generation uint64) {
	p.cancelTick = p.scheduler.In(p.timing.LedgerGranularity, func() {
		p.runTick(generation)
	})
}

func (p *Peer) runTick(generation uint64) {
	p.runEngineWork(func() {
		p.lifecycleMu.Lock()
		if p.stopped || !p.ticking || generation != p.tickGen || p.engine == nil {
			p.lifecycleMu.Unlock()
			return
		}
		engine := p.engine
		p.lifecycleMu.Unlock()
		engine.TimerEntry()

		p.lifecycleMu.Lock()
		defer p.lifecycleMu.Unlock()
		if p.stopped || !p.ticking || generation != p.tickGen {
			return
		}
		if p.targetReached() {
			p.ticking = false
			p.cancelTick = nil
			return
		}
		p.scheduleTickLocked(generation)
	})
}

func (p *Peer) Stop() error {
	p.lifecycleMu.Lock()
	if p.stopped {
		p.lifecycleMu.Unlock()
		return nil
	}
	p.stopped = true
	p.ticking = false
	p.tickGen++
	if p.cancelTick != nil {
		p.cancelTick()
		p.cancelTick = nil
	}
	if p.cancelAccept != nil {
		p.cancelAccept()
		p.cancelAccept = nil
	}
	p.validationGen++
	for id, cancel := range p.validationTimers {
		cancel()
		delete(p.validationTimers, id)
	}
	p.mu.Lock()
	clear(p.acquiringLedgers)
	clear(p.acquiringTxSets)
	p.mu.Unlock()
	engine := p.engine
	p.lifecycleMu.Unlock()

	for _, other := range p.network.Peers(p.ID) {
		p.network.Disconnect(p.ID, other)
		if peer := p.registry.get(other); peer != nil {
			peer.invalidateAcquisitions()
		}
	}

	var err error
	p.runEngineWork(func() {
		if engine != nil {
			err = engine.Stop()
		}
	})
	return err
}

func (p *Peer) acceptingMessages() (*rcl.Engine, bool) {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	return p.engine, p.started && !p.stopped && p.engine != nil
}

func (p *Peer) withEngine(fn func(*rcl.Engine)) bool {
	active := false
	p.runEngineWork(func() {
		p.lifecycleMu.Lock()
		engine := p.engine
		active = p.started && !p.stopped && engine != nil
		p.lifecycleMu.Unlock()
		if active {
			fn(engine)
		}
	})
	return active
}

func (p *Peer) runEngineWork(fn func()) {
	p.workMu.Lock()
	p.deferCollectorEvents()
	defer func() {
		events := p.releaseCollectorEvents()
		p.workMu.Unlock()
		p.dispatchCollectorEvents(events)
	}()
	fn()
}

func (p *Peer) deferCollectorEvents() {
	p.collectorMu.Lock()
	p.deferCollectors++
	p.collectorMu.Unlock()
}

func (p *Peer) releaseCollectorEvents() []collectorEvent {
	p.collectorMu.Lock()
	defer p.collectorMu.Unlock()
	p.deferCollectors--
	if p.deferCollectors != 0 || len(p.pendingCollectors) == 0 {
		return nil
	}
	events := p.pendingCollectors
	p.pendingCollectors = nil
	return events
}

func (p *Peer) emitCollectorEvent(event Event) {
	notice := collectorEvent{when: p.scheduler.Now(), event: event}
	p.collectorMu.Lock()
	if p.deferCollectors != 0 {
		p.pendingCollectors = append(p.pendingCollectors, notice)
		p.collectorMu.Unlock()
		return
	}
	p.collectorMu.Unlock()
	p.collectors.On(p.ID, notice.when, notice.event)
}

func (p *Peer) dispatchCollectorEvents(events []collectorEvent) {
	for _, event := range events {
		p.collectors.On(p.ID, event.when, event.event)
	}
}

func (p *Peer) DeferLedgerAccept(complete func()) bool {
	p.mu.Lock()
	delay := p.ledgerAcceptDelay
	p.mu.Unlock()
	if delay == 0 {
		return false
	}

	p.lifecycleMu.Lock()
	if p.stopped {
		p.lifecycleMu.Unlock()
		return false
	}
	p.cancelAccept = p.scheduler.In(delay, func() {
		p.runEngineWork(func() {
			p.lifecycleMu.Lock()
			if p.stopped {
				p.cancelAccept = nil
				p.lifecycleMu.Unlock()
				return
			}
			p.cancelAccept = nil
			p.lifecycleMu.Unlock()
			complete()
		})
	})
	p.lifecycleMu.Unlock()
	return true
}

func (p *Peer) LastClosedLedger() *Ledger {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lcl
}

func (p *Peer) FullyValidatedLedger() *Ledger {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ledger := p.ledgers[p.validated]; ledger != nil {
		return ledger
	}
	return p.canonical[0]
}

func (p *Peer) PrevLedgerID() consensus.LedgerID {
	p.lifecycleMu.Lock()
	engine := p.engine
	p.lifecycleMu.Unlock()
	if engine != nil {
		if round, ok := engine.CurrentRound(); ok {
			return round.ParentHash
		}
	}
	return p.LastClosedLedger().ID()
}

func (p *Peer) Submit(tx Tx) {
	p.receiveTx(tx.Bytes(), nil)
}

func (p *Peer) InjectTx(seq uint32, tx Tx) {
	p.mu.Lock()
	if p.injected[seq] == nil {
		p.injected[seq] = make(map[consensus.TxID][]byte)
	}
	p.injected[seq][tx.TxID()] = tx.Bytes()
	p.mu.Unlock()
}

func (p *Peer) receiveTx(blob []byte, from *PeerID) {
	p.workMu.Lock()
	defer p.workMu.Unlock()
	if p.isStopped() {
		return
	}
	id := txBlobID(blob)
	p.mu.Lock()
	if p.lcl.Transactions().Contains(id) {
		p.mu.Unlock()
		return
	}
	if _, seen := p.seenTxs[id]; seen {
		p.mu.Unlock()
		return
	}
	p.seenTxs[id] = struct{}{}
	p.openTxs[id] = append([]byte(nil), blob...)
	p.mu.Unlock()

	for _, to := range p.network.Peers(p.ID) {
		if from != nil && to == *from {
			continue
		}
		dstID := to
		cp := append([]byte(nil), blob...)
		p.network.Send(p.ID, dstID, func() {
			if dst := p.registry.get(dstID); dst != nil {
				source := p.ID
				dst.receiveTx(cp, &source)
			}
		})
	}
}

func (p *Peer) BroadcastProposal(proposal *consensus.Proposal) error {
	if p.isStopped() {
		return nil
	}
	cp := cloneProposal(proposal)
	p.markProposalSeen(proposalKey(cp), 0)
	p.broadcastProposal(cp, 0)
	return nil
}

func (p *Peer) RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error {
	if p.isStopped() {
		return nil
	}
	p.broadcastProposal(cloneProposal(proposal), exceptPeer)
	return nil
}

func (p *Peer) broadcastProposal(proposal *consensus.Proposal, exceptPeer uint64) {
	if p.isStopped() {
		return
	}
	key := proposalKey(proposal)
	have := p.PeersThatHave(key)
	for _, to := range p.network.Peers(p.ID) {
		if wirePeerID(to) == exceptPeer || containsUint64(have, wirePeerID(to)) {
			continue
		}
		dstID := to
		cp := cloneProposal(proposal)
		p.network.Send(p.ID, dstID, func() {
			if dst := p.registry.get(dstID); dst != nil {
				dst.receiveProposal(cp, p.ID)
			}
		})
	}
}

func (p *Peer) receiveProposal(proposal *consensus.Proposal, from PeerID) {
	p.withEngine(func(engine *rcl.Engine) {
		key := proposalKey(proposal)
		if !p.markProposalSeen(key, wirePeerID(from)) {
			return
		}
		p.emitCollectorEvent(ReceiveProposalEvent{Proposal: cloneProposal(proposal)})
		if err := engine.OnProposal(proposal, wirePeerID(from)); err != nil {
			p.recordAsyncError(fmt.Errorf("proposal from peer %d: %w", from, err))
		}
	})
}

func (p *Peer) BroadcastValidation(validation *consensus.Validation) error {
	if p.isStopped() {
		return nil
	}
	cp := cloneValidation(validation)
	p.markValidationSeen(validationKey(cp), 0)
	p.broadcastValidation(cp, 0)
	return nil
}

func (p *Peer) RelayValidation(validation *consensus.Validation, exceptPeer uint64) error {
	if p.isStopped() {
		return nil
	}
	p.broadcastValidation(cloneValidation(validation), exceptPeer)
	return nil
}

func (p *Peer) broadcastValidation(validation *consensus.Validation, exceptPeer uint64) {
	if p.isStopped() {
		return
	}
	key := validationKey(validation)
	have := p.PeersThatHave(key)
	for _, to := range p.network.Peers(p.ID) {
		if wirePeerID(to) == exceptPeer || containsUint64(have, wirePeerID(to)) {
			continue
		}
		dstID := to
		cp := cloneValidation(validation)
		p.network.Send(p.ID, dstID, func() {
			if dst := p.registry.get(dstID); dst != nil {
				dst.receiveValidation(cp, p.ID)
			}
		})
	}
}

func (p *Peer) receiveValidation(validation *consensus.Validation, from PeerID) {
	p.withEngine(func(engine *rcl.Engine) {
		key := validationKey(validation)
		if !p.markValidationSeen(key, wirePeerID(from)) {
			return
		}
		p.emitCollectorEvent(ReceiveValidationEvent{
			Validation: cloneValidation(validation),
		})
		p.mu.Lock()
		delay := p.recvValDelay
		p.mu.Unlock()
		if delay == 0 {
			p.handleReceivedValidationLocked(engine, validation, from)
			return
		}

		cp := cloneValidation(validation)
		p.lifecycleMu.Lock()
		if p.stopped {
			p.lifecycleMu.Unlock()
			return
		}
		generation := p.validationGen
		timerID := p.nextValidation
		p.nextValidation++
		cancel := p.scheduler.In(delay, func() {
			p.lifecycleMu.Lock()
			delete(p.validationTimers, timerID)
			valid := !p.stopped && p.validationGen == generation
			p.lifecycleMu.Unlock()
			if valid {
				p.withEngine(func(engine *rcl.Engine) {
					p.handleReceivedValidationLocked(engine, cp, from)
				})
			}
		})
		p.validationTimers[timerID] = cancel
		p.lifecycleMu.Unlock()
	})
}

func (p *Peer) handleReceivedValidationLocked(
	engine *rcl.Engine,
	validation *consensus.Validation,
	from PeerID,
) {
	if err := p.VerifyValidation(validation); err != nil {
		p.recordAsyncError(fmt.Errorf("validation from peer %d: %w", from, err))
		return
	}
	disposition, err := engine.ProcessVerifiedValidation(
		validation,
		consensus.ValidationOrigin{PeerID: wirePeerID(from)},
	)
	if err != nil {
		p.recordAsyncError(fmt.Errorf("validation from peer %d: %w", from, err))
		return
	}
	if disposition.Relay {
		_ = p.RelayValidation(validation, wirePeerID(from))
	}
	if disposition.Status == consensus.ValidationMultiple ||
		disposition.Status == consensus.ValidationConflicting {
		p.recordAsyncError(fmt.Errorf("validation from peer %d: %w", from, &consensus.ByzantineValidationError{
			NodeID:  validation.NodeID,
			Reason:  disposition.Status.String(),
			Trusted: disposition.Trusted,
		}))
		return
	}
	if disposition.AcquireEligible() {
		_ = p.RequestLedger(validation.LedgerID)
	}
}

func (p *Peer) markProposalSeen(key [32]byte, from uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if from != 0 {
		p.recordRelayHaveLocked(key, from)
	}
	if _, seen := p.seenProposals[key]; seen {
		return false
	}
	p.seenProposals[key] = struct{}{}
	return true
}

func (p *Peer) markValidationSeen(key [32]byte, from uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if from != 0 {
		p.recordRelayHaveLocked(key, from)
	}
	if _, seen := p.seenValidation[key]; seen {
		return false
	}
	p.seenValidation[key] = struct{}{}
	return true
}

func (p *Peer) recordRelayHaveLocked(key [32]byte, peer uint64) {
	if p.relayHave[key] == nil {
		p.relayHave[key] = make(map[uint64]struct{})
	}
	p.relayHave[key][peer] = struct{}{}
}

func (p *Peer) UpdateRelaySlot([]byte, uint64, []uint64) {}

func (p *Peer) PeersThatHave(key [32]byte) []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]uint64, 0, len(p.relayHave[key]))
	for peer := range p.relayHave[key] {
		result = append(result, peer)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (p *Peer) RequestTxSet(id consensus.TxSetID) error {
	if p.isStopped() {
		return errors.New("csf: peer is stopped")
	}
	peers, retryAfter := p.acquisitionWindow()
	if len(peers) == 0 {
		return errNotFound
	}
	now := p.scheduler.Now()
	deadline := now + SimTime(retryAfter)
	p.mu.Lock()
	if _, ok := p.txSets[id]; ok {
		p.mu.Unlock()
		return nil
	}
	if existing := p.acquiringTxSets[id]; existing > now {
		p.mu.Unlock()
		return nil
	}
	p.acquiringTxSets[id] = deadline
	p.mu.Unlock()

	sent := false
	for _, sourceID := range peers {
		source := p.registry.get(sourceID)
		if source == nil {
			continue
		}
		requesterID := p.ID
		if p.network.Send(requesterID, sourceID, func() {
			if source.isStopped() {
				return
			}
			source.mu.Lock()
			set := source.txSets[id]
			if set != nil {
				set = set.Clone()
			}
			source.mu.Unlock()
			if set == nil {
				return
			}
			if source.isStopped() {
				return
			}
			source.network.Send(sourceID, requesterID, func() {
				p.withEngine(func(engine *rcl.Engine) {
					p.mu.Lock()
					delete(p.acquiringTxSets, id)
					p.mu.Unlock()
					if err := engine.OnTxSet(id, set.Txs()); err != nil {
						p.recordAsyncError(fmt.Errorf("transaction set %x: %w", id, err))
					}
				})
			})
		}) {
			sent = true
		}
	}
	if sent {
		if p.isStopped() {
			p.mu.Lock()
			if p.acquiringTxSets[id] == deadline {
				delete(p.acquiringTxSets, id)
			}
			p.mu.Unlock()
			return errors.New("csf: peer is stopped")
		}
		return nil
	}
	p.mu.Lock()
	if p.acquiringTxSets[id] == deadline {
		delete(p.acquiringTxSets, id)
	}
	p.mu.Unlock()
	return errNotFound
}

func (p *Peer) RequestLedger(id consensus.LedgerID) error {
	if p.isStopped() {
		return errors.New("csf: peer is stopped")
	}
	peers, retryAfter := p.acquisitionWindow()
	if len(peers) == 0 {
		return errNotFound
	}
	now := p.scheduler.Now()
	deadline := now + SimTime(retryAfter)
	p.mu.Lock()
	if _, ok := p.ledgers[id]; ok {
		p.mu.Unlock()
		return nil
	}
	if existing := p.acquiringLedgers[id]; existing > now {
		p.mu.Unlock()
		return nil
	}
	p.acquiringLedgers[id] = deadline
	p.mu.Unlock()

	sent := false
	for _, sourceID := range peers {
		source := p.registry.get(sourceID)
		if source == nil {
			continue
		}
		requesterID := p.ID
		if p.network.Send(requesterID, sourceID, func() {
			if source.isStopped() {
				return
			}
			source.mu.Lock()
			ledger := source.ledgers[id]
			var chain []*Ledger
			for current := ledger; current != nil; current = source.ledgers[current.ParentID()] {
				chain = append(chain, current)
				if current.Seq() == 0 {
					break
				}
			}
			source.mu.Unlock()
			if ledger == nil {
				return
			}
			if source.isStopped() {
				return
			}
			source.network.Send(sourceID, requesterID, func() {
				if p.isStopped() {
					return
				}
				p.mu.Lock()
				for _, acquired := range chain {
					p.ledgers[acquired.ID()] = acquired
				}
				delete(p.acquiringLedgers, id)
				p.mu.Unlock()
			})
		}) {
			sent = true
		}
	}
	if !sent {
		p.mu.Lock()
		delete(p.acquiringLedgers, id)
		p.mu.Unlock()
		return errNotFound
	}
	if p.isStopped() {
		p.mu.Lock()
		if p.acquiringLedgers[id] == deadline {
			delete(p.acquiringLedgers, id)
		}
		p.mu.Unlock()
		return errors.New("csf: peer is stopped")
	}
	return nil
}

func (p *Peer) acquisitionWindow() ([]PeerID, SimDuration) {
	peers := p.network.Peers(p.ID)
	minimum := 10 * time.Second
	for _, peer := range peers {
		if delay, ok := p.network.Delay(p.ID, peer); ok && delay < minimum {
			minimum = delay
		}
	}
	return peers, 2 * minimum
}

func (p *Peer) invalidateAcquisitions() {
	p.mu.Lock()
	ids := make([]consensus.LedgerID, 0, len(p.acquiringLedgers))
	for id := range p.acquiringLedgers {
		ids = append(ids, id)
	}
	clear(p.acquiringLedgers)
	clear(p.acquiringTxSets)
	p.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})

	for _, id := range ids {
		p.withEngine(func(engine *rcl.Engine) {
			engine.OnLedgerAcquireFailed(id)
		})
	}
}

func (p *Peer) GetLedger(id consensus.LedgerID) (consensus.Ledger, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ledger := p.ledgers[id]; ledger != nil {
		return ledger, nil
	}
	return nil, errNotFound
}

func (p *Peer) LedgerByID(id consensus.LedgerID) (ledgertrie.Ledger, bool) {
	p.mu.Lock()
	ledger := p.ledgers[id]
	p.mu.Unlock()
	if ledger != nil {
		return ledger, true
	}
	_ = p.RequestLedger(id)
	return nil, false
}

func (p *Peer) GetLedgerBySeq(seq uint32) (consensus.Ledger, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ledger := p.canonical[seq]; ledger != nil {
		return ledger, nil
	}
	return nil, errNotFound
}

func (p *Peer) GetLastClosedLedger() (consensus.Ledger, error) {
	return p.LastClosedLedger(), nil
}

func (p *Peer) GetValidatedLedgerHash() consensus.LedgerID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validated
}

func (p *Peer) GetMaxDisallowedLedgerSeq() uint32 {
	return 0
}

func (p *Peer) BuildLedger(
	parent consensus.Ledger,
	txSet consensus.TxSet,
	closeTime time.Time,
	closeTimeCorrect bool,
	_ [][]byte,
) (consensus.Ledger, error) {
	simParent, ok := parent.(*Ledger)
	if !ok {
		return nil, fmt.Errorf("csf: unexpected parent ledger type %T", parent)
	}
	simSet, ok := txSet.(*TxSet)
	if !ok {
		return nil, fmt.Errorf("csf: unexpected transaction set type %T", txSet)
	}
	acceptedSet := simSet
	p.mu.Lock()
	injected := cloneBlobs(sortedTxBlobs(p.injected[simParent.Seq()]))
	p.mu.Unlock()
	if len(injected) != 0 {
		acceptedSet = simSet.Clone()
		for _, blob := range injected {
			if err := acceptedSet.Add(blob); err != nil {
				return nil, fmt.Errorf("csf: add injected transaction: %w", err)
			}
		}
	}
	ledger := p.oracle.Accept(
		simParent,
		acceptedSet,
		closeTime,
		closeTimeCorrect,
		nextLedgerCloseTimeResolution(simParent),
	)
	p.mu.Lock()
	p.ledgers[ledger.ID()] = ledger
	p.txSets[acceptedSet.ID()] = acceptedSet.Clone()
	p.mu.Unlock()
	return ledger, nil
}

func (p *Peer) ValidateLedger(ledger consensus.Ledger) error {
	simLedger, ok := ledger.(*Ledger)
	if !ok {
		return fmt.Errorf("csf: unexpected ledger type %T", ledger)
	}
	if p.oracle.Get(simLedger.ID()) != simLedger {
		return errors.New("csf: ledger is not interned by this simulation")
	}
	return nil
}

func (p *Peer) StoreLedger(ledger consensus.Ledger) error {
	simLedger, ok := ledger.(*Ledger)
	if !ok {
		return fmt.Errorf("csf: unexpected ledger type %T", ledger)
	}
	p.mu.Lock()
	p.ledgers[simLedger.ID()] = simLedger
	p.mu.Unlock()
	return nil
}

func (p *Peer) GetPendingTxs() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return sortedTxBlobs(p.openTxs)
}

func (p *Peer) GetProposableTxs(consensus.Ledger) [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneBlobs(sortedTxBlobs(p.openTxs))
}

func (p *Peer) GenerateFlagLedgerPseudoTxs(consensus.Ledger, []*consensus.Validation) [][]byte {
	return nil
}

func (p *Peer) GenerateNegativeUNLPseudoTx(consensus.Ledger) [][]byte {
	return nil
}

func (p *Peer) OnUNLChange(uint32, []consensus.NodeID) {}

func (p *Peer) GetTxSet(id consensus.TxSetID) (consensus.TxSet, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if set := p.txSets[id]; set != nil {
		return set.Clone(), nil
	}
	return nil, errNotFound
}

func (p *Peer) BuildTxSet(txs [][]byte) (consensus.TxSet, error) {
	set := newTxSetFromBlobs(txs)
	p.mu.Lock()
	p.txSets[set.ID()] = set.Clone()
	p.mu.Unlock()
	return set, nil
}

func (p *Peer) HasTx(id consensus.TxID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.openTxs[id]
	return ok
}

func (p *Peer) GetTx(id consensus.TxID) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if blob, ok := p.openTxs[id]; ok {
		return append([]byte(nil), blob...), nil
	}
	return nil, errNotFound
}

func (p *Peer) IsValidator() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.validator
}

func (p *Peer) IsAmendmentBlocked() bool {
	return false
}

func (p *Peer) GetValidatorKey() (consensus.NodeID, error) {
	return p.nodeID, nil
}

func (p *Peer) SignProposal(proposal *consensus.Proposal) error {
	proposal.NodeID = p.nodeID
	proposal.SigningPubKey = signingKeyFor(p.ID)
	proposal.Signature = proposalSignature(proposal)
	proposal.SuppressionHash = sha256.Sum256(proposal.Signature)
	return nil
}

func (p *Peer) SignValidation(validation *consensus.Validation) error {
	validation.NodeID = p.nodeID
	validation.SigningPubKey = signingKeyFor(p.ID)
	validation.Signature = validationSignature(validation)
	validation.SuppressionHash = sha256.Sum256(validation.Signature)
	return nil
}

func (p *Peer) VerifyProposal(proposal *consensus.Proposal) error {
	if p.registry.getNode(proposal.NodeID) == nil {
		return errors.New("csf: proposal has unknown node identity")
	}
	if !bytes.Equal(proposal.Signature, proposalSignature(proposal)) {
		return errors.New("csf: invalid proposal signature")
	}
	return nil
}

func (p *Peer) VerifyValidation(validation *consensus.Validation) error {
	if p.registry.getNode(validation.NodeID) == nil {
		return errors.New("csf: validation has unknown node identity")
	}
	if !bytes.Equal(validation.Signature, validationSignature(validation)) {
		return errors.New("csf: invalid validation signature")
	}
	return nil
}

func (p *Peer) IsTrusted(node consensus.NodeID) bool {
	peer := p.registry.getNode(node)
	return peer != nil && p.trustGraph.Trusts(p.ID, peer.ID)
}

func (p *Peer) GetTrustedValidators() []consensus.NodeID {
	peers := p.trustGraph.TrustedPeers(p.ID)
	result := make([]consensus.NodeID, 0, len(peers))
	for _, id := range peers {
		result = append(result, nodeIDFor(id))
	}
	return result
}

func (p *Peer) GetTrustedValidatorsAndQuorum() ([]consensus.NodeID, int) {
	trusted := p.GetTrustedValidators()
	return trusted, quorumFor(len(trusted))
}

func (p *Peer) GetQuorum() int {
	return quorumFor(len(p.GetTrustedValidators()))
}

func quorumFor(trusted int) int {
	if trusted < 1 {
		return 1
	}
	return (4*trusted + 4) / 5
}

func (p *Peer) IsQuorumUnavailable() bool {
	return false
}

func (p *Peer) GetNegativeUNL() []consensus.NodeID {
	return nil
}

func (p *Peer) IsFeatureEnabled(string) bool {
	return true
}

func (p *Peer) IsFeatureEnabledOnLedger(consensus.Ledger, string) bool {
	return true
}

func (p *Peer) IsStandalone() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.standalone
}

func (p *Peer) IsUNLBlocked() bool {
	return false
}

func (p *Peer) RefreshUNLState() {}

func (p *Peer) GetCookie() uint64 {
	return wirePeerID(p.ID)
}

func (p *Peer) GetServerVersion() uint64 {
	return 1
}

func (p *Peer) GetLoadFee() uint32 {
	return 0
}

func (p *Peer) GetFeeVote() consensus.FeeVoteResult {
	return consensus.FeeVoteResult{}
}

func (p *Peer) GetAmendmentVote() [][32]byte {
	return nil
}

func (p *Peer) PeerReportedLedgers() []consensus.LedgerID {
	return nil
}

func (p *Peer) Now() time.Time {
	p.mu.Lock()
	skew := p.clockSkew
	p.mu.Unlock()
	return p.scheduler.networkTime(skew)
}

func (p *Peer) CloseTimeResolution() time.Duration {
	p.mu.Lock()
	ledger := p.lcl
	p.mu.Unlock()
	return nextLedgerCloseTimeResolution(ledger)
}

func nextLedgerCloseTimeResolution(ledger *Ledger) time.Duration {
	seconds := consensus.GetNextLedgerTimeResolution(
		uint32(ledger.CloseTimeResolution()/time.Second),
		ledger.CloseAgree(),
		ledger.Seq()+1,
	)
	return time.Duration(seconds) * time.Second
}

func (p *Peer) PrevCloseTimeResolution() time.Duration {
	return p.LastClosedLedger().CloseTimeResolution()
}

func (p *Peer) AdjustCloseTime(consensus.CloseTimes) {}

func (p *Peer) GetOperatingMode() consensus.OperatingMode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.operatingMode
}

func (*Peer) SetOperatingMode(consensus.OperatingMode) {}

func (p *Peer) OnConsensusReached(
	ledger consensus.Ledger,
	_ []*consensus.Validation,
	_ time.Duration,
) {
	simLedger, ok := ledger.(*Ledger)
	if !ok {
		return
	}
	p.mu.Lock()
	if p.completed >= p.targetLedgers {
		p.mu.Unlock()
		return
	}
	prior := p.lcl
	p.ledgers[simLedger.ID()] = simLedger
	p.adoptCanonicalLocked(simLedger)
	p.completed++
	if set := p.txSets[simLedger.TxSetID()]; set != nil {
		for _, id := range set.TxIDs() {
			delete(p.openTxs, id)
		}
	}
	p.mu.Unlock()
	p.emitCollectorEvent(AcceptLedgerEvent{Ledger: simLedger, Prior: prior})
	p.promotePendingValidated(simLedger.ID())
}

func (p *Peer) OnLedgerFullyValidated(id consensus.LedgerID, seq uint32) {
	p.mu.Lock()
	ledger := p.ledgers[id]
	if ledger == nil {
		p.pendingValidated[id] = seq
		p.mu.Unlock()
		_ = p.RequestLedger(id)
		return
	}
	p.mu.Unlock()
	p.promoteFullyValidated(ledger, seq)
}

func (p *Peer) promoteFullyValidated(ledger *Ledger, seq uint32) {
	p.mu.Lock()
	current := p.ledgers[p.validated]
	if ledger.Seq() != seq ||
		(current != nil && (seq <= current.Seq() || !ledger.IsAncestor(current, p.oracle))) {
		delete(p.pendingValidated, ledger.ID())
		p.mu.Unlock()
		return
	}
	prior := current
	p.validated = ledger.ID()
	delete(p.pendingValidated, ledger.ID())
	p.mu.Unlock()
	p.emitCollectorEvent(FullyValidateLedgerEvent{Ledger: ledger, Prior: prior})
}

func (p *Peer) promotePendingValidated(id consensus.LedgerID) {
	p.mu.Lock()
	seq, pending := p.pendingValidated[id]
	ledger := p.ledgers[id]
	p.mu.Unlock()
	if pending && ledger != nil {
		p.promoteFullyValidated(ledger, seq)
	}
}

func (p *Peer) retryPendingValidated() {
	p.mu.Lock()
	ids := make([]consensus.LedgerID, 0, len(p.pendingValidated))
	for id := range p.pendingValidated {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	for _, id := range ids {
		_ = p.RequestLedger(id)
	}
}

func (p *Peer) OnModeChange(consensus.Mode, consensus.Mode) {}

func (p *Peer) OnPhaseChange(_, next consensus.Phase) {
	switch next {
	case consensus.PhaseOpen:
		p.emitCollectorEvent(StartRoundEvent{
			Ledger:   p.LastClosedLedger(),
			Proposer: p.IsValidator(),
		})
	case consensus.PhaseEstablish:
		set := newTxSetFromBlobs(p.GetProposableTxs(p.LastClosedLedger()))
		p.emitCollectorEvent(CloseLedgerEvent{
			TxSet:     set,
			CloseTime: p.scheduler.Now(),
		})
	}
}

func (p *Peer) OnLedgerSwitched(ledger consensus.Ledger) error {
	simLedger, ok := ledger.(*Ledger)
	if !ok {
		return fmt.Errorf("csf: unexpected switched ledger type %T", ledger)
	}
	p.mu.Lock()
	if p.completed >= p.targetLedgers {
		p.mu.Unlock()
		return nil
	}
	previous := p.lcl.ID()
	p.ledgers[simLedger.ID()] = simLedger
	p.mu.Unlock()
	p.emitCollectorEvent(WrongPrevLedgerEvent{
		Expected: previous,
		Received: simLedger.ID(),
	})
	return nil
}

func (p *Peer) adoptCanonicalLocked(tip *Ledger) {
	p.lcl = tip
	for current := tip; current != nil; current = p.ledgers[current.ParentID()] {
		p.canonical[current.Seq()] = current
		if current.Seq() == 0 {
			break
		}
	}
	for seq := tip.Seq() + 1; ; seq++ {
		if _, ok := p.canonical[seq]; !ok {
			break
		}
		delete(p.canonical, seq)
	}
}

func (p *Peer) OnTrustChanged(callback func([]consensus.NodeID, int)) {
	p.mu.Lock()
	p.trustChanged = callback
	p.mu.Unlock()
	trusted, quorum := p.GetTrustedValidatorsAndQuorum()
	callback(trusted, quorum)
}

func (p *Peer) notifyTrustChanged() {
	p.mu.Lock()
	callback := p.trustChanged
	p.mu.Unlock()
	if callback != nil {
		trusted, quorum := p.GetTrustedValidatorsAndQuorum()
		callback(trusted, quorum)
	}
}

func (p *Peer) isStopped() bool {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()
	return p.stopped
}

func (p *Peer) recordAsyncError(err error) {
	p.mu.Lock()
	p.asyncErrors = append(p.asyncErrors, err)
	p.mu.Unlock()
}

func (p *Peer) asyncError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return errors.Join(p.asyncErrors...)
}

func signingKeyFor(id PeerID) consensus.SigningPubKey {
	var key consensus.SigningPubKey
	key[0] = 0xED
	binary.BigEndian.PutUint32(key[1:5], uint32(id)+1)
	return key
}

func proposalSignature(proposal *consensus.Proposal) []byte {
	h := sha256.New()
	writeUint32(h, proposal.Round.Seq)
	_, _ = h.Write(proposal.Round.ParentHash[:])
	_, _ = h.Write(proposal.NodeID[:])
	_, _ = h.Write(proposal.SigningPubKey[:])
	writeUint32(h, proposal.Position)
	_, _ = h.Write(proposal.TxSet[:])
	writeInt64(h, proposal.CloseTime.UnixNano())
	_, _ = h.Write(proposal.PreviousLedger[:])
	writeInt64(h, proposal.Timestamp.UnixNano())
	return h.Sum(nil)
}

func validationSignature(validation *consensus.Validation) []byte {
	h := sha256.New()
	_, _ = h.Write(validation.LedgerID[:])
	writeUint32(h, validation.LedgerSeq)
	_, _ = h.Write(validation.NodeID[:])
	_, _ = h.Write(validation.SigningPubKey[:])
	writeInt64(h, validation.SignTime.UnixNano())
	writeInt64(h, validation.SeenTime.UnixNano())
	writeInt64(h, validation.CloseTime.UnixNano())
	if validation.Full {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
	writeUint32(h, validation.Flags)
	writeUint64(h, validation.Cookie)
	writeUint32(h, validation.LoadFee)
	_, _ = h.Write(validation.ConsensusHash[:])
	writeUint64(h, validation.ServerVersion)
	_, _ = h.Write(validation.ValidatedHash[:])
	for _, amendment := range validation.Amendments {
		_, _ = h.Write(amendment[:])
	}
	writeUint64(h, validation.BaseFee)
	writeUint32(h, validation.ReserveBase)
	writeUint32(h, validation.ReserveIncrement)
	writeUint64(h, validation.BaseFeeDrops)
	writeUint64(h, validation.ReserveBaseDrops)
	writeUint64(h, validation.ReserveIncrementDrops)
	return h.Sum(nil)
}

func proposalKey(proposal *consensus.Proposal) [32]byte {
	if proposal.SuppressionHash != ([32]byte{}) {
		return proposal.SuppressionHash
	}
	return sha256.Sum256(proposal.Signature)
}

func validationKey(validation *consensus.Validation) [32]byte {
	if validation.SuppressionHash != ([32]byte{}) {
		return validation.SuppressionHash
	}
	return sha256.Sum256(validation.Signature)
}

func cloneProposal(proposal *consensus.Proposal) *consensus.Proposal {
	clone := *proposal
	clone.Signature = append([]byte(nil), proposal.Signature...)
	return &clone
}

func cloneValidation(validation *consensus.Validation) *consensus.Validation {
	clone := *validation
	clone.Signature = append([]byte(nil), validation.Signature...)
	clone.SigningData = append([]byte(nil), validation.SigningData...)
	clone.Raw = append([]byte(nil), validation.Raw...)
	clone.Amendments = append([][32]byte(nil), validation.Amendments...)
	return &clone
}

func sortedTxBlobs(txs map[consensus.TxID][]byte) [][]byte {
	ids := make([]consensus.TxID, 0, len(txs))
	for id := range txs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	result := make([][]byte, 0, len(ids))
	for _, id := range ids {
		result = append(result, append([]byte(nil), txs[id]...))
	}
	return result
}

func cloneBlobs(blobs [][]byte) [][]byte {
	result := make([][]byte, len(blobs))
	for i, blob := range blobs {
		result[i] = append([]byte(nil), blob...)
	}
	return result
}

func containsUint64(values []uint64, target uint64) bool {
	index := sort.Search(len(values), func(i int) bool { return values[i] >= target })
	return index < len(values) && values[index] == target
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeUint32(writer hashWriter, value uint32) {
	var blob [4]byte
	binary.BigEndian.PutUint32(blob[:], value)
	_, _ = writer.Write(blob[:])
}

func writeUint64(writer hashWriter, value uint64) {
	var blob [8]byte
	binary.BigEndian.PutUint64(blob[:], value)
	_, _ = writer.Write(blob[:])
}

func writeInt64(writer hashWriter, value int64) {
	writeUint64(writer, uint64(value))
}

var _ consensus.Adaptor = (*Peer)(nil)
var _ consensus.TrustChangeNotifier = (*Peer)(nil)
