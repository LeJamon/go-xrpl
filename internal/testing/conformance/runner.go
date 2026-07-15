// Package conformance provides a test runner for xrpl-fixtures test vectors.
// It replays rippled test vectors against the go-xrpl transaction engine and
// validates that TER codes and post-state match the reference implementation.
package conformance

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// defaultEnvConfig returns rippled's standard test defaults for fixtures
// that don't specify an env section. Matches rippled's jtx::Env default
// constructor: all supported amendments enabled, standard fees/reserves.
func defaultEnvConfig() EnvConfig {
	feats := amendment.SupportedFeatures()
	names := make([]string, 0, len(feats))
	for _, f := range feats {
		names = append(names, f.Name)
	}
	return EnvConfig{
		BaseFee:           10,
		ReserveBase:       200_000_000,
		ReserveIncrement:  50_000_000,
		AmendmentsEnabled: names,
	}
}

// knownAmendments filters a fixture's captured amendment list to names still
// registered in go-xrpl. Fixtures recorded against an older rippled carry
// amendments that have since been deleted from the protocol (e.g.
// PermissionDelegation / fixDelegateV1_1, replaced by PermissionDelegationV1_1);
// those names are dropped so the vast majority of fixtures that only list them
// incidentally keep running. Fixtures that genuinely depend on a deleted
// amendment are excluded via skipTests.
func knownAmendments(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if amendment.FeatureByName(name) != nil {
			out = append(out, name)
		}
	}
	return out
}

// runner holds the state for executing a single fixture.
type runner struct {
	t        *testing.T
	env      *jtx.TestEnv
	accounts map[string]*jtx.Account // name -> account

	// enableTxQ enables TxQ routing for fee escalation and queuing.
	// Set to true for TxQ test suites (TxQPosNegFlows, TxQMetaInfo).
	enableTxQ bool

	// enableReplay enables open-ledger replay-on-close for this fixture.
	// Only needed for fixtures where canonical replay changes transaction
	// outcomes (e.g., DepositPreauth applied before Payment).
	enableReplay bool

	// txqCfg holds the full per-fixture TxQ configuration overrides.
	// Set from the fixture's testcase name using txqConfigLookup.
	txqCfg txqTestConfig

	// directApplySteps is a set of step indices where tx steps should
	// bypass TxQ and apply directly to the open ledger. This handles
	// rippled tests that use openLedger().modify() to apply transactions
	// without TxQ routing.
	directApplySteps map[int]bool

	// testcase is the fixture's testcase name, used for per-fixture lookups.
	testcase string

	// lastEnvCfg stores the most recent env config for implicit scope resets.
	lastEnvCfg EnvConfig

	// hadTxSteps tracks whether any tx steps have been executed since the
	// last env setup. Used to detect implicit scope boundaries when fund
	// steps re-create already-existing accounts.
	hadTxSteps bool

	// ammAddrMap maps fixture AMM pseudo-account addresses to actual go-xrpl
	// AMM addresses. AMM pseudo-account addresses depend on parentHash, which
	// differs between rippled and go-xrpl. Transactions referencing LP token
	// issuers (AMM accounts) need address remapping to work correctly.
	ammAddrMap map[string]string

	// fixtureAMMAddrs is the set of AMM account addresses found in the fixture
	// by pre-scanning LP token references. Used to detect which addresses need
	// remapping after AMMCreate succeeds.
	fixtureAMMAddrs map[string]bool

	// fixtureAMMPairs stores (issuer, currency) pairs for LP token references
	// found during prescan. This enables precise matching of fixture AMM
	// addresses to specific AMM instances when multiple AMMs exist.
	fixtureAMMPairs []ammPair

	// fixtureUnfundedAddrs is the set of addresses that appear in fixture
	// steps but are NOT in any fund step (and are not special addresses like
	// genesis or ACCOUNT_ZERO). These are candidates for AMM pseudo-account
	// addresses even when they don't appear with LP token currencies.
	fixtureUnfundedAddrs map[string]bool

	// fixtureNonAMMAccountAddrs is the set of unfunded addresses that appear
	// as the Account field of non-AMMCreate transactions. These are user
	// accounts (e.g., used in AMMVote with terNO_ACCOUNT expected), not AMM
	// pseudo-accounts, and should not be remapped to AMM addresses.
	fixtureNonAMMAccountAddrs map[string]bool

	// fixtureSteps stores all fixture steps for use by registerAMMMapping
	// when it needs to scan for unfunded AMM address candidates.
	fixtureSteps []Step

	// timeLeapSteps is a set of step indices where Close() should use
	// a time-leap (consensus delay). This resets TxQ fee metrics back
	// toward the minimum, matching rippled's env.close(env.now() + 5s, 10000ms).
	// These cannot be detected from the fixture data alone.
	timeLeapSteps map[int]bool

	// loadFeeEvents maps a step index to a load-factor change applied
	// before that step runs, modelling rippled's LoadFeeTrack manipulation
	// (setRemoteFee / raiseLocalFee / lowerLocalFee) in TxQ_test.cpp.
	loadFeeEvents map[int]loadFeeEvent

	// pendingHeld stores transactions that returned terPRE_TICKET or
	// terPRE_SEQ. In rippled, these are "held" and retried immediately
	// after each subsequent successful transaction submission.
	pendingHeld []tx.Transaction

	// pendingQueued stores transactions that returned other ter* results
	// (e.g., terNO_RIPPLE). In rippled, these are queued in the TxQ and
	// retried during TxQ::accept() on ledger close.
	pendingQueued []tx.Transaction

	// disabledTxBySeq maps (account address, sequence) to transactions that
	// returned temDISABLED. When the BumpSequenceAndDeductAmount path bumps
	// the sequence past one of these, the stored transaction is resubmitted
	// instead of a plain sequence bump. This matches rippled's behavior where
	// the open ledger retains submitted transactions and re-applies them when
	// the required amendment is later enabled.
	disabledTxBySeq map[string]tx.Transaction // key: "address:seq"

	// initFee stores the post-initFee fee configuration for fixtures that
	// use rippled's initFee() pattern. Applied after the initial close sequence.
	initFee *initFeeConfig

	// feeVote stores the fee-vote configuration for fixtures that use
	// rippled's fee-voting pattern. Applied when the close step's
	// ledger_seq matches the configured flag ledger sequence.
	feeVote *feeVoteConfig

	// feeVoteApplied tracks whether the fee-vote reserve reduction has
	// already been applied in the current scope. Reset on env_reset and
	// scope boundaries.
	feeVoteApplied bool
}

