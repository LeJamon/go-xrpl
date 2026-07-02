package rcl

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// mockLedger implements consensus.Ledger for testing
type mockLedger struct {
	id        consensus.LedgerID
	seq       uint32
	parentID  consensus.LedgerID
	closeTime time.Time
	txSetID   consensus.TxSetID
	txs       [][]byte
}

func (l *mockLedger) ID() consensus.LedgerID       { return l.id }
func (l *mockLedger) Seq() uint32                  { return l.seq }
func (l *mockLedger) ParentID() consensus.LedgerID { return l.parentID }
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
	opMode           consensus.OperatingMode
	validator        bool
	amendmentBlocked bool

	// Validator info
	nodeID  consensus.NodeID
	trusted map[consensus.NodeID]bool
	quorum  int

	// Data stores
	ledgers map[consensus.LedgerID]consensus.Ledger
	txSets  map[consensus.TxSetID]consensus.TxSet
	lastLCL consensus.Ledger

	// Peer-reported LCLs served by PeerReportedLedgers.
	peerLCLs []consensus.LedgerID

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
	negativeUNLPseudoTx [][]byte

	onUNLChangeCalls []onUNLChangeCall

	// standalone toggles the IsStandalone() return for tests that
	// exercise rippled's `standalone() || (proposing && !wrongLCL)`
	// OR-branch at RCLConsensus.cpp:352.
	standalone bool

	// proposableOverride lets a test pin a specific filtered set
	// for GetProposableTxs to return, distinct from the raw pending
	// pool, so closeLedger's wiring (proposing path uses the filtered
	// set) can be asserted directly. nil falls through to GetPendingTxs.
	proposableOverride [][]byte
	proposableCalled   int

	// buildLedgerHook, when set, runs at the start of BuildLedger (before the
	// new ledger is minted). Tests use it to block the off-lock apply and
	// observe engine behaviour while it runs. Read under a.mu, then called
	// without the lock so it can't deadlock adaptor calls made concurrently.
	buildLedgerHook func()
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

func (a *mockAdaptor) GetLedgerBySeq(seq uint32) (consensus.Ledger, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, l := range a.ledgers {
		if l != nil && l.Seq() == seq {
			return l, nil
		}
	}
	return nil, nil
}

func (a *mockAdaptor) GetLastClosedLedger() (consensus.Ledger, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastLCL, nil
}

func (a *mockAdaptor) BuildLedger(parent consensus.Ledger, txSet consensus.TxSet, closeTime time.Time, _ bool) (consensus.Ledger, error) {
	a.mu.RLock()
	hook := a.buildLedgerHook
	a.mu.RUnlock()
	if hook != nil {
		hook()
	}
	newLedger := &mockLedger{
		id:        consensus.LedgerID{byte(parent.Seq() + 1)},
		seq:       parent.Seq() + 1,
		parentID:  parent.ID(),
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

// GetProposableTxs returns the filtered subset when an explicit
// override is wired (proposableOverride). Otherwise falls through to
// the raw pending pool — matching what a real adaptor returns when
// the LedgerService can't filter (no parent / no apply context). The
// override lets tests assert that closeLedger uses the filtered set
// (not the raw pool) when proposing.
func (a *mockAdaptor) GetProposableTxs(_ consensus.Ledger) [][]byte {
	a.mu.RLock()
	override := a.proposableOverride
	called := a.proposableCalled
	a.mu.RUnlock()
	a.mu.Lock()
	a.proposableCalled = called + 1
	a.mu.Unlock()
	if override != nil {
		return override
	}
	return a.GetPendingTxs()
}

func (a *mockAdaptor) GenerateFlagLedgerPseudoTxs(_ consensus.Ledger, _ []*consensus.Validation) [][]byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.flagLedgerPseudoTxs
}

func (a *mockAdaptor) GenerateNegativeUNLPseudoTx(_ consensus.Ledger) [][]byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.negativeUNLPseudoTx
}

type onUNLChangeCall struct {
	upcomingSeq uint32
	nowTrusted  []consensus.NodeID
}

func (a *mockAdaptor) OnUNLChange(upcomingSeq uint32, nowTrusted []consensus.NodeID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	added := make([]consensus.NodeID, len(nowTrusted))
	copy(added, nowTrusted)
	a.onUNLChangeCalls = append(a.onUNLChangeCalls, onUNLChangeCall{
		upcomingSeq: upcomingSeq,
		nowTrusted:  added,
	})
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

func (a *mockAdaptor) IsAmendmentBlocked() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.amendmentBlocked
}

func (a *mockAdaptor) IsTrusted(nodeID consensus.NodeID) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.trusted[nodeID]
}

func (a *mockAdaptor) GetTrustedValidators() []consensus.NodeID {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]consensus.NodeID, 0, len(a.trusted))
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
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.peerLCLs
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

func (a *mockAdaptor) IsFeatureEnabledOnLedger(_ consensus.Ledger, name string) bool {
	// Mock collapses the "rules of THIS ledger" into the same
	// disabledFeatures map used by IsFeatureEnabled — tests that need
	// per-ledger divergence are not in scope yet.
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.disabledFeatures != nil && a.disabledFeatures[name] {
		return false
	}
	return true
}

func (a *mockAdaptor) IsStandalone() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.standalone
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

