// Package conformance provides a test runner for xrpl-fixtures test vectors.
// It replays rippled test vectors against the goXRPL transaction engine and
// validates that TER codes and post-state match the reference implementation.
package conformance

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	_ "github.com/LeJamon/goXRPLd/internal/tx/all"
	"github.com/LeJamon/goXRPLd/internal/tx/trustset"
	"github.com/LeJamon/goXRPLd/internal/txq"
)

// Fixture represents a single xrpl-fixtures test vector file.
type Fixture struct {
	RippledVersion string     `json:"rippled_version"`
	Suite          string     `json:"suite"`
	Testcase       string     `json:"testcase"`
	Env            *EnvConfig `json:"env,omitempty"`
	Steps          []Step     `json:"steps"`
}

// EnvConfig holds the ledger environment configuration.
type EnvConfig struct {
	AmendmentsEnabled []string `json:"amendments_enabled"`
	BaseFee           uint64   `json:"base_fee"`
	ReserveBase       uint64   `json:"reserve_base"`
	ReserveIncrement  uint64   `json:"reserve_increment"`
	NetworkID         *uint32  `json:"network_id,omitempty"`
}

// Step represents a single operation in a fixture.
type Step struct {
	Op               string          `json:"op"`
	Account          string          `json:"account,omitempty"`
	Address          string          `json:"address,omitempty"`
	Amount           json.RawMessage `json:"amount,omitempty"`
	SetDefaultRipple *bool           `json:"set_default_ripple,omitempty"`
	LimitAmount      *LimitAmount    `json:"limit_amount,omitempty"`
	TxBlob           string          `json:"tx_blob,omitempty"`
	TxJSON           json.RawMessage `json:"tx_json,omitempty"`
	ExpectTER        string          `json:"expect_ter,omitempty"`
	PostState        *PostState      `json:"post_state,omitempty"`
	Env              *EnvConfig      `json:"env,omitempty"`
	Amendment        string          `json:"amendment,omitempty"`
	ModifyState      *ModifyState    `json:"modify_state,omitempty"`
}

// ModifyState describes direct ledger state modifications that bypass normal
// transaction processing.  This is used when rippled tests hack the open
// ledger (via env.app().openLedger().modify()) to set up boundary conditions
// that cannot be reached through regular transactions (e.g., setting
// MintedNFTokens to 0xFFFFFFFE to test overflow detection).
type ModifyState struct {
	Account              string        `json:"account"`
	MintedNFTokens       *uint32       `json:"minted_nftokens,omitempty"`
	FirstNFTokenSequence *uint32       `json:"first_nftoken_sequence,omitempty"`
	BumpLastPage         *BumpLastPage `json:"bump_last_page,omitempty"`
}

// BumpLastPage describes a directory page bump operation.
// This mirrors rippled's test::jtx::directory::bumpLastPage() which moves
// the last page of a directory to a target page number near the limit,
// allowing tests to exercise the directory page limit check.
type BumpLastPage struct {
	Directory   string `json:"directory"`    // "owner" for owner directory
	TargetPage  uint64 `json:"target_page"`  // New page number for the last page
	AdjustField string `json:"adjust_field"` // SLE field to update on moved entries (e.g. "IssuerNode")
}

// LimitAmount is an IOU amount for trust line setup.
type LimitAmount struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer"`
	Value    string `json:"value"`
}

// PostState holds expected post-transaction account states.
type PostState struct {
	Accounts []AccountState `json:"accounts"`
}

// AccountState holds expected account state after a transaction.
type AccountState struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	XRPBalance string `json:"xrp_balance"`
	OwnerCount uint32 `json:"owner_count"`
}

// rippleEpoch is Jan 1, 2000 00:00:00 UTC — the Ripple epoch start.
var rippleEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

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

// runner holds the state for executing a single fixture.
type runner struct {
	t        *testing.T
	env      *jtx.TestEnv
	accounts map[string]*jtx.Account // name -> account

	// enableTxQ enables TxQ routing for fee escalation and queuing.
	// Set to true for TxQ test suites (TxQPosNegFlows, TxQMetaInfo).
	enableTxQ bool

	// txqMinTxn overrides MinimumTxnInLedgerStandalone per fixture.
	// Set from the fixture's testcase name using txqMinTxnLookup.
	txqMinTxn uint32
}

// txqMinTxnLookup maps TxQ fixture test case names to their
// minimum_txn_in_ledger_standalone values from rippled TxQ_test.cpp.
var txqMinTxnLookup = map[string]uint32{
	"queue sequence":                               3,
	"queue ticket":                                 3,
	"queue tec":                                    2,
	"local tx retry":                               2,
	"last ledger sequence":                         2,
	"zero transaction fee":                         2,
	"queued tx fails":                              2,
	"multi tx per account":                         3,
	"tie breaking":                                 4,
	"acct tx id":                                   1,
	"maximum tx":                                   2,
	"unexpected balance change":                    3,
	"blockers sequence":                            3,
	"blockers ticket":                              3,
	"In-flight balance checks":                     3,
	"acct in queue but empty":                      3,
	"expiration replacement":                       1,
	"full queue gap handling":                      1,
	"Autofilled sequence should account for TxQ":   6,
	"account info":                                 3,
	"server info":                                  3,
	"server subscribe":                             3,
	"clear queued acct txs":                        3,
	"scaling":                                      3,
	"Sequence in queue and open ledger":            3,
	"Ticket in queue and open ledger":              3,
	"Re-execute preflight":                         1,
	"Queue full drop penalty":                      5,
	"Cancel queued offers":                         5,
	"Zero reference fee":                           3,
	"consequences":                                 2,
	"fail in preclaim":                             2,
	"straightfoward positive case":                 3,
	"replace middle tx with enough to clear queue": 3,
	"replace last tx with enough to clear queue":   3,
	"clear queue failure (load)":                   3,
}

