package csf

import (
	"fmt"
	"math/rand"
)

// Sim orchestrates a consensus simulation.
// It manages the scheduler, network, trust graph, peers, and collectors.
type Sim struct {
	// Core components
	Scheduler  *Scheduler
	Oracle     *LedgerOracle
	Net        *BasicNetwork
	TrustGraph *TrustGraph
	Collectors *Collectors

	// Random number generator
	Rng *rand.Rand

	// Peer management
	peers    []*Peer
	allPeers *PeerGroup
	nextID   PeerID
}

// NewSim creates a new simulation.
// The simulation has no peers, no trust links, and no network connections initially.
func NewSim() *Sim {
	// Clear peer registry for fresh simulation
	ClearPeerRegistry()

	scheduler := NewScheduler()
	return &Sim{
		Scheduler:  scheduler,
		Oracle:     NewLedgerOracle(),
		Net:        NewBasicNetwork(scheduler),
		TrustGraph: NewTrustGraph(),
		Collectors: NewCollectors(),
		Rng:        rand.New(rand.NewSource(0)),
		peers:      make([]*Peer, 0),
		allPeers:   NewPeerGroup(),
		nextID:     0,
	}
}

// NewSimWithSeed creates a new simulation with a specific random seed.
func NewSimWithSeed(seed int64) *Sim {
	sim := NewSim()
	sim.Rng = rand.New(rand.NewSource(seed))
	return sim
}

// CreateGroup creates a new group of peers.
// The peers do not have any trust relations or network connections by default.
func (s *Sim) CreateGroup(numPeers int) *PeerGroup {
	newPeers := make([]*Peer, numPeers)
	for i := 0; i < numPeers; i++ {
		peer := NewPeer(
			s.nextID,
			s.Scheduler,
			s.Oracle,
			s.Net,
			s.TrustGraph,
			s.Collectors,
		)
		s.peers = append(s.peers, peer)
		newPeers[i] = peer
		// Register peer for message delivery
		RegisterPeer(peer)
		s.nextID++
	}

	group := NewPeerGroupFrom(newPeers)
	s.allPeers = s.allPeers.Union(group)
	return group
}

// Size returns the number of peers in the simulation.
func (s *Sim) Size() int {
	return len(s.peers)
}

// Peers returns all peers in the simulation.
func (s *Sim) Peers() []*Peer {
	return s.peers
}

// AllPeers returns a PeerGroup containing all peers.
func (s *Sim) AllPeers() *PeerGroup {
	return s.allPeers
}

// GetPeer returns the peer with the given ID.
func (s *Sim) GetPeer(id PeerID) *Peer {
	for _, peer := range s.peers {
		if peer.ID == id {
			return peer
		}
	}
	return nil
}

// Run runs consensus protocol to generate the specified number of ledgers.
// Each peer runs consensus until it closes `ledgers` more ledgers.
func (s *Sim) Run(ledgers int) {
	for _, p := range s.peers {
		p.SetTargetLedgers(p.CompletedLedgers() + ledgers)
		p.Start()
	}
	// Process all events until all peers complete their target or no more events
	s.Scheduler.StepWhile(func() bool {
		// Check if any peer hasn't completed
		for _, p := range s.peers {
			if p.CompletedLedgers() < p.TargetLedgers() {
				return true
			}
		}
		return false
	})
}

// RunFor runs consensus for the given duration.
func (s *Sim) RunFor(dur SimDuration) {
	for _, p := range s.peers {
		p.SetTargetLedgers(1<<31 - 1) // Max int
		p.Start()
	}
	s.Scheduler.StepFor(dur)
}

// RunUntil runs consensus until the given simulated time.
func (s *Sim) RunUntil(until SimTime) {
	for _, p := range s.peers {
		p.SetTargetLedgers(1<<31 - 1) // Max int
		p.Start()
	}
	s.Scheduler.StepUntil(until)
}

