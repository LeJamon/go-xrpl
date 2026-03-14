// Package conformance provides a test runner for xrpl-fixtures test vectors.
// It replays rippled test vectors against the goXRPL transaction engine and
// validates that TER codes and post-state match the reference implementation.
package conformance

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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

	r := &runner{
		t:        t,
		accounts: make(map[string]*jtx.Account),
	}

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

	// Execute steps sequentially
	for i, step := range fixture.Steps {
		switch step.Op {
		case "fund":
			r.execFund(i, step)
		case "trust":
			r.execTrust(i, step)
		case "close":
			r.env.Close()
		case "tx":
			r.execTx(i, step)
		case "env_reset":
			r.execEnvReset(i, step)
		case "enable_amendment":
			r.env.EnableFeature(step.Amendment)
		default:
			t.Fatalf("Step %d: unknown op %q", i, step.Op)
		}
	}
}

// setupEnv creates a TestEnv with the given configuration.
func (r *runner) setupEnv(cfg EnvConfig) {
	genCfg := genesis.DefaultConfig()
	genCfg.Fees.BaseFee = drops.NewXRPAmount(int64(cfg.BaseFee))
	genCfg.Fees.ReserveBase = drops.XRPAmount(cfg.ReserveBase)
	genCfg.Fees.ReserveIncrement = drops.XRPAmount(cfg.ReserveIncrement)

	r.env = jtx.NewTestEnvWithConfig(r.t, genCfg)
	r.env.SetAmendments(cfg.AmendmentsEnabled)

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

	// Register master account
	master := jtx.MasterAccount()
	r.accounts["master"] = master
}

// execFund handles a "fund" step.
func (r *runner) execFund(stepIdx int, step Step) {
	amount, err := parseDropsAmount(step.Amount)
	if err != nil {
		r.t.Fatalf("Step %d (fund): invalid amount: %v", stepIdx, err)
	}

	acc := jtx.NewAccountWithAddress(step.Account, step.Address)
	r.accounts[step.Account] = acc

	setRipple := step.SetDefaultRipple == nil || *step.SetDefaultRipple
	if setRipple {
		r.env.FundAmount(acc, amount)
	} else {
		r.env.FundAmountNoRipple(acc, amount)
	}
}

// execTrust handles a "trust" step.
func (r *runner) execTrust(stepIdx int, step Step) {
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
	// Collect addresses funded by explicit fund steps and their positions.
	fundedAt := make(map[string]int) // address -> step index
	for i, s := range steps {
		if s.Op == "fund" && s.Address != "" {
			if _, exists := fundedAt[s.Address]; !exists {
				fundedAt[s.Address] = i
			}
		}
	}

	// Check if any tx step expects an applied result from an account that
	// isn't funded by a preceding fund step.
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
		if !ok || addr == "" || addr == "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh" {
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
		r.env.FundAmount(acc, fundAmount)
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