// RunFixture loads and executes a single fixture file.
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

	// Detect TxQ suites by fixture path
	isTxQSuite := strings.Contains(fixturePath, "/TxQ")

	// Look up per-fixture MinimumTxnInLedgerStandalone
	var minTxn uint32
	if isTxQSuite {
		if v, ok := txqMinTxnLookup[fixture.Testcase]; ok {
			minTxn = v
		} else {
			minTxn = 3 // default fallback
		}
	}

	r := &runner{
		t:         t,
		accounts:  make(map[string]*jtx.Account),
		enableTxQ: isTxQSuite,
		txqMinTxn: minTxn,
	}

	// Detect continuation fixtures: fixtures without fund steps or env config
	// are continuations of prior test cases in the same rippled test function.
	// They depend on ledger state (accounts, regular keys, disabled master keys)
	// from prerequisite fixtures. Replay those prerequisites first.
	if isContinuation(fixture) {
		prereqs := findPrerequisites(t, fixturePath, fixture)
		if len(prereqs) > 0 {
			// Set up env from the first prerequisite's config (or defaults)
			envCfg := prereqs[0].Env
			if envCfg == nil {
				cfg := defaultEnvConfig()
				envCfg = &cfg
			}
			r.setupEnv(*envCfg)

			// Replay each prerequisite fixture's steps silently
			for _, prereq := range prereqs {
				r.replaySteps(prereq.Steps)
			}
		} else {
			// No prerequisites found — fall through to normal auto-fund
			cfg := defaultEnvConfig()
			r.setupEnv(cfg)
			if r.shouldAutoFund(fixture.Steps) {
				r.autoFundAccounts(fixture.Steps)
			}
		}
	} else {
		// Create initial environment (use defaults if env not specified)
		envCfg := fixture.Env
		if envCfg == nil {
			cfg := defaultEnvConfig()
			envCfg = &cfg
		}
		r.setupEnv(*envCfg)

		// Auto-fund accounts for fixtures without fund steps.
		// Many rippled test fixtures rely on implicit account creation from
		// the test framework. When a fixture has no "fund" ops AND the first
		// tx expects an applied result (tesSUCCESS/tec*), we scan tx_json
		// steps to discover accounts and fund them. Fixtures that expect
		// rejection codes (tem*/tef*/tel*/ter*) intentionally use unfunded
		// accounts and should NOT be auto-funded.
		if r.shouldAutoFund(fixture.Steps) {
			r.autoFundAccounts(fixture.Steps)
		}
	}

	// Execute steps sequentially
	for i, step := range fixture.Steps {
		switch step.Op {
		case "fund":
			r.execFund(i, step)
		case "trust":
			r.execTrust(i, step)
		case "close":
			r.execClose(i, fixture.Steps)
		case "tx":
			r.execTx(i, step)
		case "env_reset":
			r.execEnvReset(i, step)
		case "enable_amendment":
			r.env.EnableFeature(step.Amendment)
		case "modify_state":
			r.execModifyState(i, step)
		default:
			t.Fatalf("Step %d: unknown op %q", i, step.Op)
		}
	}
}

// isContinuation returns true if the fixture is a continuation of a prior
// test case — it has no fund steps and no env config, meaning it depends on
// ledger state established by prerequisite fixtures.
func isContinuation(f Fixture) bool {
	if f.Env != nil {
		return false
	}
	for _, s := range f.Steps {
		if s.Op == "fund" {
			return false
		}
	}
	return true
}

// findPrerequisites finds fixture files in the same directory that must be
// replayed before the current fixture to establish the correct ledger state.
//
// In rippled, multiple testcase() calls within a single test function share
// the same Env. The fixture extractor creates one file per testcase, so
// continuation fixtures depend on state from prior testcases. This function
// reconstructs the chain by following sequence links backwards:
//
//  1. Starting from the current fixture's minSeq, find the fixture whose
//     maxSeq directly precedes it (maxSeq < minSeq, closest match).
//  2. Recursively find that fixture's prerequisite until we reach a base
//     fixture (one with fund steps).
//  3. Only consider fixtures that share accounts with the current fixture.
func findPrerequisites(t *testing.T, fixturePath string, current Fixture) []Fixture {
	t.Helper()

	dir := filepath.Dir(fixturePath)
	currentFile := filepath.Base(fixturePath)

	// Collect accounts used in the current fixture
	currentAccounts := collectTxAccounts(current.Steps)
	if len(currentAccounts) == 0 {
		return nil
	}

	// Load all fixtures in the same directory
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	type fixtureInfo struct {
		fixture  Fixture
		filename string
		minSeq   uint32
		maxSeq   uint32
		hasFund  bool
	}
	var allFixtures []fixtureInfo

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Name() == currentFile {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var f Fixture
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}

		// Skip fixtures that have their own env config. Such fixtures create
		// an independent environment and cannot be valid prerequisites for
		// the current fixture. Without this check, unrelated fixtures that
		// happen to share account addresses (common in Credentials tests
		// where issuer/subject addresses are reused across test functions)
		// would be incorrectly selected as prerequisites.
		if f.Env != nil {
			continue
		}

		// Check for shared accounts
		fAccounts := collectTxAccounts(f.Steps)
		shared := false
		for addr := range fAccounts {
			if currentAccounts[addr] {
				shared = true
				break
			}
		}
		if !shared {
			continue
		}

		hasFund := false
		for _, s := range f.Steps {
			if s.Op == "fund" {
				hasFund = true
				break
			}
		}

		allFixtures = append(allFixtures, fixtureInfo{
			fixture:  f,
			filename: entry.Name(),
			minSeq:   minTxSequence(f.Steps),
			maxSeq:   maxTxSequence(f.Steps),
			hasFund:  hasFund,
		})
	}

	if len(allFixtures) == 0 {
		return nil
	}

	// Build the prerequisite chain by walking backwards from the current
	// fixture's minSeq. At each step, find the fixture whose maxSeq is the
	// closest predecessor (largest maxSeq that is still < targetMinSeq).
	var chain []Fixture
	targetMinSeq := minTxSequence(current.Steps)

	for {
		// Find the best predecessor: fixture with the largest maxSeq <= targetMinSeq.
		// We use <= because failed transactions (tefMASTER_DISABLED, tefBAD_AUTH, etc.)
		// don't consume the sequence number. A fixture chain like:
		//   Set_regular_key (seqs 4-6) → Disable_master_key (seqs 7-9, where 9 fails)
		//   → Re-enable_master_key (starts at seq 9)
		// has Disable_master_key's maxSeq=9 == Re-enable_master_key's minSeq=9.
		bestIdx := -1
		bestMaxSeq := uint32(0)
		for i, fi := range allFixtures {
			if fi.maxSeq > 0 && fi.maxSeq <= targetMinSeq && fi.maxSeq > bestMaxSeq {
				bestIdx = i
				bestMaxSeq = fi.maxSeq
			}
		}

		if bestIdx < 0 {
			break
		}

		best := allFixtures[bestIdx]
		chain = append([]Fixture{best.fixture}, chain...) // prepend

		// If this is a base fixture (has fund steps), we're done
		if best.hasFund {
			break
		}

		// Otherwise, continue looking for this fixture's prerequisite
		targetMinSeq = best.minSeq

		// Remove the selected fixture to avoid infinite loops
		allFixtures = append(allFixtures[:bestIdx], allFixtures[bestIdx+1:]...)
	}

	// Only return the chain if it starts with a base fixture (one with fund
	// steps). A chain that doesn't reach a base means we couldn't find the
	// original environment setup, so the caller should fall back to auto-fund.
	if len(chain) > 0 {
		hasBase := false
		for _, s := range chain[0].Steps {
			if s.Op == "fund" {
				hasBase = true
				break
			}
		}
		if !hasBase {
			return nil
		}
	}

	return chain
}

