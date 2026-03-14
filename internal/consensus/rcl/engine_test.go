package rcl

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// mockLedger implements consensus.Ledger for testing
type mockLedger struct {
	id        consensus.LedgerID
	seq       uint32
	closeTime time.Time
	txSetID   consensus.TxSetID
	txs       [][]byte
}

func (l *mockLedger) ID() consensus.LedgerID       { return l.id }
func (l *mockLedger) Seq() uint32                  { return l.seq }
func (l *mockLedger) ParentID() consensus.LedgerID { return consensus.LedgerID{} }
func (l *mockLedger) CloseTime() time.Time         { return l.closeTime }
func (l *mockLedger) TxSetID() consensus.TxSetID   { return l.txSetID }
func (l *mockLedger) Bytes() []byte                { return nil }

// mockTxSet implements consensus.TxSet for testing
type mockTxSet struct {
	id  consensus.TxSetID
	txs [][]byte
}

func (ts *mockTxSet) ID() consensus.TxSetID           { return ts.id }
func (ts *mockTxSet) Txs() [][]byte                   { return ts.txs }
func (ts *mockTxSet) Size() int                       { return len(ts.txs) }
func (ts *mockTxSet) Contains(id consensus.TxID) bool { return false }
func (ts *mockTxSet) Add(tx []byte) error             { ts.txs = append(ts.txs, tx); return nil }
func (ts *mockTxSet) Remove(id consensus.TxID) error  { return nil }
func (ts *mockTxSet) Bytes() []byte                   { return nil }

// mockAdaptor implements consensus.Adaptor for testing
type mockAdaptor struct {
	mu sync.RWMutex

	// Mode
	opMode    consensus.OperatingMode
	validator bool

	// Validator info
	nodeID  consensus.NodeID
	trusted map[consensus.NodeID]bool
	quorum  int

	// Data stores
	ledgers map[consensus.LedgerID]consensus.Ledger
	txSets  map[consensus.TxSetID]consensus.TxSet
	lastLCL consensus.Ledger

	// Pending transactions
	pendingTxs [][]byte

	// Callback tracking
	proposalsBroadcast   []*consensus.Proposal
	validationsBroadcast []*consensus.Validation
	proposalsRelayed     []*consensus.Proposal
	txSetsRequested      []consensus.TxSetID
	ledgersRequested     []consensus.LedgerID
	modeChanges          []consensus.Mode
	phaseChanges         []consensus.Phase

	// Time
	now time.Time
}

func newMockAdaptor() *mockAdaptor {
	now := time.Now()
	initialLedger := &mockLedger{
		id:        consensus.LedgerID{1},
		seq:       100,
		closeTime: now.Add(-5 * time.Second),
	}

	return &mockAdaptor{
		opMode:    consensus.OpModeFull,
		validator: true,
		nodeID:    consensus.NodeID{1},
		trusted:   make(map[consensus.NodeID]bool),
		quorum:    2,
		ledgers:   map[consensus.LedgerID]consensus.Ledger{initialLedger.ID(): initialLedger},
		txSets:    make(map[consensus.TxSetID]consensus.TxSet),
		lastLCL:   initialLedger,
		now:       now,
	}
}

// Network operations
func (a *mockAdaptor) BroadcastProposal(proposal *consensus.Proposal) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.proposalsBroadcast = append(a.proposalsBroadcast, proposal)
	return nil
}

func (a *mockAdaptor) BroadcastValidation(validation *consensus.Validation) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.validationsBroadcast = append(a.validationsBroadcast, validation)
	return nil
}

func (a *mockAdaptor) RelayProposal(proposal *consensus.Proposal) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.proposalsRelayed = append(a.proposalsRelayed, proposal)
	return nil
}

func (a *mockAdaptor) RequestTxSet(id consensus.TxSetID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.txSetsRequested = append(a.txSetsRequested, id)
	return nil
}

