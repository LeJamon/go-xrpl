package testing

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// TestEnv manages a test ledger environment for transaction testing.
// It provides a simplified interface for creating accounts, funding them,
// submitting transactions, and verifying results.
type TestEnv struct {
	// t is the active testing.TB used for Helper / Fatalf / Cleanup, captured at
	// construction. testing.TB is an interface so both *testing.T and *testing.B work.
	t        testing.TB
	ledger   *ledger.Ledger
	clock    *ManualClock
	accounts map[string]*Account

	// Genesis ledger reference
	genesisLedger *ledger.Ledger

	// Lightweight ledger history: sequence -> state map root hash.
	// Matches rippled's LedgerHistory pattern -- stores only hashes, not full objects.
	// Past state can be reconstructed on demand via NewFromRootHash(hash, family).
	ledgerRootHashes map[uint32][32]byte

	// Current ledger sequence
	currentSeq uint32

	// Fees configuration
	baseFee          uint64
	reserveBase      uint64
	reserveIncrement uint64

	// feeTrack models rippled's LoadFeeTrack: the node-local / remote /
	// cluster load factor that scales the open-ledger fee floor. It is
	// threaded into every EngineConfig so that checkFee applies the
	// load-scaled minimum fee, mirroring rippled where the floor fires
	// whenever the view is open. Conformance fixtures that exercise a
	// mid-test load change (TxQ_test.cpp setRemoteFee / raiseLocalFee)
	// drive it via FeeTrack(). Defaults to the normal fee (no escalation).
	feeTrack *feetrack.LoadFeeTrack

	// Amendment rules - controls which amendments are enabled.
	// Reference: rippled's FeatureBitset in test/jtx/Env.h
	rulesBuilder *amendment.RulesBuilder

	// pendingAmendments / pendingEnable / pendingDisable stage amendment changes
	// that take effect on the next Close(), matching rippled where
	// enableFeature/disableFeature require close() for changes to take effect.
	// pendingAmendments (set by SetAmendments) REPLACES the whole rule set;
	// pendingEnable/pendingDisable (set by EnableFeature/DisableFeature) are
	// deltas applied on top of the current set. All are validated at call time.
	// Reference: rippled Env.cpp: "Env::close() must be called for feature
	// enable to take place."
	pendingAmendments []string
	pendingEnable     []string
	pendingDisable    []string

	// NetworkID for engine configuration (0 = mainnet default, >1024 requires NetworkID in txns)
	networkID uint32

	// VerifySignatures enables cryptographic signature verification in the engine.
	// Default is false (test mode). Set to true for conformance tests with real tx_blobs.
	VerifySignatures bool

	// openLedger controls whether the engine checks fee adequacy.
	// When true (default for normal tests), fee adequacy is checked
	// (Fee >= calculateBaseFee). When false (conformance replay mode),
	// fee adequacy is skipped, matching rippled's behavior where
	// checkFee only checks when ctx.view.open() is true.
	// Reference: rippled Transactor.cpp checkFee — "Only check fee is
	// sufficient when the ledger is open."
	openLedger bool

	// Optional state map family for backed SHAMaps (PebbleDB on disk).
	// Only set when using NewTestEnvBacked() for heavy tests that would OOM otherwise.
	// When nil, SHAMaps use unbacked mode (fast, full in-memory clones).
	stateFamily *shamap.NodeStoreFamily

	// Transaction queue (optional). When non-nil, Submit() routes through the
	// TxQ for fee escalation and sequence-gap queuing.
	// Reference: rippled's TxQ used by NetworkOPs::processTransaction.
	txQueue *txq.TxQ

	// bypassTxQ temporarily bypasses TxQ routing when true. Used for setup
	// operations (fund, trust) that should go directly to the ledger, matching
	// rippled's apply() vs submit() distinction for setup operations.
	bypassTxQ bool

	// txQApplyFlags is the ApplyFlags handed to the TxQ for the next
	// submission. Reset to zero on every Submit; tests that want to
	// simulate rippled's tapFAIL_HARD admission rule can set it via
	// the field before calling Submit.
	txQApplyFlags tx.ApplyFlags

	// txInLedger tracks the number of transactions applied to the current open
	// ledger. Reset on Close(). Used by TxQ for fee escalation computation.
	txInLedger uint32

	// invariantViolationHook, when set, is installed on the per-submit engine
	// to force an invariant violation. Used by invariant-escalation tests; nil
	// for every normal submission.
	invariantViolationHook tx.InvariantViolationHook

	// closingTxTotal tracks the total transaction count including inner batch
	// transactions. In rippled, the closed ledger's tx map includes inner
	// batch txns as separate entries. This counter matches that behavior for
	// ProcessClosedLedger fee metrics computation.
	// Reset on Close(). Incremented by 1 for regular txns and by 1+N for
	// batch txns with N inner transactions.
	closingTxTotal uint32

	// closingFeeLevels tracks the actual fee levels of transactions in the
	// current open ledger. Used by ProcessClosedLedger to compute the median
	// fee level (escalation multiplier). Without this, the median would always
	// be BaseLevel, causing fee escalation to be less aggressive than rippled.
	// Reset on Close().
	closingFeeLevels []txq.FeeLevel

	// heldTxns stores transactions that got terPRE_SEQ or other retryable
	// results. After a successful transaction for the same account, held
	// transactions are retried. This mirrors rippled's LedgerMaster held
	// transaction mechanism.
	// Key: account address string -> slice of held transactions.
	heldTxns map[string][]tx.Transaction

	// replayOnClose enables the open-ledger consensus replay behavior.
	// When true, Close() rebuilds the closed ledger from the parent
	// closed ledger by replaying all tracked transactions in canonical
	// order with retry passes. This matches rippled's standalone
	// consensus simulation (Consensus::simulate -> buildLedger ->
	// applyTransactions).
	//
	// Needed for tests that depend on:
	// - terPRE_SEQ transactions being retried after close
	// - tec transactions being re-applied from a clean state after
	//   prerequisite objects are created by batch transactions
	//
	// Reference: rippled BuildLedger.cpp applyTransactions()
	replayOnClose bool

	// openLedgerSetupTxns tracks fund/trust setup transactions submitted
	// to the current open ledger. Applied first during replay (in submission
	// order) to ensure prerequisites (accounts, trust lines) exist before
	// user transactions are replayed in canonical order.
	openLedgerSetupTxns []tx.Transaction

	// openLedgerUserTxns tracks fixture/user transactions submitted to the
	// current open ledger. Applied second during replay in canonical sorted
	// order (sortCanonicalSalted), matching rippled's CanonicalTXSet.
	openLedgerUserTxns []tx.Transaction

	// inSetupMode indicates whether the current transactions are setup
	// operations (fund/trust). When true, transactions are routed to
	// openLedgerSetupTxns; when false, to openLedgerUserTxns.
	inSetupMode bool

	// lastClosedLedger stores the most recent closed ledger, used as the
	// parent for replay-on-close. Updated in Close().
	lastClosedLedger *ledger.Ledger

	// nextCloseSalt overrides the canonical sort salt for the next closeWithReplay.
	// Set from the fixture's tx_set_hash field to match rippled's exact ordering.
	// Cleared after use.
	nextCloseSalt *[32]byte
}