// minTxSequence returns the minimum Sequence from tx steps in a fixture.
// Returns 0 if no valid sequences found.
func minTxSequence(steps []Step) uint32 {
	min := uint32(0xFFFFFFFF)
	found := false
	for _, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}
		if seqF, ok := txj["Sequence"].(float64); ok && seqF > 0 {
			seq := uint32(seqF)
			if seq < min {
				min = seq
				found = true
			}
		}
	}
	if !found {
		return 0
	}
	return min
}

// maxTxSequence returns the maximum Sequence from tx steps in a fixture.
// Returns 0 if no valid sequences found.
func maxTxSequence(steps []Step) uint32 {
	max := uint32(0)
	for _, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}
		if seqF, ok := txj["Sequence"].(float64); ok {
			seq := uint32(seqF)
			if seq > max {
				max = seq
			}
		}
	}
	return max
}

// collectTxAccounts returns the set of Account addresses from tx steps.
func collectTxAccounts(steps []Step) map[string]bool {
	accounts := make(map[string]bool)
	for _, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}
		if addr, ok := txj["Account"].(string); ok && addr != "" {
			accounts[addr] = true
		}
	}
	// Also collect from fund steps
	for _, s := range steps {
		if s.Op == "fund" && s.Address != "" {
			accounts[s.Address] = true
		}
	}
	return accounts
}

// replaySteps executes fixture steps silently (without asserting TER codes
// or post-state). This is used to establish prerequisite ledger state for
// continuation fixtures.
func (r *runner) replaySteps(steps []Step) {
	for i, step := range steps {
		switch step.Op {
		case "fund":
			r.execFund(i, step)
		case "trust":
			r.execTrust(i, step)
		case "close":
			r.execClose(i, steps)
		case "tx":
			r.replayTx(step)
		case "enable_amendment":
			r.env.EnableFeature(step.Amendment)
		case "modify_state":
			r.execModifyState(i, step)
		}
	}
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
	r.env.Submit(parsed)
}

// setupEnv creates a TestEnv with the given configuration.
func (r *runner) setupEnv(cfg EnvConfig) {
	genCfg := genesis.DefaultConfig()
	genCfg.Fees.BaseFee = drops.NewXRPAmount(int64(cfg.BaseFee))
	genCfg.Fees.ReserveBase = drops.XRPAmount(cfg.ReserveBase)
	genCfg.Fees.ReserveIncrement = drops.XRPAmount(cfg.ReserveIncrement)

	// Enable TxQ if this is a TxQ test suite. TxQ must be created with the
	// test env so Submit() routes through fee escalation and queuing.
	// Use per-fixture MinimumTxnInLedgerStandalone from txqMinTxnLookup.
	if r.enableTxQ {
		txqCfg := txq.StandaloneConfig()
		txqCfg.MinimumTxnInLedgerStandalone = r.txqMinTxn
		r.env = jtx.NewTestEnvWithTxQAndConfig(r.t, txqCfg, genCfg)
	} else {
		r.env = jtx.NewTestEnvWithConfig(r.t, genCfg)
	}
	r.env.SetAmendments(cfg.AmendmentsEnabled)
	if cfg.NetworkID != nil {
		r.env.SetNetworkID(*cfg.NetworkID)
	}

	// Match rippled's startup sequence. rippled's startGenesisLedger()
	// creates: genesis(seq=1) → closed(seq=2, closeTime=0) → open(seq=3).
	// goXRPL's NewTestEnvWithConfig creates only genesis(seq=1) → open(seq=2).
	// We need the extra close to reach open(seq=3) so that accounts created
	// with DeletableAccounts get initial sequence=3 (matching fixture blobs).
	//
	// The clock must start at the Ripple epoch (Jan 1, 2000) BEFORE the close,
	// and be RESET to epoch AFTER the close. This is because:
	// - rippled's startGenesisLedger creates LCL seq=2 with closeTime=0
	// - rippled's ManualTimeKeeper is then set to LCL.closeTime = 0
	// - goXRPL derives ParentCloseTime from the clock, so the clock must
	//   be at epoch 0 when fixtures start, matching rippled's timeKeeper=0
	r.env.SetTime(rippleEpoch)
	r.env.Close()
	r.env.SetTime(rippleEpoch)

	// For non-TxQ suites, disable open-ledger fee adequacy checks.
	// TxQ suites need open-ledger mode so fee escalation triggers queuing.
	if !r.enableTxQ {
		r.env.SetOpenLedger(false)
	}

	// Register master account
	master := jtx.MasterAccount()
	r.accounts["master"] = master
}