func (a *mockAdaptor) RequestLedger(id consensus.LedgerID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ledgersRequested = append(a.ledgersRequested, id)
	return nil
}

// Ledger operations
func (a *mockAdaptor) GetLedger(id consensus.LedgerID) (consensus.Ledger, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ledgers[id], nil
}

func (a *mockAdaptor) GetLastClosedLedger() (consensus.Ledger, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastLCL, nil
}

func (a *mockAdaptor) BuildLedger(parent consensus.Ledger, txSet consensus.TxSet, closeTime time.Time) (consensus.Ledger, error) {
	newLedger := &mockLedger{
		id:        consensus.LedgerID{byte(parent.Seq() + 1)},
		seq:       parent.Seq() + 1,
		closeTime: closeTime,
		txSetID:   txSet.ID(),
		txs:       txSet.Txs(),
	}
	return newLedger, nil
}

func (a *mockAdaptor) ValidateLedger(ledger consensus.Ledger) error {
	return nil
}

func (a *mockAdaptor) StoreLedger(ledger consensus.Ledger) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ledgers[ledger.ID()] = ledger
	a.lastLCL = ledger
	return nil
}

// Transaction operations
func (a *mockAdaptor) GetTxSet(id consensus.TxSetID) (consensus.TxSet, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if txSet, ok := a.txSets[id]; ok {
		return txSet, nil
	}
	// Return empty tx set for missing
	return &mockTxSet{id: id}, nil
}

func (a *mockAdaptor) BuildTxSet(txs [][]byte) (consensus.TxSet, error) {
	txSet := &mockTxSet{txs: txs}
	// Generate a simple ID based on length
	txSet.id = consensus.TxSetID{byte(len(txs))}
	a.mu.Lock()
	a.txSets[txSet.id] = txSet
	a.mu.Unlock()
	return txSet, nil
}

func (a *mockAdaptor) GetPendingTxs() [][]byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.pendingTxs
}

func (a *mockAdaptor) HasTx(id consensus.TxID) bool {
	return false
}

func (a *mockAdaptor) GetTx(id consensus.TxID) ([]byte, error) {
	return nil, nil
}

// Validator operations
func (a *mockAdaptor) GetValidatorKey() (consensus.NodeID, error) {
	return a.nodeID, nil
}

func (a *mockAdaptor) IsValidator() bool {
	return a.validator
}

func (a *mockAdaptor) IsTrusted(nodeID consensus.NodeID) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.trusted[nodeID]
}

func (a *mockAdaptor) GetTrustedValidators() []consensus.NodeID {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var result []consensus.NodeID
	for nodeID := range a.trusted {
		result = append(result, nodeID)
	}
	return result
}

func (a *mockAdaptor) GetQuorum() int {
	return a.quorum
}

// Signing and verification
func (a *mockAdaptor) SignProposal(proposal *consensus.Proposal) error {
	proposal.Signature = []byte("test-sig")
	return nil
}

func (a *mockAdaptor) SignValidation(validation *consensus.Validation) error {
	validation.Signature = []byte("test-sig")
	return nil
}

func (a *mockAdaptor) VerifyProposal(proposal *consensus.Proposal) error {
	return nil
}

func (a *mockAdaptor) VerifyValidation(validation *consensus.Validation) error {
	return nil
}

// Status and timing
func (a *mockAdaptor) GetOperatingMode() consensus.OperatingMode {
	return a.opMode
}

func (a *mockAdaptor) SetOperatingMode(mode consensus.OperatingMode) {
	a.opMode = mode
}

func (a *mockAdaptor) Now() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.now
}

func (a *mockAdaptor) CloseTimeResolution() time.Duration {
	return time.Second
}

func (a *mockAdaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation) {
}

func (a *mockAdaptor) OnModeChange(oldMode, newMode consensus.Mode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.modeChanges = append(a.modeChanges, newMode)
}