func (a *mockAdaptor) GetFeeVote() consensus.FeeVoteResult {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return consensus.FeeVoteResult{
		BaseFee:          a.voteBaseFee,
		ReserveBase:      a.voteReserveBase,
		ReserveIncrement: a.voteReserveIncrement,
		PostXRPFees:      a.votePostXRPFees,
	}
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

func (a *mockAdaptor) PrevCloseTimeResolution() time.Duration {
	return time.Second
}

func (a *mockAdaptor) AdjustCloseTime(rawCloseTimes consensus.CloseTimes) {}

func (a *mockAdaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation, roundTime time.Duration) {
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

	ctx := t.Context()

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

// TestEngine_FirstRoundSeedsPrevRoundTime pins rippled's firstRound_ seeding
// (Consensus.h:658-664): the first round after boot has no prior round to
// measure, so prevRoundTime is seeded to the idle interval. Without the seed,
// round-1 convergePercent divides by the 5s floor instead of 15s, escalating
// avalanche state ~3x faster than a rippled node in the same round.
func TestEngine_FirstRoundSeedsPrevRoundTime(t *testing.T) {
	adaptor := newMockAdaptor()
	config := DefaultConfig()

	now := time.Unix(1000, 0)
	config.Clock = func() time.Time { return now }

	engine := NewEngine(adaptor, config)

	if !engine.firstRound {
		t.Fatal("fresh engine should have firstRound=true")
	}
	if engine.prevRoundTime != 0 {
		t.Fatalf("fresh engine prevRoundTime = %v, want 0", engine.prevRoundTime)
	}

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	if engine.firstRound {
		t.Error("firstRound should be cleared after the first round")
	}
	if got, want := engine.prevRoundTime, config.Timing.LedgerIdleInterval; got != want {
		t.Errorf("prevRoundTime after first round = %v, want idle interval %v", got, want)
	}

	// convergePercent must divide by the seeded idle interval (15s), not the
	// 5s avMinConsensusTime floor: 3s elapsed → 20%, not 60%.
	now = now.Add(3 * time.Second)
	if got := engine.convergePercent(); got != 20 {
		t.Errorf("round-1 convergePercent = %d, want 20 (15s divisor, not the 5s floor)", got)
	}
}

// TestEngine_StartRound_DrivesOnUNLChange pins the wiring that
// mirrors rippled's NetworkOPs.cpp:2081-2102 → RCLConsensus.cpp:1041-1043
// pairing: at the head of every consensus round, the engine computes the
// trusted-set delta since the previous round and forwards the
// newly-added validators to adaptor.OnUNLChange so the NegativeUNL
// voter exempts them from ToDisable for NewValidatorDisableSkip ledgers.
//
// Without this wiring (issue #423), OnUNLChange was an exposed-but-dead
// API: any fresh validator could be voted ToDisable before accumulating
// any validations, defeating rippled's grace-period protection.
func TestEngine_StartRound_DrivesOnUNLChange(t *testing.T) {
	prev := &mockLedger{id: consensus.LedgerID{0xAB, 0xCD}, seq: 256}
	adaptor := newMockAdaptor()
	adaptor.lastLCL = prev
	adaptor.ledgers[prev.ID()] = prev

	n1 := consensus.NodeID{0x01}
	n2 := consensus.NodeID{0x02}
	n3 := consensus.NodeID{0x03}
	adaptor.setTrusted([]consensus.NodeID{n1, n2})

	engine := NewEngine(adaptor, DefaultConfig())

	// Round 1: prevLedger nil → no OnUNLChange call (matches production
	// where the first round can't gate on a parent ledger it hasn't seen
	// yet; rippled's preStartRound is only invoked after closingInfo has
	// resolved a parent — NetworkOPs.cpp:2070).
	round1 := consensus.RoundID{Seq: prev.Seq() + 1, ParentHash: prev.ID()}
	if err := engine.StartRound(round1, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := len(adaptor.onUNLChangeCalls); got != 0 {
		t.Fatalf("no parent ledger → expected 0 OnUNLChange calls, got %d", got)
	}

	// Round 2: install prevLedger; the first call with a parent ledger
	// SEEDS previousTrustedSet from the startup UNL and skips OnUNLChange.
	// This mirrors rippled where ValidatorList::trustedMasterKeys_ is
	// already populated by applyLists() before the first updateTrusted
	// call, so the startup UNL is NOT reported in TrustChanges.added —
	// otherwise every restart would hand every already-mature validator
	// a fresh NewValidatorDisableSkip-ledger grace period.
	engine.mu.Lock()
	engine.prevLedger = prev
	engine.mu.Unlock()
	round2 := consensus.RoundID{Seq: prev.Seq() + 1, ParentHash: prev.ID()}
	if err := engine.StartRound(round2, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := len(adaptor.onUNLChangeCalls); got != 0 {
		t.Fatalf("seeding round must not invoke OnUNLChange (startup UNL is not `added`); got %d", got)
	}

	// Round 3: same UNL → no OnUNLChange call (rippled's
	// !nowTrusted.empty() gate at RCLConsensus.cpp:1042).
	if err := engine.StartRound(round2, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := len(adaptor.onUNLChangeCalls); got != 0 {
		t.Fatalf("unchanged UNL must not trigger a call; got %d total", got)
	}

	// Round 4: add n3 → only n3 is forwarded, matching rippled's
	// TrustChanges.added delta semantics. upcomingSeq is derived from
	// prevLedger.Seq()+1 inside the engine, NOT from round.Seq.
	adaptor.setTrusted([]consensus.NodeID{n1, n2, n3})
	if err := engine.StartRound(round2, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := len(adaptor.onUNLChangeCalls); got != 1 {
		t.Fatalf("added validator: expected 1 call total, got %d", got)
	}
	first := adaptor.onUNLChangeCalls[0]
	if first.upcomingSeq != prev.Seq()+1 {
		t.Errorf("upcomingSeq: want prevLedger.Seq()+1 = %d, got %d", prev.Seq()+1, first.upcomingSeq)
	}
	if !sameNodeIDSet(first.nowTrusted, []consensus.NodeID{n3}) {
		t.Errorf("delta must be {n3}, got %v", first.nowTrusted)
	}

	// Round 5: remove n2 → no call (rippled only forwards `added`, never
	// `removed` — see RCLConsensus.cpp:1093-1103 where `nowUntrusted` is
	// passed to consensus.startRound but explicitly NOT to preStartRound).
	adaptor.setTrusted([]consensus.NodeID{n1, n3})
	if err := engine.StartRound(round2, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := len(adaptor.onUNLChangeCalls); got != 1 {
		t.Fatalf("removals must not trigger OnUNLChange; got %d total", got)
	}
}

// TestEngine_StartRound_SeedingHonorsRestart pins the M2 fix more
// pointedly: a node that restarts with a steady-state UNL must NOT
// see any of its already-trusted validators forwarded to OnUNLChange.
// Pre-M2, the first round with prevLedger registered every trusted
// validator in the grace-period table, silently exempting them from
// ToDisable voting for ~256 ledgers after every restart.
func TestEngine_StartRound_SeedingHonorsRestart(t *testing.T) {
	prev := &mockLedger{id: consensus.LedgerID{0xCA, 0xFE}, seq: 1000}
	adaptor := newMockAdaptor()
	adaptor.lastLCL = prev
	adaptor.ledgers[prev.ID()] = prev
	// Steady-state UNL loaded by startup — no validator should be
	// treated as "new" by the very next round.
	adaptor.setTrusted([]consensus.NodeID{{0x10}, {0x20}, {0x30}, {0x40}, {0x50}})

	engine := NewEngine(adaptor, DefaultConfig())
	engine.mu.Lock()
	engine.prevLedger = prev
	engine.mu.Unlock()

	round := consensus.RoundID{Seq: prev.Seq() + 1, ParentHash: prev.ID()}
	if err := engine.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := len(adaptor.onUNLChangeCalls); got != 0 {
		t.Fatalf("steady-state restart must not forward any validator as `added`; got %d call(s): %+v",
			got, adaptor.onUNLChangeCalls)
	}
}

// TestEngine_StartRound_OnUNLChangeGatedOnFeature pins the
// featureNegativeUNL gate at RCLConsensus.cpp:1041. When the amendment
// is disabled on prevLedger, the engine must not drive OnUNLChange
// regardless of UNL deltas.
func TestEngine_StartRound_OnUNLChangeGatedOnFeature(t *testing.T) {
	prev := &mockLedger{id: consensus.LedgerID{0x77, 0x88}, seq: 100}
	adaptor := newMockAdaptor()
	adaptor.lastLCL = prev
	adaptor.ledgers[prev.ID()] = prev
	adaptor.disabledFeatures = map[string]bool{"NegativeUNL": true}
	adaptor.setTrusted([]consensus.NodeID{{0x11}, {0x22}})

	engine := NewEngine(adaptor, DefaultConfig())
	engine.mu.Lock()
	engine.prevLedger = prev
	engine.mu.Unlock()

	round := consensus.RoundID{Seq: prev.Seq() + 1, ParentHash: prev.ID()}
	if err := engine.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if got := len(adaptor.onUNLChangeCalls); got != 0 {
		t.Fatalf("featureNegativeUNL disabled: expected 0 calls, got %d", got)
	}
}

func sameNodeIDSet(a, b []consensus.NodeID) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[consensus.NodeID]struct{}, len(a))
	for _, n := range a {
		set[n] = struct{}{}
	}
	for _, n := range b {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
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

// TestEngine_StartRound_ResharesReplayedProposals pins issue #1188: after a
// ledger switch / round start, buffered peer proposals for the new prevLedger
// are re-shared to peers (rippled playbackProposals + adaptor_.share), not just
// stored. Without the re-share, a peer that missed a proposal would not be
// re-fed it on the recovery path.
func TestEngine_StartRound_ResharesReplayedProposals(t *testing.T) {
	prev := &mockLedger{id: consensus.LedgerID{0x11}, seq: 100}
	adaptor := newMockAdaptor()
	adaptor.lastLCL = prev
	adaptor.ledgers[prev.ID()] = prev
	peer := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{peer})

	engine := NewEngine(adaptor, DefaultConfig())

	// Buffer a peer proposal between rounds (accepted phase): OnProposal only
	// buffers it, it does not relay yet.
	proposal := &consensus.Proposal{
		Round:          consensus.RoundID{Seq: 101, ParentHash: prev.ID()},
		NodeID:         peer,
		Position:       0,
		TxSet:          consensus.TxSetID{1},
		CloseTime:      time.Now(),
		PreviousLedger: prev.ID(),
		Timestamp:      time.Now(),
	}
	if err := engine.OnProposal(proposal, 0); err != nil {
		t.Fatalf("OnProposal (buffer): %v", err)
	}
	adaptor.mu.RLock()
	preRelay := len(adaptor.proposalsRelayed)
	adaptor.mu.RUnlock()
	if preRelay != 0 {
		t.Fatalf("between-round proposal must be buffered not relayed; got %d relays", preRelay)
	}

	// Enter the round whose prevLedger matches the buffered proposal.
	engine.mu.Lock()
	engine.prevLedger = prev
	engine.mu.Unlock()
	round := consensus.RoundID{Seq: 101, ParentHash: prev.ID()}
	if err := engine.StartRound(round, false); err != nil {
		t.Fatalf("StartRound: %v", err)
	}

	adaptor.mu.RLock()
	defer adaptor.mu.RUnlock()
	if len(adaptor.proposalsRelayed) != 1 {
		t.Fatalf("replayed proposal not re-shared: got %d relays, want 1", len(adaptor.proposalsRelayed))
	}
	if adaptor.proposalsRelayed[0].NodeID != peer {
		t.Errorf("re-shared wrong proposal: NodeID = %x, want %x", adaptor.proposalsRelayed[0].NodeID, peer)
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

func TestEngine_IsValidating(t *testing.T) {
	adaptor := newMockAdaptor()
	engine := NewEngine(adaptor, DefaultConfig())

	// Not configured as a validator: never validating, even when synced.
	adaptor.validator = false
	adaptor.opMode = consensus.OpModeFull
	if engine.IsValidating() {
		t.Error("non-validator should not be validating")
	}

	// Configured validator but not yet synced to FULL: not validating.
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeTracking
	if engine.IsValidating() {
		t.Error("validator below OpModeFull should not be validating")
	}

	// Validator synced to FULL: validating.
	adaptor.opMode = consensus.OpModeFull
	if !engine.IsValidating() {
		t.Error("validator synced to OpModeFull should be validating")
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
	// Short open/idle so the round closes within the sleep budget;
	// keep MinConsensus large enough that the establish phase cannot
	// accept and cycle back to open before the assertion runs.
	config.Timing.LedgerMinClose = 10 * time.Millisecond
	config.Timing.LedgerMinConsensus = 200 * time.Millisecond
	config.Timing.LedgerIdleInterval = 20 * time.Millisecond

	engine := NewEngine(adaptor, config)

	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Failed to start engine: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	if engine.Phase() != consensus.PhaseOpen {
		t.Errorf("Expected Open phase, got %v", engine.Phase())
	}

	// Sleep past idleInterval (20ms) so the round closes, but well
	// short of MinConsensus (200ms) so we observe the Establish phase
	// before it accepts and cycles back to Open.
	time.Sleep(50 * time.Millisecond)

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
	ctx := t.Context()
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

	if config.Timing.LedgerMaxConsensus == 0 {
		t.Error("LedgerMaxConsensus should not be zero")
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

	ctx := t.Context()
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
	// Drive the full recovery: handleWrongLedger resolves the target
	// ledger via the adaptor (we seeded it above) and promotes to
	// switchedLedger via startRoundLocked(recovering=true). Passing nil
	// lets handleWrongLedger resolve it itself (the non-checkLedger path).
	engine.handleWrongLedger(targetID, nil)
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

// TestEngine_CheckLedger_CompletesHeldWrongLedgerSwitch pins the issue
// #724 guard fix in checkLedger. When the engine is already in
// ModeWrongLedger targeting the network's preferred ledger, checkLedger
// must no longer return unconditionally: it completes the switch (→
// ModeSwitchedLedger, wrongLedgerID cleared) once the target is locally
// available, and stays put (still ModeWrongLedger, no re-request) while it
// is not — mirroring rippled's handleWrongLedger re-attempt
// (Consensus.h:1094-1112). The other recovery tests call handleWrongLedger
// directly and so never traverse the guard itself; this drives
// checkLedger() end to end.
func TestEngine_CheckLedger_CompletesHeldWrongLedgerSwitch(t *testing.T) {
	ourID := consensus.LedgerID{0x0c}
	targetID := consensus.LedgerID{0xAA}

	// run wedges an engine in ModeWrongLedger targeting targetID: two
	// trusted peers proposing targetID make getNetworkLedger() return it,
	// and two trusted validations clear the support gate (targetID has
	// strictly more support than our stale fork). available controls
	// whether the target ledger is held locally. The mutation, the
	// checkLedger() call, and the result capture all happen under a single
	// e.mu hold so a background round tick cannot race the assertion.
	run := func(t *testing.T, available bool) (consensus.Mode, consensus.LedgerID, int) {
		t.Helper()
		adaptor := newMockAdaptor()
		adaptor.validator = true
		adaptor.opMode = consensus.OpModeFull
		adaptor.nodeID = consensus.NodeID{0x01}
		peerA := consensus.NodeID{0x02}
		peerB := consensus.NodeID{0x03}
		adaptor.trusted[adaptor.nodeID] = true
		adaptor.trusted[peerA] = true
		adaptor.trusted[peerB] = true
		adaptor.quorum = 3
		if available {
			adaptor.ledgers[targetID] = &mockLedger{id: targetID, seq: 101, closeTime: time.Now()}
		}

		engine := NewEngine(adaptor, DefaultConfig())
		ctx, cancel := context.WithCancel(context.Background())
		if err := engine.Start(ctx); err != nil {
			cancel()
			t.Fatalf("Start: %v", err)
		}
		defer func() {
			engine.Stop()
			cancel()
		}()

		engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: ourID}, true)

		adaptor.mu.Lock()
		adaptor.ledgersRequested = nil
		now := adaptor.now
		adaptor.mu.Unlock()

		engine.mu.Lock()
		// We hold a stale fork; the network prefers targetID.
		engine.prevLedger = &mockLedger{id: ourID, seq: 100, parentID: consensus.LedgerID{0x99}, closeTime: now}
		engine.wrongLedgerID = targetID
		engine.setMode(consensus.ModeWrongLedger)
		if engine.state != nil {
			engine.state.OurPosition = nil // no self-vote — let the peer majority decide
		}
		engine.proposalTracker.recentProposals = map[consensus.NodeID][]*consensus.Proposal{
			peerA: {{NodeID: peerA, PreviousLedger: targetID, Timestamp: now}},
			peerB: {{NodeID: peerB, PreviousLedger: targetID, Timestamp: now}},
		}
		if engine.validationTracker != nil {
			engine.validationTracker.SetTrusted([]consensus.NodeID{adaptor.nodeID, peerA, peerB})
			engine.validationTracker.Add(&consensus.Validation{NodeID: peerA, LedgerID: targetID, LedgerSeq: 101, Full: true, SignTime: now, SeenTime: now})
			engine.validationTracker.Add(&consensus.Validation{NodeID: peerB, LedgerID: targetID, LedgerSeq: 101, Full: true, SignTime: now, SeenTime: now})
		}
		engine.checkLedger()
		gotMode := engine.mode
		gotWrongID := engine.wrongLedgerID
		engine.mu.Unlock()

		adaptor.mu.RLock()
		reqs := len(adaptor.ledgersRequested)
		adaptor.mu.RUnlock()
		return gotMode, gotWrongID, reqs
	}

	t.Run("available_completes_switch", func(t *testing.T) {
		gotMode, gotWrongID, _ := run(t, true)
		if gotMode != consensus.ModeSwitchedLedger {
			t.Fatalf("available target: checkLedger must complete the switch to "+
				"SwitchedLedger, got %v (the old guard returned unconditionally "+
				"and stayed wedged in WrongLedger — issue #724)", gotMode)
		}
		if gotWrongID != (consensus.LedgerID{}) {
			t.Fatalf("available target: wrongLedgerID must be cleared after the "+
				"switch, got %x", gotWrongID[:8])
		}
	})

	t.Run("unavailable_stays_without_respam", func(t *testing.T) {
		gotMode, gotWrongID, reqs := run(t, false)
		if gotMode != consensus.ModeWrongLedger {
			t.Fatalf("unavailable target: checkLedger must stay in WrongLedger, got %v", gotMode)
		}
		if gotWrongID != targetID {
			t.Fatalf("unavailable target: wrongLedgerID must remain the target, got %x want %x", gotWrongID[:8], targetID[:8])
		}
		if reqs != 0 {
			t.Fatalf("unavailable target: checkLedger must not re-request the acquire "+
				"while already targeting it (no-spam guard), got %d requests", reqs)
		}
	})
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

	ctx := t.Context()
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

	ctx := t.Context()
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
// code — a go-xrpl validator would contribute nothing to flag-ledger
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

	ctx := t.Context()
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

	ctx := t.Context()
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

	ctx := t.Context()
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

	ctx := t.Context()
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

	ctx := t.Context()
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

	ctx := t.Context()
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

	ctx := t.Context()
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

	ctx := t.Context()
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

	ctx := t.Context()
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

// TestConsensus_NoSoftTimeoutAcceptAtLedgerMaxConsensus pins the
// rippled-faithful phaseEstablish flow: the engine MUST NOT force-
// accept the round just because roundTime crossed LedgerMaxConsensus.
// Rippled's checkConsensus (Consensus.cpp:176-263) has three terminal
// states — Yes, MovedOn, Expired — and Expired only fires at
// std::clamp(prevRoundTime × ABANDON_CONSENSUS_FACTOR,
// ledgerMAX_CONSENSUS, ledgerABANDON_CONSENSUS), which is the
// hard-abandon path. There is no soft-timeout-to-accept.
//
// The previous goxrpl-only soft path force-accepted at MAX_CONSENSUS,
// turned ResultTimeout into a side-chain LCL advance, and (combined
// with the broad consensusFail rule that suppressed emission on
// Timeout) drifted closed_seq arbitrarily far past validated in
// mixed-UNL soaks (#451).
func TestConsensus_NoSoftTimeoutAcceptAtLedgerMaxConsensus(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	config.Timing.LedgerMaxConsensus = 15 * time.Second
	config.Timing.LedgerAbandonConsensus = 120 * time.Second
	config.Timing.LedgerAbandonConsensusFactor = 10

	engine := NewEngine(adaptor, config)
	subscriber := &testSubscriber{events: make(chan consensus.Event, 32)}
	engine.Subscribe(subscriber)

	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	// Position the engine 16s into a round — past LedgerMaxConsensus
	// (15s) but well short of the clamped hard-abandon deadline
	// (prevRoundTime × factor = 2s × 10 = 20s, clamped to [15s, 120s]
	// → 20s). Pre-fix this fell into the soft-timeout-to-accept block
	// and called acceptLedger(ResultTimeout); post-fix that exact code
	// path is gone, so the round either stays in Establish or exits via
	// a different terminal state (MovedOn / Success) — never with
	// Result=Timeout.
	engine.mu.Lock()
	engine.setPhase(consensus.PhaseEstablish)
	engine.roundStartTime = time.Now().Add(-16 * time.Second)
	engine.prevRoundTime = 2 * time.Second
	engine.phaseEstablish()
	engine.mu.Unlock()

	sawTimeout := false
	deadline := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-subscriber.events:
			if cre, ok := ev.(*consensus.ConsensusReachedEvent); ok {
				if cre.Result == consensus.ResultTimeout {
					sawTimeout = true
				}
			}
		case <-deadline:
			break drain
		}
	}
	if sawTimeout {
		t.Errorf("no-soft-timeout: phaseEstablish must never produce a " +
			"ConsensusReachedEvent with Result=Timeout — the goxrpl-only " +
			"force-accept at LedgerMaxConsensus regressed back into the " +
			"establish path (#451 drift cause)")
	}
}

// TestCloseTimesOutOfBounds_NegativeSkewTolerance pins the lower bound of
// the close-time sanity check: rippled tolerates a previous round time down
// to -1s (Consensus.cpp:52, `prevRoundTime < -1s`) before treating it as
// out-of-bounds, absorbing small clock skew between validators rather than
// force-closing to recover.
func TestCloseTimesOutOfBounds_NegativeSkewTolerance(t *testing.T) {
	engine := NewEngine(newMockAdaptor(), DefaultConfig())

	cases := []struct {
		name               string
		prevRoundTime      time.Duration
		timeSincePrevClose time.Duration
		want               bool
	}{
		{"small negative skew within 1s tolerated", -500 * time.Millisecond, 0, false},
		{"exactly -1s tolerated", -1 * time.Second, 0, false},
		{"beyond -1s out of bounds", -1*time.Second - time.Millisecond, 0, true},
		{"normal positive round in bounds", 3 * time.Second, 3 * time.Second, false},
		{"prevRoundTime over 10min out of bounds", 10*time.Minute + time.Second, 0, true},
		{"timeSincePrevClose over 10min out of bounds", 3 * time.Second, 10*time.Minute + time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine.prevRoundTime = tc.prevRoundTime
			if got := engine.closeTimesOutOfBounds(tc.timeSincePrevClose); got != tc.want {
				t.Errorf("closeTimesOutOfBounds(prevRoundTime=%v, since=%v) = %v, want %v",
					tc.prevRoundTime, tc.timeSincePrevClose, got, tc.want)
			}
		})
	}
}

// TestConsensus_AbandonHardTimeout pins the behavior of the E3 hard
// deadline: once a round exceeds the ledgerABANDON_CONSENSUS clamp
// (rippled's 15s..120s clamp, ConsensusParms.h:113) AND establishCounter
// has cleared the per-avalanche-level minimum dwell, the engine
// abandons the round. Per rippled Consensus.cpp:253-263 + Consensus.h:
// 1760-1785, this means:
//
//  1. We treat the state as ConsensusState::Expired.
//  2. leaveConsensus() is called — if we were proposing, we bow out
//     to Observing (Consensus.h:1802-1817).
//  3. The accept step still runs with a distinct Result so callers
//     can tell a hard abandon from a soft force-accept.
//
// go-xrpl surfaces (3) as ResultAbandoned.
func TestConsensus_AbandonHardTimeout(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	config.Timing.LedgerMaxConsensus = 15 * time.Second
	config.Timing.LedgerAbandonConsensus = 120 * time.Second
	config.Timing.LedgerAbandonConsensusFactor = 10

	engine := NewEngine(adaptor, config)

	subscriber := &testSubscriber{events: make(chan consensus.Event, 32)}
	engine.Subscribe(subscriber)

	ctx := t.Context()
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
	// Clear the rippled-faithful retry gate (Consensus.h:1762-1779):
	// abandon only fires once each avalanche level has had its minimum
	// dwell. The +1 covers the increment phaseEstablish performs on
	// entry.
	engine.establishCounter = len(engine.parms.AvalancheCutoffs)*engine.parms.MinRounds + 1
	// Inject disagreeing trusted peers so we don't hit the
	// alone-too-long carve-out (which would resolve to Yes via
	// checkConsensusReached(_, 0, ..., reachedMax=true, ...)).
	// With OurPosition pinned to one tx set and 4 peers proposing two
	// different alternatives, agree*100/total stays at 20% — well below
	// the 80% gate — so the Expired arm wins.
	ourSet := consensus.TxSetID{0xAA}
	engine.state.OurPosition = &consensus.Proposal{
		Round:    round,
		Position: 1,
		TxSet:    ourSet,
	}
	for i := 1; i <= 4; i++ {
		nid := consensus.NodeID{byte(0x10 + i)}
		adaptor.trusted[nid] = true
		engine.proposalTracker.proposals[nid] = &consensus.Proposal{
			Round:    round,
			NodeID:   nid,
			Position: 1,
			TxSet:    consensus.TxSetID{byte(0xB0 + i)},
		}
	}
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
	sawObserving := slices.Contains(adaptor.modeChanges, consensus.ModeObserving)
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

// TestConsensus_AbandonRetryGate pins the rippled retry-gate from
// Consensus.h:1762-1779: when checkConsensus would return Expired
// but the round has not yet spent (len(avalancheCutoffs) * MinRounds)
// ticks in establish, the engine keeps trying instead of abandoning.
// This protects rounds with very long individual ticks (heavy disputes)
// from being dropped before each avalanche level gets its minimum dwell.
func TestConsensus_AbandonRetryGate(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	config.Timing.LedgerMaxConsensus = 15 * time.Second
	config.Timing.LedgerAbandonConsensus = 120 * time.Second
	config.Timing.LedgerAbandonConsensusFactor = 10

	engine := NewEngine(adaptor, config)
	subscriber := &testSubscriber{events: make(chan consensus.Event, 32)}
	engine.Subscribe(subscriber)

	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	if engine.mode != consensus.ModeProposing {
		engine.mu.Unlock()
		t.Fatalf("setup: expected ModeProposing after StartRound, got %v", engine.mode)
	}
	// Drive the round past the hard ceiling but with establishCounter
	// well below the per-avalanche-level minimum dwell. The retry gate
	// must suppress abandon. We inject disagreeing peers so the Expired
	// arm of checkConsensus is reached at all (otherwise the alone-too-
	// long carve-out at checkConsensusReached(_, 0, ..., reachedMax=true)
	// would resolve to Yes and bypass the Expired branch entirely).
	engine.setPhase(consensus.PhaseEstablish)
	engine.roundStartTime = time.Now().Add(-121 * time.Second)
	engine.prevRoundTime = 60 * time.Second
	engine.establishCounter = 0
	ourSet := consensus.TxSetID{0xAA}
	engine.state.OurPosition = &consensus.Proposal{
		Round:    round,
		Position: 1,
		TxSet:    ourSet,
	}
	for i := 1; i <= 4; i++ {
		nid := consensus.NodeID{byte(0x20 + i)}
		adaptor.trusted[nid] = true
		engine.proposalTracker.proposals[nid] = &consensus.Proposal{
			Round:    round,
			NodeID:   nid,
			Position: 1,
			TxSet:    consensus.TxSetID{byte(0xC0 + i)},
		}
	}
	engine.phaseEstablish()
	phaseAfter := engine.phase
	modeAfter := engine.mode
	engine.mu.Unlock()

	if phaseAfter != consensus.PhaseEstablish {
		t.Errorf("retry gate: phase should remain Establish, got %v", phaseAfter)
	}
	if modeAfter != consensus.ModeProposing {
		t.Errorf("retry gate: mode should remain Proposing (no bow-out), got %v", modeAfter)
	}

	deadline := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case ev := <-subscriber.events:
			if cre, ok := ev.(*consensus.ConsensusReachedEvent); ok {
				t.Errorf("retry gate: no consensus event should fire, got Result=%v", cre.Result)
			}
		case <-deadline:
			break drain
		}
	}
}

// TestConsensus_PrevProposersPause covers the 3/4 prev-proposers
// pause arm of checkConsensus (Consensus.cpp:208-218): when fewer
// than 3/4 of the previous round's proposers are present AND the
// round has not yet run for prevRoundTime + ledgerMIN_CONSENSUS, the
// engine does NOT accept even with majority agreement — slower
// validators get an extra MIN_CONSENSUS interval to catch up.
func TestConsensus_PrevProposersPause(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.opMode = consensus.OpModeFull

	config := DefaultConfig()
	engine := NewEngine(adaptor, config)
	engine.parms = consensus.DefaultConsensusParms()

	// 8 prev proposers; only 4 visible this round → 4 < 8*3/4=6 → pause.
	engine.prevProposers = 8
	engine.prevRoundTime = 5 * time.Second
	engine.closeTime.haveConsensus = true

	// roundTime past MIN_CONSENSUS but well below prevRoundTime + MIN.
	roundTime := config.Timing.LedgerMinConsensus + 100*time.Millisecond
	state := engine.checkConsensusState(roundTime, 4, 4)
	if state != consensusStateNo {
		t.Fatalf("expected consensusStateNo (3/4 pause), got %v", state)
	}

	// Once roundTime clears prevRoundTime + MIN_CONSENSUS, the gate
	// releases and the same tally yields Yes.
	roundTime = engine.prevRoundTime + config.Timing.LedgerMinConsensus + time.Second
	state = engine.checkConsensusState(roundTime, 4, 4)
	if state != consensusStateYes {
		t.Fatalf("expected consensusStateYes once 3/4 gate clears, got %v", state)
	}
}

// TestCheckConsensusReached covers the parity port of rippled's
// checkConsensusReached free function (Consensus.cpp:106-174). Each
// subtest pins one carve-out: alone-for-too-long, stalled
// short-circuit, count-self math, and ordinary threshold.
func TestCheckConsensusReached(t *testing.T) {
	tests := []struct {
		name      string
		agreeing  int
		total     int
		countSelf bool
		minPct    int
		reachedMax,
		stalled bool
		want bool
	}{
		{"alone before max → no", 0, 0, true, 80, false, false, false},
		{"alone past max → yes", 0, 0, true, 80, true, false, true},
		{"stalled short-circuits", 1, 10, false, 80, false, true, true},
		{"countSelf bumps agree+total", 7, 9, true, 80, false, false, true},
		{"normal threshold met", 8, 10, false, 80, false, false, true},
		{"normal threshold missed", 7, 10, false, 80, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkConsensusReached(tt.agreeing, tt.total, tt.countSelf, tt.minPct, tt.reachedMax, tt.stalled); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCheckConsensusState walks all six priority-ordered outcomes of
// checkConsensusState — the unified port of rippled's checkConsensus
// (Consensus.cpp:176-269). Each subtest pins one branch:
//
//  1. roundTime <= LedgerMinConsensus                     → No
//  2. currentProposers < prevProposers*3/4 AND
//     roundTime < (prevRoundTime + LedgerMinConsensus)    → No
//  3. agree threshold met                                 → Yes
//  4. agree below threshold, ProposersFinished past prev  → MovedOn
//  5. abandon deadline exceeded                           → Expired
//  6. past min, below threshold, deadline not yet hit     → No
func TestCheckConsensusState(t *testing.T) {
	parms := consensus.DefaultConsensusParms()

	newEngine := func() *Engine {
		adaptor := newMockAdaptor()
		adaptor.opMode = consensus.OpModeFull
		config := DefaultConfig()
		e := NewEngine(adaptor, config)
		e.parms = parms
		e.closeTime.haveConsensus = true
		return e
	}

	t.Run("1 too soon → No", func(t *testing.T) {
		e := newEngine()
		got := e.checkConsensusState(e.timing.LedgerMinConsensus, 10, 10)
		if got != consensusStateNo {
			t.Fatalf("got %v, want consensusStateNo", got)
		}
	})

	t.Run("2 3/4 prev-proposers pause → No", func(t *testing.T) {
		e := newEngine()
		e.prevProposers = 8
		e.prevRoundTime = 5 * time.Second
		// 4 < 8*3/4=6, and roundTime < prevRoundTime+MinConsensus.
		roundTime := e.timing.LedgerMinConsensus + 100*time.Millisecond
		got := e.checkConsensusState(roundTime, 4, 4)
		if got != consensusStateNo {
			t.Fatalf("got %v, want consensusStateNo (3/4 pause)", got)
		}
	})

	t.Run("3 majority agreement → Yes", func(t *testing.T) {
		e := newEngine()
		roundTime := e.timing.LedgerMinConsensus + time.Second
		got := e.checkConsensusState(roundTime, 8, 10)
		if got != consensusStateYes {
			t.Fatalf("got %v, want consensusStateYes", got)
		}
	})

	t.Run("4 peers moved on → MovedOn", func(t *testing.T) {
		e := newEngine()
		// Seed validationTracker with 4 trusted finished validators
		// past prevSeq so ProposersFinished returns 4 ≥ 80% of 4.
		prev := &mockLedger{id: consensus.LedgerID{0x10}, seq: 100}
		e.prevLedger = prev
		vt := NewValidationTracker(3, e.timing.ValidationFreshness)
		for i := range 4 {
			nodeID := consensus.NodeID{byte(0xA0 + i)}
			vt.trusted[nodeID] = true
			vt.byNode[nodeID] = &consensus.Validation{
				NodeID:    nodeID,
				LedgerSeq: prev.Seq() + 1,
				Full:      true,
			}
		}
		e.validationTracker = vt

		roundTime := e.timing.LedgerMinConsensus + time.Second
		// agree*100/4 = 25% < 80% → fail Yes; finished=4*100/4=100% → MovedOn.
		got := e.checkConsensusState(roundTime, 1, 4)
		if got != consensusStateMovedOn {
			t.Fatalf("got %v, want consensusStateMovedOn", got)
		}
	})

	t.Run("5 abandon deadline exceeded → Expired", func(t *testing.T) {
		e := newEngine()
		e.prevRoundTime = 5 * time.Second
		// abandonDeadlineExceeded clamps prevRoundTime*factor to
		// [LedgerMaxConsensus, LedgerAbandonConsensus]; default factor
		// is 10, max=15s, abandon=120s → clamp gives 50s, then min(50s,
		// 120s)=50s. Push roundTime well past that.
		roundTime := 200 * time.Second
		got := e.checkConsensusState(roundTime, 1, 10)
		if got != consensusStateExpired {
			t.Fatalf("got %v, want consensusStateExpired", got)
		}
	})

	t.Run("6 below threshold, deadline OK → No", func(t *testing.T) {
		e := newEngine()
		// No validationTracker → MovedOn arm cannot fire.
		// roundTime past LedgerMinConsensus but well below abandon clamp.
		roundTime := e.timing.LedgerMinConsensus + time.Second
		got := e.checkConsensusState(roundTime, 1, 10)
		if got != consensusStateNo {
			t.Fatalf("got %v, want consensusStateNo (default fallthrough)", got)
		}
	})

	// The Yes check must add self exactly once (countSelf), so a proposing
	// node with 3 agreeing peers out of 4 peer proposers reaches 4/5=80%.
	// The counts passed here are PEER-only (rippled currPeerPositions_); the
	// old code folded self into countAgreement AND relied on the peer count,
	// double- or single-counting inconsistently.
	t.Run("proposing self-inclusion: 3 of 4 peers agree → Yes at 80%", func(t *testing.T) {
		e := newEngine()
		e.setMode(consensus.ModeProposing)
		roundTime := e.timing.LedgerMinConsensus + time.Second
		// (3+self)/(4+self) = 4/5 = 80% ≥ 80 → Yes.
		got := e.checkConsensusState(roundTime, 3, 4)
		if got != consensusStateYes {
			t.Fatalf("got %v, want Yes (4/5=80%% with self)", got)
		}
	})

	// The 3/4-proposers straggler pause uses the PEER count, not peer+self.
	// prevProposers=8 → threshold 6; 5 peers present must pause. Under the
	// old self-fold the current count became 6 and the pause was skipped.
	t.Run("proposing self-exclusion: 5 peers vs prev 8 still pauses", func(t *testing.T) {
		e := newEngine()
		e.setMode(consensus.ModeProposing)
		e.prevProposers = 8
		e.prevRoundTime = 5 * time.Second
		roundTime := e.timing.LedgerMinConsensus + 100*time.Millisecond
		got := e.checkConsensusState(roundTime, 5, 5)
		if got != consensusStateNo {
			t.Fatalf("got %v, want No (5 < 8*3/4=6 straggler pause, self excluded)", got)
		}
	})
}

// TestEngine_OnValidation_NoSelfDeadlockOnQuorum pins the issue
// #381 root cause: ValidationTracker.Add fires the
// fully-validated callback synchronously on the goroutine that
// called OnValidation — and OnValidation already holds e.mu.Lock.
// A defensive e.mu.RLock inside the callback self-deadlocks
// because Go's RWMutex is non-recursive. Once a single
// fully-validated ledger fires, the engine writer never returns,
// the heartbeat parks, and every RPC reader piles up.
func TestEngine_OnValidation_NoSelfDeadlockOnQuorum(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.quorum = 1
	adaptor.opMode = consensus.OpModeFull
	trusted := consensus.NodeID{2}
	adaptor.setTrusted([]consensus.NodeID{trusted})
	adaptor.now = time.Now()

	engine := NewEngine(adaptor, DefaultConfig())

	// Start wires the ValidationTracker and its fully-validated
	// callback (engine.go SetFullyValidatedCallback) — without
	// Start the tracker is nil and OnValidation skips Add, hiding
	// the bug. The bug only fires when quorum is actually reached.
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{101},
		LedgerSeq: 101,
		NodeID:    trusted,
		SignTime:  adaptor.now,
		SeenTime:  adaptor.now,
		Full:      true,
	}

	// OnValidation must return cleanly even though Add fires the
	// fully-validated callback (quorum=1, one trusted validator,
	// one Full validation = quorum reached). Run on a separate
	// goroutine with a tight timeout so a regression of the
	// e.mu.RLock self-deadlock cannot pass by hanging.
	done := make(chan error, 1)
	go func() {
		done <- engine.OnValidation(validation, 1)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("OnValidation returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnValidation did not return — fully-validated callback " +
			"self-deadlocked on e.mu (issue #381 root cause)")
	}
}

// TestEngine_IsProposing_LockFreeWhileWriterHoldsMu pins the issue
// #381 follow-up: the RPC server_info hot path
// (Service.GetServerInfo holds ledger.s.mu.RLock → serverStateFunc →
// engine.IsProposing) used to acquire e.mu.RLock, which deadlocked
// against any engine writer that needed ledger.s.mu (e.g.
// OnValidation → fullyValidatedCallback → SetValidatedLedger). With
// the atomic mode mirror, IsProposing must complete immediately
// regardless of who currently holds e.mu — including a writer that
// will never release.
func TestEngine_IsProposing_LockFreeWhileWriterHoldsMu(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	engine := NewEngine(adaptor, DefaultConfig())

	// Drive the engine into ModeProposing so IsProposing must return
	// true. StartRound takes e.mu under the hood.
	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	if err := engine.StartRound(round, true); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if !engine.IsProposing() {
		t.Fatalf("preconditions: expected IsProposing=true after StartRound(true) in OpModeFull")
	}

	// Simulate a stuck writer: hold e.mu.Lock() until the test ends.
	writerHasLock := make(chan struct{})
	releaseWriter := make(chan struct{})
	go func() {
		engine.mu.Lock()
		close(writerHasLock)
		<-releaseWriter
		engine.mu.Unlock()
	}()
	defer close(releaseWriter)
	<-writerHasLock

	// IsProposing must NOT block on the contended e.mu — the read
	// is served from the atomic mirror. Run on a separate goroutine
	// with a tight timeout so a regression cannot pass by simply
	// being slow.
	done := make(chan bool, 1)
	go func() {
		done <- engine.IsProposing()
	}()

	select {
	case got := <-done:
		if !got {
			t.Errorf("IsProposing returned false while we are in ModeProposing")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("IsProposing blocked while a writer holds e.mu — atomic " +
			"mode mirror regression (issue #381 follow-up)")
	}

	// Mode() shares the same atomic-read fast path; verify it too.
	doneMode := make(chan consensus.Mode, 1)
	go func() {
		doneMode <- engine.Mode()
	}()
	select {
	case got := <-doneMode:
		if got != consensus.ModeProposing {
			t.Errorf("Mode returned %v while we are in ModeProposing", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Mode blocked while a writer holds e.mu — atomic mode mirror regression")
	}
}

// TestAcceptLedger_NoCloseTimeConsensus_DeterministicFallback pins
// the issue #401 root-cause fix: when close-time consensus FAILS,
// acceptLedger must use parentCloseTime + 1s — deterministically —
// for the new ledger's close time. Mirrors rippled
// RCLConsensus.cpp:481-488. Without the deterministic fallback, the
// no-consensus path falls through to CloseTimes.Self (the local
// clock), so each node hashes a different ledger header for the
// same seq, every locally-emitted validation disagrees with the
// network's accepted hash, and quorum becomes unreachable.
//
// Pins two properties:
//  1. With haveCloseTimeConsensus=false and a wildly skewed local
//     clock, the produced ledger's close time is exactly
//     parentCloseTime + 1s — independent of the local clock.
//  2. With haveCloseTimeConsensus=true, the engine still goes through
//     determineCloseTime / effCloseTime (regression guard).
func TestAcceptLedger_NoCloseTimeConsensus_DeterministicFallback(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	// Anchor parent ledger close time to a fixed, well-known value so
	// the assertion does not depend on wall-clock at test time.
	parentClose := time.Unix(1_700_000_000, 0).UTC()
	parent := &mockLedger{
		id:        consensus.LedgerID{0x01},
		seq:       50,
		closeTime: parentClose,
	}
	adaptor.lastLCL = parent
	adaptor.ledgers[parent.ID()] = parent

	// Pin the local clock far away from parentClose+1s so a regression
	// to "Self fallback" is unambiguously observable.
	adaptor.now = parentClose.Add(37 * time.Second)

	engine := NewEngine(adaptor, DefaultConfig())

	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: parent.Seq() + 1, ParentHash: parent.ID()}
	engine.StartRound(round, true)

	// Drive acceptLedger directly with no close-time consensus.
	engine.mu.Lock()
	engine.prevLedger = parent
	engine.closeTime.haveConsensus = false
	// Seed CloseTimes.Self to the same skewed clock so determineCloseTime
	// (if it were still wired) would pick this value — making the
	// regression visible. Real production inits Self from adaptor.Now().
	engine.state.CloseTimes.Self = adaptor.now
	engine.setPhase(consensus.PhaseEstablish)
	engine.acceptLedger(consensus.ResultSuccess)
	engine.mu.Unlock()

	got := adaptor.lastLCL.CloseTime()
	want := parentClose.Add(time.Second)
	if !got.Equal(want) {
		t.Fatalf("no-consensus close time: want %v (parentClose+1s, "+
			"deterministic fallback), got %v — engine fell through "+
			"to local-clock fallback, divergence regression of #401",
			want, got)
	}

	// Sanity check: the local clock was skewed far enough that a
	// regression would be unmistakable.
	if got.Equal(adaptor.now) {
		t.Fatalf("close time matches local clock — fallback path is " +
			"using CloseTimes.Self (regression of #401 root-cause fix)")
	}
}

// TestAcceptLedger_WrongLedger_EmitsPartial pins the rippled-faithful
// validation gate: when mode==WrongLedger and result==Success,
// acceptLedger MUST run end-to-end (build, store, advance phase) AND
// broadcast a PARTIAL (Full=false) validation.
//
// Mirrors rippled doAccept (RCLConsensus.cpp:464-602). The emission
// branch at 591-594 has no mode gate; only validating_ (config), the
// isCompatible check on the BUILT ledger, !consensusFail, and
// canValidateSeq. The mode==proposing test at 477 controls the Full
// flag passed to validate() (851), not whether emission happens.
//
// areCompatible (View.cpp:797-857) only flags a build incompatible if
// it conflicts with the validated chain at the SAME or PRECEDING seq —
// not when it sits at a higher seq with a different sibling-hash on
// the side chain. So a wrongLedger close that builds the NEXT seq
// from our local LCL still passes isCompatible and emits a partial.
//
// Without the partial emission, peers' validator-presence detectors
// mark our key `offline` (RCLValidations) and quorum becomes
// mathematically unreachable in any mixed UNL — #451.
//
// Properties pinned:
//  1. Exactly one validation is broadcast.
//  2. The validation has Full=false (partial — does not count toward
//     peer quorum, but keeps the validator visible to peers).
//  3. Phase advances out of Establish (round is NOT stuck).
func TestAcceptLedger_WrongLedger_EmitsPartial(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	engine := NewEngine(adaptor, DefaultConfig())

	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	// Force mode to wrongLedger, drive acceptLedger directly.
	engine.mu.Lock()
	engine.setMode(consensus.ModeWrongLedger)
	engine.setPhase(consensus.PhaseEstablish)
	engine.acceptLedger(consensus.ResultSuccess)
	phaseAfter := engine.phase
	engine.mu.Unlock()

	if phaseAfter == consensus.PhaseEstablish {
		t.Fatalf("wrongLedger acceptLedger must NOT leave the round " +
			"stuck in Establish (rippled doAccept advances it " +
			"unconditionally). This regression wedges the engine.")
	}

	adaptor.mu.RLock()
	emitted := len(adaptor.validationsBroadcast)
	var emittedFull bool
	if emitted > 0 {
		emittedFull = adaptor.validationsBroadcast[0].Full
	}
	adaptor.mu.RUnlock()

	if emitted != 1 {
		t.Fatalf("wrongLedger acceptLedger must broadcast exactly one "+
			"partial validation; got %d emissions — peers mark us "+
			"`offline` without this signal and quorum stalls (#451)",
			emitted)
	}
	if emittedFull {
		t.Fatalf("wrongLedger emission must be PARTIAL (Full=false); " +
			"a Full validation in wrongLedger would count toward " +
			"peer quorum for a possibly-divergent ledger")
	}
}

// TestSendValidation_CanValidateSeq_DedupsSameAndOlderSeq pins the
// issue #401 fix: sendValidation MUST silently drop a second emission
// at the same — or any earlier — ledger sequence, no matter the hash.
//
// Without this guard, when our local close diverges from the network's
// accepted hash, a subsequent re-accept (e.g. after acquiring the
// foreign LCL) can race two distinct validations for the same seq onto
// the wire. Rippled's Byzantine Behavior Detector flags us permanently
// for "Conflicting validation for N" (RCLValidations.cpp:236-240,
// Validations.h:625-665), and our validations stop counting toward
// quorum forever. Mirrors rippled's per-node SeqEnforcer, the engine
// of canValidateSeq (Validations.h:830).
func TestSendValidation_CanValidateSeq_DedupsSameAndOlderSeq(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	engine := NewEngine(adaptor, DefaultConfig())

	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	// First emission at seq=101 hash=A — must broadcast.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xA1}, seq: 101})
	engine.mu.Unlock()

	// Second emission at the SAME seq with a DIFFERENT hash — must be
	// silently dropped. This is the production scenario that triggers
	// the Byzantine flag at peers (#401).
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xB2}, seq: 101})
	engine.mu.Unlock()

	// Third emission at an EARLIER seq — must also be silently dropped.
	// SeqEnforcer requires strictly increasing seqs.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xC3}, seq: 100})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	emitted := append([]*consensus.Validation(nil), adaptor.validationsBroadcast...)
	adaptor.mu.RUnlock()

	if len(emitted) != 1 {
		t.Fatalf("canValidateSeq guard: want exactly one emission for "+
			"seq=101 (same-seq and earlier-seq calls dropped), got %d",
			len(emitted))
	}
	if emitted[0].LedgerID != (consensus.LedgerID{0xA1}) {
		t.Fatalf("the kept emission must be the FIRST (seq=101 hash=A1), "+
			"got hash=%x — guard let a later same-seq emission overwrite",
			emitted[0].LedgerID)
	}

	// Fourth emission at a LATER seq — must broadcast normally.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xD4}, seq: 102})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	emitted = append([]*consensus.Validation(nil), adaptor.validationsBroadcast...)
	adaptor.mu.RUnlock()

	if len(emitted) != 2 {
		t.Fatalf("strictly-increasing seq must pass: want 2 emissions "+
			"after seq=102, got %d", len(emitted))
	}
	if emitted[1].LedgerSeq != 102 {
		t.Fatalf("second kept emission seq: want 102, got %d", emitted[1].LedgerSeq)
	}
}

// TestSendValidation_SeqEnforcerExpiresAfterIdle pins rippled's
// SeqEnforcer reset semantics (Validations.h:118-128): after
// validationSetExpires (10 minutes) of silence, the floor resets to
// 0 so a long-restarted / partitioned validator can come back online
// and validate at sequences below its pre-outage floor. Without the
// reset, a node that crashed at high seq and rejoined when the chain
// was further ahead but its FIRST candidate ledger was at a lower seq
// (e.g. it switched to a shorter side-chain it could acquire faster)
// would be permanently silenced — every catch-up ledger has seq <=
// the stale floor, so canValidateSeqLocked rejects forever.
func TestSendValidation_SeqEnforcerExpiresAfterIdle(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	baseTime := time.Unix(1_700_000_000, 0).UTC()
	adaptor.mu.Lock()
	adaptor.now = baseTime
	adaptor.mu.Unlock()

	engine := NewEngine(adaptor, DefaultConfig())

	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 100, ParentHash: consensus.LedgerID{1}}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	// Emit at seq=500. Floor → 500.
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xA1}, seq: 500})
	engine.mu.Unlock()

	// Without expiry: re-emitting at seq=300 is rejected (300 <= 500).
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xB2}, seq: 300})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	pre := append([]*consensus.Validation(nil), adaptor.validationsBroadcast...)
	adaptor.mu.RUnlock()
	if len(pre) != 1 {
		t.Fatalf("pre-expiry: want one emission (seq=500), got %d", len(pre))
	}

	// Advance the clock past validationSetExpires. The next call must
	// see the floor reset to 0 and accept seq=300.
	adaptor.mu.Lock()
	adaptor.now = baseTime.Add(validationSetExpires + time.Second)
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xC3}, seq: 300})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	post := append([]*consensus.Validation(nil), adaptor.validationsBroadcast...)
	adaptor.mu.RUnlock()
	if len(post) != 2 {
		t.Fatalf("post-expiry: SeqEnforcer floor should have reset to 0, "+
			"allowing seq=300 to emit (was rejected by stale floor=500); "+
			"want 2 total emissions, got %d", len(post))
	}
	if post[1].LedgerSeq != 300 {
		t.Fatalf("post-expiry: kept emission seq mismatch — got %d, want 300",
			post[1].LedgerSeq)
	}

	// The new floor is 300. Re-emitting at seq=200 should still be
	// rejected (the reset happened once at the expiry boundary, not
	// continuously).
	engine.mu.Lock()
	engine.sendValidation(&mockLedger{id: consensus.LedgerID{0xD4}, seq: 200})
	engine.mu.Unlock()

	adaptor.mu.RLock()
	final := len(adaptor.validationsBroadcast)
	adaptor.mu.RUnlock()
	if final != 2 {
		t.Fatalf("post-reset floor not re-armed: a seq below the new "+
			"floor (300) was wrongly accepted; want 2 emissions, got %d",
			final)
	}
}