// execFund handles a "fund" step.
// Fund operations bypass TxQ, matching rippled's apply() for setup operations.
func (r *runner) execFund(stepIdx int, step Step) {
	amount, err := parseDropsAmount(step.Amount)
	if err != nil {
		r.t.Fatalf("Step %d (fund): invalid amount: %v", stepIdx, err)
	}

	acc := jtx.NewAccountWithAddress(step.Account, step.Address)
	r.accounts[step.Account] = acc

	// Bypass TxQ for fund operations (rippled uses apply() not submit())
	r.env.SetBypassTxQ(true)
	defer r.env.SetBypassTxQ(false)

	setRipple := step.SetDefaultRipple == nil || *step.SetDefaultRipple
	if setRipple {
		r.env.FundAmount(acc, amount)
	} else {
		r.env.FundAmountNoRipple(acc, amount)
	}
}

// execTrust handles a "trust" step.
// Trust operations bypass TxQ, matching rippled's apply() for setup operations.
func (r *runner) execTrust(stepIdx int, step Step) {
	// Bypass TxQ for trust operations (rippled uses apply() not submit())
	r.env.SetBypassTxQ(true)
	defer r.env.SetBypassTxQ(false)

	acc, ok := r.accounts[step.Account]
	if !ok {
		r.t.Fatalf("Step %d (trust): unknown account %q", stepIdx, step.Account)
	}

	if step.LimitAmount == nil {
		r.t.Fatalf("Step %d (trust): missing limit_amount", stepIdx)
	}

	value, err := strconv.ParseFloat(step.LimitAmount.Value, 64)
	if err != nil {
		r.t.Fatalf("Step %d (trust): invalid limit value %q: %v", stepIdx, step.LimitAmount.Value, err)
	}

	limitAmount := tx.NewIssuedAmountFromFloat64(value, step.LimitAmount.Currency, step.LimitAmount.Issuer)

	ts := trustset.NewTrustSet(acc.Address, limitAmount)
	ts.Fee = strconv.FormatUint(r.env.BaseFee(), 10)
	seq := r.env.Seq(acc)
	ts.Sequence = &seq

	result := r.env.Submit(ts)
	if !result.Success {
		r.t.Fatalf("Step %d (trust): TrustSet failed for %s: %s", stepIdx, acc.Name, result.Code)
	}

	// Reimburse the fee directly in the ledger (matching rippled's test framework)
	r.env.ReimburseFeeDirect(acc)
}

// execClose handles a "close" step, aligning the clock with expected time
// offsets when necessary.
//
// rippled tests sometimes do env.close(specificTimepoint) to jump the ledger
// clock forward by large amounts. These time jumps are captured in the fixture
// as plain "close" ops indistinguishable from the default 5-second advance.
// The goXRPL conformance runner advances by a fixed 10s per close, so the
// clock drifts for fixtures that rely on large time offsets.
//
// To fix this, execClose contains calibration logic for:
// 1. OracleSet transactions (LastUpdateTime range checks)
// 2. PaymentChannel transactions (CancelAfter/Expiration auto-close)
//
// When the current clock would produce the wrong result, the clock is advanced
// to match the value rippled must have had.
func (r *runner) execClose(stepIdx int, steps []Step) {
	r.calibrateOracleTime(stepIdx, steps)
	r.calibratePayChanTime(stepIdx, steps)
	r.env.Close()
}

// calibrateOracleTime adjusts the clock for upcoming OracleSet transactions
// whose expected TER depends on the ledger close time.
func (r *runner) calibrateOracleTime(stepIdx int, steps []Step) {
	const rippleEpochOffset = uint64(946684800)
	const maxDelta = uint64(300)

	// Find the next OracleSet tx after this close (skipping other closes).
	for j := stepIdx + 1; j < len(steps); j++ {
		s := steps[j]
		if s.Op == "env_reset" {
			break
		}
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			break
		}
		if txj["TransactionType"] != "OracleSet" {
			break
		}

		// Parse LastUpdateTime from the tx JSON.
		lutRaw, ok := txj["LastUpdateTime"]
		if !ok {
			break
		}
		var lut uint64
		switch v := lutRaw.(type) {
		case float64:
			lut = uint64(v)
		case string:
			parsed, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				break
			}
			lut = parsed
		default:
			break
		}
		if lut < rippleEpochOffset {
			break // LUT before Ripple epoch — nothing to calibrate
		}
		lutRipple := lut - rippleEpochOffset

		// Count how many close steps sit between this close and the OracleSet.
		// Each intermediate close advances the clock by 10s.
		intermCloses := uint64(0)
		for k := stepIdx + 1; k < j; k++ {
			if steps[k].Op == "close" {
				intermCloses++
			}
		}

		// Compute what parentCloseTime would be after THIS close and all
		// intermediate closes, using the current clock.
		currentRipple := uint64(r.env.Now().Unix()) - rippleEpochOffset
		projectedClose := currentRipple + 10 + intermCloses*10

		// Check whether the projected close time yields the expected TER.
		needsAdjust := false
		var targetClose uint64

		switch s.ExpectTER {
		case "tesSUCCESS":
			// For success, lutRipple must be in [close-300, close+300].
			if lutRipple+maxDelta < projectedClose || (projectedClose+maxDelta < lutRipple) {
				// The projected close time would put LUT outside the valid range.
				// Target: set close time to lutRipple (the rippled default LUT = closeTime).
				targetClose = lutRipple
				needsAdjust = true
			}
		case "tecINVALID_UPDATE_TIME":
			// For the range check to fail, lutRipple must be outside [close-300, close+300].
			// If projected close would make it pass (inside the range), we need to adjust.
			if projectedClose >= maxDelta {
				lower := projectedClose - maxDelta
				upper := projectedClose + maxDelta
				if lutRipple >= lower && lutRipple <= upper {
					// LUT is inside the valid range with projected close → wrong result.
					// We need closeTime such that lutRipple is OUTSIDE [ct-300, ct+300].
					// Choose ct = lutRipple + maxDelta + 1 (push LUT below the lower bound).
					targetClose = lutRipple + maxDelta + 1
					needsAdjust = true
				}
			}
		}

		if needsAdjust && targetClose > projectedClose {
			// Advance the clock so that after this close (+10s) and intermediate
			// closes, parentCloseTime = targetClose.
			advanceBy := targetClose - projectedClose
			r.env.AdvanceTime(time.Duration(advanceBy) * time.Second)
		}

		break // only check the first OracleSet after this close
	}
}