// NewTestEnv creates a new test environment with a genesis ledger.
func NewTestEnv(t testing.TB) *TestEnv {
	t.Helper()

	// Ensure every transaction type is registered before tests run.
	// Idempotent — safe to call from any test environment constructor.
	all.RegisterAll()

	// Create genesis ledger with test configuration matching rippled's test env
	// (200 XRP base reserve, 50 XRP increment -- see rippled/src/test/jtx/impl/envconfig.cpp)
	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Fees.ReserveBase = drops.DropsPerXRP * 200     // 200 XRP
	genesisConfig.Fees.ReserveIncrement = drops.DropsPerXRP * 50 // 50 XRP
	genesisResult, err := genesis.Create(genesisConfig)
	if err != nil {
		t.Fatalf("Failed to create genesis ledger: %v", err)
	}

	// Note: drops.Fees has unexported fields, so we use a zero value
	var fees drops.Fees
	genesisLedger := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		fees,
	)

	clock := NewManualClock()
	openLedger, err := ledger.NewOpen(genesisLedger, clock.Now())
	if err != nil {
		t.Fatalf("Failed to create open ledger: %v", err)
	}

	env := &TestEnv{
		t:                t,
		ledger:           openLedger,
		clock:            clock,
		accounts:         make(map[string]*Account),
		genesisLedger:    genesisLedger,
		ledgerRootHashes: make(map[uint32][32]byte),
		currentSeq:       2,
		baseFee:          10,
		reserveBase:      200_000_000, // 200 XRP (matches rippled test env)
		reserveIncrement: 50_000_000,  // 50 XRP (matches rippled test env)
		// Initialize with all supported amendments enabled (like rippled's testable_amendments())
		rulesBuilder: amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported),
		openLedger:   true, // Normal test mode: check fee adequacy
		feeTrack:     feetrack.New(),
	}

	// Register master account
	master := MasterAccount()
	env.accounts[master.Name] = master

	return env
}