// ammPair associates an LP token issuer with its currency code.
type ammPair struct {
	issuer   string
	currency string
}

// txqTestConfig holds the per-fixture TxQ configuration overrides.
// Fields are pointers so nil means "use default". This matches the
// rippled makeConfig() pattern where each test can override specific
// transaction_queue section values.
type txqTestConfig struct {
	MinTxn                         uint32  // minimum_txn_in_ledger_standalone (always set)
	LedgersInQueue                 *uint32 // nil = use makeConfig default (2)
	QueueSizeMin                   *uint32 // nil = use makeConfig default (2)
	MaximumTxnPerAccount           *uint32 // nil = use rippled default (10)
	NormalConsensusIncreasePercent *uint32 // nil = use makeConfig default (0)
	SlowConsensusDecreasePercent   *uint32 // nil = use rippled default (50)
	TargetTxnInLedger              *uint32 // nil = use rippled default (256)
}

// Helper to create *uint32 from a literal.
func u32(v uint32) *uint32 { return &v }

// txqConfigLookup maps TxQ fixture test case names to their full
// TxQ configuration from rippled TxQ_test.cpp makeConfig() calls.
// Each test's makeConfig overrides are faithfully transcribed here.
var txqConfigLookup = map[string]txqTestConfig{
	// Default makeConfig tests (only minimum_txn_in_ledger_standalone differs)
	"queue sequence":            {MinTxn: 3},
	"queue ticket":              {MinTxn: 3},
	"queue tec":                 {MinTxn: 2},
	"local tx retry":            {MinTxn: 2},
	"last ledger sequence":      {MinTxn: 2},
	"zero transaction fee":      {MinTxn: 2},
	"queued tx fails":           {MinTxn: 2},
	"multi tx per account":      {MinTxn: 3},
	"tie breaking":              {MinTxn: 4},
	"acct tx id":                {MinTxn: 1},
	"maximum tx":                {MinTxn: 2},
	"unexpected balance change": {MinTxn: 3},
	"blockers sequence":         {MinTxn: 3},
	"blockers ticket":           {MinTxn: 3},
	"In-flight balance checks":  {MinTxn: 3},
	"acct in queue but empty":   {MinTxn: 3},
	"Autofilled sequence should account for TxQ": {MinTxn: 6},
	"account info":                                 {MinTxn: 3},
	"server info":                                  {MinTxn: 3},
	"server subscribe":                             {MinTxn: 3},
	"clear queued acct txs":                        {MinTxn: 3},
	"Sequence in queue and open ledger":            {MinTxn: 3},
	"Ticket in queue and open ledger":              {MinTxn: 3},
	"Cancel queued offers":                         {MinTxn: 5},
	"Zero reference fee":                           {MinTxn: 3},
	"consequences":                                 {MinTxn: 2},
	"fail in preclaim":                             {MinTxn: 2},
	"straightfoward positive case":                 {MinTxn: 3},
	"replace middle tx with enough to clear queue": {MinTxn: 3},
	"replace last tx with enough to clear queue":   {MinTxn: 3},
	"clear queue failure (load)":                   {MinTxn: 3},

	// Tests with non-default TxQ config overrides from rippled makeConfig()
	"expiration replacement": {
		MinTxn:               1,
		LedgersInQueue:       u32(10),
		MaximumTxnPerAccount: u32(20),
	},
	"full queue gap handling": {
		MinTxn:               1,
		LedgersInQueue:       u32(10),
		MaximumTxnPerAccount: u32(11),
	},
	"Queue full drop penalty": {
		MinTxn:               5,
		LedgersInQueue:       u32(5),
		QueueSizeMin:         u32(50),
		MaximumTxnPerAccount: u32(30),
	},
	"scaling": {
		MinTxn:                         3,
		MaximumTxnPerAccount:           u32(200),
		NormalConsensusIncreasePercent: u32(25),
		SlowConsensusDecreasePercent:   u32(50),
		TargetTxnInLedger:              u32(10),
	},
	"Re-execute preflight": {
		MinTxn:               1,
		LedgersInQueue:       u32(5),
		MaximumTxnPerAccount: u32(10),
	},
}

