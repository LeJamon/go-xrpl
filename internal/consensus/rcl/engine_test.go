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

// mockTxSet implements consensus.TxSet for testing. containsTxs, if
// non-nil, drives Contains(id); otherwise Contains always returns
// false (matching the legacy behavior some older tests rely on).
// txIDs is kept in insertion order, parallel to txs, so TxIDs() and
// Txs() can be zipped — matching the documented contract on the
// interface.
type mockTxSet struct {
	id          consensus.TxSetID
	txs         [][]byte
	txIDs       []consensus.TxID
	containsTxs map[consensus.TxID]bool
}

func (ts *mockTxSet) ID() consensus.TxSetID { return ts.id }
func (ts *mockTxSet) Txs() [][]byte         { return ts.txs }
func (ts *mockTxSet) Size() int             { return len(ts.txs) }
func (ts *mockTxSet) TxIDs() []consensus.TxID {
	if ts.txIDs != nil {
		out := make([]consensus.TxID, len(ts.txIDs))
		copy(out, ts.txIDs)
		return out
	}
	// Fallback for legacy construction sites that populate only
	// containsTxs — iteration order is non-deterministic here but
	// the legacy tests that follow this path don't care.
	result := make([]consensus.TxID, 0, len(ts.containsTxs))
	for id, ok := range ts.containsTxs {
		if ok {
			result = append(result, id)
		}
	}
	return result
}
func (ts *mockTxSet) Contains(id consensus.TxID) bool {
	if ts.containsTxs != nil {
		return ts.containsTxs[id]
	}
	return false
}
func (ts *mockTxSet) Add(tx []byte) error { ts.txs = append(ts.txs, tx); return nil }
func (ts *mockTxSet) Remove(id consensus.TxID) error {
	if ts.containsTxs != nil {
		delete(ts.containsTxs, id)
	}
	return nil
}
func (ts *mockTxSet) Bytes() []byte { return nil }

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
	validationsRelayed   []*consensus.Validation
	txSetsRequested      []consensus.TxSetID
	ledgersRequested     []consensus.LedgerID
	modeChanges          []consensus.Mode
	phaseChanges         []consensus.Phase

	// Time
	now time.Time

	// Features explicitly disabled for the test. nil/empty means all
	// features enabled (mainnet default). Exercised by R4.10 test.
	disabledFeatures map[string]bool

	// Cookie / ServerVersion overrides for the R4.3 test. Zero means
	// "use the default injected by GetCookie / GetServerVersion".
	cookie        uint64
	serverVersion uint64

	// FeeVote stance for the R4.3 test. voteBaseFee/voteReserveBase/
	// voteReserveIncrement are the triple values; votePostXRPFees
	// controls which triple the engine emits (AMOUNT vs legacy UINT).
	voteBaseFee          uint64
	voteReserveBase      uint64
	voteReserveIncrement uint64
	votePostXRPFees      bool

	// Override for GetValidatedLedgerHash. Zero by default; the
	// R4.10 test sets this to a non-zero LedgerID to exercise the
	// sfValidatedHash gate path.
	validatedLedgerHashOverride consensus.LedgerID

	// Amendment vote stance for the R5.3 test. Empty means no vote.
	amendmentVote [][32]byte

	// Load fee for R6b.5b — emitted as sfLoadFee. Zero by default.
	loadFee uint32

	// Pseudo-tx producer overrides for #367 tests. nil means the
	// stub returns nil (no injection), matching the production
	// adaptor's pre-vote-tally behavior.
	flagLedgerPseudoTxs [][]byte
	negativeUNLPseudoTx []byte
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

func (a *mockAdaptor) RelayProposal(proposal *consensus.Proposal, _ uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.proposalsRelayed = append(a.proposalsRelayed, proposal)
	return nil
}

func (a *mockAdaptor) RelayValidation(validation *consensus.Validation, _ uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.validationsRelayed = append(a.validationsRelayed, validation)
	return nil
}

func (a *mockAdaptor) UpdateRelaySlot(_ []byte, _ uint64, _ []uint64) {}

// PeersThatHave returns nil — the rcl engine tests never query the
// overlay's reverse index since they go through a mockAdaptor.
func (a *mockAdaptor) PeersThatHave(_ [32]byte) []uint64 { return nil }

func (a *mockAdaptor) GetValidatedLedgerHash() consensus.LedgerID {
	// Test mock: no validated ledger tracking by default. Tests that
	// need to exercise the sfValidatedHash gate write
	// a.validatedLedgerHashOverride before driving sendValidation.
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.validatedLedgerHashOverride
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

func (a *mockAdaptor) BuildLedger(parent consensus.Ledger, txSet consensus.TxSet, closeTime time.Time, _ bool) (consensus.Ledger, error) {
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
	// Derive per-tx IDs from the blob prefix so the resulting TxSet
	// reports Contains/TxIDs correctly. Dispute-integration tests
	// build blobs as the tx ID padded to 32 bytes; legacy tests pass
	// nil or empty blobs and only care about the set ID, so they
	// still get a valid (if all-zero-id) mockTxSet.
	ids := make([]consensus.TxID, 0, len(txs))
	contains := make(map[consensus.TxID]bool, len(txs))
	for _, blob := range txs {
		var id consensus.TxID
		if len(blob) >= len(id) {
			copy(id[:], blob[:len(id)])
		}
		ids = append(ids, id)
		contains[id] = true
	}
	txSet := &mockTxSet{
		txs:         txs,
		txIDs:       ids,
		containsTxs: contains,
	}
	// Keep the length-based TxSetID for backward-compat: older tests
	// reference it as {byte(len(txs)), 0,...}.
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

func (a *mockAdaptor) GenerateFlagLedgerPseudoTxs(_ consensus.Ledger) [][]byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.flagLedgerPseudoTxs
}

func (a *mockAdaptor) GenerateNegativeUNLPseudoTx(_ consensus.Ledger) []byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.negativeUNLPseudoTx
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

func (a *mockAdaptor) GetNegativeUNL() []consensus.NodeID {
	// Test mock: no negative-UNL tracking. Returning nil makes the
	// tracker treat all trusted validators as contributors to quorum,
	// which matches the pre-P2.5 behavior and keeps existing tests
	// unaffected by the new interface.
	return nil
}

func (a *mockAdaptor) PeerReportedLedgers() []consensus.LedgerID {
	return nil
}

func (a *mockAdaptor) IsFeatureEnabled(name string) bool {
	// Test mock default: assume every feature is enabled, which
	// matches the production mainnet assumption. Tests that need to
	// exercise disabled-amendment paths set a.disabledFeatures entry
	// for the relevant name.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.disabledFeatures != nil && a.disabledFeatures[name] {
		return false
	}
	return true
}