// RunWhile runs consensus while the predicate returns true.
func (s *Sim) RunWhile(pred func() bool) {
	for _, p := range s.peers {
		p.SetTargetLedgers(1<<31 - 1) // Max int
		p.Start()
	}
	s.Scheduler.StepWhile(pred)
}

// Synchronized checks whether all peers in the group are synchronized.
// Peers are synchronized if they share the same last fully validated
// and last closed ledger.
func (s *Sim) Synchronized(group *PeerGroup) bool {
	if group.Size() < 1 {
		return true
	}

	ref := group.Get(0)
	for _, peer := range group.Peers() {
		if peer.LastClosedLedger().ID() != ref.LastClosedLedger().ID() {
			return false
		}
		if peer.FullyValidatedLedger().ID() != ref.FullyValidatedLedger().ID() {
			return false
		}
	}
	return true
}

// SynchronizedAll checks whether all peers in the network are synchronized.
func (s *Sim) SynchronizedAll() bool {
	return s.Synchronized(s.allPeers)
}

// Branches calculates the number of branches in the group.
// A branch occurs if two peers have fully validated ledgers
// that are not on the same chain of ledgers.
func (s *Sim) Branches(group *PeerGroup) int {
	if group.Size() < 1 {
		return 0
	}

	// Collect unique ledgers
	ledgerSet := make(map[LedgerID]*Ledger)
	for _, peer := range group.Peers() {
		ledger := peer.FullyValidatedLedger()
		ledgerSet[ledger.ID()] = ledger
	}

	ledgers := make([]*Ledger, 0, len(ledgerSet))
	for _, ledger := range ledgerSet {
		ledgers = append(ledgers, ledger)
	}

	return s.Oracle.Branches(ledgers)
}

// BranchesAll calculates the number of branches in the entire network.
func (s *Sim) BranchesAll() int {
	return s.Branches(s.allPeers)
}

// Now returns the current simulated time.
func (s *Sim) Now() SimTime {
	return s.Scheduler.Now()
}

// AddCollector adds a collector to receive simulation events.
func (s *Sim) AddCollector(collector Collector) {
	s.Collectors.Add(collector)
}

// PrintStatus prints the current simulation status.
func (s *Sim) PrintStatus() {
	fmt.Printf("Simulation Status at time %v:\n", s.Now())
	fmt.Printf("  Peers: %d\n", s.Size())
	fmt.Printf("  Synchronized: %v\n", s.SynchronizedAll())
	fmt.Printf("  Branches: %d\n", s.BranchesAll())

	for _, peer := range s.peers {
		fmt.Printf("  Peer %d: LCL=%d FVL=%d completed=%d\n",
			peer.ID,
			peer.LastClosedLedger().Seq(),
			peer.FullyValidatedLedger().Seq(),
			peer.CompletedLedgers(),
		)
	}
}

// -----------------------------------------------------------------------------
// Convenience setup methods

// SetupFullyConnected creates a fully connected network where all peers
// trust and are connected to each other.
func (s *Sim) SetupFullyConnected(numPeers int, delay SimDuration) *PeerGroup {
	group := s.CreateGroup(numPeers)
	group.TrustAndConnect(group, delay)
	return group
}

// SetupHubAndSpokes creates a network topology where a hub peer is connected
// to all spoke peers, but spokes are not connected to each other.
// All peers trust each other (full trust graph) but only hub-spoke connections exist.
func (s *Sim) SetupHubAndSpokes(numSpokes int, delay SimDuration) (*Peer, *PeerGroup) {
	hub := s.CreateGroup(1).Get(0)
	spokes := s.CreateGroup(numSpokes)

	hubGroup := NewPeerGroupSingle(hub)

	// Full trust graph (everyone trusts everyone)
	// This ensures consistent quorum calculations
	network := hubGroup.Union(spokes)
	network.Trust(network)

	// But only hub-spoke connections (no spoke-spoke connections)
	spokes.Connect(hubGroup, delay)
	hubGroup.Connect(spokes, delay)

	return hub, spokes
}