// txqDirectApplyLookup maps TxQ fixture test case names to step indices
// where the tx should bypass TxQ and apply directly to the open ledger.
// This handles rippled tests that use openLedger().modify() to apply
// transactions without TxQ routing.
// Reference: TxQ_test.cpp testInLedgerSeq / testInLedgerTicket
var txqDirectApplyLookup = map[string][]int{
	"Sequence in queue and open ledger": {5},
	"Ticket in queue and open ledger":   {5},
}

// openLedgerInject describes a synthetic noop transaction that rippled's TxQ
// tests apply via env.app().openLedger().modify() — a direct write to the open
// ledger (bypassing the queue) that the fixture exporter does not capture.
// Its effect is nonetheless baked into the following step's expected post-state
// (a consumed ticket/sequence and an openLedgerCost fee charge), so the runner
// must replay it to stay in sync.
type openLedgerInject struct {
	beforeStep int
	account    string
	ticketSeq  uint32 // 0 => use the account's current sequence
}

// txqOpenLedgerInjectLookup maps TxQ fixture testcase names to the synthetic
// open-ledger transactions that must be replayed before a given step.
// Reference: TxQ_test.cpp testInLedgerSeq / testInLedgerTicket — the
// env.app().openLedger().modify() calls that apply a noop bypassing the queue.
// In the ticket test the injected tx reuses the queued tx's ticket
// (tktSeq0+1 == 5); in the sequence test it consumes the queued sequence.
var txqOpenLedgerInjectLookup = map[string][]openLedgerInject{
	"Sequence in queue and open ledger": {{beforeStep: 5, account: "alice"}},
	"Ticket in queue and open ledger":   {{beforeStep: 5, account: "alice", ticketSeq: 5}},
}

