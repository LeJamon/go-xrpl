package csf

import (
	"errors"
	"fmt"
	"math"
)

type Sim struct {
	Scheduler  *Scheduler
	Oracle     *LedgerOracle
	Net        *BasicNetwork
	TrustGraph *TrustGraph
	Collectors *Collectors

	peers    []*Peer
	allPeers *PeerGroup
	nextID   PeerID
	registry *peerRegistry
}

func NewSim() *Sim {
	scheduler := NewScheduler()
	return &Sim{
		Scheduler:  scheduler,
		Oracle:     NewLedgerOracle(),
		Net:        NewBasicNetwork(scheduler),
		TrustGraph: NewTrustGraph(),
		Collectors: NewCollectors(),
		allPeers:   NewPeerGroup(),
		registry:   newPeerRegistry(),
	}
}

func (s *Sim) CreateGroup(numPeers int) *PeerGroup {
	if numPeers < 0 {
		panic("csf: peer count must not be negative")
	}
	peers := make([]*Peer, numPeers)
	for i := range numPeers {
		peer := newPeer(
			s.nextID,
			s.Scheduler,
			s.Oracle,
			s.Net,
			s.TrustGraph,
			s.Collectors,
			s.registry,
		)
		s.TrustGraph.Trust(peer.ID, peer.ID)
		s.registry.add(peer)
		s.peers = append(s.peers, peer)
		peers[i] = peer
		s.nextID++
	}
	group := NewPeerGroupFrom(peers)
	s.allPeers = s.allPeers.Union(group)
	return group
}

func (s *Sim) Size() int {
	return len(s.peers)
}

func (s *Sim) Peers() []*Peer {
	return append([]*Peer(nil), s.peers...)
}

func (s *Sim) AllPeers() *PeerGroup {
	return s.allPeers
}

func (s *Sim) Peer(id PeerID) *Peer {
	return s.registry.get(id)
}

func (s *Sim) Run(ledgers int) error {
	if ledgers < 0 {
		return errors.New("csf: ledger count must not be negative")
	}
	if ledgers == 0 {
		return nil
	}
	for _, peer := range s.peers {
		completed := peer.CompletedLedgers()
		if ledgers > math.MaxInt-completed {
			return errors.New("csf: ledger completion target overflows int")
		}
		peer.SetTargetLedgers(completed + ledgers)
		if err := peer.Start(); err != nil {
			return errors.Join(fmt.Errorf("start peer %d: %w", peer.ID, err), s.Stop())
		}
	}
	var runErr error
	s.Scheduler.StepWhile(func() bool {
		for _, peer := range s.peers {
			if err := peer.asyncError(); err != nil {
				runErr = fmt.Errorf("peer %d: %w", peer.ID, err)
				return false
			}
		}
		for _, peer := range s.peers {
			if !peer.targetReached() {
				return true
			}
		}
		return false
	})
	if runErr != nil {
		return errors.Join(runErr, s.Stop())
	}
	for _, peer := range s.peers {
		if err := peer.asyncError(); err != nil {
			return fmt.Errorf("peer %d: %w", peer.ID, err)
		}
		if !peer.targetReached() {
			return fmt.Errorf(
				"csf: scheduler became idle before peer %d reached completion target %d",
				peer.ID,
				peer.TargetLedgers(),
			)
		}
	}
	return nil
}

func (s *Sim) Stop() error {
	errs := make([]error, 0, len(s.peers))
	for _, peer := range s.peers {
		if err := peer.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("stop peer %d: %w", peer.ID, err))
		}
	}
	s.Net.DisconnectAll()
	return errors.Join(errs...)
}

func (s *Sim) Synchronized(group *PeerGroup) bool {
	if group.Size() < 2 {
		return true
	}
	reference := group.Get(0)
	referenceLCL := reference.LastClosedLedger().ID()
	referenceFVL := reference.FullyValidatedLedger().ID()
	for _, peer := range group.Peers()[1:] {
		if peer.LastClosedLedger().ID() != referenceLCL {
			return false
		}
		if peer.FullyValidatedLedger().ID() != referenceFVL {
			return false
		}
	}
	return true
}

func (s *Sim) SynchronizedAll() bool {
	return s.Synchronized(s.allPeers)
}

func (s *Sim) Branches(group *PeerGroup) int {
	byID := make(map[LedgerID]*Ledger)
	for _, peer := range group.Peers() {
		ledger := peer.FullyValidatedLedger()
		byID[ledger.ID()] = ledger
	}
	ledgers := make([]*Ledger, 0, len(byID))
	for _, ledger := range byID {
		ledgers = append(ledgers, ledger)
	}
	return s.Oracle.Branches(ledgers)
}

func (s *Sim) BranchesAll() int {
	return s.Branches(s.allPeers)
}

func (s *Sim) Now() SimTime {
	return s.Scheduler.Now()
}

func (s *Sim) AddCollector(collector Collector) {
	s.Collectors.Add(collector)
}

func (s *Sim) SetupFullyConnected(numPeers int, delay SimDuration) *PeerGroup {
	group := s.CreateGroup(numPeers)
	group.TrustAndConnect(group, delay)
	return group
}

func (s *Sim) SetupHubAndSpokes(numSpokes int, delay SimDuration) (*Peer, *PeerGroup) {
	hub := s.CreateGroup(1).Get(0)
	spokes := s.CreateGroup(numSpokes)
	all := NewPeerGroupSingle(hub).Union(spokes)
	all.Trust(all)
	spokes.Connect(NewPeerGroupSingle(hub), delay)
	return hub, spokes
}

func (s *Sim) SetupPartitioned(sizeA, sizeB int, delay SimDuration) (*PeerGroup, *PeerGroup) {
	groupA := s.CreateGroup(sizeA)
	groupB := s.CreateGroup(sizeB)
	groupA.TrustAndConnect(groupA, delay)
	groupB.TrustAndConnect(groupB, delay)
	return groupA, groupB
}

func (s *Sim) SubmitTx(peer *Peer, tx Tx) {
	peer.Submit(tx)
}

func (s *Sim) SubmitTxAll(tx Tx) {
	for _, peer := range s.peers {
		peer.Submit(tx)
	}
}

func (s *Sim) InjectTx(peer *Peer, seq uint32, tx Tx) {
	peer.InjectTx(seq, tx)
}

func (s *Sim) Disconnect(a, b *Peer) {
	a.Disconnect(b)
}

func (s *Sim) Reconnect(a, b *Peer, delay SimDuration) {
	a.Connect(b, delay)
}

func (s *Sim) PartitionNetwork(groupA, groupB *PeerGroup) {
	groupA.Disconnect(groupB)
}

func (s *Sim) HealPartition(groupA, groupB *PeerGroup, delay SimDuration) {
	groupA.Connect(groupB, delay)
}