func (a *mockAdaptor) OnPhaseChange(oldPhase, newPhase consensus.Phase) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.phaseChanges = append(a.phaseChanges, newPhase)
}

func (a *mockAdaptor) setTrusted(nodes []consensus.NodeID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trusted = make(map[consensus.NodeID]bool)
	for _, n := range nodes {
		a.trusted[n] = true
	}
}

// Tests

func TestEngine_NewEngine(t *testing.T) {
	adaptor := newMockAdaptor()
	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	if engine == nil {
		t.Fatal("Expected engine to be created")
	}

	if engine.Mode() != consensus.ModeObserving {
		t.Errorf("Expected initial mode to be Observing, got %v", engine.Mode())
	}

	if engine.Phase() != consensus.PhaseAccepted {
		t.Errorf("Expected initial phase to be Accepted, got %v", engine.Phase())
	}
}

func TestEngine_StartStop(t *testing.T) {
	adaptor := newMockAdaptor()
	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	if err := engine.Stop(); err != nil {
		t.Fatalf("Failed to stop engine: %v", err)
	}
}

func TestEngine_StartRound_Proposing(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("Failed to start round: %v", err)
	}

	if engine.Mode() != consensus.ModeProposing {
		t.Errorf("Expected Proposing mode, got %v", engine.Mode())
	}

	if engine.Phase() != consensus.PhaseOpen {
		t.Errorf("Expected Open phase, got %v", engine.Phase())
	}

	state := engine.State()
	if state == nil {
		t.Fatal("Expected state to be set")
	}

	if state.Round != round {
		t.Errorf("Expected round %v, got %v", round, state.Round)
	}
}

func TestEngine_StartRound_Observing(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = false

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, false); err != nil {
		t.Fatalf("Failed to start round: %v", err)
	}

	if engine.Mode() != consensus.ModeObserving {
		t.Errorf("Expected Observing mode, got %v", engine.Mode())
	}
}

func TestEngine_OnProposal(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.setTrusted([]consensus.NodeID{{2}, {3}})

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	// Start a round first
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Receive a proposal from a trusted validator
	proposal := &consensus.Proposal{
		Round:          round,
		NodeID:         consensus.NodeID{2},
		Position:       0,
		TxSet:          consensus.TxSetID{1},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}

	if err := engine.OnProposal(proposal); err != nil {
		t.Fatalf("Failed to process proposal: %v", err)
	}

	// Check that proposal was relayed (since from trusted validator)
	adaptor.mu.RLock()
	relayed := len(adaptor.proposalsRelayed)
	adaptor.mu.RUnlock()

	if relayed != 1 {
		t.Errorf("Expected 1 proposal to be relayed, got %d", relayed)
	}
}

func TestEngine_OnProposal_Untrusted(t *testing.T) {
	adaptor := newMockAdaptor()
	// Don't set any trusted validators

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Receive a proposal from an untrusted validator
	proposal := &consensus.Proposal{
		Round:          round,
		NodeID:         consensus.NodeID{2},
		Position:       0,
		TxSet:          consensus.TxSetID{1},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}

	if err := engine.OnProposal(proposal); err != nil {
		t.Fatalf("Failed to process proposal: %v", err)
	}

	// Check that proposal was NOT relayed (since from untrusted validator)
	adaptor.mu.RLock()
	relayed := len(adaptor.proposalsRelayed)
	adaptor.mu.RUnlock()

	if relayed != 0 {
		t.Errorf("Expected 0 proposals to be relayed, got %d", relayed)
	}
}

func TestEngine_OnValidation(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.setTrusted([]consensus.NodeID{{2}})

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{101},
		LedgerSeq: 101,
		NodeID:    consensus.NodeID{2},
		SignTime:  time.Now(),
		SeenTime:  time.Now(),
		Full:      true,
	}

	if err := engine.OnValidation(validation); err != nil {
		t.Fatalf("Failed to process validation: %v", err)
	}
}