// NewTestEnvWithTxQ creates a test environment with a transaction queue.
// Submit() will route transactions through the TxQ for fee escalation and
// sequence-gap queuing, matching rippled's behavior when using Env with TxQ.
// Reference: rippled's test Env routes through NetworkOPs -> TxQ.
func NewTestEnvWithTxQ(t testing.TB, cfg txq.Config) *TestEnv {
	t.Helper()
	env := NewTestEnv(t)
	env.txQueue = txq.New(cfg)
	return env
}

// NewTestEnvWithTxQAndConfig creates a test environment with a transaction queue
// and custom genesis configuration.
func NewTestEnvWithTxQAndConfig(t testing.TB, txqCfg txq.Config, genesisCfg genesis.Config) *TestEnv {
	t.Helper()
	env := NewTestEnvWithConfig(t, genesisCfg)
	env.txQueue = txq.New(txqCfg)
	return env
}

// NewTestEnvBacked creates a test environment with PebbleDB-backed SHAMaps.
// Use this for heavy tests (e.g., crossing_limits with 2000+ offers) that would
// OOM with unbacked mode. Data goes to disk; only the LRU cache lives in RAM.
func NewTestEnvBacked(t testing.TB) *TestEnv {
	t.Helper()
	env := NewTestEnv(t)
	env.enablePebbleBacking(t)
	return env
}

// enablePebbleBacking enables PebbleDB-backed SHAMaps on the environment.
// Must be called before any transactions are submitted.
func (e *TestEnv) enablePebbleBacking(t testing.TB) {
	t.Helper()
	stateFamily, err := shamap.NewPebbleNodeStoreFamily(t.TempDir(), 200000)
	if err != nil {
		t.Fatalf("Failed to create state family: %v", err)
	}
	t.Cleanup(func() { stateFamily.Close() })
	e.stateFamily = stateFamily
	e.genesisLedger.SetStateMapFamily(stateFamily)

	// Recreate the open ledger so it inherits the backed state map
	openLedger, err := ledger.NewOpen(e.genesisLedger, e.clock.Now())
	if err != nil {
		t.Fatalf("Failed to recreate open ledger with backing: %v", err)
	}
	e.ledger = openLedger
}

// NewTestEnvWithConfig creates a new test environment with custom genesis configuration.
func NewTestEnvWithConfig(t testing.TB, cfg genesis.Config) *TestEnv {
	t.Helper()

	// Ensure every transaction type is registered before tests run.
	all.RegisterAll()

	genesisResult, err := genesis.Create(cfg)
	if err != nil {
		t.Fatalf("Failed to create genesis ledger: %v", err)
	}

	// Note: drops.Fees has unexported fields, so we use a zero value
	var fees drops.Fees
	genesisLedger := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		fees,
	)

	clock := NewManualClock()
	openLedger, err := ledger.NewOpen(genesisLedger, clock.Now())
	if err != nil {
		t.Fatalf("Failed to create open ledger: %v", err)
	}

	env := &TestEnv{
		t:                t,
		ledger:           openLedger,
		clock:            clock,
		accounts:         make(map[string]*Account),
		genesisLedger:    genesisLedger,
		ledgerRootHashes: make(map[uint32][32]byte),
		currentSeq:       2,
		baseFee:          uint64(cfg.Fees.BaseFee.Drops()),
		reserveBase:      uint64(cfg.Fees.ReserveBase.Drops()),
		reserveIncrement: uint64(cfg.Fees.ReserveIncrement.Drops()),
		// Initialize with all supported amendments enabled (like rippled's testable_amendments())
		rulesBuilder: amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported),
		openLedger:   true, // Normal test mode: check fee adequacy
		feeTrack:     feetrack.New(),
	}
	master := MasterAccount()
	env.accounts[master.Name] = master

	return env
}

// SetOpenLedger controls whether the engine checks fee adequacy.
// When false, fee adequacy checks are skipped (matching rippled's closed-ledger behavior).
func (e *TestEnv) SetOpenLedger(open bool) {
	e.openLedger = open
}

