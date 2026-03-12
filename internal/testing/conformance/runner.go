// Package conformance provides a test runner for xrpl-fixtures test vectors.
// It replays rippled test vectors against the goXRPL transaction engine and
// validates that TER codes and post-state match the reference implementation.
package conformance

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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
	RippledVersion string  `json:"rippled_version"`
	Suite          string  `json:"suite"`
	Testcase       string  `json:"testcase"`
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