// txqInitFeeConfig maps TxQ fixture test case names that use initFee()
// to the resulting fee configuration (base, reserve, increment) after
// the fee vote completes. initFee() runs 257 ledger closes to reach the
// flag ledger, executes a fee vote that changes the reserves, then does
// a time-leap close. Since go-xrpl doesn't implement fee voting pseudo-
// transactions, we apply the post-initFee reserves directly after
// processing the initial close sequence.
// The step index is the step AFTER which the reserves should be applied.
type initFeeConfig struct {
	BaseFee          uint64
	ReserveBase      uint64
	ReserveIncrement uint64
	ApplyAfterStep   int // Step index after which to apply the config
}

var txqInitFeeLookup = map[string]initFeeConfig{
	"multi tx per account":      {BaseFee: 10, ReserveBase: 200, ReserveIncrement: 50, ApplyAfterStep: 257},
	"In-flight balance checks":  {BaseFee: 10, ReserveBase: 200, ReserveIncrement: 50, ApplyAfterStep: 257},
	"unexpected balance change": {BaseFee: 10, ReserveBase: 200, ReserveIncrement: 50, ApplyAfterStep: 257},
	// Zero reference fee: the fee vote sets baseFee=0 at the flag ledger
	// (around step 254). Steps 255-256 are tx steps that need baseFee=0
	// to apply correctly. Apply immediately after the last close before
	// the first tx step, not after the time-leap close at step 257.
	"Zero reference fee": {BaseFee: 0, ReserveBase: 0, ReserveIncrement: 0, ApplyAfterStep: 254},
}

// feeVoteConfig describes the post-vote fee configuration for fixtures that
// use rippled's fee-voting pattern (many consecutive ledger closes that
// trigger a fee vote at the flag ledger). Applied when the close step's
// ledger_seq matches FlagLedgerSeq.
type feeVoteConfig struct {
	BaseFee          uint64
	ReserveBase      uint64
	ReserveIncrement uint64
	FlagLedgerSeq    uint32 // The ledger_seq at which fee vote takes effect
}

// feeVoteLookup maps (suite, testcase) keys to fee-vote configurations.
// These fixtures have long runs of consecutive closes that trigger rippled's
// fee-voting mechanism, reducing reserves from genesis values (200 XRP) to
// test config values (200 drops).
var feeVoteLookup = map[string]feeVoteConfig{
	"ripple.app.PayChan/Account Delete":     {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 256},
	"ripple.app.AMM/Auto Delete":            {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 257},
	"ripple.app.AccountDelete/Resurrection": {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 256},
	// These fixtures fund accounts with 10,000 XRP but exceed the object count
	// affordable under the 200/50 XRP reserve the env seeds at genesis; they only
	// pass once the seq-256 fee vote drops the reserve to 10/2.
	"ripple.app.NFTokenBurnBaseUtil/Burn random":          {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 256},
	"ripple.app.NFTokenBurnAllFeatures/Burn random":       {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 256},
	"ripple.app.NFTokenBurnWOfixFungTokens/Burn random":   {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 256},
	"ripple.app.NFTokenBurnWOFixNFTPageLinks/Burn random": {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 256},
	"ripple.app.NFTokenBurnWOFixTokenRemint/Burn random":  {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 256},
	"ripple.app.NFTokenDir/NFToken consecutive packing":   {BaseFee: 10, ReserveBase: 10_000_000, ReserveIncrement: 2_000_000, FlagLedgerSeq: 256},
}

// txqTimeLeapLookup maps TxQ fixture test case names to the step indices
// where Close() should use a time-leap (consensus delay). Time-leap closes
// reset TxQ fee metrics back toward the minimum, matching rippled's
// env.close(env.now() + 5s, 10000ms) in TxQ_test.cpp.
//
// These indices cannot be auto-detected from fixture data because the
// fixture recorder doesn't capture consensus delay information.
// Derived from rippled/src/test/app/TxQ_test.cpp.
var txqTimeLeapLookup = map[string][]int{
	"queue sequence":       {27},
	"last ledger sequence": {2, 5, 8},
	"zero transaction fee": {2, 4},
	"scaling":              {150, 151, 152, 153, 203},
	// initFee pattern: 255 close steps + 2 tx steps + 1 time-leap close at step 257
	"multi tx per account":      {257},
	"In-flight balance checks":  {257},
	"unexpected balance change": {257},
	"Zero reference fee":        {257},
}