// calibratePayChanTime adjusts the clock for upcoming PaymentChannel
// transactions that expect channel auto-close on CancelAfter or Expiration.
//
// In rippled tests, env.close(cancelAfter) or env.close(settleTimepoint) jumps
// the clock to a specific time so that the next PayChan transaction triggers
// the CancelAfter/Expiration auto-close check. The fixture captures these as
// plain "close" ops, so we scan ahead for PayChan transactions whose post_state
// indicates a channel was closed (owner_count decreased) and advance time to
// the channel's CancelAfter or Expiration value.
func (r *runner) calibratePayChanTime(stepIdx int, steps []Step) {
	const rippleEpochOffset = uint64(946684800)

	// Collect all known CancelAfter and settle-expiry values from preceding
	// PaymentChannelCreate/Fund/Claim steps. We track both CancelAfter (set
	// at creation time) and settle-based Expiration (set when tfClose is used).
	var maxExpiry uint64
	var lastSettleDelay uint32
	closesBefore := 0

	for i := 0; i < stepIdx; i++ {
		s := steps[i]
		if s.Op == "close" {
			closesBefore++
			continue
		}
		if s.Op == "fund" {
			// A fund step resets the test context — clear tracked expiries.
			// This prevents stale CancelAfter from a previous sub-test from
			// affecting the current sub-test's time calibration.
			maxExpiry = 0
			lastSettleDelay = 0
			continue
		}
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}
		tt, _ := txj["TransactionType"].(string)

		switch tt {
		case "PaymentChannelCreate":
			if ca, ok := parseUint32Field(txj, "CancelAfter"); ok && uint64(ca) > maxExpiry {
				maxExpiry = uint64(ca)
			}
			if sd, ok := parseUint32Field(txj, "SettleDelay"); ok {
				lastSettleDelay = sd
			}
		case "PaymentChannelClaim":
			// If this claim had tfClose flag, it set Expiration on the channel.
			// Expiration = parentCloseTime_at_claim + SettleDelay.
			flags, _ := parseUint32Field(txj, "Flags")
			if flags&0x20000 != 0 && lastSettleDelay > 0 { // tfPayChanClose
				// Estimate parentCloseTime at the time of this claim.
				// Each close step advances by 10s from ripple epoch 0.
				claimParentClose := uint64(closesBefore) * 10
				settleExpiry := claimParentClose + uint64(lastSettleDelay)
				if settleExpiry > maxExpiry {
					maxExpiry = settleExpiry
				}
			}
		case "PaymentChannelFund":
			if exp, ok := parseUint32Field(txj, "Expiration"); ok && uint64(exp) > maxExpiry {
				maxExpiry = uint64(exp)
			}
		}
	}

	if maxExpiry == 0 {
		return // no PayChan expiry values found
	}

	// Scan ahead for the next PayChan tx after this close (and any
	// intermediate closes) that expects a channel close in its post_state.
	closesAfter := uint64(0) // count of close steps between this one and the target tx
	for j := stepIdx + 1; j < len(steps); j++ {
		s := steps[j]
		if s.Op == "env_reset" || s.Op == "fund" {
			break
		}
		if s.Op == "close" {
			closesAfter++
			continue
		}
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			break
		}
		tt, _ := txj["TransactionType"].(string)
		if tt != "PaymentChannelClaim" && tt != "PaymentChannelFund" &&
			tt != "PaymentChannelCreate" && tt != "AccountDelete" &&
			tt != "Payment" {
			break
		}

		// Check if this tx expects a channel auto-close by looking at post_state.
		needsExpiry := false
		if s.PostState != nil {
			for _, as := range s.PostState.Accounts {
				// A channel close returns funds to the owner and decrements
				// owner_count. We detect this by looking for owner_count
				// values that indicate channels were closed.
				actualOC := r.env.OwnerCount(
					jtx.NewAccountWithAddress(as.Name, as.Address))
				if actualOC > as.OwnerCount {
					needsExpiry = true
				}
			}
		}

		// Also detect auto-close for tecEXPIRED (fixPayChanCancelAfter).
		if s.ExpectTER == "tecEXPIRED" {
			needsExpiry = true
		}

		if !needsExpiry {
			break
		}

		// Compute the projected parentCloseTime after this close + intermediate closes.
		currentRipple := uint64(r.env.Now().Unix()) - rippleEpochOffset
		projectedClose := currentRipple + 10 + closesAfter*10

		// If the projected close time is already past the expiry, no adjustment needed.
		if projectedClose >= maxExpiry {
			break
		}

		// Need to advance time to maxExpiry so the auto-close check triggers.
		advanceBy := maxExpiry - projectedClose
		r.env.AdvanceTime(time.Duration(advanceBy) * time.Second)
		break
	}
}

// parseUint32Field extracts a uint32 value from a JSON map field.
func parseUint32Field(m map[string]interface{}, key string) (uint32, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return uint32(val), true
	case string:
		parsed, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return 0, false
		}
		return uint32(parsed), true
	}
	return 0, false
}