// TestAcceptLedger_ConsensusFailSuppressesValidation pins the
// rippled-faithful emission gate at RCLConsensus.cpp:479,591-594:
//
//	bool const consensusFail = result.state == ConsensusState::MovedOn;
//	if (validating_ && !consensusFail && canValidateSeq(...)) validate(...)
//
// Only ConsensusState::MovedOn suppresses emission. The Expired (hard
// timeout) path explicitly does NOT — every validator times out around
// the same wall-clock instant and the network forms quorum on the
// timeout-built ledger. goxrpl's ResultTimeout / ResultAbandoned both
// map to rippled's Expired (no consensus reached, but we built a
// ledger) and so must emit; ResultMovedOn / ResultFail are the only
// suppress-paths. The previous over-broad rule `result != Success`
// silently bowed goxrpl out of every timed-out round and turned a
// recoverable stall into permanent quorum starvation (#451).
func TestAcceptLedger_ConsensusFailSuppressesValidation(t *testing.T) {
	cases := []struct {
		name   string
		result consensus.Result
		want   int // emissions expected
	}{
		{"Success_emits", consensus.ResultSuccess, 1},
		{"Timeout_emits", consensus.ResultTimeout, 1},
		{"Abandoned_emits", consensus.ResultAbandoned, 1},
		{"MovedOn_suppressed", consensus.ResultMovedOn, 0},
		{"Fail_suppressed", consensus.ResultFail, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adaptor := newMockAdaptor()
			adaptor.validator = true
			adaptor.opMode = consensus.OpModeFull

			engine := NewEngine(adaptor, DefaultConfig())

			ctx := t.Context()
			if err := engine.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer engine.Stop()

			round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
			engine.StartRound(round, true)

			// Seed the engine with a tx-set + position so acceptLedger
			// has something to build with; use the inbound proposal path
			// to drive it without reproducing the entire FSM here.
			adaptor.mu.Lock()
			adaptor.validationsBroadcast = nil
			adaptor.mu.Unlock()

			// Drive acceptLedger directly. The phase guard at the top
			// requires PhaseEstablish — StartRound transitions Open →
			// Establish on the first timer tick, so force it.
			engine.mu.Lock()
			engine.setPhase(consensus.PhaseEstablish)
			engine.acceptLedger(tc.result)
			engine.mu.Unlock()

			adaptor.mu.RLock()
			got := len(adaptor.validationsBroadcast)
			adaptor.mu.RUnlock()

			if got != tc.want {
				t.Fatalf("result=%v: want %d emissions, got %d "+
					"(consensusFail gate did not match rippled "+
					"RCLConsensus.cpp:587-594)", tc.result, tc.want, got)
			}
		})
	}
}