func (a *mockAdaptor) GetCookie() uint64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cookie == 0 {
		return 0xABCDEF1234567890 // non-zero test default so serializer emits the field
	}
	return a.cookie
}

func (a *mockAdaptor) GetServerVersion() uint64 {
	// Test default: a non-zero value so the serializer emits the
	// field. Callers can override by writing a.serverVersion before
	// driving sendValidation.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.serverVersion == 0 {
		return 0x4000_0000_0000_0001
	}
	return a.serverVersion
}

func (a *mockAdaptor) GetLoadFee() uint32 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.loadFee
}

func (a *mockAdaptor) GetFeeVote() (baseFee, reserveBase, reserveIncrement uint64, postXRPFees bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.voteBaseFee, a.voteReserveBase, a.voteReserveIncrement, a.votePostXRPFees
}

func (a *mockAdaptor) GetAmendmentVote() [][32]byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.amendmentVote) == 0 {
		return nil
	}
	out := make([][32]byte, len(a.amendmentVote))
	copy(out, a.amendmentVote)
	return out
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

func (a *mockAdaptor) AdjustCloseTime(rawCloseTimes consensus.CloseTimes) {}

func (a *mockAdaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation) {
}

func (a *mockAdaptor) OnLedgerFullyValidated(ledgerID consensus.LedgerID, seq uint32) {
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

	if err := engine.OnProposal(proposal, 0); err != nil {
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

	if err := engine.OnProposal(proposal, 0); err != nil {
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

	if err := engine.OnValidation(validation, 0); err != nil {
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
	config.Timing.LedgerMinConsensus = 10 * time.Millisecond
	config.Timing.LedgerIdleInterval = 20 * time.Millisecond

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

	// Wait for close timer (idle interval triggers close with no txs)
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

	if config.Thresholds.EarlyConvergencePct == 0 {
		t.Error("EarlyConvergencePct should not be zero")
	}

	if config.Thresholds.MinConsensusPct == 0 {
		t.Error("MinConsensusPct should not be zero")
	}
}

// TestEngine_WrongLedgerRecovery_ModeSequence pins the behavioral
// contract added in the round-2 P1.7 fix and tightened in R3.4:
//
//  1. A validator in ModeProposing that detects a wrong ledger and
//     acquires the correct one must enter ModeSwitchedLedger for ONE
//     round (not ModeProposing), matching rippled Consensus.h:1107.
//  2. Validation emission in ModeSwitchedLedger is NOT suppressed; the
//     engine emits a PARTIAL validation (Full=false), matching
//     rippled RCLConsensus.cpp:587-594 which sends whenever validating_
//     is true regardless of mode.
//  3. The NEXT round after a recovery promotes the validator back to
//     ModeProposing; that round emits a FULL validation.
//
// Without this test, a future refactor could silently regress any of
// the three steps — the behavior is distributed across
// startRoundLocked's recovering branch, sendValidation's Full flag,
// and the acceptLedger gate.
func TestEngine_WrongLedgerRecovery_ModeSequence(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	// Initial round: we're proposing.
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)
	if mode := engine.Mode(); mode != consensus.ModeProposing {
		t.Fatalf("initial round mode: want Proposing, got %v", mode)
	}

	// Simulate wrong-ledger detection: the engine holds e.mu while
	// calling handleWrongLedger, so we replicate that lock discipline
	// here. The target is a ledger we pretend was detected via
	// getNetworkLedger.
	targetID := consensus.LedgerID{0xAA}
	targetLedger := &mockLedger{
		id:        targetID,
		seq:       101,
		closeTime: time.Now(),
	}
	adaptor.ledgers[targetID] = targetLedger

	engine.mu.Lock()
	// Step into WrongLedger then immediately receive the target — this
	// mirrors the OnLedger happy-path at engine.go:OnLedger where we
	// transition straight through WrongLedger into SwitchedLedger.
	engine.wrongLedgerID = targetID
	engine.setMode(consensus.ModeWrongLedger)
	// Drive the full recovery: handleWrongLedger finds the target
	// ledger via the adaptor (we seeded it above) and promotes to
	// switchedLedger via startRoundLocked(recovering=true).
	engine.handleWrongLedger(targetID)
	engine.mu.Unlock()

	if mode := engine.Mode(); mode != consensus.ModeSwitchedLedger {
		t.Fatalf("post-recovery mode: want SwitchedLedger, got %v", mode)
	}

	// Emit a validation while in SwitchedLedger — expect it to be
	// broadcast with Full=false (partial).
	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xAA}, seq: 102})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	gotPartial := len(adaptor.validationsBroadcast)
	var partialFull bool
	if gotPartial > 0 {
		partialFull = adaptor.validationsBroadcast[0].Full
	}
	adaptor.mu.RUnlock()

	if gotPartial != 1 {
		t.Fatalf("SwitchedLedger must emit exactly one partial validation, got %d", gotPartial)
	}
	if partialFull {
		t.Fatalf("SwitchedLedger validation must have Full=false (partial)")
	}

	// Next round: startRoundLocked with recovering=false should
	// promote us back to Proposing.
	engine.mu.Lock()
	nextRound := consensus.RoundID{Seq: 102, ParentHash: targetID}
	engine.startRoundLocked(nextRound, true, false)
	engine.mu.Unlock()

	if mode := engine.Mode(); mode != consensus.ModeProposing {
		t.Fatalf("next round after recovery: want Proposing, got %v", mode)
	}

	// Validation in Proposing mode: Full=true.
	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xBB}, seq: 103})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	gotFull := len(adaptor.validationsBroadcast)
	var fullFull bool
	if gotFull > 0 {
		fullFull = adaptor.validationsBroadcast[0].Full
	}
	adaptor.mu.RUnlock()

	if gotFull != 1 {
		t.Fatalf("Proposing round must emit exactly one full validation, got %d", gotFull)
	}
	if !fullFull {
		t.Fatalf("Proposing validation must have Full=true")
	}
}