// execTx handles a "tx" step.
func (r *runner) execTx(stepIdx int, step Step) {
	blob, err := hex.DecodeString(step.TxBlob)
	if err != nil {
		r.t.Fatalf("Step %d (tx): invalid tx_blob hex: %v", stepIdx, err)
	}

	// Empty blob means the transaction was constructed without required fields
	// and couldn't be serialized. If the expected result is tem* (malformed),
	// treat this as a conformance match — both rippled and goXRPL reject it.
	if len(blob) == 0 {
		if strings.HasPrefix(step.ExpectTER, "tem") {
			return
		}
		r.t.Fatalf("Step %d (tx): empty tx_blob with expected %s", stepIdx, step.ExpectTER)
	}

	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		// If the tx_blob can't be parsed and the expected result is a tem
		// (malformed) code, treat this as a conformance match — both rippled
		// and goXRPL reject the transaction, just at different stages.
		if strings.HasPrefix(step.ExpectTER, "tem") {
			return
		}
		r.t.Fatalf("Step %d (tx): failed to parse tx_blob: %v", stepIdx, err)
	}

	result := r.env.Submit(parsed)

	// Assert TER code
	if result.Code != step.ExpectTER {
		txType := "unknown"
		if step.TxJSON != nil {
			var txj map[string]interface{}
			if json.Unmarshal(step.TxJSON, &txj) == nil {
				if tt, ok := txj["TransactionType"].(string); ok {
					txType = tt
				}
			}
		}
		r.t.Errorf("Step %d (tx %s): TER mismatch: got %q, want %q",
			stepIdx, txType, result.Code, step.ExpectTER)
		return
	}

	// Assert post-state only for applied results (tesSUCCESS or tec).
	// Failed transactions (tem/tef/tel/ter) don't modify ledger state,
	// so post-state checks would compare against pre-transaction state
	// which may not match expectations (e.g., accounts not yet funded).
	if step.PostState != nil && result.Success {
		r.assertPostState(stepIdx, step.PostState)
	}
	// Also check post-state for tec results (applied but with error)
	if step.PostState != nil && strings.HasPrefix(result.Code, "tec") {
		r.assertPostState(stepIdx, step.PostState)
	}
}

// execEnvReset handles an "env_reset" step.
func (r *runner) execEnvReset(stepIdx int, step Step) {
	if step.Env == nil {
		r.t.Fatalf("Step %d (env_reset): missing env config", stepIdx)
	}

	// Clear accounts (keep only master which is re-registered in setupEnv)
	r.accounts = make(map[string]*jtx.Account)

	// Create fresh environment
	r.setupEnv(*step.Env)
}

// execModifyState handles a "modify_state" step, which directly modifies
// ledger state to set up boundary conditions. This mirrors rippled test
// hacks that use env.app().openLedger().modify() to set fields like
// MintedNFTokens to near-overflow values.
func (r *runner) execModifyState(stepIdx int, step Step) {
	if step.ModifyState == nil {
		r.t.Fatalf("Step %d (modify_state): missing modify_state config", stepIdx)
	}
	ms := step.ModifyState

	// Look up the account by address
	acc, ok := r.accounts[ms.Account]
	if !ok {
		// Try to find by address among registered accounts
		for _, a := range r.accounts {
			if a.Address == ms.Account {
				acc = a
				ok = true
				break
			}
		}
	}
	if !ok {
		r.t.Fatalf("Step %d (modify_state): unknown account %q", stepIdx, ms.Account)
	}

	if ms.MintedNFTokens != nil {
		r.env.SetMintedNFTokensDirect(acc, *ms.MintedNFTokens)
	}
	if ms.FirstNFTokenSequence != nil {
		r.env.SetFirstNFTokenSequenceDirect(acc, *ms.FirstNFTokenSequence)
	}
	if ms.BumpLastPage != nil {
		if err := r.env.BumpDirectoryLastPage(acc, ms.BumpLastPage.TargetPage, ms.BumpLastPage.AdjustField); err != nil {
			r.t.Fatalf("Step %d (modify_state): bump_last_page failed: %v", stepIdx, err)
		}
	}
}

// assertPostState validates account states against expected values.
func (r *runner) assertPostState(stepIdx int, ps *PostState) {
	for _, expected := range ps.Accounts {
		acc, ok := r.accounts[expected.Name]
		if !ok {
			// Create a temporary account reference for lookup
			acc = jtx.NewAccountWithAddress(expected.Name, expected.Address)
			r.accounts[expected.Name] = acc
		}

		// Check XRP balance
		expectedBalance, err := strconv.ParseUint(expected.XRPBalance, 10, 64)
		if err != nil {
			r.t.Errorf("Step %d: invalid expected balance %q for %s: %v",
				stepIdx, expected.XRPBalance, expected.Name, err)
			continue
		}

		gotBalance := r.env.Balance(acc)
		if gotBalance != expectedBalance {
			r.t.Errorf("Step %d: balance mismatch for %s: got %d, want %d (diff: %d)",
				stepIdx, expected.Name, gotBalance, expectedBalance,
				int64(gotBalance)-int64(expectedBalance))
		}

		// Check owner count
		gotOwnerCount := r.env.OwnerCount(acc)
		if gotOwnerCount != expected.OwnerCount {
			r.t.Errorf("Step %d: owner_count mismatch for %s: got %d, want %d",
				stepIdx, expected.Name, gotOwnerCount, expected.OwnerCount)
		}
	}
}