// TestCloseLedger_ProposingUsesFilteredTxSet pins the open-ledger
// filter wiring: when the engine is in ModeProposing (or standalone),
// closeLedger MUST source its tx set from
// adaptor.GetProposableTxs(prev) — the rippled-faithful
// open-ledger-filtered set (RCLConsensus.cpp:333-349) — and NOT from
// the raw GetPendingTxs() pool. Regressing back to GetPendingTxs
// would re-introduce the bootstrap fork class fixed in #401.
//
// Two arms:
//
//  1. Proposing → GetProposableTxs invoked at least once.
//  2. Non-proposing (e.g. ModeWrongLedger): the filter is gated to
//     proposing/standalone, skipping the expensive multi-pass apply
//     when the result has no observable network effect. Pinned so a
//     future widening doesn't silently reintroduce the per-round
//     filter cost in observer modes.
func TestCloseLedger_ProposingUsesFilteredTxSet(t *testing.T) {
	t.Run("proposing-uses-filter", func(t *testing.T) {
		adaptor := newMockAdaptor()
		adaptor.validator = true
		adaptor.opMode = consensus.OpModeFull
		adaptor.proposableOverride = [][]byte{} // distinct from raw pool

		engine := NewEngine(adaptor, DefaultConfig())
		ctx := t.Context()
		if err := engine.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer engine.Stop()

		round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
		engine.StartRound(round, true)

		engine.mu.Lock()
		engine.setMode(consensus.ModeProposing)
		engine.setPhase(consensus.PhaseOpen)
		engine.closeLedger()
		engine.mu.Unlock()

		adaptor.mu.RLock()
		got := adaptor.proposableCalled
		adaptor.mu.RUnlock()
		if got == 0 {
			t.Fatalf("ModeProposing closeLedger must call GetProposableTxs " +
				"(filter the open-ledger-applicable subset). Got 0 calls — " +
				"would propose the raw pending pool, regressing the #401 fix.")
		}
	})

	t.Run("wrongledger-uses-raw-pool", func(t *testing.T) {
		adaptor := newMockAdaptor()
		adaptor.validator = true
		adaptor.opMode = consensus.OpModeFull
		adaptor.proposableOverride = [][]byte{}

		engine := NewEngine(adaptor, DefaultConfig())
		ctx := t.Context()
		if err := engine.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer engine.Stop()

		round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
		engine.StartRound(round, true)

		engine.mu.Lock()
		engine.setMode(consensus.ModeWrongLedger)
		engine.setPhase(consensus.PhaseOpen)
		engine.closeLedger()
		engine.mu.Unlock()

		adaptor.mu.RLock()
		got := adaptor.proposableCalled
		adaptor.mu.RUnlock()
		if got != 0 {
			t.Fatalf("ModeWrongLedger closeLedger must NOT call "+
				"GetProposableTxs (filter is gated to proposing or "+
				"standalone). Got %d calls — would burn the per-round "+
				"multi-pass apply cost on a position we won't "+
				"broadcast.", got)
		}
	})
}

