package jtx

import (
	"bytes"
	"fmt"
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

// TestEnv manages a test ledger environment for transaction testing.
// It provides a simplified interface for creating accounts, funding them,
// submitting transactions, and verifying results.
type TestEnv struct {
	// t is the active testing.TB used for Helper / Fatalf / Cleanup, captured at
	// construction. testing.TB is an interface so both *testing.T and *testing.B work.
	t                 testing.TB
	ledger            *ledger.Ledger
	clock             *ManualClock
	accounts          map[string]*Account
	accountsByAddress map[string]*Account

	// Genesis ledger reference
	genesisLedger *ledger.Ledger

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

	numberContextOverride *state.NumberContext

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

	// viewOpen marks the apply view as open (rippled's view.open()) WITHOUT the
	// fee-adequacy floor that openLedger controls. Conformance non-TxQ suites turn
	// openLedger off but still need the open-view fee branch (terINSUF_FEE_B vs the
	// closed-only tecINSUFF_FEE) and its internal-failure variants.
	viewOpen bool

	// Optional state map family for backed SHAMaps (PebbleDB on disk).
	// Only set when using NewTestEnvBacked() for heavy tests that would OOM otherwise.
	// When nil, SHAMaps use unbacked mode (fast, full in-memory clones).
	stateFamily *backend.NodeStore

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
	invariantViolationHook txengine.InvariantViolationHook

	// heldTxns stores transactions that hit a retryable (ter*) result because of a
	// sequence gap. They are retried mid-window once a transaction for the same
	// account succeeds, mirroring rippled's mHeldTransactions. Keyed by account.
	heldTxns map[string][]tx.Transaction

	localTxs *localtxs.LocalTxs

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
	replayOnClose       bool
	needsConsensusBuild bool

	// lastClosedLedger stores the most recent closed ledger, used as the
	// parent for replay-on-close. Updated in Close().
	lastClosedLedger *ledger.Ledger

	// nextCloseSalt overrides the canonical sort salt for the next closed-ledger build.
	// Set from the fixture's tx_set_hash field to match rippled's exact ordering.
	// Cleared after use.
	nextCloseSalt *[32]byte
}

// NewTestEnv creates a new test environment with a genesis ledger.
func NewTestEnv(t testing.TB) *TestEnv {
	t.Helper()
	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Fees.ReserveBase = drops.DropsPerXRP * 200     // 200 XRP
	genesisConfig.Fees.ReserveIncrement = drops.DropsPerXRP * 50 // 50 XRP
	return newTestEnv(t, genesisConfig)
}

// NewTestEnvWithTxQ creates a test environment with a transaction queue.
// Submit() will route transactions through the TxQ for fee escalation and
// sequence-gap queuing, matching rippled's behavior when using Env with TxQ.
// Reference: rippled's test Env routes through NetworkOPs -> TxQ.
func NewTestEnvWithTxQ(t testing.TB, cfg txq.Config) *TestEnv {
	t.Helper()
	env := NewTestEnv(t)
	queue, err := txq.New(cfg)
	if err != nil {
		t.Fatalf("invalid transaction queue configuration: %v", err)
	}
	env.txQueue = queue
	return env
}

// NewTestEnvWithTxQAndConfig creates a test environment with a transaction queue
// and custom genesis configuration.
func NewTestEnvWithTxQAndConfig(t testing.TB, txqCfg txq.Config, genesisCfg genesis.Config) *TestEnv {
	t.Helper()
	env := NewTestEnvWithConfig(t, genesisCfg)
	queue, err := txq.New(txqCfg)
	if err != nil {
		t.Fatalf("invalid transaction queue configuration: %v", err)
	}
	env.txQueue = queue
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
	stateFamily, err := backend.OpenPebble(t.TempDir(), 256, 200000)
	if err != nil {
		t.Fatalf("Failed to create state family: %v", err)
	}
	t.Cleanup(func() {
		if err := stateFamily.Close(); err != nil {
			t.Errorf("close state family: %v", err)
		}
	})
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
	return newTestEnv(t, cfg)
}

func newTestEnv(t testing.TB, cfg genesis.Config) *TestEnv {
	t.Helper()
	// Ensure every transaction type is registered before tests run.
	all.RegisterAll()
	baseFee := cfg.Fees.BaseFee.Drops()
	reserveBase := cfg.Fees.ReserveBase.Drops()
	reserveIncrement := cfg.Fees.ReserveIncrement.Drops()
	if baseFee < 0 || reserveBase < 0 || reserveIncrement < 0 {
		t.Fatal("genesis fees cannot be negative")
	}
	modernFees := false
	for _, feature := range cfg.Amendments {
		if feature == amendment.FeatureXRPFees {
			modernFees = true
			break
		}
	}
	if !modernFees && (reserveBase > math.MaxUint32 || reserveIncrement > math.MaxUint32) {
		t.Fatal("legacy genesis reserves exceed uint32")
	}

	genesisResult, err := genesis.Create(cfg)
	if err != nil {
		t.Fatalf("Failed to create genesis ledger: %v", err)
	}

	// Note: drops.Fees has unexported fields, so we use a zero value
	var fees drops.Fees
	genesisLedger, err := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		fees,
	)
	if err != nil {
		t.Fatalf("Failed to construct genesis ledger: %v", err)
	}

	clock := NewManualClock()
	openLedger, err := ledger.NewOpen(genesisLedger, clock.Now())
	if err != nil {
		t.Fatalf("Failed to create open ledger: %v", err)
	}

	env := &TestEnv{
		t:                 t,
		ledger:            openLedger,
		clock:             clock,
		accounts:          make(map[string]*Account),
		accountsByAddress: make(map[string]*Account),
		genesisLedger:     genesisLedger,
		lastClosedLedger:  genesisLedger,
		currentSeq:        2,
		baseFee:           uint64(baseFee),
		reserveBase:       uint64(reserveBase),
		reserveIncrement:  uint64(reserveIncrement),
		// Initialize with all supported amendments enabled (like rippled's testable_amendments())
		rulesBuilder: amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported),
		openLedger:   true, // Normal test mode: check fee adequacy
		feeTrack:     feetrack.New(),
		localTxs:     localtxs.New(),
	}
	master := MasterAccount()
	if err := env.registerAccount(master); err != nil {
		t.Fatalf("register master account: %v", err)
	}

	return env
}