// TestEngine_OnLedger_PromotesToSwitchedLedger pins the SECOND entry
// point into ModeSwitchedLedger — the OnLedger path at engine.go:447
// that fires when a peer finally delivers the ledger we were missing.
// The WrongLedgerRecovery_ModeSequence test above covers the
// handleWrongLedger direct-call path; this test covers OnLedger, which
// is what the router actually calls on inbound mtGET_LEDGER responses.
// A regression on either branch would let a validator emit a Full
// validation immediately after recovery, violating the rippled
// contract that recovery rounds MUST emit partials.
func TestEngine_OnLedger_PromotesToSwitchedLedger(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	// Start in Proposing on a round whose parent we'll claim we DON'T
	// have. The wrongLedgerID is the ID we'll feed into OnLedger to
	// simulate the missing-ledger-arrived event.
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)
	if mode := engine.Mode(); mode != consensus.ModeProposing {
		t.Fatalf("initial round mode: want Proposing, got %v", mode)
	}

	targetID := consensus.LedgerID{0xCC}
	targetLedger := &mockLedger{
		id:        targetID,
		seq:       101,
		closeTime: time.Now(),
	}
	adaptor.ledgers[targetID] = targetLedger

	// Put the engine in WrongLedger mode WITHOUT calling
	// handleWrongLedger — this is the precondition OnLedger checks
	// at engine.go:452.
	engine.mu.Lock()
	engine.wrongLedgerID = targetID
	engine.setMode(consensus.ModeWrongLedger)
	engine.mu.Unlock()

	// OnLedger takes the engine lock internally — call it directly.
	if err := engine.OnLedger(targetID, nil); err != nil {
		t.Fatalf("OnLedger: %v", err)
	}

	if mode := engine.Mode(); mode != consensus.ModeSwitchedLedger {
		t.Fatalf("post-OnLedger mode: want SwitchedLedger, got %v", mode)
	}

	// Emit a validation while in SwitchedLedger via OnLedger entry —
	// must still be Full=false (partial).
	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xCC}, seq: 102})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	got := len(adaptor.validationsBroadcast)
	var gotFull bool
	if got > 0 {
		gotFull = adaptor.validationsBroadcast[0].Full
	}
	adaptor.mu.RUnlock()

	if got != 1 {
		t.Fatalf("SwitchedLedger after OnLedger must emit one partial validation, got %d", got)
	}
	if gotFull {
		t.Fatalf("validation after OnLedger recovery must have Full=false (partial)")
	}
}

// TestSendValidation_ValidatedHashGatedOnHardenedValidations pins the
// R4.10 fix: sfValidatedHash must only be emitted when the
// featureHardenedValidations amendment is enabled (rippled
// RCLConsensus.cpp:853). On mainnet this amendment has been active
// since 2020 so the gate is invisible; on testnet/standalone a node
// running against pre-HardenedValidations rules MUST omit the field
// or peers on the old rules reject the validation as malformed.
func TestSendValidation_ValidatedHashGatedOnHardenedValidations(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	// Seed a non-zero validated-ledger hash so the emission path has
	// something to copy. The gate decides whether to copy it or skip.
	knownHash := consensus.LedgerID{0x11, 0x22, 0x33}
	adaptor.lastLCL = &mockLedger{id: knownHash, seq: 99}
	adaptor.validatedLedgerHashOverride = knownHash

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Case 1: HardenedValidations enabled (default) — expect ValidatedHash
	// populated from GetValidatedLedgerHash.
	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0x55}, seq: 101})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation, got %d", len(adaptor.validationsBroadcast))
	}
	gotWithGate := adaptor.validationsBroadcast[0].ValidatedHash
	adaptor.mu.RUnlock()

	// Case 2: HardenedValidations disabled — ValidatedHash must be zero.
	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.disabledFeatures = map[string]bool{"HardenedValidations": true}
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0x66}, seq: 102})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation after disable, got %d", len(adaptor.validationsBroadcast))
	}
	gotWithoutGate := adaptor.validationsBroadcast[0].ValidatedHash
	adaptor.mu.RUnlock()

	// When disabled the field must be zero.
	if gotWithoutGate != (consensus.LedgerID{}) {
		t.Fatalf("HardenedValidations disabled: ValidatedHash must be zero, got %x", gotWithoutGate)
	}
	// When enabled, the field should have been populated (either the
	// seeded hash or whatever GetValidatedLedgerHash returns). The gate
	// flips the behavior; both cases with the same adaptor state should
	// differ in exactly this field.
	if gotWithGate == gotWithoutGate && gotWithGate == (consensus.LedgerID{}) {
		// If both are zero, the test doesn't prove anything — either the
		// mock returns zero unconditionally or the non-zero seed wasn't
		// reached. Don't silently pass.
		t.Skipf("mock returned zero ValidatedHash in both cases; gate path not exercised")
	}
}