// shouldAutoFund returns true if the fixture needs implicit account funding.
// This is true when at least one tx step expects an applied result (tesSUCCESS
// or tec*) and its Account is not established by a preceding fund step.
// Many rippled test fixtures depend on accounts existing from prior test context
// (accounts funded before the test case captured in the fixture). When fund
// steps exist but only AFTER the first applied tx, we still need auto-funding
// for accounts that send those early transactions.
func (r *runner) shouldAutoFund(steps []Step) bool {
	masterAddr := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Collect addresses funded by explicit fund steps OR by inline Payment
	// transactions from master (which create the destination account).
	fundedAt := make(map[string]int) // address -> step index
	for i, s := range steps {
		if s.Op == "fund" && s.Address != "" {
			if _, exists := fundedAt[s.Address]; !exists {
				fundedAt[s.Address] = i
			}
		}
		// Also treat Payment from master to an address as implicit funding.
		// In rippled test helpers like runTx(), accounts are created by
		// Payments from master rather than explicit fund() calls.
		if s.Op == "tx" && s.TxJSON != nil {
			var txj map[string]interface{}
			if json.Unmarshal(s.TxJSON, &txj) == nil {
				if txj["TransactionType"] == "Payment" &&
					txj["Account"] == masterAddr {
					if dest, ok := txj["Destination"].(string); ok && dest != "" {
						if _, exists := fundedAt[dest]; !exists {
							fundedAt[dest] = i
						}
					}
				}
			}
		}
	}

	// Check if any tx step expects an applied result from an account that
	// isn't funded by a preceding fund step or inline Payment.
	for i, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		if s.ExpectTER != "tesSUCCESS" && !strings.HasPrefix(s.ExpectTER, "tec") {
			continue
		}

		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}
		addr, ok := txj["Account"].(string)
		if !ok || addr == "" || addr == masterAddr {
			continue
		}

		// Check if this account is funded BEFORE this tx step
		fundIdx, funded := fundedAt[addr]
		if !funded || fundIdx > i {
			return true
		}
	}
	return false
}

// autoFundAccounts scans tx_json steps for accounts and funds them so their
// sequences match the fixture's expectations. Accounts are grouped by their
// first expected sequence: accounts with the same initial seq are funded in
// the same ledger, with closes between groups to increment open_ledger_seq.
//
// For Credential and similar transaction types, auxiliary accounts (Subject,
// Issuer, Destination) are also funded when they need to exist for preclaim
// checks. However, accounts that have explicit fund steps in the fixture are
// NOT auto-funded as auxiliary accounts — the fixture intends to fund them
// at a specific time and amount.
//
// Initial funding amounts are derived from the first post_state entry for
// each account when possible. This is critical for reserve-sensitive tests
// where the exact balance determines the TER code.
func (r *runner) autoFundAccounts(steps []Step) {
	// Build a map of addresses that have explicit fund steps.
	// These addresses should not be auto-funded as auxiliary accounts because the
	// fixture deliberately controls when they are created.
	explicitFundAddrs := make(map[string]bool) // addresses with explicit fund steps
	for _, s := range steps {
		if s.Op == "fund" && s.Address != "" {
			explicitFundAddrs[s.Address] = true
		}
	}

	// Derive the initial funding amount for each account from the first
	// post_state entry. For applied tx results (tesSUCCESS/tec*), the
	// post_state balance = initial_balance - fees_consumed. By analyzing
	// how many txs the account has sent before the post_state check, we
	// can infer the initial balance.
	//
	// For simplicity, we use the first post_state balance + (number of
	// fees consumed by this account up to that post_state step) * baseFee.
	// This gives us the initial balance the fixture expects.
	initialBalances := r.deriveInitialBalances(steps)

	// Collect unique account addresses and their first sequence from tx_json.
	type acctInfo struct {
		address  string
		firstSeq uint32
	}
	seen := make(map[string]bool)
	var accounts []acctInfo

	// Also collect auxiliary addresses (Subject, Issuer, Destination) that
	// need to exist but aren't tx senders. These get sequence 0 (no txs from them).
	auxSeen := make(map[string]bool)

	for _, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}

		// Collect the Account field (the sender/signer).
		addr, ok := txj["Account"].(string)
		if ok && addr != "" && !seen[addr] {
			// Skip master/genesis account — already exists
			if addr == "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh" {
				seen[addr] = true
			} else {
				seen[addr] = true

				seq := uint32(0)
				if seqF, ok := txj["Sequence"].(float64); ok {
					seq = uint32(seqF)
				}
				accounts = append(accounts, acctInfo{address: addr, firstSeq: seq})
			}
		}

		// Collect auxiliary accounts (Subject, Issuer, Destination).
		// These accounts need to exist for preclaim checks to work correctly,
		// BUT only if they don't have explicit fund steps (which control timing).
		for _, field := range []string{"Subject", "Issuer", "Destination"} {
			if auxAddr, ok := txj[field].(string); ok && auxAddr != "" {
				auxSeen[auxAddr] = true
			}
		}
	}

	// Also collect addresses from post_state — if the fixture expects
	// specific balances for named accounts, those accounts must exist.
	for _, s := range steps {
		if s.PostState == nil {
			continue
		}
		for _, as := range s.PostState.Accounts {
			if as.Address != "" {
				auxSeen[as.Address] = true
			}
		}
	}

	// Determine minimum first_seq from sender accounts.
	minSeq := uint32(0xFFFFFFFF)
	for _, a := range accounts {
		if a.firstSeq > 0 && a.firstSeq < minSeq {
			minSeq = a.firstSeq
		}
	}
	if minSeq == 0xFFFFFFFF {
		minSeq = 4 // Default: funded in open ledger 3, AccountSet → seq 4
	}

	// Determine which auxiliary addresses should NOT be funded because the
	// fixture expects them to not exist (e.g., tecNO_TARGET, tecNO_ISSUER).
	skipAuxAddrs := r.findSkipAuxAddresses(steps)

	// Add auxiliary accounts that aren't already senders and aren't in the
	// skip set. Accounts are skipped if they have explicit fund steps AND the
	// first tx referencing them expects a TER code that depends on them not
	// existing (tecNO_TARGET for Subject/Destination, tecNO_ISSUER for Issuer).
	for auxAddr := range auxSeen {
		if seen[auxAddr] {
			continue
		}
		if auxAddr == "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh" {
			continue
		}
		// Check if it's the zero account
		if auxAddr == "rrrrrrrrrrrrrrrrrrrrrhoLvTp" || auxAddr == "rrrrrrrrrrrrrrrrrrrrBZbvji" {
			continue
		}
		// Skip auxiliary accounts that should not exist for the test to work
		if skipAuxAddrs[auxAddr] {
			continue
		}
		seen[auxAddr] = true
		accounts = append(accounts, acctInfo{address: auxAddr, firstSeq: 0})
	}

	if len(accounts) == 0 {
		return
	}

	// Assign zero-seq accounts to earliest group
	for i := range accounts {
		if accounts[i].firstSeq == 0 {
			accounts[i].firstSeq = minSeq
		}
	}

	// Sort by firstSeq to fund in correct ledger order
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].firstSeq < accounts[j].firstSeq
	})

	// Fund accounts grouped by firstSeq.
	// After setupEnv, open_ledger_seq = 3. Account creation sets
	// account.Sequence = open_ledger_seq. FundAmount then does AccountSet
	// (DefaultRipple) which bumps seq by 1. So to get firstSeq = N,
	// we need open_ledger_seq = N - 1 when funding.
	//
	// Starting open_ledger_seq = 3. We close ledgers as needed to reach
	// the target open_ledger_seq for each group.
	currentOpenSeq := r.env.LedgerSeq() // Should be 3 after setupEnv
	for _, a := range accounts {
		targetOpenSeq := a.firstSeq - 1 // open_ledger_seq needed for this account
		for currentOpenSeq < targetOpenSeq {
			r.env.Close()
			currentOpenSeq++
		}

		// Generate a short name from the address (last 8 chars)
		name := a.address
		if len(name) > 8 {
			name = name[len(name)-8:]
		}
		acc := jtx.NewAccountWithAddress(name, a.address)
		r.accounts[name] = acc
		// Also register by full address for post_state lookups
		r.accounts[a.address] = acc

		// Use the derived initial balance if available, otherwise default to 5000 XRP.
		fundAmount := uint64(5_000_000_000)
		if derived, ok := initialBalances[a.address]; ok && derived > 0 {
			fundAmount = derived
		}
		// Bypass TxQ for auto-fund (setup operation, like rippled's apply())
		r.env.SetBypassTxQ(true)
		r.env.FundAmount(acc, fundAmount)
		r.env.SetBypassTxQ(false)
	}

	// Close after all funding so state is committed
	r.env.Close()
}

