package consensus

import (
	"log/slog"
	"sync"
	"sync/atomic"
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

	// EventTimerFired fires when a consensus timer expires.
	EventTimerFired
)

func (t EventType) String() string {
	switch t {
	case EventRoundStarted:
		return "RoundStarted"
	case EventModeChanged:
		return "ModeChanged"
	case EventPhaseChanged:
		return "PhaseChanged"
	case EventProposalReceived:
		return "ProposalReceived"
	case EventValidationReceived:
		return "ValidationReceived"
	case EventConsensusReached:
		return "ConsensusReached"
	case EventLedgerAccepted:
		return "LedgerAccepted"
	case EventTimerFired:
		return "TimerFired"
	default:
		return "Unknown"
	}
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
	Round     RoundID
	TxSet     TxSetID
	CloseTime time.Time
	Proposers int
	Result    Result
	Duration  time.Duration
	Timestamp time.Time
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

// TimerType identifies consensus timer types.
type TimerType int

const (
	// TimerLedgerClose fires when it's time to close the ledger.
	TimerLedgerClose TimerType = iota

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
	mu          sync.RWMutex
	subscribers []EventSubscriber
	eventCh     chan Event
	stopCh      chan struct{}
	stopOnce    sync.Once
	dropped     atomic.Uint64
	lastDropLog atomic.Int64 // unix-nanos of the last drop warning, for rate limiting
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
	eb.mu.Lock()
	eb.subscribers = append(eb.subscribers, sub)
	eb.mu.Unlock()
}

// Publish sends an event to all subscribers. On a full buffer the event is
// dropped — but counted and (rate-limited) logged, so an operator debugging
// "missing validations on the stream" has a signal instead of silence. This is
// the one lossy consensus channel; every other shed in the tree is counted, and
// now so is this one.
func (eb *EventBus) Publish(event Event) {
	select {
	case eb.eventCh <- event:
	default:
		n := eb.dropped.Add(1)
		// Rate-limit to at most one warning per second so a burst that overflows
		// the buffer doesn't itself flood the log.
		now := time.Now().UnixNano()
		last := eb.lastDropLog.Load()
		if now-last >= int64(time.Second) && eb.lastDropLog.CompareAndSwap(last, now) {
			slog.Warn("consensus event bus buffer full; dropping events",
				"t", "consensus", "eventType", event.Type(), "droppedTotal", n)
		}
	}
}

// DroppedEvents returns the cumulative count of events shed because the buffer
// was full.
func (eb *EventBus) DroppedEvents() uint64 { return eb.dropped.Load() }

// Start begins processing events.
func (eb *EventBus) Start() {
	go eb.run()
}

// Stop stops the event bus. Idempotent: a second call (defensive cleanup,
// error-path + deferred stop) is a no-op rather than a close-of-closed panic.
func (eb *EventBus) Stop() {
	eb.stopOnce.Do(func() { close(eb.stopCh) })
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
			eb.mu.RLock()
			subs := eb.subscribers
			eb.mu.RUnlock()
			for _, sub := range subs {
				sub.OnEvent(event)
			}
		}
	}
}
