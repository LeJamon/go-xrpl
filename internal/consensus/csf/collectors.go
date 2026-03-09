package csf

// Event represents a simulation event that can be collected.
type Event interface {
	isEvent()
}

// StartRoundEvent fires when a peer starts a new consensus round.
type StartRoundEvent struct {
	Ledger   *Ledger
	Proposer bool
}

func (StartRoundEvent) isEvent() {}

// CloseLedgerEvent fires when a peer closes its ledger.
type CloseLedgerEvent struct {
	Ledger    *Ledger
	PriorSeq  uint32
	Proposers int
}

func (CloseLedgerEvent) isEvent() {}

// AcceptLedgerEvent fires when a peer accepts a new ledger.
type AcceptLedgerEvent struct {
	Ledger *Ledger
}

func (AcceptLedgerEvent) isEvent() {}

// FullyValidateLedgerEvent fires when a peer fully validates a ledger.
type FullyValidateLedgerEvent struct {
	Ledger *Ledger
}

func (FullyValidateLedgerEvent) isEvent() {}

// ReceiveProposalEvent fires when a peer receives a proposal.
type ReceiveProposalEvent struct {
	Proposal *Proposal
}

func (ReceiveProposalEvent) isEvent() {}

// ReceiveValidationEvent fires when a peer receives a validation.
type ReceiveValidationEvent struct {
	Validation *Validation
}

func (ReceiveValidationEvent) isEvent() {}

// WrongPrevLedgerEvent fires when peer detects wrong previous ledger.
type WrongPrevLedgerEvent struct {
	WrongLedger   *Ledger
	CorrectLedger *Ledger
}

func (WrongPrevLedgerEvent) isEvent() {}

// Collector collects events from the simulation for analysis.
type Collector interface {
	// On is called when an event occurs.
	On(peer PeerID, when SimTime, event Event)
}

// CollectorFunc is a function adapter for Collector.
type CollectorFunc func(peer PeerID, when SimTime, event Event)

func (f CollectorFunc) On(peer PeerID, when SimTime, event Event) {
	f(peer, when, event)
}

// Collectors manages a set of collectors.
type Collectors struct {
	collectors []Collector
}

// NewCollectors creates a new collector manager.
func NewCollectors() *Collectors {
	return &Collectors{}
}

// Add adds a collector.
func (c *Collectors) Add(collector Collector) {
	c.collectors = append(c.collectors, collector)
}

// On dispatches an event to all collectors.
func (c *Collectors) On(peer PeerID, when SimTime, event Event) {
	for _, collector := range c.collectors {
		collector.On(peer, when, event)
	}
}

// SimDurationCollector tracks the start and end time of the simulation.
type SimDurationCollector struct {
	Start SimTime
	Stop  SimTime
}

func (c *SimDurationCollector) On(peer PeerID, when SimTime, event Event) {
	if c.Start == 0 || when < c.Start {
		c.Start = when
	}
	if when > c.Stop {
		c.Stop = when
	}
}

// Duration returns the total simulation duration.
func (c *SimDurationCollector) Duration() SimDuration {
	return SimDuration(c.Stop - c.Start)
}

// JumpCollector tracks ledger history "jumps" (when LCL changes chains).
type JumpCollector struct {
	CloseJumps          []Jump
	FullyValidatedJumps []Jump
}

// Jump represents a ledger history jump.
type Jump struct {
	From *Ledger
	To   *Ledger
}

func (c *JumpCollector) On(peer PeerID, when SimTime, event Event) {
	switch e := event.(type) {
	case AcceptLedgerEvent:
		// Could track close jumps here if we had previous ledger info
		_ = e
	case FullyValidateLedgerEvent:
		// Could track fully validated jumps here
		_ = e
	}
}

// ByNodeCollector collects events grouped by node.
type ByNodeCollector[T Collector] struct {
	collectors map[PeerID]T
	factory    func() T
}

// NewByNodeCollector creates a collector that maintains per-node collectors.
func NewByNodeCollector[T Collector](factory func() T) *ByNodeCollector[T] {
	return &ByNodeCollector[T]{
		collectors: make(map[PeerID]T),
		factory:    factory,
	}
}

func (c *ByNodeCollector[T]) On(peer PeerID, when SimTime, event Event) {
	collector, ok := c.collectors[peer]
	if !ok {
		collector = c.factory()
		c.collectors[peer] = collector
	}
	collector.On(peer, when, event)
}

// Get returns the collector for a specific peer.
func (c *ByNodeCollector[T]) Get(peer PeerID) T {
	return c.collectors[peer]
}

// LedgerCollector tracks all ledgers seen in the simulation.
type LedgerCollector struct {
	Ledgers map[LedgerID]*Ledger
}

// NewLedgerCollector creates a new ledger collector.
func NewLedgerCollector() *LedgerCollector {
	return &LedgerCollector{
		Ledgers: make(map[LedgerID]*Ledger),
	}
}

func (c *LedgerCollector) On(peer PeerID, when SimTime, event Event) {
	switch e := event.(type) {
	case AcceptLedgerEvent:
		c.Ledgers[e.Ledger.ID()] = e.Ledger
	case FullyValidateLedgerEvent:
		c.Ledgers[e.Ledger.ID()] = e.Ledger
	}
}

// TxSubmitCollector tracks transaction submission to validation latency.
type TxSubmitCollector struct {
	Submitted  map[uint32]SimTime        // tx ID -> submit time
	Validated  map[uint32]SimTime        // tx ID -> validation time
	ByPeer     map[PeerID]map[uint32]SimTime // peer -> tx ID -> validation time
}

// NewTxSubmitCollector creates a new transaction tracking collector.
func NewTxSubmitCollector() *TxSubmitCollector {
	return &TxSubmitCollector{
		Submitted: make(map[uint32]SimTime),
		Validated: make(map[uint32]SimTime),
		ByPeer:    make(map[PeerID]map[uint32]SimTime),
	}
}

func (c *TxSubmitCollector) On(peer PeerID, when SimTime, event Event) {
	switch e := event.(type) {
	case FullyValidateLedgerEvent:
		if c.ByPeer[peer] == nil {
			c.ByPeer[peer] = make(map[uint32]SimTime)
		}
		for _, tx := range e.Ledger.Txs().Transactions() {
			if _, ok := c.Validated[tx.ID]; !ok {
				c.Validated[tx.ID] = when
			}
			c.ByPeer[peer][tx.ID] = when
		}
	}
}

// RecordSubmit records when a transaction was submitted.
func (c *TxSubmitCollector) RecordSubmit(txID uint32, when SimTime) {
	if _, ok := c.Submitted[txID]; !ok {
		c.Submitted[txID] = when
	}
}

// Latency returns the submission to validation latency for a transaction.
func (c *TxSubmitCollector) Latency(txID uint32) SimDuration {
	submit, ok := c.Submitted[txID]
	if !ok {
		return 0
	}
	validate, ok := c.Validated[txID]
	if !ok {
		return 0
	}
	return SimDuration(validate - submit)
}