// loadFeeEvent describes a mid-test load-factor change applied BEFORE a given
// step executes, modelling rippled's LoadFeeTrack manipulation in TxQ_test.cpp
// (env.app().getFeeTrack().setRemoteFee / raiseLocalFee / lowerLocalFee). The
// load factor scales the open-ledger fee floor in the engine's checkFee, so a
// spike pushes previously-affordable queued transactions below the floor —
// they fail telINSUF_FEE_P on apply/close, matching rippled.
type loadFeeEvent struct {
	RemoteFee  uint32 // SetRemoteFee(RemoteFee) when non-zero (256 = normal)
	RaiseLocal int    // call RaiseLocalFee() this many times when > 0
	Reset      bool   // return the load factor to normal (ResetLoadFee)
}

// txqLoadFeeLookup maps TxQ fixture test case names to load-factor changes
// keyed by the step index they take effect before. These cannot be derived
// from fixture data (the recorder doesn't capture load-track state), so they
// are transcribed from rippled/src/test/app/TxQ_test.cpp.
var txqLoadFeeLookup = map[string]map[int]loadFeeEvent{
	// "clear queue failure (load)": after queuing five txns, the test does
	//   feeTrack.setRemoteFee(origFee * 5)   // "server load went up!"
	// so the totalFee tx (step 19) can no longer clear the queue and is
	// queued instead; the close at step 20 applies only the txns whose fee
	// still clears the 5x floor. Load is restored (setRemoteFee(origFee))
	// after that close, before bob's fill (step 21).
	// Reference: TxQ_test.cpp testClearAccountQueue ~3993-4009.
	"clear queue failure (load)": {
		19: {RemoteFee: 5 * feetrack.LoadBase},
		21: {Reset: true},
	},
	// "Queue full drop penalty": after re-filling the queue the test raises
	// the local fee 30 times to force every queued txn to fail telINSUF_FEE_P
	// on the next close (step 97), then runs the fee back down before bob
	// re-fills the ledger (step 98).
	// Reference: TxQ_test.cpp testQueueFullDropPenalty 4673-4685.
	"Queue full drop penalty": {
		97: {RaiseLocal: 30},
		98: {Reset: true},
	},
}

// RunFixture loads and executes a single fixture file.
// disabledRetiredAmendments returns the retired amendments absent from a
// fixture's enabled set, in name order. A non-empty result means the fixture
// records a retired amendment as disabled and can no longer be reproduced.
func disabledRetiredAmendments(enabled []string) []string {
	on := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		on[n] = true
	}
	var missing []string
	for _, f := range amendment.AllFeatures() {
		if f.Retired && !on[f.Name] {
			missing = append(missing, f.Name)
		}
	}
	sort.Strings(missing)
	return missing
}