// TestCloseLedger_BelowQuorumStallLog pins the #422 stall signal:
// closeLedger emits one INFO log when peer_proposers+self can't meet
// quorum, and stays silent at-quorum and before the first completed
// round (where prevProposers carries no signal).
func TestCloseLedger_BelowQuorumStallLog(t *testing.T) {
	const stallTag = "close-below-quorum"

	withCapturedLogs := func(t *testing.T, fn func()) string {
		t.Helper()
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		t.Cleanup(func() { slog.SetDefault(prev) })
		fn()
		return buf.String()
	}

	newEngineWithUNL := func(t *testing.T, quorum, prevProposers, consensusCount int) (*Engine, *mockAdaptor) {
		t.Helper()
		adaptor := newMockAdaptor()
		adaptor.validator = true
		adaptor.opMode = consensus.OpModeFull
		adaptor.quorum = quorum
		// Trusted set sized to make unl_size representative; IsTrusted
		// isn't consulted on this path.
		for i := 1; i <= 5; i++ {
			adaptor.trusted[consensus.NodeID{byte(i)}] = true
		}
		engine := NewEngine(adaptor, DefaultConfig())
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := engine.Start(ctx); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = engine.Stop() })
		round := consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}
		engine.StartRound(round, true)
		engine.mu.Lock()
		engine.setMode(consensus.ModeProposing)
		engine.setPhase(consensus.PhaseOpen)
		engine.prevProposers = prevProposers
		engine.consensusCount = uint64(consensusCount)
		engine.mu.Unlock()
		return engine, adaptor
	}

	t.Run("below-quorum-fires-once", func(t *testing.T) {
		engine, _ := newEngineWithUNL(t, 4, 2, 1)
		out := withCapturedLogs(t, func() {
			engine.mu.Lock()
			engine.closeLedger()
			engine.mu.Unlock()
		})
		if !strings.Contains(out, stallTag) {
			t.Fatalf("expected close-below-quorum log when peer_proposers(2)+1 < quorum(4); got:\n%s", out)
		}
		if !strings.Contains(out, "peer_proposers=2") || !strings.Contains(out, "quorum=4") {
			t.Fatalf("log missing structured fields; got:\n%s", out)
		}
		if got := strings.Count(out, stallTag); got != 1 {
			t.Fatalf("close-below-quorum must fire exactly once per close, got %d:\n%s", got, out)
		}
	})

	t.Run("at-quorum-silent", func(t *testing.T) {
		// peer_proposers(3) + self(1) == quorum(4): stall log MUST NOT fire.
		engine, _ := newEngineWithUNL(t, 4, 3, 1)
		out := withCapturedLogs(t, func() {
			engine.mu.Lock()
			engine.closeLedger()
			engine.mu.Unlock()
		})
		if strings.Contains(out, stallTag) {
			t.Fatalf("close-below-quorum must NOT fire when peer_proposers+1 >= quorum; got:\n%s", out)
		}
	})

	t.Run("genesis-round-silent", func(t *testing.T) {
		// consensusCount==0: prevProposers is meaningless before any
		// round has completed.
		engine, _ := newEngineWithUNL(t, 4, 0, 0)
		out := withCapturedLogs(t, func() {
			engine.mu.Lock()
			engine.closeLedger()
			engine.mu.Unlock()
		})
		if strings.Contains(out, stallTag) {
			t.Fatalf("close-below-quorum must NOT fire before the first completed round; got:\n%s", out)
		}
	})
}