func (e *TestEnv) registerAccount(account *Account) error {
	if account == nil {
		return fmt.Errorf("cannot register nil account")
	}
	_, decoded, err := addresscodec.DecodeClassicAddressToAccountID(account.Address)
	if err != nil || len(decoded) != len(account.ID) {
		return fmt.Errorf("account %q has invalid classic address %q", account.Name, account.Address)
	}
	if !bytes.Equal(decoded, account.ID[:]) {
		return fmt.Errorf("account %q address does not match its account ID", account.Name)
	}
	byName := e.accounts[account.Name]
	if byName != nil && !sameAccountIdentity(byName, account) {
		return fmt.Errorf("account name %q is already registered with different credentials", account.Name)
	}
	byAddress := e.accountsByAddress[account.Address]
	if byAddress != nil && !sameAccountIdentity(byAddress, account) {
		return fmt.Errorf("account address %s is already registered with different credentials", account.Address)
	}
	if byName != nil {
		e.accountsByAddress[account.Address] = byName
		return nil
	}
	if byAddress != nil {
		e.accounts[account.Name] = byAddress
		return nil
	}
	e.accounts[account.Name] = account
	e.accountsByAddress[account.Address] = account
	return nil
}

// Memoize registers an account's fixture identity without funding it.
func (e *TestEnv) Memoize(account *Account) {
	e.t.Helper()
	if err := e.registerAccount(account); err != nil {
		e.t.Fatalf("memoize account: %v", err)
	}
}

func sameAccountIdentity(left, right *Account) bool {
	return left != nil && right != nil &&
		left.Address == right.Address &&
		left.ID == right.ID &&
		left.KeyType == right.KeyType &&
		bytes.Equal(left.PublicKey, right.PublicKey) &&
		bytes.Equal(left.PrivateKey, right.PrivateKey)
}