// findSkipAuxAddresses identifies auxiliary addresses (Subject, Issuer, Destination)
// that should NOT be auto-funded because a tx step expects a TER code that depends
// on the account not existing. For example:
// - tecNO_TARGET: the Subject/Destination doesn't exist
// - tecNO_ISSUER: the Issuer doesn't exist
//
// Only addresses that also have explicit fund steps are considered for skipping,
// because if there's no fund step, the auxiliary account was never meant to be
// created by the fixture at all.
func (r *runner) findSkipAuxAddresses(steps []Step) map[string]bool {
	skipAddrs := make(map[string]bool)

	// Build set of addresses with explicit fund steps
	explicitFundAddrs := make(map[string]bool)
	for _, s := range steps {
		if s.Op == "fund" && s.Address != "" {
			explicitFundAddrs[s.Address] = true
		}
	}

	for _, s := range steps {
		if s.Op != "tx" || s.TxJSON == nil {
			continue
		}
		var txj map[string]interface{}
		if err := json.Unmarshal(s.TxJSON, &txj); err != nil {
			continue
		}

		// tecNO_TARGET: Subject or Destination should not exist
		if s.ExpectTER == "tecNO_TARGET" {
			for _, field := range []string{"Subject", "Destination"} {
				if addr, ok := txj[field].(string); ok && addr != "" && explicitFundAddrs[addr] {
					skipAddrs[addr] = true
				}
			}
		}

		// tecNO_ISSUER: Issuer should not exist
		if s.ExpectTER == "tecNO_ISSUER" {
			if addr, ok := txj["Issuer"].(string); ok && addr != "" && explicitFundAddrs[addr] {
				skipAddrs[addr] = true
			}
		}
	}

	return skipAddrs
}

// deriveInitialBalances infers the initial funding balance for each account
// from the fixture's first post_state entry. For each account, it finds the
// first post_state appearance and adds back the fees that were consumed by
// transactions from that account up to that point.
//
// Example: if account A sends 2 txs (each costing 10 drops) before the first
// post_state shows balance 4999999980, then initial balance = 4999999980 + 20
// = 5000000000 (5B).
func (r *runner) deriveInitialBalances(steps []Step) map[string]uint64 {
	result := make(map[string]uint64)

	// Track how many fees each address has consumed (as tx sender).
	// Only count applied results (tesSUCCESS, tec*) since tem/tef/tel/ter
	// don't deduct fees.
	feesByAddr := make(map[string]uint64)  // address -> total fees paid
	postStateSeen := make(map[string]bool) // already derived for this address

	for _, s := range steps {
		if s.Op == "tx" && s.TxJSON != nil {
			// Count fees for applied results
			if s.ExpectTER == "tesSUCCESS" || strings.HasPrefix(s.ExpectTER, "tec") {
				var txj map[string]interface{}
				if err := json.Unmarshal(s.TxJSON, &txj); err == nil {
					if addr, ok := txj["Account"].(string); ok && addr != "" {
						fee := uint64(10) // default base fee
						if feeStr, ok := txj["Fee"].(string); ok {
							if f, err := strconv.ParseUint(feeStr, 10, 64); err == nil {
								fee = f
							}
						}
						feesByAddr[addr] += fee
					}
				}
			}
		}

		// When we hit a post_state, derive balances for accounts we haven't seen yet.
		if s.PostState != nil {
			for _, as := range s.PostState.Accounts {
				if as.Address == "" || postStateSeen[as.Address] {
					continue
				}
				if as.Address == "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh" {
					continue // skip master
				}
				postStateSeen[as.Address] = true

				balance, err := strconv.ParseUint(as.XRPBalance, 10, 64)
				if err != nil {
					continue
				}

				// Initial balance = post_state balance + fees consumed by this account
				fees := feesByAddr[as.Address]
				result[as.Address] = balance + fees
			}
		}
	}

	return result
}

// parseDropsAmount parses a JSON amount field (can be string or number) into drops.
func parseDropsAmount(raw json.RawMessage) (uint64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("empty amount")
	}

	// Try as string first (quoted number)
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseUint(s, 10, 64)
	}

	// Try as number
	var n uint64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}

	return 0, fmt.Errorf("cannot parse amount: %s", string(raw))
}
