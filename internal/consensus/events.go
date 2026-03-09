package consensus

import (
	"time"
)

// Event represents a consensus event that can be emitted.
type Event interface {
	// Type returns the event type identifier.
	Type() EventType
}

// EventType identifies the type of consensus event.
type EventType int

const (
	// EventRoundStarted fires when a new consensus round begins.
	EventRoundStarted EventType = iota

	// EventModeChanged fires when the consensus mode changes.
	EventModeChanged

	// EventPhaseChanged fires when the consensus phase changes.
	EventPhaseChanged

	// EventProposalReceived fires when a proposal is received.
	EventProposalReceived

	// EventValidationReceived fires when a validation is received.
	EventValidationReceived

	// EventConsensusReached fires when consensus is achieved.
	EventConsensusReached

	// EventLedgerAccepted fires when a new ledger is accepted.
	EventLedgerAccepted

	// EventDisputeCreated fires when a new disputed transaction is found.
	EventDisputeCreated

	// EventDisputeResolved fires when a dispute is resolved.
	EventDisputeResolved

	// EventTimerFired fires when a consensus timer expires.
	EventTimerFired
)

// String returns the string representation.
func (t EventType) String() string {
	names := map[EventType]string{
		EventRoundStarted:       "RoundStarted",
		EventModeChanged:        "ModeChanged",
		EventPhaseChanged:       "PhaseChanged",
		EventProposalReceived:   "ProposalReceived",
		EventValidationReceived: "ValidationReceived",
		EventConsensusReached:   "ConsensusReached",
		EventLedgerAccepted:     "LedgerAccepted",
		EventDisputeCreated:     "DisputeCreated",
		EventDisputeResolved:    "DisputeResolved",
		EventTimerFired:         "TimerFired",
	}
	if name, ok := names[t]; ok {
		return name
	}
	return "Unknown"
}

// RoundStartedEvent is emitted when a new consensus round begins.
type RoundStartedEvent struct {
	Round     RoundID
	Mode      Mode
	Timestamp time.Time
}

func (e *RoundStartedEvent) Type() EventType { return EventRoundStarted }

// ModeChangedEvent is emitted when the consensus mode changes.
type ModeChangedEvent struct {
	OldMode   Mode
	NewMode   Mode
	Reason    string
	Timestamp time.Time
}

func (e *ModeChangedEvent) Type() EventType { return EventModeChanged }

// PhaseChangedEvent is emitted when the consensus phase changes.
type PhaseChangedEvent struct {
	Round     RoundID
	OldPhase  Phase
	NewPhase  Phase
	Timestamp time.Time
}

func (e *PhaseChangedEvent) Type() EventType { return EventPhaseChanged }

// ProposalReceivedEvent is emitted when a proposal is received.
type ProposalReceivedEvent struct {
	Proposal  *Proposal
	Trusted   bool
	Timestamp time.Time
}

func (e *ProposalReceivedEvent) Type() EventType { return EventProposalReceived }

// ValidationReceivedEvent is emitted when a validation is received.
type ValidationReceivedEvent struct {
	Validation *Validation
	Trusted    bool
	Timestamp  time.Time
}

func (e *ValidationReceivedEvent) Type() EventType { return EventValidationReceived }

// ConsensusReachedEvent is emitted when consensus is achieved.
type ConsensusReachedEvent struct {
	Round       RoundID
	TxSet       TxSetID
	CloseTime   time.Time
	Proposers   int
	Result      Result
	Duration    time.Duration
	Timestamp   time.Time
}

func (e *ConsensusReachedEvent) Type() EventType { return EventConsensusReached }

// LedgerAcceptedEvent is emitted when a new ledger is accepted.
type LedgerAcceptedEvent struct {
	LedgerID    LedgerID
	LedgerSeq   uint32
	TxCount     int
	CloseTime   time.Time
	Validations int
	Timestamp   time.Time
}

func (e *LedgerAcceptedEvent) Type() EventType { return EventLedgerAccepted }

// DisputeCreatedEvent is emitted when a disputed transaction is found.
type DisputeCreatedEvent struct {
	Round     RoundID
	TxID      TxID
	OurVote   bool
	Timestamp time.Time
}

func (e *DisputeCreatedEvent) Type() EventType { return EventDisputeCreated }

// DisputeResolvedEvent is emitted when a dispute is resolved.
type DisputeResolvedEvent struct {
	Round     RoundID
	TxID      TxID
	Included  bool
	YayVotes  int
	NayVotes  int
	Timestamp time.Time
}

func (e *DisputeResolvedEvent) Type() EventType { return EventDisputeResolved }

// TimerType identifies consensus timer types.
type TimerType int

const (
	// TimerLedgerClose fires when it's time to close the ledger.
	TimerLedgerClose TimerType = iota

	// TimerProposalExpire fires when proposals have expired.
	TimerProposalExpire

	// TimerRoundTimeout fires when a round has timed out.
	TimerRoundTimeout
)

// TimerFiredEvent is emitted when a consensus timer expires.
type TimerFiredEvent struct {
	Timer     TimerType
	Round     RoundID
	Timestamp time.Time
}

func (e *TimerFiredEvent) Type() EventType { return EventTimerFired }

// EventSubscriber receives consensus events.
type EventSubscriber interface {
	// OnEvent is called when an event occurs.
	OnEvent(event Event)
}

// EventBus manages event subscriptions and delivery.
type EventBus struct {
	subscribers []EventSubscriber
	eventCh     chan Event
	stopCh      chan struct{}
}

// NewEventBus creates a new event bus.
func NewEventBus(bufferSize int) *EventBus {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	return &EventBus{
		subscribers: make([]EventSubscriber, 0),
		eventCh:     make(chan Event, bufferSize),
		stopCh:      make(chan struct{}),
	}
}

// Subscribe adds a subscriber to receive events.
func (eb *EventBus) Subscribe(sub EventSubscriber) {
	eb.subscribers = append(eb.subscribers, sub)
}

// Publish sends an event to all subscribers.
func (eb *EventBus) Publish(event Event) {
	select {
	case eb.eventCh <- event:
	default:
		// Channel full, drop event (could log warning)
	}
}

// Start begins processing events.
func (eb *EventBus) Start() {
	go eb.run()
}

// Stop stops the event bus.
func (eb *EventBus) Stop() {
	close(eb.stopCh)
}

// Events returns the event channel for direct consumption.
func (eb *EventBus) Events() <-chan Event {
	return eb.eventCh
}

func (eb *EventBus) run() {
	for {
		select {
		case <-eb.stopCh:
			return
		case event := <-eb.eventCh:
			for _, sub := range eb.subscribers {
				sub.OnEvent(event)
			}
		}
	}
}