// SetOpenLedger controls whether the engine checks fee adequacy.
// When false, fee adequacy checks are skipped (matching rippled's closed-ledger behavior).
func (e *TestEnv) SetOpenLedger(open bool) {
	e.openLedger = open
}

// SetViewOpen marks the apply view as open without the fee-adequacy floor
// (see the viewOpen field).
func (e *TestEnv) SetViewOpen(open bool) {
	e.viewOpen = open
}

// SetNumberContextOverride selects a Number context independently of the
// environment's amendment rules, matching rippled's test scale guard.
func (e *TestEnv) SetNumberContextOverride(numberContext state.NumberContext) {
	e.numberContextOverride = &numberContext
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
func (e *TestEnv) SetInvariantViolationHook(hook txengine.InvariantViolationHook) {
	e.invariantViolationHook = hook
}

// ReinitializeTxQ replaces the test queue with a fresh queue using the same
// configuration. Callers must use it before submitting transactions.
func (e *TestEnv) ReinitializeTxQ() {
	if e.txQueue != nil {
		queue, err := txq.New(e.txQueue.Config())
		if err != nil {
			e.t.Fatalf("reset transaction queue: %v", err)
		}
		e.txQueue = queue
	}
}

// SetBaseFee changes the base fee for subsequent transactions.
// Used to apply post-initFee() fee changes in conformance tests.
func (e *TestEnv) SetBaseFee(baseFee uint64) {
	if err := e.writeFeeSettings(baseFee, e.reserveBase, e.reserveIncrement); err != nil {
		e.t.Fatalf("set base fee: %v", err)
	}
	e.baseFee = baseFee
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
	if err := e.writeFeeSettings(e.baseFee, reserveBase, reserveIncrement); err != nil {
		e.t.Fatalf("set reserves: %v", err)
	}
	e.reserveBase = reserveBase
	e.reserveIncrement = reserveIncrement
}

// syncFeeSettings writes the env's current fee/reserve values into the ledger's
// FeeSettings entry. rippled changes reserves via a fee vote that rewrites the
// FeeSettings ledger object; the conformance harness shortcuts that vote with
// SetBaseFee/SetReserves, so without this sync the engine (which reads reserves
// from the FeeSettings object, e.g. payment.GetLedgerReserves) would keep seeing
// the stale genesis values and misclassify offers as unfunded.
func (e *TestEnv) writeFeeSettings(baseFee, reserveBase, reserveIncrement uint64) error {
	feesKey := keylet.Fees()
	data, err := e.ledger.Read(feesKey)
	if err != nil {
		return fmt.Errorf("read FeeSettings: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("read FeeSettings: entry is missing")
	}
	fs, err := state.ParseFeeSettings(data)
	if err != nil {
		return fmt.Errorf("parse FeeSettings: %w", err)
	}
	if fs.XRPFeesMode {
		fs.BaseFeeDrops = baseFee
		fs.ReserveBaseDrops = reserveBase
		fs.ReserveIncrementDrops = reserveIncrement
	} else {
		if reserveBase > uint64(^uint32(0)) || reserveIncrement > uint64(^uint32(0)) {
			return fmt.Errorf("legacy FeeSettings reserves exceed uint32")
		}
		fs.BaseFee = baseFee
		fs.ReserveBase = uint32(reserveBase)
		fs.ReserveIncrement = uint32(reserveIncrement)
	}
	newData, err := state.SerializeFeeSettings(fs)
	if err != nil {
		return fmt.Errorf("serialize FeeSettings: %w", err)
	}
	if err := e.ledger.Update(feesKey, newData); err != nil {
		return fmt.Errorf("update FeeSettings: %w", err)
	}
	return nil
}

// SetNextCloseSalt sets the canonical sort salt for the next replay close.
// When set, the closed-ledger build uses this salt instead of computing one from
// the transaction set. Cleared after use.
func (e *TestEnv) SetNextCloseSalt(salt [32]byte) {
	e.nextCloseSalt = &salt
}