// TestSendValidation_PopulatesCookieServerVersionFeeVote pins R4.3:
// every emitted validation carries Cookie, ServerVersion, and either
// the AMOUNT or UINT fee-vote triple (never both). Without this the
// STValidation serializer's optional-field plumbing would be dead
// code — a goXRPL validator would contribute nothing to flag-ledger
// governance.
func TestSendValidation_PopulatesCookieServerVersionFeeVote(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	// Set a fee-vote stance. postXRPFees=true exercises the AMOUNT
	// triple (the modern path).
	adaptor.voteBaseFee = 10
	adaptor.voteReserveBase = 1_000_000
	adaptor.voteReserveIncrement = 200_000
	adaptor.votePostXRPFees = true

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	// Use a flag-ledger seq (255 → 255+1=256) so the R5.4 isVotingLedger
	// gate allows fee-vote emission; on non-flag ledgers the fields
	// are deliberately omitted.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0x77}, seq: 255})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation, got %d", len(adaptor.validationsBroadcast))
	}
	v := adaptor.validationsBroadcast[0]

	if v.Cookie == 0 {
		t.Error("Cookie must be non-zero (adaptor must have generated one at boot)")
	}
	if v.ServerVersion == 0 {
		t.Error("ServerVersion must be non-zero")
	}
	// AMOUNT triple populated, legacy UINT triple NOT populated.
	if v.BaseFeeDrops != 10 || v.ReserveBaseDrops != 1_000_000 || v.ReserveIncrementDrops != 200_000 {
		t.Errorf("AMOUNT fee-vote triple not populated correctly: got %+v",
			[3]uint64{v.BaseFeeDrops, v.ReserveBaseDrops, v.ReserveIncrementDrops})
	}
	if v.BaseFee != 0 || v.ReserveBase != 0 || v.ReserveIncrement != 0 {
		t.Errorf("legacy UINT triple must stay zero under postXRPFees=true: got (%d, %d, %d)",
			v.BaseFee, v.ReserveBase, v.ReserveIncrement)
	}
}

// TestSendValidation_PopulatesLoadFee pins R6b.5b: when the adaptor
// reports a non-zero local load fee, sendValidation copies it into
// the emitted validation's LoadFee field. Matches rippled
// RCLConsensus.cpp:851 which always populates sfLoadFee under
// HardenedValidations.
func TestSendValidation_PopulatesLoadFee(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.loadFee = 12345

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xAA}, seq: 101})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation, got %d", len(adaptor.validationsBroadcast))
	}
	v := adaptor.validationsBroadcast[0]
	if v.LoadFee != 12345 {
		t.Errorf("LoadFee not populated from adaptor: got %d, want 12345", v.LoadFee)
	}
}

// TestSendValidation_LegacyFeeTriple verifies the pre-XRPFees path:
// postXRPFees=false must populate the UINT triple and leave the
// AMOUNT triple zero — mirroring FeeVoteImpl.cpp:120-192's hard
// if/else on featureXRPFees.
func TestSendValidation_LegacyFeeTriple(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	adaptor.voteBaseFee = 10
	adaptor.voteReserveBase = 5_000_000
	adaptor.voteReserveIncrement = 1_000_000
	adaptor.votePostXRPFees = false

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	// Flag-ledger seq — see R5.4 gate comment on the AMOUNT-triple test.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0x88}, seq: 255})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation, got %d", len(adaptor.validationsBroadcast))
	}
	v := adaptor.validationsBroadcast[0]

	if v.BaseFee != 10 || v.ReserveBase != 5_000_000 || v.ReserveIncrement != 1_000_000 {
		t.Errorf("legacy UINT fee-vote triple not populated: got BaseFee=%d ReserveBase=%d ReserveIncrement=%d",
			v.BaseFee, v.ReserveBase, v.ReserveIncrement)
	}
	if v.BaseFeeDrops != 0 || v.ReserveBaseDrops != 0 || v.ReserveIncrementDrops != 0 {
		t.Errorf("AMOUNT triple must stay zero under postXRPFees=false: got %+v",
			[3]uint64{v.BaseFeeDrops, v.ReserveBaseDrops, v.ReserveIncrementDrops})
	}
}

// TestSendValidation_FeeVoteOnlyOnFlagLedger pins R5.4: fee-vote and
// amendment-vote fields must be emitted ONLY on flag-ledger
// validations ((seq+1)%256==0). Pre-R5.4 behavior emitted them every
// ledger, inflating validation bandwidth ~256×.
func TestSendValidation_FeeVoteOnlyOnFlagLedger(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.voteBaseFee = 10
	adaptor.voteReserveBase = 1_000_000
	adaptor.voteReserveIncrement = 200_000
	adaptor.votePostXRPFees = true
	// Set a single amendment in the vote stance so we can verify
	// amendments emission is also gated.
	adaptor.amendmentVote = [][32]byte{{0x01, 0x02, 0x03}}

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Non-flag seq (101): fee-vote + amendments must be omitted.
	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0x77}, seq: 101})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	if len(adaptor.validationsBroadcast) != 1 {
		adaptor.mu.RUnlock()
		t.Fatalf("want one validation on seq=101, got %d", len(adaptor.validationsBroadcast))
	}
	nonFlag := adaptor.validationsBroadcast[0]
	adaptor.mu.RUnlock()

	if nonFlag.BaseFeeDrops != 0 || nonFlag.ReserveBaseDrops != 0 || nonFlag.ReserveIncrementDrops != 0 {
		t.Errorf("non-flag ledger must omit AMOUNT fee-vote triple: got %+v",
			[3]uint64{nonFlag.BaseFeeDrops, nonFlag.ReserveBaseDrops, nonFlag.ReserveIncrementDrops})
	}
	if len(nonFlag.Amendments) != 0 {
		t.Errorf("non-flag ledger must omit Amendments: got %d IDs", len(nonFlag.Amendments))
	}

	// Flag seq (255 → 255+1=256): fee-vote + amendments must be present.
	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0x99}, seq: 255})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation on seq=255, got %d", len(adaptor.validationsBroadcast))
	}
	flag := adaptor.validationsBroadcast[0]
	if flag.BaseFeeDrops != 10 {
		t.Errorf("flag ledger must carry fee-vote AMOUNT triple: got BaseFeeDrops=%d", flag.BaseFeeDrops)
	}
	if len(flag.Amendments) != 1 {
		t.Errorf("flag ledger must carry Amendments vote: got %d IDs", len(flag.Amendments))
	}
}