// fixtureDisablesRetiredAmendments returns the retired amendments that any of a
// fixture's env scopes — the top-level env or any mid-fixture env_reset — record
// as disabled, in name order. Fixtures split their scenarios across scopes: a
// scope that disables a retired amendment exercises pre-retirement behaviour
// that no longer exists, so the whole fixture cannot be reproduced end-to-end.
func fixtureDisablesRetiredAmendments(fixture *Fixture) []string {
	missing := make(map[string]bool)
	collect := func(env *EnvConfig) {
		if env == nil || len(env.AmendmentsEnabled) == 0 {
			return
		}
		for _, name := range disabledRetiredAmendments(env.AmendmentsEnabled) {
			missing[name] = true
		}
	}
	collect(fixture.Env)
	for i := range fixture.Steps {
		collect(fixture.Steps[i].Env)
	}
	if len(missing) == 0 {
		return nil
	}
	out := make([]string, 0, len(missing))
	for name := range missing {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func RunFixture(t *testing.T, fixturePath string) {
	t.Helper()

	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("Failed to read fixture %s: %v", fixturePath, err)
	}

	var fixture Fixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("Failed to parse fixture %s: %v", fixturePath, err)
	}

	// A fixture that records a retired amendment as disabled — in its top-level
	// env or in any mid-fixture env_reset scope — tests a protocol configuration
	// that no longer exists. Retired amendments are permanently enabled, so the
	// engine forces them on and the recording cannot be reproduced. Mirrors
	// rippled 3.2.0 deleting these FeatureBitset variations; the fixtures should
	// be re-recorded from a 3.2.0 corpus.
	if missing := fixtureDisablesRetiredAmendments(&fixture); len(missing) > 0 {
		t.Skipf("Skipped: fixture disables retired amendment(s) %s — unreachable post-3.2.0 retirement; re-record from rippled 3.2.0", strings.Join(missing, ", "))
	}

	// Detect TxQ suites by fixture path.
	// Transaction_ordering also needs the real TxQ for fee escalation
	// to correctly limit how many queued transactions get applied.
	isTxQSuite := strings.Contains(fixturePath, "/TxQ") ||
		strings.Contains(fixturePath, "/Transaction_ordering")

	// Look up per-fixture TxQ configuration
	var txqCfg txqTestConfig
	if isTxQSuite {
		if cfg, ok := txqConfigLookup[fixture.Testcase]; ok {
			txqCfg = cfg
		} else {
			txqCfg = txqTestConfig{MinTxn: 3} // default fallback
		}
	}

	fixtureAddrs, fixturePairs, unfundedAddrs, nonAMMAcctAddrs := prescanAMMAddresses(fixture.Steps)

	// Build time-leap step index set from the lookup table
	timeLeapSet := make(map[int]bool)
	if isTxQSuite {
		if indices, ok := txqTimeLeapLookup[fixture.Testcase]; ok {
			for _, idx := range indices {
				timeLeapSet[idx] = true
			}
		}
	}

	// Build direct-apply step index set for tests that use
	// openLedger().modify() to bypass TxQ
	directApplySet := make(map[int]bool)
	if isTxQSuite {
		if indices, ok := txqDirectApplyLookup[fixture.Testcase]; ok {
			for _, idx := range indices {
				directApplySet[idx] = true
			}
		}
	}

	// Look up initFee config for this fixture
	var initFee *initFeeConfig
	if isTxQSuite {
		if cfg, ok := txqInitFeeLookup[fixture.Testcase]; ok {
			initFee = &cfg
		}
	}

	// Look up mid-test load-factor events for this fixture (rippled's
	// LoadFeeTrack setRemoteFee / raiseLocalFee usage).
	var loadFeeEvents map[int]loadFeeEvent
	if isTxQSuite {
		loadFeeEvents = txqLoadFeeLookup[fixture.Testcase]
	}

	// Look up fee-vote config for this fixture
	var feeVote *feeVoteConfig
	feeVoteKey := fixture.Suite + "/" + fixture.Testcase
	if cfg, ok := feeVoteLookup[feeVoteKey]; ok {
		feeVote = &cfg
	}

	r := &runner{
		t:                         t,
		accounts:                  make(map[string]*jtx.Account),
		enableTxQ:                 isTxQSuite,
		enableReplay:              !isTxQSuite, // TxQ suites need direct close for correct fee metrics
		txqCfg:                    txqCfg,
		directApplySteps:          directApplySet,
		testcase:                  fixture.Testcase,
		ammAddrMap:                make(map[string]string),
		fixtureAMMAddrs:           fixtureAddrs,
		fixtureAMMPairs:           fixturePairs,
		fixtureUnfundedAddrs:      unfundedAddrs,
		fixtureNonAMMAccountAddrs: nonAMMAcctAddrs,
		fixtureSteps:              fixture.Steps,
		timeLeapSteps:             timeLeapSet,
		loadFeeEvents:             loadFeeEvents,
		initFee:                   initFee,
		feeVote:                   feeVote,
	}

	// If this fixture depends on a predecessor, build the dependency chain
	// using the depends_on field and replay predecessors first.
	if fixture.DependsOn != "" {
		chain := loadDependsOnChain(t, fixturePath, fixture.DependsOn)
		if len(chain) > 0 {
			envCfg := chain[0].Env
			if envCfg == nil {
				cfg := defaultEnvConfig()
				envCfg = &cfg
			}
			r.setupEnv(*envCfg)
			for _, prereq := range chain {
				r.replaySteps(prereq.Steps, prereq.DependsOn != "")
			}
		} else {
			// Chain broken — fall back to defaults
			cfg := defaultEnvConfig()
			r.setupEnv(cfg)
			if r.shouldAutoFund(fixture.Steps) {
				r.autoFundAccounts(fixture.Steps)
			}
		}
	} else {
		// Normal fixture: set up env and optionally auto-fund
		envCfg := fixture.Env
		if envCfg == nil {
			cfg := defaultEnvConfig()
			envCfg = &cfg
		}
		r.setupEnv(*envCfg)

		if r.shouldAutoFund(fixture.Steps) {
			r.autoFundAccounts(fixture.Steps)
		}
	}

	// Execute steps sequentially
	for i := 0; i < len(fixture.Steps); i++ {
		r.injectOpenLedgerTxs(i)

		step := fixture.Steps[i]
		r.applyLoadFeeEvent(i)
		switch step.Op {
		case "fund":
			// Detect implicit scope boundary: when fund steps re-create
			// accounts that already exist in the LEDGER AND tx steps have
			// been executed, this indicates a new test scope in the original
			// rippled test that was captured without an explicit env_reset.
			// We check the ledger (not just the accounts map) to avoid
			// false positives when accounts were legitimately deleted
			// (e.g., AccountDelete followed by re-fund).
			if r.hadTxSteps && step.Address != "" {
				if acc, exists := r.accounts[step.Account]; exists {
					if r.env.Exists(acc) {
						r.accounts = make(map[string]*jtx.Account)
						r.ammAddrMap = make(map[string]string)
						r.feeVoteApplied = false
						r.pendingHeld = nil
						r.pendingQueued = nil
						r.disabledTxBySeq = nil
						r.setupEnv(r.lastEnvCfg)
					}
				}
			}
			r.execFund(i, step)
		case "trust":
			r.execTrust(i, step)
		case "close":
			r.execClose(i, step)
		case "tx":
			r.hadTxSteps = true
			r.execTx(i, step)
		case "retry":
			// Collect all consecutive retry steps into a batch.
			// These represent queued TxQ transactions that were applied
			// atomically during the preceding close. Apply them all first,
			// then check the post_state of the last one.
			retryBatch := []struct {
				idx  int
				step Step
			}{{idx: i, step: step}}
			for i+1 < len(fixture.Steps) && fixture.Steps[i+1].Op == "retry" {
				i++
				retryBatch = append(retryBatch, struct {
					idx  int
					step Step
				}{idx: i, step: fixture.Steps[i]})
			}
			r.execRetryBatch(retryBatch)
		case "env_reset":
			r.execEnvReset(i, step)
		case "enable_amendment":
			if amendment.FeatureByName(step.Amendment) != nil {
				r.env.EnableFeatureNow(step.Amendment)
			}
		case "modify_state":
			r.execModifyState(i, step)
		default:
			t.Fatalf("Step %d: unknown op %q", i, step.Op)
		}
	}
}