// SetBypassTxQ temporarily bypasses TxQ routing. When true, Submit() goes
// directly to the engine even when a TxQ is configured. This matches rippled's
// distinction between apply() (direct, used for setup) and submit() (via TxQ).
func (e *TestEnv) SetBypassTxQ(bypass bool) {
	e.bypassTxQ = bypass
}

// SetInvariantViolationHook installs a test-only hook on every subsequently
// submitted transaction's engine, forcing the invariant pass to report a
// violation. Used to exercise the tec→tecINVARIANT_FAILED→tefINVARIANT_FAILED
// escalation. Pass nil to clear it.
func (e *TestEnv) SetInvariantViolationHook(hook tx.InvariantViolationHook) {
	e.invariantViolationHook = hook
}

// ResetTxQMaxSize resets the TxQ's maxSize to nil (no limit).
// This matches rippled's initial state where maxSize_ is std::nullopt
// before the first user-initiated processClosedLedger call. In rippled,
// the genesis close (startGenesisLedger) does NOT call processClosedLedger,
// so maxSize_ remains nullopt until the first env.close() in the test.
func (e *TestEnv) ResetTxQMaxSize() {
	if e.txQueue != nil {
		e.txQueue.ResetMaxSize()
	}
}

// SetBaseFee changes the base fee for subsequent transactions.
// Used to apply post-initFee() fee changes in conformance tests.
func (e *TestEnv) SetBaseFee(baseFee uint64) {
	e.baseFee = baseFee
	e.syncFeeSettings()
}

// FeeTrack returns the environment's LoadFeeTrack so conformance fixtures
// can model rippled's mid-test load changes. Mirrors rippled tests reaching
// for env.app().getFeeTrack() to call setRemoteFee / raiseLocalFee /
// lowerLocalFee. The returned tracker scales the open-ledger fee floor
// applied by the engine's checkFee.
func (e *TestEnv) FeeTrack() *feetrack.LoadFeeTrack {
	return e.feeTrack
}

// ResetLoadFee returns the load factor to its normal (unescalated) value:
// it clears any remote-reported escalation and decays the local fee back to
// the reference fee. Mirrors a rippled test running the local fee back down
// (`while (getFeeTrack().lowerLocalFee());`) and clearing the remote fee.
func (e *TestEnv) ResetLoadFee() {
	if e.feeTrack == nil {
		return
	}
	e.feeTrack.SetRemoteFee(feetrack.LoadBase)
	e.feeTrack.SetClusterFee(feetrack.LoadBase)
	for e.feeTrack.LowerLocalFee() {
	}
}

// SetReserves changes the reserve base and increment for subsequent transactions.
// Used to apply post-initFee() reserve changes in conformance tests.
func (e *TestEnv) SetReserves(reserveBase, reserveIncrement uint64) {
	e.reserveBase = reserveBase
	e.reserveIncrement = reserveIncrement
	e.syncFeeSettings()
}

// syncFeeSettings writes the env's current fee/reserve values into the ledger's
// FeeSettings entry. rippled changes reserves via a fee vote that rewrites the
// FeeSettings ledger object; the conformance harness shortcuts that vote with
// SetBaseFee/SetReserves, so without this sync the engine (which reads reserves
// from the FeeSettings object, e.g. payment.GetLedgerReserves) would keep seeing
// the stale genesis values and misclassify offers as unfunded.
func (e *TestEnv) syncFeeSettings() {
	feesKey := keylet.Fees()
	data, err := e.ledger.Read(feesKey)
	if err != nil || len(data) == 0 {
		return
	}
	fs, err := state.ParseFeeSettings(data)
	if err != nil {
		return
	}
	if fs.XRPFeesMode {
		fs.BaseFeeDrops = e.baseFee
		fs.ReserveBaseDrops = e.reserveBase
		fs.ReserveIncrementDrops = e.reserveIncrement
	} else {
		fs.BaseFee = e.baseFee
		fs.ReserveBase = uint32(e.reserveBase)
		fs.ReserveIncrement = uint32(e.reserveIncrement)
	}
	newData, err := state.SerializeFeeSettings(fs)
	if err != nil {
		return
	}
	_ = e.ledger.Update(feesKey, newData)
}

// SetNextCloseSalt sets the canonical sort salt for the next replay close.
// When set, closeWithReplay() uses this salt instead of computing one from
// the transaction set. Cleared after use.
func (e *TestEnv) SetNextCloseSalt(salt [32]byte) {
	e.nextCloseSalt = &salt
}