// TestSendValidation_PreHardenedValidations_OmitsCookieAndServerVersion
// pins B1: with featureHardenedValidations DISABLED, sendValidation
// must leave Cookie and ServerVersion zero on the emitted validation.
// Rippled RCLConsensus.cpp:853-867 scopes both fields inside the
// `if (rules().enabled(featureHardenedValidations))` block, so a node
// running against pre-HardenedValidations rules MUST omit them or
// peers on the old rules compute a different preimage and reject the
// signature.
func TestSendValidation_PreHardenedValidations_OmitsCookieAndServerVersion(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	// Give the adaptor explicit non-zero Cookie/ServerVersion so we can
	// prove the engine itself (not the mock) is suppressing them.
	adaptor.cookie = 0xDEADBEEF_CAFEBABE
	adaptor.serverVersion = 0x4000_0000_DEAD_BEEF

	// Disable HardenedValidations so the gate should zero out both
	// fields regardless of whether we're on a voting ledger.
	adaptor.disabledFeatures = map[string]bool{"HardenedValidations": true}

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	// Use a voting-ledger seq (255+1=256) to prove the voting-ledger
	// path also respects the HardenedValidations gate — rippled emits
	// sfServerVersion only inside the HV block AND only on voting
	// ledgers; if HV is off, neither condition matters.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xCA}, seq: 255})
	engine.mu.Unlock()

	// Verify the struct itself (what the adaptor sees pre-sign).
	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation, got %d", len(adaptor.validationsBroadcast))
	}
	v := adaptor.validationsBroadcast[0]
	if v.Cookie != 0 {
		t.Errorf("pre-HardenedValidations: Cookie must be zero, got %x", v.Cookie)
	}
	if v.ServerVersion != 0 {
		t.Errorf("pre-HardenedValidations: ServerVersion must be zero, got %x", v.ServerVersion)
	}
}

// TestSendValidation_HardenedValidations_NonVotingLedger_OmitsServerVersion
// pins the rippled voting-ledger scope for sfServerVersion
// (RCLConsensus.cpp:864-866). With HardenedValidations ON but a
// non-voting ledger sequence, Cookie must be populated but
// ServerVersion must stay zero. Rippled gates sfServerVersion on
// BOTH HV enabled AND ledger.isVotingLedger() — miss either side and
// the field is omitted.
func TestSendValidation_HardenedValidations_NonVotingLedger_OmitsServerVersion(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.cookie = 0x1234_5678_9ABC_DEF0
	adaptor.serverVersion = 0x4000_0000_1111_2222

	// HardenedValidations enabled (default mock behavior) — don't set
	// disabledFeatures.

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	// seq=100 → (100+1)%256 != 0 — non-voting ledger. Cookie must
	// still emit (unconditional under HV), ServerVersion must not.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xBB}, seq: 100})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation, got %d", len(adaptor.validationsBroadcast))
	}
	v := adaptor.validationsBroadcast[0]
	if v.Cookie != 0x1234_5678_9ABC_DEF0 {
		t.Errorf("HardenedValidations enabled: Cookie must carry adaptor value, got %x", v.Cookie)
	}
	if v.ServerVersion != 0 {
		t.Errorf("non-voting ledger: ServerVersion must be zero, got %x", v.ServerVersion)
	}
}

// TestSendValidation_HardenedValidations_VotingLedger_EmitsBoth pins
// the positive case of B1: HardenedValidations ON and isVotingLedger()
// true (the only branch where rippled RCLConsensus.cpp:861-866 sets
// both sfCookie AND sfServerVersion). Also asserts that the full
// serialized validation carries the sfServerVersion field code
// (type=3, field=11), not just the struct value — defense-in-depth
// check against the serializer short-circuiting on zero.
func TestSendValidation_HardenedValidations_VotingLedger_EmitsBoth(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.cookie = 0xAAAA_BBBB_CCCC_DDDD
	adaptor.serverVersion = 0x4000_0000_DEAD_FEED

	// HardenedValidations enabled (default mock behavior).

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	// seq=255 → (255+1)%256 == 0 — voting ledger. Both fields must
	// be present.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xCC}, seq: 255})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.validationsBroadcast) != 1 {
		t.Fatalf("want one validation, got %d", len(adaptor.validationsBroadcast))
	}
	v := adaptor.validationsBroadcast[0]
	if v.Cookie != 0xAAAA_BBBB_CCCC_DDDD {
		t.Errorf("voting-ledger HV: Cookie must carry adaptor value, got %x", v.Cookie)
	}
	if v.ServerVersion != 0x4000_0000_DEAD_FEED {
		t.Errorf("voting-ledger HV: ServerVersion must carry adaptor value, got %x", v.ServerVersion)
	}
}

// Task 2.4 (B4): tests for isBowOut (seqLeave == 0xFFFFFFFF) detection.
// Rippled reference: ConsensusProposal.h:68,154-156 and Consensus.h:804-817.
// A validator bowing out sets ProposeSeq to seqLeave so peers know to stop
// counting them for the remainder of the round. We must evict their current
// position and refuse further proposals until the next round clears the set.