// loadDependsOnChain follows depends_on links backwards to build the full
// prerequisite chain. Returns fixtures in order from root to immediate parent.
func loadDependsOnChain(t *testing.T, fixturePath string, firstDep string) []Fixture {
	t.Helper()
	dir := filepath.Dir(fixturePath)

	var chain []Fixture
	dep := firstDep
	seen := make(map[string]bool) // cycle protection

	for dep != "" {
		if seen[dep] {
			t.Logf("depends_on cycle detected at %q", dep)
			break
		}
		seen[dep] = true

		depPath := filepath.Join(dir, dep+".json")
		data, err := os.ReadFile(depPath)
		if err != nil {
			t.Logf("depends_on: cannot read %s: %v", depPath, err)
			return nil
		}
		var f Fixture
		if err := json.Unmarshal(data, &f); err != nil {
			t.Logf("depends_on: cannot parse %s: %v", depPath, err)
			return nil
		}

		chain = append([]Fixture{f}, chain...) // prepend
		dep = f.DependsOn                      // follow the chain
	}

	return chain
}

// replaySteps executes fixture steps silently (without asserting TER codes
// or post-state). This is used to establish prerequisite ledger state for
// continuation fixtures.
func (r *runner) replaySteps(steps []Step, isContinuation bool) {
	// Determine the start index for replay.
	//
	// For root fixtures (isContinuation=false): skip steps from a prior
	// env scope. When a fixture has tx/close steps followed by fund steps,
	// the tx/close steps are remnants from the old scope. The fund steps
	// mark the beginning of the current scope.
	//
	// For continuation fixtures (isContinuation=true, has depends_on):
	// the tx steps at the beginning ARE the current scope's content —
	// they extend the predecessor's state. Replay from the beginning.
	// Trailing fund steps may set up the next scope (env reset + re-fund),
	// which is fine — the next fixture in the chain expects that state.
	startIdx := 0
	if !isContinuation {
		startIdx = findScopeBoundary(steps)
	}

	hadReplayTx := false
	for i := startIdx; i < len(steps); i++ {
		step := steps[i]

		// For continuation fixtures, detect scope boundary: when fund
		// steps re-create already-existing accounts after tx steps,
		// reset the env (matching the main execution loop behavior).
		if isContinuation && step.Op == "fund" && hadReplayTx && step.Address != "" {
			// Check by account name first, then by address
			var foundAcc *jtx.Account
			if acc, exists := r.accounts[step.Account]; exists {
				foundAcc = acc
			} else {
				// Also check by address (accounts may be registered by address)
				for _, acc := range r.accounts {
					if acc.Address == step.Address {
						foundAcc = acc
						break
					}
				}
			}
			if foundAcc != nil && r.env.Exists(foundAcc) {
				r.accounts = make(map[string]*jtx.Account)
				r.ammAddrMap = make(map[string]string)
				r.setupEnv(r.lastEnvCfg)
				hadReplayTx = false
			}
		}

		switch step.Op {
		case "fund":
			r.execFund(i, step)
		case "trust":
			r.execTrust(i, step)
		case "close":
			r.execClose(i, step)
		case "tx":
			hadReplayTx = true
			r.replayTx(step)
		case "retry":
			// Retry ops are post-close observations of queued txns.
			// During replay, the txns were already applied by Close().
			// Nothing to do here.
		case "enable_amendment":
			if amendment.FeatureByName(step.Amendment) != nil {
				r.env.EnableFeatureNow(step.Amendment)
			}
		case "modify_state":
			r.execModifyState(i, step)
		case "env_reset":
			r.execEnvReset(i, step)
		}
	}
}