func TestEngine_OnTxSet(t *testing.T) {
	adaptor := newMockAdaptor()

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Receive a tx set with 3 transactions
	txs := [][]byte{
		{1, 2, 3},
		{4, 5, 6},
		{7, 8, 9},
	}

	// The mock adaptor generates ID based on tx count
	expectedID := consensus.TxSetID{3}

	if err := engine.OnTxSet(expectedID, txs); err != nil {
		t.Fatalf("Failed to process tx set: %v", err)
	}
}

func TestEngine_IsProposing(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	// Before starting round
	if engine.IsProposing() {
		t.Error("Should not be proposing before round starts")
	}

	// Start round as proposer
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	if !engine.IsProposing() {
		t.Error("Should be proposing after starting round as proposer")
	}
}

func TestEngine_Timing(t *testing.T) {
	adaptor := newMockAdaptor()
	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	timing := engine.Timing()
	if timing.LedgerMinClose != config.Timing.LedgerMinClose {
		t.Error("Timing mismatch")
	}
}

func TestEngine_Events(t *testing.T) {
	adaptor := newMockAdaptor()
	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	events := engine.Events()
	if events == nil {
		t.Error("Expected events channel to be non-nil")
	}
}

func TestEngine_ModeTransitions(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	// Start as observer
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, false)

	if engine.Mode() != consensus.ModeObserving {
		t.Errorf("Expected Observing mode, got %v", engine.Mode())
	}

	// Start new round as proposer
	round = consensus.RoundID{Seq: 102, ParentHash: consensus.LedgerID{101}}
	engine.StartRound(round, true)

	if engine.Mode() != consensus.ModeProposing {
		t.Errorf("Expected Proposing mode, got %v", engine.Mode())
	}
}

func TestEngine_PhaseTransitions(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	// Use very short timings for testing
	config.Timing.LedgerMinClose = 10 * time.Millisecond
	config.Timing.LedgerMaxClose = 100 * time.Millisecond

	engine := NewEngine(adaptor, config)

	// Must call Start to initialize prevLedger
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Should start in Open phase
	if engine.Phase() != consensus.PhaseOpen {
		t.Errorf("Expected Open phase, got %v", engine.Phase())
	}

	// Wait for close timer
	time.Sleep(50 * time.Millisecond)

	// Should transition to Establish phase
	if engine.Phase() != consensus.PhaseEstablish {
		t.Errorf("Expected Establish phase, got %v", engine.Phase())
	}
}

// testSubscriber implements consensus.EventSubscriber for testing
type testSubscriber struct {
	events chan consensus.Event
}

func (s *testSubscriber) OnEvent(event consensus.Event) {
	select {
	case s.events <- event:
	default:
	}
}

func TestEngine_Subscribe(t *testing.T) {
	adaptor := newMockAdaptor()
	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	subscriber := &testSubscriber{events: make(chan consensus.Event, 10)}
	engine.Subscribe(subscriber)

	// Must call Start to start the EventBus
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}
	defer engine.Stop()

	// Start round to generate event
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Wait for events (multiple events may be fired)
	foundRoundStarted := false
	timeout := time.After(500 * time.Millisecond)
	for !foundRoundStarted {
		select {
		case event := <-subscriber.events:
			if _, ok := event.(*consensus.RoundStartedEvent); ok {
				foundRoundStarted = true
			}
		case <-timeout:
			t.Error("Expected to receive RoundStartedEvent")
			return
		}
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.Timing.LedgerMinClose == 0 {
		t.Error("LedgerMinClose should not be zero")
	}

	if config.Timing.LedgerMaxClose == 0 {
		t.Error("LedgerMaxClose should not be zero")
	}

	if config.Thresholds.MinConsensusPct == 0 {
		t.Error("MinConsensusPct should not be zero")
	}

	if config.Thresholds.MaxConsensusPct == 0 {
		t.Error("MaxConsensusPct should not be zero")
	}
}