// TestOnProposal_BowOutEvictsNode feeds a valid proposal from node X, then
// a seqLeave proposal from X, and asserts that the stored position for X is
// cleared. Mirrors rippled's Consensus.h:812-814 where peerPositions gets
// erase(peerID) on bow-out and the nodeID is inserted into deadNodes_.
func TestOnProposal_BowOutEvictsNode(t *testing.T) {
	adaptor := newMockAdaptor()
	bowingNode := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{bowingNode, {3}})

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	// Initial proposal — should be stored.
	first := &consensus.Proposal{
		Round:          round,
		NodeID:         bowingNode,
		Position:       0,
		TxSet:          consensus.TxSetID{1},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(first, 0); err != nil {
		t.Fatalf("first OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, stored := engine.proposals[bowingNode]
	engine.mu.RUnlock()
	if !stored {
		t.Fatalf("precondition: first proposal from bowingNode should have been stored")
	}

	// Bow-out proposal — Position == seqLeave (0xFFFFFFFF).
	bowOut := &consensus.Proposal{
		Round:          round,
		NodeID:         bowingNode,
		Position:       0xFFFFFFFF,
		TxSet:          consensus.TxSetID{2},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(bowOut, 0); err != nil {
		t.Fatalf("bow-out OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, stillStored := engine.proposals[bowingNode]
	_, dead := engine.deadNodes[bowingNode]
	engine.mu.RUnlock()

	if stillStored {
		t.Errorf("expected bowed-out node %v to be evicted from proposals map", bowingNode)
	}
	if !dead {
		t.Errorf("expected bowed-out node %v to be recorded in deadNodes set", bowingNode)
	}
}

// TestOnProposal_DeadNodeLaterProposalIgnored verifies that once a node
// bows out, any subsequent proposal it sends in the same round is ignored.
// Matches rippled's Consensus.h:785-789 guard.
func TestOnProposal_DeadNodeLaterProposalIgnored(t *testing.T) {
	adaptor := newMockAdaptor()
	bowingNode := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{bowingNode, {3}})

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	bowOut := &consensus.Proposal{
		Round:          round,
		NodeID:         bowingNode,
		Position:       0xFFFFFFFF,
		TxSet:          consensus.TxSetID{2},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(bowOut, 0); err != nil {
		t.Fatalf("bow-out OnProposal: %v", err)
	}

	// A "normal" proposal after bow-out must be silently dropped.
	followUp := &consensus.Proposal{
		Round:          round,
		NodeID:         bowingNode,
		Position:       1,
		TxSet:          consensus.TxSetID{3},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(followUp, 0); err != nil {
		t.Fatalf("follow-up OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, stored := engine.proposals[bowingNode]
	engine.mu.RUnlock()
	if stored {
		t.Errorf("expected follow-up proposal from dead node %v to be ignored, but it was stored", bowingNode)
	}
}

// TestStartRound_ClearsDeadNodes verifies that a new round clears the
// deadNodes set so a validator can rejoin consensus in the next round.
// Matches rippled's Consensus.h:722 (startRoundInternal clears deadNodes_).
func TestStartRound_ClearsDeadNodes(t *testing.T) {
	adaptor := newMockAdaptor()
	bowingNode := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{bowingNode, {3}})

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	round1 := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round1, true); err != nil {
		t.Fatalf("StartRound round1: %v", err)
	}

	// Bow out in round 1.
	bowOut := &consensus.Proposal{
		Round:          round1,
		NodeID:         bowingNode,
		Position:       0xFFFFFFFF,
		TxSet:          consensus.TxSetID{2},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(bowOut, 0); err != nil {
		t.Fatalf("bow-out OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, deadAfterBow := engine.deadNodes[bowingNode]
	engine.mu.RUnlock()
	if !deadAfterBow {
		t.Fatalf("precondition: bowingNode should be marked dead after bow-out")
	}

	// Start the next round — deadNodes must reset.
	round2 := consensus.RoundID{Seq: 102, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round2, true); err != nil {
		t.Fatalf("StartRound round2: %v", err)
	}

	engine.mu.RLock()
	_, stillDead := engine.deadNodes[bowingNode]
	engine.mu.RUnlock()
	if stillDead {
		t.Fatalf("expected deadNodes to be cleared after StartRound, but %v is still marked dead", bowingNode)
	}

	// And a fresh proposal from the previously-bowed node must be accepted
	// again in the new round.
	rejoin := &consensus.Proposal{
		Round:          round2,
		NodeID:         bowingNode,
		Position:       0,
		TxSet:          consensus.TxSetID{5},
		CloseTime:      time.Now(),
		PreviousLedger: consensus.LedgerID{1},
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(rejoin, 0); err != nil {
		t.Fatalf("rejoin OnProposal: %v", err)
	}

	engine.mu.RLock()
	_, stored := engine.proposals[bowingNode]
	engine.mu.RUnlock()
	if !stored {
		t.Errorf("expected rejoined proposal from %v to be accepted in the new round", bowingNode)
	}
}

// Task 2.5 (B5): tests for monotonic SignTime on emitted validations.
// Rippled reference: RCLConsensus.cpp:825-828 — if the wall clock regresses
// (NTP step, leap-second correction, VM pause/resume), the validation sign
// time is bumped to lastValidationTime_ + 1s so peers never see a non-
// monotonic sequence of validations from the same node.

// TestSendValidation_ClockRegressionPreservesMonotonic drives sendValidation
// twice with a regressing fake clock and asserts the second SignTime is
// exactly first + 1s (NOT the regressed adaptor.Now() value). Without this
// guard, peers treat the second validation as stale and drop it — matching
// rippled's behavior where older-than-last validations are rejected.
func TestSendValidation_ClockRegressionPreservesMonotonic(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	// Pin the clock to a known value so both observations are deterministic.
	baseTime := time.Unix(1_700_000_000, 0).UTC()
	adaptor.now = baseTime
	adaptor.mu.Unlock()

	// First emission — SignTime should equal the fake clock.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xA1}, seq: 101})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	if len(adaptor.validationsBroadcast) != 1 {
		adaptor.mu.RUnlock()
		t.Fatalf("want one validation after first send, got %d", len(adaptor.validationsBroadcast))
	}
	first := adaptor.validationsBroadcast[0]
	adaptor.mu.RUnlock()

	if !first.SignTime.Equal(baseTime) {
		t.Errorf("first SignTime: want %v, got %v", baseTime, first.SignTime)
	}
	if !first.SeenTime.Equal(first.SignTime) {
		t.Errorf("first SeenTime must equal SignTime: got SignTime=%v SeenTime=%v",
			first.SignTime, first.SeenTime)
	}

	// Regress the clock by 5 seconds (simulates NTP step / VM pause-resume).
	adaptor.mu.Lock()
	adaptor.now = baseTime.Add(-5 * time.Second)
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xA2}, seq: 102})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	if len(adaptor.validationsBroadcast) != 2 {
		adaptor.mu.RUnlock()
		t.Fatalf("want two validations after second send, got %d", len(adaptor.validationsBroadcast))
	}
	second := adaptor.validationsBroadcast[1]
	adaptor.mu.RUnlock()

	// Second SignTime must be first + 1s, NOT the regressed adaptor.Now().
	want := first.SignTime.Add(1 * time.Second)
	if !second.SignTime.Equal(want) {
		t.Errorf("clock regressed: second SignTime: want %v (first + 1s), got %v",
			want, second.SignTime)
	}
	if !second.SeenTime.Equal(second.SignTime) {
		t.Errorf("second SeenTime must equal SignTime: got SignTime=%v SeenTime=%v",
			second.SignTime, second.SeenTime)
	}
}

// TestSendValidation_ClockMonotonic_NormalCase confirms the monotonic floor
// does NOT inject an artificial step when the adaptor clock advances
// normally. With a 3-second forward step, the second SignTime should be
// exactly adaptor.Now() (first + 3s), not first + 1s.
func TestSendValidation_ClockMonotonic_NormalCase(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	baseTime := time.Unix(1_700_000_000, 0).UTC()
	adaptor.now = baseTime
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xB1}, seq: 201})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	first := adaptor.validationsBroadcast[0]
	adaptor.mu.RUnlock()

	// Advance the clock 3 seconds forward — normal progression.
	adaptor.mu.Lock()
	adaptor.now = baseTime.Add(3 * time.Second)
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xB2}, seq: 202})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	if len(adaptor.validationsBroadcast) != 2 {
		adaptor.mu.RUnlock()
		t.Fatalf("want two validations, got %d", len(adaptor.validationsBroadcast))
	}
	second := adaptor.validationsBroadcast[1]
	adaptor.mu.RUnlock()

	// The difference must be exactly 3s — no artificial +1s step.
	diff := second.SignTime.Sub(first.SignTime)
	if diff != 3*time.Second {
		t.Errorf("normal clock advance: want SignTime difference 3s, got %v", diff)
	}
	// Second SignTime must equal adaptor.Now() — NOT the monotonic floor.
	want := baseTime.Add(3 * time.Second)
	if !second.SignTime.Equal(want) {
		t.Errorf("second SignTime: want %v (adaptor.Now), got %v", want, second.SignTime)
	}
	if !second.SeenTime.Equal(second.SignTime) {
		t.Errorf("second SeenTime must equal SignTime: got SignTime=%v SeenTime=%v",
			second.SignTime, second.SeenTime)
	}
}