// findScopeBoundary returns the index of the first fund or env_reset step,
// which marks the beginning of the current env scope. Steps before this
// index are remnants from a prior rippled env scope and should be skipped
// during replay.
//
// If there are no fund/env_reset steps, or the first such step is at
// index 0, returns 0 (no skipping needed).
//
// Only skips when there are tx/close steps before the first fund — if the
// fixture starts with fund steps, there's no prior scope to skip.
func findScopeBoundary(steps []Step) int {
	firstFund := -1
	for i, s := range steps {
		if s.Op == "fund" || s.Op == "env_reset" {
			firstFund = i
			break
		}
	}
	if firstFund <= 0 {
		return 0
	}

	// Check if there are tx or close steps before the first fund.
	// If so, those are from the prior scope.
	hasPriorScope := false
	for _, s := range steps[:firstFund] {
		if s.Op == "tx" || s.Op == "close" {
			hasPriorScope = true
			break
		}
	}
	if !hasPriorScope {
		return 0
	}
	return firstFund
}

// replayTx submits a transaction silently without asserting TER codes.
// Used for replaying prerequisite fixture steps.
func (r *runner) replayTx(step Step) {
	blob, err := hex.DecodeString(step.TxBlob)
	if err != nil || len(blob) == 0 {
		return
	}
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		return
	}
	r.remapAMMAddresses(parsed)
	result := r.env.Submit(parsed)

	// Register AMM mapping after successful AMMCreate
	if result.Success && step.TxJSON != nil {
		var txj map[string]any
		if json.Unmarshal(step.TxJSON, &txj) == nil {
			if txj["TransactionType"] == "AMMCreate" {
				r.registerAMMMapping(step)
			}
		}
	}
}