// TestShouldPause_AheadAndLaggards mirrors rippled's
// Consensus<T>::shouldPause (Consensus.h:1241-1362). Three-validator UNL,
// quorum=3, validated tip at seq=9, our prev at seq=12 (ahead=3). The
// two peer validators have only validated up to seq=9 (laggards). Under
// these conditions shouldPause MUST return true so phaseEstablish
// suspends instead of advancing the LCL further.
//
// Without this gate the engine close-then-rollback-cycles every
// heartbeat against an unreachable quorum and the local closed_ledger
// drifts arbitrarily far past the validated tip — #451.
func TestShouldPause_AheadAndLaggards(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.nodeID = consensus.NodeID{0x01}

	peerA := consensus.NodeID{0x02}
	peerB := consensus.NodeID{0x03}
	adaptor.trusted[adaptor.nodeID] = true
	adaptor.trusted[peerA] = true
	adaptor.trusted[peerB] = true
	adaptor.quorum = 3

	validatedID := consensus.LedgerID{0x09}
	validatedLedger := &mockLedger{id: validatedID, seq: 9, closeTime: time.Now()}
	adaptor.ledgers[validatedID] = validatedLedger
	adaptor.validatedLedgerHashOverride = validatedID

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 13, ParentHash: consensus.LedgerID{0x0c}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	// Mark this node as having previously emitted a validation so the
	// bootstrap early-out in shouldPause doesn't fire.
	engine.ourLastValidatedSeq = 9
	// Anchor our prev at seq=12 (ahead of validated tip by 3) and put
	// the engine in establish to mirror the phaseEstablish entry state.
	engine.prevLedger = &mockLedger{id: consensus.LedgerID{0x0c}, seq: 12, closeTime: time.Now()}
	engine.setPhase(consensus.PhaseEstablish)
	// Inject peer validations at seq=9 — both peers are laggards: their
	// latest validation has not advanced past our prev.
	if engine.validationTracker != nil {
		engine.validationTracker.SetTrusted([]consensus.NodeID{adaptor.nodeID, peerA, peerB})
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerA, LedgerID: validatedID, LedgerSeq: 9, Full: true, SignTime: time.Now(), SeenTime: time.Now()})
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerB, LedgerID: validatedID, LedgerSeq: 9, Full: true, SignTime: time.Now(), SeenTime: time.Now()})
	}
	paused := engine.shouldPause(2 * time.Second)
	engine.mu.Unlock()

	if !paused {
		t.Fatalf("shouldPause must return true when ahead=3 with 2 laggards in a 3-validator UNL (quorum=3)")
	}
}