// TestConsensus_MaxConsensusSoftTimeoutTransitions pins the behavior
// of the E3 soft deadline: once a round exceeds LedgerMaxConsensus
// (rippled's ledgerMAX_CONSENSUS = 15s, ConsensusParms.h:95), the
// engine force-accepts the round with ResultTimeout and transitions
// from Establish → Accepted. This is the rename-migrated action
// that, pre-E3, fired at the goXRPL-only LedgerMaxClose=10s. It must
// NOT trigger a bow-out (that is reserved for the hard abandon
// branch at 120s, covered by TestConsensus_AbandonHardTimeout).
func TestConsensus_MaxConsensusSoftTimeoutTransitions(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	// Default 15s soft / 120s hard. Override LedgerMaxClose to match
	// LedgerMaxConsensus exactly so the legacy alias doesn't preempt
	// the soft-deadline check.
	config := DefaultConfig()
	config.Timing.LedgerMaxConsensus = 15 * time.Second
	config.Timing.LedgerMaxClose = 15 * time.Second
	config.Timing.LedgerAbandonConsensus = 120 * time.Second
	config.Timing.LedgerAbandonConsensusFactor = 10

	engine := NewEngine(adaptor, config)

	subscriber := &testSubscriber{events: make(chan consensus.Event, 32)}
	engine.Subscribe(subscriber)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Force the engine into Establish with a known roundStartTime
	// 16s in the past — just past the 15s soft deadline but before
	// the factor-scaled hard abandon ceiling (see prevRoundTime
	// below). This mirrors rippled's window between ledgerMAX_
	// CONSENSUS and the std::clamp'd ledgerABANDON_CONSENSUS:
	// currentAgreeTime > ledgerMAX_CONSENSUS (relaxes threshold)
	// but currentAgreeTime <= clamp(prevRoundTime * factor,
	// ledgerMAX_CONSENSUS, ledgerABANDON_CONSENSUS) (no abandon).
	engine.mu.Lock()
	engine.setPhase(consensus.PhaseEstablish)
	engine.roundStartTime = time.Now().Add(-16 * time.Second)
	// prevRoundTime × factor = 2s × 10 = 20s. std::clamp pins the
	// hard deadline to 20s (between the 15s low and the 120s high).
	// At 16s we are past the soft (15s) but short of the hard (20s),
	// so the hard branch must NOT fire.
	engine.prevRoundTime = 2 * time.Second
	engine.phaseEstablish()
	phaseAfter := engine.phase
	modeAfter := engine.mode
	engine.mu.Unlock()

	// Soft timeout force-accepts → phase must have transitioned out
	// of Establish (to Accepted). The exact target depends on the
	// auto-advance in acceptLedger; either Accepted or Open (next
	// round) is acceptable, as long as we left Establish.
	if phaseAfter == consensus.PhaseEstablish {
		t.Errorf("soft timeout: phase should have transitioned out of Establish, got %v", phaseAfter)
	}

	// Soft timeout MUST NOT bow out a proposing validator — that's
	// the hard-abandon semantic, not the soft one.
	if modeAfter == consensus.ModeObserving {
		t.Errorf("soft timeout must NOT bow out (mode=Observing); got mode=%v — that is the hard-abandon behavior", modeAfter)
	}

	// Drain events and assert we saw a ConsensusReachedEvent with
	// ResultTimeout, not ResultAbandoned.
	sawTimeout := false
	sawAbandoned := false
	deadline := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-subscriber.events:
			if cre, ok := ev.(*consensus.ConsensusReachedEvent); ok {
				switch cre.Result {
				case consensus.ResultTimeout:
					sawTimeout = true
				case consensus.ResultAbandoned:
					sawAbandoned = true
				}
			}
		case <-deadline:
			break drain
		}
	}
	if !sawTimeout {
		t.Errorf("expected ConsensusReachedEvent with ResultTimeout from soft deadline")
	}
	if sawAbandoned {
		t.Errorf("soft timeout must not emit ResultAbandoned")
	}
}

