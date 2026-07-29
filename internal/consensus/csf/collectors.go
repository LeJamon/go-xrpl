package csf

import (
	"sync"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

type Event interface {
	isEvent()
}

type StartRoundEvent struct {
	Ledger   *Ledger
	Proposer bool
}

func (StartRoundEvent) isEvent() {}

type CloseLedgerEvent struct {
	TxSet     *TxSet
	CloseTime SimTime
}

func (CloseLedgerEvent) isEvent() {}

type AcceptLedgerEvent struct {
	Ledger *Ledger
}

func (AcceptLedgerEvent) isEvent() {}

type FullyValidateLedgerEvent struct {
	Ledger *Ledger
}

func (FullyValidateLedgerEvent) isEvent() {}

type ReceiveProposalEvent struct {
	Proposal *consensus.Proposal
}

func (ReceiveProposalEvent) isEvent() {}

type ReceiveValidationEvent struct {
	Validation *consensus.Validation
}

func (ReceiveValidationEvent) isEvent() {}

type WrongPrevLedgerEvent struct {
	Expected consensus.LedgerID
	Received consensus.LedgerID
}

func (WrongPrevLedgerEvent) isEvent() {}

type Collector interface {
	On(peer PeerID, when SimTime, event Event)
}

type CollectorFunc func(peer PeerID, when SimTime, event Event)

func (f CollectorFunc) On(peer PeerID, when SimTime, event Event) {
	f(peer, when, event)
}

type Collectors struct {
	mu         sync.RWMutex
	collectors []Collector
}

func NewCollectors() *Collectors {
	return &Collectors{}
}

func (c *Collectors) Add(collector Collector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.collectors = append(c.collectors, collector)
}

func (c *Collectors) On(peer PeerID, when SimTime, event Event) {
	c.mu.RLock()
	collectors := append([]Collector(nil), c.collectors...)
	c.mu.RUnlock()
	for _, collector := range collectors {
		collector.On(peer, when, event)
	}
}

type SimDurationCollector struct {
	mu    sync.Mutex
	seen  bool
	Start SimTime
	Stop  SimTime
}

func (c *SimDurationCollector) On(_ PeerID, when SimTime, _ Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.seen || when < c.Start {
		c.Start = when
	}
	c.seen = true
	if when > c.Stop {
		c.Stop = when
	}
}

func (c *SimDurationCollector) Duration() SimDuration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return SimDuration(c.Stop - c.Start)
}