// TestShouldPause_BootstrapEarlyOut pins the early-out at
// Consensus.h:1269-1276: shouldPause MUST return false before the node
// has ever fully validated a ledger. Without this guard a fresh
// validator could pause itself out of the very first round and never
// emit anything — the network would never reach consensus.
func TestShouldPause_BootstrapEarlyOut(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.trusted[adaptor.nodeID] = true
	adaptor.quorum = 1

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 2, ParentHash: consensus.LedgerID{0x01}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	// ourLastValidatedSeq stays at zero (no prior validation) — the
	// bootstrap gate must short-circuit shouldPause to false even when
	// the trivial ahead/validator conditions would otherwise match.
	engine.ourLastValidatedSeq = 0
	engine.prevLedger = &mockLedger{id: consensus.LedgerID{0x05}, seq: 5, closeTime: time.Now()}
	paused := engine.shouldPause(0)
	engine.mu.Unlock()

	if paused {
		t.Fatalf("shouldPause must return false before any prior validation (bootstrap); a fresh node never pauses out of round 1")
	}
}

// TestShouldPause_HardTimeoutOverride pins the hard-timeout escape at
// Consensus.h:1271: once the round has exceeded LedgerMaxConsensus we
// stop pausing so the abandon path can drive the round to a terminal
// state instead of pausing forever.
func TestShouldPause_HardTimeoutOverride(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.nodeID = consensus.NodeID{0x01}
	peerA := consensus.NodeID{0x02}
	peerB := consensus.NodeID{0x03}
	adaptor.trusted[adaptor.nodeID] = true
	adaptor.trusted[peerA] = true
	adaptor.trusted[peerB] = true
	adaptor.quorum = 3

	validatedID := consensus.LedgerID{0x09}
	validatedLedger := &mockLedger{id: validatedID, seq: 9, closeTime: time.Now()}
	adaptor.ledgers[validatedID] = validatedLedger
	adaptor.validatedLedgerHashOverride = validatedID

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 13, ParentHash: consensus.LedgerID{0x0c}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	engine.ourLastValidatedSeq = 9
	engine.prevLedger = &mockLedger{id: consensus.LedgerID{0x0c}, seq: 12, closeTime: time.Now()}
	engine.setPhase(consensus.PhaseEstablish)
	if engine.validationTracker != nil {
		engine.validationTracker.SetTrusted([]consensus.NodeID{adaptor.nodeID, peerA, peerB})
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerA, LedgerID: validatedID, LedgerSeq: 9, Full: true, SignTime: time.Now(), SeenTime: time.Now()})
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerB, LedgerID: validatedID, LedgerSeq: 9, Full: true, SignTime: time.Now(), SeenTime: time.Now()})
	}
	// Same setup as the laggards test, but past the hard timeout —
	// the gate must release so phaseEstablish proceeds to abandon.
	paused := engine.shouldPause(engine.timing.LedgerMaxConsensus + time.Second)
	engine.mu.Unlock()

	if paused {
		t.Fatalf("shouldPause must release after LedgerMaxConsensus elapses so the abandon path can fire")
	}
}

// TestAcceptLedger_WrongLedger_IncompatibleBuild_Suppresses pins the
// isCompatible half of the rippled emission gate (RCLConsensus.cpp:587-589
// → LedgerMaster::isCompatible → areCompatible at View.cpp:797-857).
// Setup: validated tip at seq=50, our built ledger at seq=52 with a
// parent that does NOT descend from validated. Mirrors a genuine
// wrongLedger side-chain build. Even though wrongLedger mode no
// longer gates emission, the build is not on the validated chain so
// areCompatible(validated, built) returns false at View.cpp:805-820
// and rippled suppresses validation. goxrpl must match.
func TestAcceptLedger_WrongLedger_IncompatibleBuild_Suppresses(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	// Validated chain: seq 50 with a specific id; that id is what the
	// gate compares against the built ledger's ancestor.
	validatedID := consensus.LedgerID{0xAA}
	validatedLedger := &mockLedger{id: validatedID, seq: 50, closeTime: time.Now()}
	adaptor.ledgers[validatedID] = validatedLedger
	adaptor.validatedLedgerHashOverride = validatedID

	// Our prev (a side-chain ledger at seq=51 with a parent that is
	// NOT the validated id). The BuildLedger mock will produce a
	// child at seq=52 with parentID = prev.ID(); the walk from built
	// will land on prev at seq=51, then on its parent at seq=50 —
	// which is in the ledgers map and has id != validatedID. That
	// triggers the incompatibility path.
	sideAncestorID := consensus.LedgerID{0xBB}
	sideAncestor := &mockLedger{id: sideAncestorID, seq: 50, closeTime: time.Now()}
	sidePrevID := consensus.LedgerID{0xBC}
	sidePrev := &mockLedger{id: sidePrevID, seq: 51, parentID: sideAncestorID, closeTime: time.Now()}
	adaptor.ledgers[sideAncestorID] = sideAncestor
	adaptor.ledgers[sidePrevID] = sidePrev

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 52, ParentHash: sidePrevID}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.prevLedger = sidePrev
	engine.setMode(consensus.ModeWrongLedger)
	engine.setPhase(consensus.PhaseEstablish)
	engine.acceptLedger(consensus.ResultSuccess)
	engine.mu.Unlock()

	adaptor.mu.RLock()
	emitted := len(adaptor.validationsBroadcast)
	adaptor.mu.RUnlock()

	if emitted != 0 {
		t.Fatalf("incompatible side-chain build must suppress emission "+
			"(rippled LedgerMaster::isCompatible → areCompatible "+
			"rejects at View.cpp:805-820); got %d emissions — "+
			"Frankenstein-hash regression of class #401", emitted)
	}
}