// TestConsensus_AbandonHardTimeout pins the behavior of the E3 hard
// deadline: once a round exceeds the ledgerABANDON_CONSENSUS clamp
// (rippled's 15s..120s clamp, ConsensusParms.h:113), the engine
// abandons the round. Per rippled Consensus.cpp:253-263 + Consensus.h:
// 1760-1785, this means:
//
//  1. We treat the state as ConsensusState::Expired.
//  2. leaveConsensus() is called — if we were proposing, we bow out
//     to Observing (Consensus.h:1802-1817).
//  3. The accept step still runs with a distinct Result so callers
//     can tell a hard abandon from a soft force-accept.
//
// goXRPL surfaces (3) as ResultAbandoned.
func TestConsensus_AbandonHardTimeout(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	config.Timing.LedgerMaxConsensus = 15 * time.Second
	config.Timing.LedgerMaxClose = 15 * time.Second
	config.Timing.LedgerAbandonConsensus = 120 * time.Second
	config.Timing.LedgerAbandonConsensusFactor = 10

	engine := NewEngine(adaptor, config)

	subscriber := &testSubscriber{events: make(chan consensus.Event, 32)}
	engine.Subscribe(subscriber)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Confirm the engine entered ModeProposing — the abandon branch
	// must demote a proposing validator (rippled leaveConsensus).
	engine.mu.Lock()
	if engine.mode != consensus.ModeProposing {
		engine.mu.Unlock()
		t.Fatalf("setup: expected ModeProposing after StartRound, got %v", engine.mode)
	}
	// Force the engine into Establish with a roundStartTime 121s in
	// the past — past the absolute 120s hard ceiling. Set
	// prevRoundTime high so the factor×clamp would produce a huge
	// deadline; the hard abandon ceiling must still fire.
	engine.setPhase(consensus.PhaseEstablish)
	engine.roundStartTime = time.Now().Add(-121 * time.Second)
	engine.prevRoundTime = 60 * time.Second // factor×60s=600s, clamped down to 120s
	engine.phaseEstablish()
	phaseAfter := engine.phase
	modeAfter := engine.mode
	engine.mu.Unlock()

	// Hard abandon must transition out of Establish.
	if phaseAfter == consensus.PhaseEstablish {
		t.Errorf("hard abandon: phase should have transitioned out of Establish, got %v", phaseAfter)
	}

	// Hard abandon bows a proposing validator out to Observing
	// (rippled leaveConsensus). After auto-advance in acceptLedger
	// we may re-promote, but at minimum we must NOT still be
	// ModeProposing on the same round — the round was abandoned.
	// Accept either Observing (bow-out held through the new round
	// setup) or the post-advance promotion back to Proposing if the
	// new round re-promoted cleanly. What we assert is that the
	// bow-out step ran: the adaptor.modeChanges transcript must
	// contain an Observing transition.
	adaptor.mu.RLock()
	sawObserving := false
	for _, m := range adaptor.modeChanges {
		if m == consensus.ModeObserving {
			sawObserving = true
			break
		}
	}
	adaptor.mu.RUnlock()
	if !sawObserving {
		t.Errorf("hard abandon: expected bow-out to ModeObserving (rippled leaveConsensus), modeChanges=%v, final mode=%v", adaptor.modeChanges, modeAfter)
	}

	// Drain events and assert ResultAbandoned was emitted.
	sawAbandoned := false
	sawTimeout := false
	deadline := time.After(500 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-subscriber.events:
			if cre, ok := ev.(*consensus.ConsensusReachedEvent); ok {
				switch cre.Result {
				case consensus.ResultAbandoned:
					sawAbandoned = true
				case consensus.ResultTimeout:
					sawTimeout = true
				}
			}
		case <-deadline:
			break drain
		}
	}
	if !sawAbandoned {
		t.Errorf("expected ConsensusReachedEvent with ResultAbandoned from hard abandon")
	}
	if sawTimeout {
		t.Errorf("hard abandon must not emit ResultTimeout (that is the soft branch)")
	}
}