// SetupPartitioned creates two groups of peers that are internally
// connected but have no connections between groups.
func (s *Sim) SetupPartitioned(sizeA, sizeB int, delay SimDuration) (*PeerGroup, *PeerGroup) {
	groupA := s.CreateGroup(sizeA)
	groupB := s.CreateGroup(sizeB)

	// Each group is fully connected internally
	groupA.TrustAndConnect(groupA, delay)
	groupB.TrustAndConnect(groupB, delay)

	return groupA, groupB
}

// -----------------------------------------------------------------------------
// Test helper methods

// WaitForConsensus waits until all peers reach the specified ledger sequence.
func (s *Sim) WaitForConsensus(targetSeq uint32) bool {
	maxIterations := 1000000 // Safety limit

	for i := 0; i < maxIterations; i++ {
		allReached := true
		for _, peer := range s.peers {
			if peer.LastClosedLedger().Seq() < targetSeq {
				allReached = false
				break
			}
		}

		if allReached {
			return true
		}

		if !s.Scheduler.StepOne() {
			// No more events
			return false
		}
	}

	return false
}

// AssertSynchronized panics if peers are not synchronized.
func (s *Sim) AssertSynchronized() {
	if !s.SynchronizedAll() {
		panic("Simulation: peers are not synchronized")
	}
}

// AssertNoBranches panics if there are multiple branches.
func (s *Sim) AssertNoBranches() {
	if s.BranchesAll() > 1 {
		panic(fmt.Sprintf("Simulation: found %d branches", s.BranchesAll()))
	}
}

// GetConsensusResults returns a summary of consensus results for all peers.
func (s *Sim) GetConsensusResults() []PeerResult {
	results := make([]PeerResult, len(s.peers))
	for i, peer := range s.peers {
		results[i] = PeerResult{
			ID:                    peer.ID,
			CompletedLedgers:      peer.CompletedLedgers(),
			LastClosedLedgerSeq:   peer.LastClosedLedger().Seq(),
			FullyValidatedSeq:     peer.FullyValidatedLedger().Seq(),
			LastClosedLedgerID:    peer.LastClosedLedger().ID(),
			FullyValidatedLedgerID: peer.FullyValidatedLedger().ID(),
		}
	}
	return results
}

// PeerResult summarizes a peer's consensus state.
type PeerResult struct {
	ID                     PeerID
	CompletedLedgers       int
	LastClosedLedgerSeq    uint32
	FullyValidatedSeq      uint32
	LastClosedLedgerID     LedgerID
	FullyValidatedLedgerID LedgerID
}

// -----------------------------------------------------------------------------
// Event injection for testing

// SubmitTx submits a transaction to a specific peer.
func (s *Sim) SubmitTx(peer *Peer, tx Tx) {
	peer.Submit(tx)
}

// SubmitTxAll submits a transaction to all peers.
func (s *Sim) SubmitTxAll(tx Tx) {
	for _, peer := range s.peers {
		peer.Submit(tx)
	}
}

// InjectTx injects a transaction into a peer's ledger at a specific sequence.
// This is used for testing byzantine failures.
func (s *Sim) InjectTx(peer *Peer, seq uint32, tx Tx) {
	peer.InjectTx(seq, tx)
}

// Disconnect disconnects two peers.
func (s *Sim) Disconnect(a, b *Peer) {
	a.Disconnect(b)
	b.Disconnect(a)
}

// Reconnect reconnects two peers with the given delay.
func (s *Sim) Reconnect(a, b *Peer, delay SimDuration) {
	a.Connect(b, delay)
	b.Connect(a, delay)
}

// PartitionNetwork partitions the network between two groups.
func (s *Sim) PartitionNetwork(groupA, groupB *PeerGroup) {
	groupA.Disconnect(groupB)
	groupB.Disconnect(groupA)
}

// HealPartition reconnects two previously partitioned groups.
func (s *Sim) HealPartition(groupA, groupB *PeerGroup, delay SimDuration) {
	groupA.Connect(groupB, delay)
	groupB.Connect(groupA, delay)
}