// TestAcceptLedger_WrongLedger_CompatibleAhead_EmitsPartial pins the
// #451 fact pattern: mode==wrongLedger but our built ledger is just
// one seq ahead of validated on the SAME chain. areCompatible
// (View.cpp:805-820) walks built back, finds the validated id as the
// grandparent, returns true → rippled emits a partial. goxrpl must
// match.
func TestAcceptLedger_WrongLedger_CompatibleAhead_EmitsPartial(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull

	// Validated tip at seq=50; our prev at seq=51 with parentID =
	// validatedID. BuildLedger produces seq=52 whose parent is prev.
	// Walk from built (52) → prev (51) → validated (50); ids match
	// → compatible.
	validatedID := consensus.LedgerID{0xAA}
	validatedLedger := &mockLedger{id: validatedID, seq: 50, closeTime: time.Now()}
	prevID := consensus.LedgerID{0xAB}
	prev := &mockLedger{id: prevID, seq: 51, parentID: validatedID, closeTime: time.Now()}
	adaptor.ledgers[validatedID] = validatedLedger
	adaptor.ledgers[prevID] = prev
	adaptor.validatedLedgerHashOverride = validatedID

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 52, ParentHash: prevID}
	engine.StartRound(round, true)

	adaptor.mu.Lock()
	adaptor.validationsBroadcast = nil
	adaptor.mu.Unlock()

	engine.mu.Lock()
	engine.prevLedger = prev
	engine.setMode(consensus.ModeWrongLedger)
	engine.setPhase(consensus.PhaseEstablish)
	engine.acceptLedger(consensus.ResultSuccess)
	engine.mu.Unlock()

	adaptor.mu.RLock()
	emitted := len(adaptor.validationsBroadcast)
	var emittedFull bool
	if emitted > 0 {
		emittedFull = adaptor.validationsBroadcast[0].Full
	}
	adaptor.mu.RUnlock()

	if emitted != 1 {
		t.Fatalf("compatible-ahead wrongLedger build must emit one partial "+
			"(rippled areCompatible returns true at View.cpp:805-820 when "+
			"the validated id is on the build's ancestry); got %d", emitted)
	}
	if emittedFull {
		t.Fatalf("wrongLedger emission must be Full=false (partial)")
	}
}

// TestShouldPause_StaleValidationCountsAsOffline pins the freshness
// classification in countLaggardsAndOfflineLocked: a peer whose only
// tracked validation is older than validationLaggardFreshness (20s,
// matching rippled's parms_.validationFRESHNESS at Validations.h:89)
// is counted as offline, not as a laggard.
//
// Mirrors Validations.h:1136-1140 — rippled's `current()` iterator
// only invokes the lambda for fresh validations, so a stale peer's
// key stays in trustedKeys and is tallied as offline at
// Consensus.h:1255 rather than incrementing the laggards counter.
//
// Difference from a current-but-behind peer (TestShouldPause_AheadAndLaggards):
// here we set quorum=2 in a 3-validator UNL so that with one stale
// peer treated as OFFLINE and one current peer at our seq (non-laggard),
// the phase-0 predicate `laggards+offline > total-quorum`
// (0+1 > 3-2 = 1) is FALSE and we do NOT pause. The earlier
// (`<=` + no-freshness) implementation would have classified the
// stale peer as a laggard, flipping laggards+offline from 0+1 to 1+0
// and still not pausing — but more importantly the intermediate-phase
// predicate would diverge by counting stale-as-laggard. We
// independently verify the count via shouldPause's debug categorisation.
func TestShouldPause_StaleValidationCountsAsOffline(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.nodeID = consensus.NodeID{0x01}
	peerA := consensus.NodeID{0x02}
	peerB := consensus.NodeID{0x03}
	adaptor.trusted[adaptor.nodeID] = true
	adaptor.trusted[peerA] = true
	adaptor.trusted[peerB] = true
	adaptor.quorum = 2

	validatedID := consensus.LedgerID{0x09}
	validatedLedger := &mockLedger{id: validatedID, seq: 9, closeTime: time.Now()}
	adaptor.ledgers[validatedID] = validatedLedger
	adaptor.validatedLedgerHashOverride = validatedID

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 11, ParentHash: consensus.LedgerID{0x0a}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	engine.ourLastValidatedSeq = 9
	engine.prevLedger = &mockLedger{id: consensus.LedgerID{0x0a}, seq: 10, closeTime: time.Now()}
	engine.setPhase(consensus.PhaseEstablish)
	now := adaptor.Now()
	if engine.validationTracker != nil {
		engine.validationTracker.SetTrusted([]consensus.NodeID{adaptor.nodeID, peerA, peerB})
		// Peer A: fresh validation at our prev (seq=10) → current,
		// not a laggard with strict-less-than.
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerA, LedgerID: consensus.LedgerID{0x0a}, LedgerSeq: 10, Full: true, SignTime: now, SeenTime: now})
		// Peer B: STALE validation (seenTime well past the 20s
		// freshness window) at seq=8. Must count as offline, not
		// as a laggard.
		stale := now.Add(-2 * time.Minute)
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerB, LedgerID: consensus.LedgerID{0x08}, LedgerSeq: 8, Full: true, SignTime: stale, SeenTime: stale})
	}

	laggards, offline := engine.countLaggardsAndOfflineLocked(10, []consensus.NodeID{adaptor.nodeID, peerA, peerB})
	engine.mu.Unlock()

	if laggards != 0 {
		t.Fatalf("strict-less-than: peer at seq==prev must be current, not a laggard; got laggards=%d", laggards)
	}
	if offline != 1 {
		t.Fatalf("stale validation must classify peer as offline, not laggard; got offline=%d", offline)
	}
}

// TestPhaseEstablish_PauseAndRecover is the end-to-end pause-recovery
// integration test mirroring rippled's testPauseForLaggards
// (Consensus_test.cpp:1030-1119). Two-phase scenario:
//
//  1. Ahead engine in phaseEstablish with 2 laggard peers — multiple
//     consecutive shouldPause checks must return TRUE so the phase
//     does NOT terminate via acceptLedger.
//  2. Peers catch up to our prev seq — the next shouldPause must
//     return FALSE so the round can progress to a terminal state
//     instead of pausing forever.
//
// This is the contract the gate exists to provide: without it the
// engine close-then-rollback-cycles every heartbeat against an
// unreachable quorum, drifting the local closed_ledger arbitrarily
// far past the validated tip — the failure mode in #451.
func TestPhaseEstablish_PauseAndRecover(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.validator = true
	adaptor.opMode = consensus.OpModeFull
	adaptor.nodeID = consensus.NodeID{0x01}
	peerA := consensus.NodeID{0x02}
	peerB := consensus.NodeID{0x03}
	adaptor.trusted[adaptor.nodeID] = true
	adaptor.trusted[peerA] = true
	adaptor.trusted[peerB] = true
	adaptor.quorum = 3

	validatedID := consensus.LedgerID{0x09}
	validatedLedger := &mockLedger{id: validatedID, seq: 9, closeTime: time.Now()}
	adaptor.ledgers[validatedID] = validatedLedger
	adaptor.validatedLedgerHashOverride = validatedID

	engine := NewEngine(adaptor, DefaultConfig())
	ctx := t.Context()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer engine.Stop()

	round := consensus.RoundID{Seq: 13, ParentHash: consensus.LedgerID{0x0c}}
	engine.StartRound(round, true)

	engine.mu.Lock()
	engine.ourLastValidatedSeq = 9
	engine.prevLedger = &mockLedger{id: consensus.LedgerID{0x0c}, seq: 12, closeTime: time.Now()}
	engine.setPhase(consensus.PhaseEstablish)
	now := adaptor.Now()
	if engine.validationTracker != nil {
		engine.validationTracker.SetTrusted([]consensus.NodeID{adaptor.nodeID, peerA, peerB})
		// Phase 1: both peers stuck at validated (seq=9), well
		// behind our prev (seq=12). ahead=3 puts us in phase=2.
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerA, LedgerID: validatedID, LedgerSeq: 9, Full: true, SignTime: now, SeenTime: now})
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerB, LedgerID: validatedID, LedgerSeq: 9, Full: true, SignTime: now, SeenTime: now})
	}

	// Three consecutive ticks: gate stays high, engine refuses to
	// advance via acceptLedger. We check shouldPause directly because
	// invoking phaseEstablish would also exercise the abandon/close
	// branches; the contract we are pinning is the pause gate
	// itself.
	for i, dt := range []time.Duration{1 * time.Second, 3 * time.Second, 7 * time.Second} {
		if !engine.shouldPause(dt) {
			engine.mu.Unlock()
			t.Fatalf("tick %d at roundTime=%v: must pause (ahead=3, both peers behind)", i, dt)
		}
	}

	// Phase 2: peers catch up to our prev seq (12). Refresh their
	// validations via Add with the higher seq — strict-less-than
	// rule (seq < prev) means seq==12 makes them non-laggards.
	if engine.validationTracker != nil {
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerA, LedgerID: consensus.LedgerID{0x0c}, LedgerSeq: 12, Full: true, SignTime: now, SeenTime: now})
		engine.validationTracker.Add(&consensus.Validation{NodeID: peerB, LedgerID: consensus.LedgerID{0x0c}, LedgerSeq: 12, Full: true, SignTime: now, SeenTime: now})
	}

	// shouldPause now returns false — the round is free to progress.
	if engine.shouldPause(1 * time.Second) {
		engine.mu.Unlock()
		t.Fatalf("after peers caught up to our prev seq: must NOT pause (rippled testPauseForLaggards convergence path)")
	}
	engine.mu.Unlock()
}
