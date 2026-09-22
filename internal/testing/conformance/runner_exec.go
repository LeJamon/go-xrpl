package conformance

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

// setupEnv creates a TestEnv with the given configuration.
func (r *runner) setupEnv(cfg EnvConfig) {
	r.lastEnvCfg = cfg
	r.hadTxSteps = false

	// Clear pending transaction queues — stale transactions from a previous
	// scope must not leak into the new environment. Without this, a ter*
	// transaction queued in one scope could be retried after an env_reset,
	// consuming a sequence number in the new scope and causing tefPAST_SEQ.
	r.pendingHeld = nil
	r.pendingQueued = nil
	genCfg := genesis.DefaultConfig()
	genCfg.Fees.BaseFee = drops.NewXRPAmount(int64(cfg.BaseFee))
	genCfg.Fees.ReserveBase = drops.XRPAmount(cfg.ReserveBase)
	genCfg.Fees.ReserveIncrement = drops.XRPAmount(cfg.ReserveIncrement)

	// Enable TxQ if this is a TxQ test suite. TxQ must be created with the
	// test env so Submit() routes through fee escalation and queuing.
	// Use per-fixture TxQ config from txqConfigLookup.
	//
	// The base config values match rippled's test makeConfig() in envconfig.cpp:
	//   ledgers_in_queue = 2
	//   minimum_queue_size = 2
	//   normal_consensus_increase_percent = 0
	//   retry_sequence_percent = 25
	// Individual tests can override these via txqConfigLookup entries.
	if r.enableTxQ {
		txqCfg := txq.StandaloneConfig()
		txqCfg.MinimumTxnInLedgerStandalone = r.txqCfg.MinTxn
		// Apply makeConfig defaults (matching rippled envconfig.cpp)
		txqCfg.LedgersInQueue = 2
		txqCfg.QueueSizeMin = 2
		txqCfg.NormalConsensusIncreasePercent = 0
		// Apply per-test overrides from txqConfigLookup
		if r.txqCfg.LedgersInQueue != nil {
			txqCfg.LedgersInQueue = *r.txqCfg.LedgersInQueue
		}
		if r.txqCfg.QueueSizeMin != nil {
			txqCfg.QueueSizeMin = *r.txqCfg.QueueSizeMin
		}
		if r.txqCfg.MaximumTxnPerAccount != nil {
			txqCfg.MaximumTxnPerAccount = *r.txqCfg.MaximumTxnPerAccount
		}
		if r.txqCfg.NormalConsensusIncreasePercent != nil {
			txqCfg.NormalConsensusIncreasePercent = *r.txqCfg.NormalConsensusIncreasePercent
		}
		if r.txqCfg.SlowConsensusDecreasePercent != nil {
			txqCfg.SlowConsensusDecreasePercent = *r.txqCfg.SlowConsensusDecreasePercent
		}
		if r.txqCfg.TargetTxnInLedger != nil {
			txqCfg.TargetTxnInLedger = *r.txqCfg.TargetTxnInLedger
		}
		r.env = jtx.NewTestEnvWithTxQAndConfig(r.t, txqCfg, genCfg)
	} else {
		r.env = jtx.NewTestEnvWithConfig(r.t, genCfg)
	}
	r.env.SetAmendments(knownAmendments(cfg.AmendmentsEnabled))
	if r.numberContextOverride != nil {
		r.env.SetNumberContextOverride(*r.numberContextOverride)
	}
	if cfg.NetworkID != nil {
		r.env.SetNetworkID(*cfg.NetworkID)
	}

	// Enable open-ledger replay for fixtures that depend on canonical
	// replay-on-close to match rippled's closed-ledger state. In rippled,
	// Env::close() rebuilds the ledger from parent state by replaying all
	// transactions in CanonicalTXSet order. This changes the outcome of
	// some transactions (e.g., DepositPreauth applied before Payment).
	// Most fixtures don't need this because submission order produces the
	// same closed-ledger state. Enable selectively to avoid regressions
	// from ordering mismatches in the canonical sort.
	if r.enableReplay {
		r.env.EnableOpenLedgerReplay()
	}

	// Match rippled's startup sequence. rippled's startGenesisLedger()
	// creates: genesis(seq=1) → closed(seq=2, closeTime=0) → open(seq=3).
	// go-xrpl's NewTestEnvWithConfig creates only genesis(seq=1) → open(seq=2).
	// We need the extra close to reach open(seq=3) so that accounts created
	// with DeletableAccounts get initial sequence=3 (matching fixture blobs).
	//
	// Set the clock so that after Close()'s resolution-based advance, the
	// clock lands at exactly the Ripple epoch and the LCL gets closeTime=epoch
	// (ripple time 0). This matches rippled's startGenesisLedger which creates
	// LCL seq=2 with closeTime=0.
	setupResolution := time.Duration(r.env.Ledger().CloseTimeResolution()) * time.Second
	if setupResolution == 0 {
		setupResolution = 10 * time.Second
	}
	r.env.SetTime(rippleEpoch.Add(-setupResolution))
	r.env.Close()
	if cfg.InitialLedgerSeq != nil {
		for r.env.LedgerSeq() < *cfg.InitialLedgerSeq {
			r.env.Close()
		}
		if r.env.LedgerSeq() != *cfg.InitialLedgerSeq {
			r.t.Fatalf("initial_ledger_seq=%d cannot be represented (got %d)", *cfg.InitialLedgerSeq, r.env.LedgerSeq())
		}
	}

	// Reinitialize the still-empty TxQ after the initial close. In rippled,
	// startGenesisLedger() does NOT call TxQ::processClosedLedger, so
	// maxSize_ remains std::nullopt until the first user env.close().
	// Our Close() above triggers ProcessClosedLedger which prematurely
	// sets maxSize. Reinitializing it ensures the queue starts unlimited,
	// matching rippled's behavior where the first "real" close sets it.
	if r.enableTxQ {
		r.env.ReinitializeTxQ()
	}

	// For non-TxQ suites, disable open-ledger fee adequacy checks by default.
	// The fixture's recorded environment, rather than its expected TER, selects
	// this mode; a fee-floor result must still match exactly at submission.
	//
	// TxQ suites need open-ledger mode so fee escalation triggers queuing.
	//
	// The view stays open even so: SetViewOpen keeps view.open() true without the
	// fee floor, so the open-view fee branch (terINSUF_FEE_B, not closed-only
	// tecINSUFF_FEE) matches rippled. See TestEnv.viewOpen.
	if !r.enableTxQ {
		r.env.SetOpenLedger(false)
		r.env.SetViewOpen(true)
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

	// Derive keys from account name using the same algorithm as rippled
	// (SHA512-Half seed → secp256k1 keypair). This produces signed setup
	// transactions with identical binary serialization to rippled's,
	// enabling correct SHAMap root hash for canonical sort during replay.
	acc := jtx.NewAccount(step.Account)
	if acc.Address != step.Address {
		// Address doesn't match — use fixture address without keys
		// (happens for AMM pseudo-accounts or special addresses)
		acc = jtx.NewAccountWithAddress(step.Account, step.Address)
	}
	r.accounts[step.Account] = acc

	r.env.SetBypassTxQ(true)
	defer func() {
		r.env.SetBypassTxQ(false)
	}()

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
	r.env.SetBypassTxQ(true)
	defer func() {
		r.env.SetBypassTxQ(false)
	}()

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

	// Remap AMM issuer address if needed
	issuer := step.LimitAmount.Issuer
	if actual, ok := r.ammAddrMap[issuer]; ok {
		issuer = actual
	}

	limitAmount := tx.NewIssuedAmountFromFloat64(value, step.LimitAmount.Currency, issuer)

	ts := trustset.NewTrustSet(acc.Address, limitAmount)
	ts.Fee = strconv.FormatUint(r.env.BaseFee(), 10)
	seq := r.env.Seq(acc)
	ts.Sequence = &seq

	result := r.env.Submit(ts)
	if !result.Success {
		r.t.Fatalf("Step %d (trust): TrustSet failed for %s: %s", stepIdx, acc.Name, result.Code)
	}

	// Reimburse the TrustSet fee via a real Payment from master, matching
	// rippled's Env::trust() which submits Payment(master → account, baseFee).
	// This is tracked as a setup transaction and survives replay-on-close.
	r.env.ReimburseWithPayment(acc)
}

// applyLoadFeeEvent applies any load-factor change scheduled to take effect
// before step stepIdx. Mirrors rippled's mid-test LoadFeeTrack manipulation in
// TxQ_test.cpp; see txqLoadFeeLookup.
func (r *runner) applyLoadFeeEvent(stepIdx int) {
	if r.loadFeeEvents == nil {
		return
	}
	ev, ok := r.loadFeeEvents[stepIdx]
	if !ok {
		return
	}
	if ev.Reset {
		r.env.ResetLoadFee()
	}
	if ev.RemoteFee != 0 {
		r.env.FeeTrack().SetRemoteFee(ev.RemoteFee)
	}
	for i := 0; i < ev.RaiseLocal; i++ {
		r.env.FeeTrack().RaiseLocalFee()
	}
}

// execClose handles a "close" step. With v3 fixtures, the close_time field
// provides the exact close time, eliminating the need for time calibration.
func (r *runner) execClose(stepIdx int, step Step) {
	if step.CloseTime != nil {
		// v3 fixture: set clock so that after Close()'s resolution-based advance
		// (default 10s), the resulting close time matches the fixture's close_time.
		// close_time is in seconds since Ripple epoch (Jan 1, 2000).
		targetTime := rippleEpoch.Add(time.Duration(*step.CloseTime) * time.Second)
		resolution := time.Duration(r.env.Ledger().CloseTimeResolution()) * time.Second
		if resolution == 0 {
			resolution = 10 * time.Second
		}
		r.env.SetTime(targetTime.Add(-resolution))
	}
	if step.TxSetHash != nil {
		saltBytes, err := hex.DecodeString(*step.TxSetHash)
		if err != nil {
			r.t.Fatalf("Step %d (close): invalid tx_set_hash: %v", stepIdx, err)
		}
		if len(saltBytes) != 32 {
			r.t.Fatalf("Step %d (close): tx_set_hash must be 32 bytes, got %d", stepIdx, len(saltBytes))
		}
		var salt [32]byte
		copy(salt[:], saltBytes)
		r.env.SetNextCloseSalt(salt)
	}

	// Use time-leap close if this step index is in the time-leap set.
	// Time-leap closes reset TxQ fee metrics (txnsExpected) back toward
	// the minimum, matching rippled's env.close(env.now() + 5s, 10000ms).
	if r.timeLeapSteps[stepIdx] {
		r.env.CloseWithTimeLeap()
	} else {
		r.env.Close()
	}

	// Apply post-initFee reserves after the initFee close sequence.
	// initFee() in rippled runs a fee vote that changes reserves to much
	// lower values (e.g., 200 drops instead of 200 XRP). Since go-xrpl
	// doesn't implement fee voting, we apply the changed values directly.
	if r.initFee != nil && stepIdx == r.initFee.ApplyAfterStep {
		r.env.SetBaseFee(r.initFee.BaseFee)
		r.env.SetReserves(r.initFee.ReserveBase, r.initFee.ReserveIncrement)
	}

	// Apply fee-vote reserve reduction for fixtures that use rippled's
	// fee-voting pattern. In these fixtures, many consecutive ledger closes
	// trigger a fee vote that reduces reserves from genesis values (200 XRP)
	// to test config values (200 drops). The reduction is applied at the
	// flag ledger (ledger_seq % 256 == 0). TxQ suites use the explicit
	// initFee mechanism instead.
	if r.feeVote != nil && !r.feeVoteApplied {
		if step.LedgerSeq != nil && *step.LedgerSeq == r.feeVote.FlagLedgerSeq {
			r.env.SetBaseFee(r.feeVote.BaseFee)
			r.env.SetReserves(r.feeVote.ReserveBase, r.feeVote.ReserveIncrement)
			r.feeVoteApplied = true
		}
	}

	// Retry TxQ-queued transactions on close. In rippled, TxQ::accept()
	// retries queued transactions during ledger close. A queued tx may
	// now get a tec result (e.g., tecNO_ENTRY after a check is canceled),
	// which charges the fee even though the tx doesn't fully apply.
	// Skip for TxQ-enabled suites — the real TxQ handles retries there.
	if !r.enableTxQ {
		r.retryQueuedTxs()
	}
}

// injectOpenLedgerTxs replays the synthetic open-ledger transactions (if any)
// registered for this fixture at the given step index. rippled's TxQ tests
// apply these via env.app().openLedger().modify(), which the fixture exporter
// does not capture; replaying them here keeps the engine's balances and owner
// counts aligned with the fixture's expected post-state.
func (r *runner) injectOpenLedgerTxs(stepIdx int) {
	for _, inj := range txqOpenLedgerInjectLookup[r.testcase] {
		if inj.beforeStep != stepIdx {
			continue
		}
		acc := r.accounts[inj.account]
		if acc == nil {
			r.t.Fatalf("Step %d: open-ledger injection references unknown account %q", stepIdx, inj.account)
		}

		noop := &account.AccountSet{}
		common := noop.GetCommon()
		common.Account = acc.Address
		common.TransactionType = "AccountSet"
		common.Fee = strconv.FormatUint(r.env.OpenLedgerFee(r.env.BaseFee()), 10)
		if inj.ticketSeq != 0 {
			zero := uint32(0)
			ticket := inj.ticketSeq
			common.Sequence = &zero
			common.TicketSequence = &ticket
		} else {
			seq := r.env.Seq(acc)
			common.Sequence = &seq
		}

		signed := r.env.SignWith(noop, acc)
		r.env.SetBypassTxQ(true)
		result := r.env.Submit(signed)
		r.env.SetBypassTxQ(false)
		if !result.Success {
			r.t.Fatalf("Step %d: injected open-ledger tx failed: %s", stepIdx, result.Code)
		}
	}
}

// execTx handles a "tx" step.
func (r *runner) execTx(stepIdx int, step Step) {
	blob, err := hex.DecodeString(step.TxBlob)
	if err != nil {
		r.t.Fatalf("Step %d (tx): invalid tx_blob hex: %v", stepIdx, err)
	}
	if len(blob) == 0 {
		r.t.Fatalf("Step %d (tx): empty tx_blob", stepIdx)
	}

	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		r.t.Fatalf("Step %d (tx): failed to parse tx_blob: %v", stepIdx, err)
	}

	// Remap AMM pseudo-account addresses in the parsed transaction.
	// This is needed because AMM addresses depend on parentHash, which
	// differs between rippled and go-xrpl.
	r.remapAMMAddresses(parsed)

	// Set the clock to match the fixture's parent_close_time so that
	// time-dependent checks (expiration, cancel-after, etc.) evaluate
	// correctly regardless of how many closes were replayed from
	// prerequisite fixtures.
	if step.ParentCloseTime != nil {
		targetTime := rippleEpoch.Add(time.Duration(*step.ParentCloseTime) * time.Second)
		r.env.SetTime(targetTime)
	}

	// Some rippled tests use openLedger().modify() to apply transactions
	// directly to the open ledger, bypassing TxQ. The fixture captures
	// these as normal "tx" steps. Temporarily bypass TxQ for these steps.
	if r.directApplySteps[stepIdx] {
		r.env.SetBypassTxQ(true)
		defer r.env.SetBypassTxQ(false)
	}

	submitParsed := func(transaction tx.Transaction) jtx.TxResult {
		return r.env.SubmitWithOptions(transaction, jtx.SubmitOptions{
			SkipFee:       true,
			SkipSequence:  true,
			SkipNetworkID: true,
			SkipSignature: true,
		})
	}
	beforeState, err := r.env.Ledger().StateMapHash()
	if err != nil {
		r.t.Fatalf("Step %d (tx): read pre-state hash: %v", stepIdx, err)
	}
	result := submitParsed(parsed)
	afterState, err := r.env.Ledger().StateMapHash()
	if err != nil {
		r.t.Fatalf("Step %d (tx): read post-state hash: %v", stepIdx, err)
	}
	if !result.Applied && beforeState != afterState {
		r.t.Errorf("Step %d (tx): rejected transaction mutated ledger state", stepIdx)
	}

	if err := resultExpectationError(step, result, hex.EncodeToString(afterState[:])); err != nil {
		r.t.Errorf("Step %d (tx): %v", stepIdx, err)
		return
	}

	// Queue ter* transactions for later retry. Skip for TxQ-enabled suites
	// where the real TxQ handles queuing and retries.
	//
	// - terPRE_TICKET / terPRE_SEQ: "held" transactions retried after
	//   each successful tx submission (rippled's held-tx mechanism).
	//
	// - ter* codes from actual apply (terNO_RIPPLE, etc.): queued and
	//   retried during ledger close (matching TxQ::accept()).
	//   terNO_ACCOUNT is excluded — returned early from TxQ::apply().
	if !r.enableTxQ {
		if result.Code == "terPRE_TICKET" || result.Code == "terPRE_SEQ" {
			r.pendingHeld = append(r.pendingHeld, parsed)
		} else if strings.HasPrefix(result.Code, "ter") && result.Code != "terNO_ACCOUNT" {
			r.pendingQueued = append(r.pendingQueued, parsed)
		}
	}

	// After a successful AMMCreate, discover the actual AMM account address
	// and register the mapping from fixture to actual address.
	if result.Success && step.TxJSON != nil {
		var txj map[string]any
		if json.Unmarshal(step.TxJSON, &txj) == nil {
			if txj["TransactionType"] == "AMMCreate" {
				r.registerAMMMapping(step)
			}
		}
	}

	// After a successful transaction, retry held transactions (terPRE_TICKET,
	// terPRE_SEQ). In rippled, these are retried immediately after each
	// successful submission — not via the TxQ, but through a held-tx path.
	if result.Success {
		r.retryHeldTxs()
	}

	if step.PostState != nil {
		r.assertPostState(stepIdx, step.PostState)
	}
}

func resultExpectationError(step Step, result jtx.TxResult, stateHash string) error {
	expected := step.ExpectedResult
	if expected == nil || expected.TERCode == nil || expected.Applied == nil || expected.Queued == nil || expected.Fee == nil || expected.MetadataSHA512Half == nil || expected.StateSHA512Half == nil {
		return fmt.Errorf("missing expected_result for %q", step.ExpectTER)
	}
	if result.Code != step.ExpectTER {
		return fmt.Errorf("TER mismatch: got %q, want %q", result.Code, step.ExpectTER)
	}
	if int(result.Result) != *expected.TERCode {
		return fmt.Errorf("numeric TER mismatch: got %d, want %d", result.Result, *expected.TERCode)
	}
	if result.Applied != *expected.Applied {
		return fmt.Errorf("applied mismatch: got %t, want %t", result.Applied, *expected.Applied)
	}
	if result.Queued != *expected.Queued {
		return fmt.Errorf("queued mismatch: got %t, want %t", result.Queued, *expected.Queued)
	}
	if result.Fee != *expected.Fee {
		return fmt.Errorf("fee mismatch: got %d, want %d", result.Fee, *expected.Fee)
	}
	actualMetadataHash := ""
	if result.Metadata != nil {
		metadata, err := tx.SerializeMetadata(result.Metadata)
		if err != nil {
			return fmt.Errorf("serialize metadata: %w", err)
		}
		sum := sha512half.Sum(metadata)
		actualMetadataHash = hex.EncodeToString(sum[:])
	}
	if !strings.EqualFold(actualMetadataHash, strings.TrimSpace(*expected.MetadataSHA512Half)) {
		return fmt.Errorf("metadata hash mismatch: got %q, want %q", actualMetadataHash, strings.TrimSpace(*expected.MetadataSHA512Half))
	}
	if !strings.EqualFold(stateHash, strings.TrimSpace(*expected.StateSHA512Half)) {
		return fmt.Errorf("state hash mismatch: got %q, want %q", stateHash, strings.TrimSpace(*expected.StateSHA512Half))
	}
	return nil
}

// retryHeldTxs retries transactions that returned terPRE_TICKET or
// terPRE_SEQ. In rippled, these are retried immediately after each
// successful submission through a held-transaction mechanism (not the TxQ).
func (r *runner) retryHeldTxs() {
	if len(r.pendingHeld) == 0 {
		return
	}
	remaining := r.pendingHeld[:0]
	for _, txn := range r.pendingHeld {
		result := r.env.Submit(txn)
		if result.Code == "terPRE_TICKET" || result.Code == "terPRE_SEQ" {
			remaining = append(remaining, txn)
		}
	}
	r.pendingHeld = remaining
}

// retryQueuedTxs retries TxQ-queued transactions on ledger close.
// In rippled, TxQ::accept() processes queued ter* transactions against the
// fresh open ledger, so a payer that cannot cover the fee yields the retryable
// terINSUF_FEE_B (stays queued, no fee charged) rather than the closed-ledger
// tecINSUFF_FEE that would claim its remaining balance. Retries that still
// return ter* stay queued; tec results charge the fee; tef/tem results dropped.
func (r *runner) retryQueuedTxs() {
	if len(r.pendingQueued) == 0 {
		return
	}
	r.env.SetOpenLedger(true)
	defer r.env.SetOpenLedger(false)
	remaining := r.pendingQueued[:0]
	for _, txn := range r.pendingQueued {
		result := r.env.Submit(txn)
		if strings.HasPrefix(result.Code, "ter") {
			remaining = append(remaining, txn)
		}
	}
	r.pendingQueued = remaining
}

// execRetryBatch handles a batch of consecutive "retry" steps. Retry ops
// represent transactions that were queued in rippled's TxQ and applied
// atomically when the ledger closed. The fixture exporter captures them
// after the close step because that is when they become visible.
//
// All retries in a batch are sorted by sequence and applied directly
// (bypassing TxQ). Every recorded retry must have a parseable blob and its
// exact recorded TER; a missing predecessor is a malformed corpus, not a
// reason to accept a substitute result. Each supplied post_state is checked.
func (r *runner) execRetryBatch(batch []struct {
	idx  int
	step Step
}) {
	// Parse all retry transactions up front.
	type parsedRetry struct {
		idx       int
		step      Step
		txn       tx.Transaction
		seq       uint32
		result    jtx.TxResult
		stateHash string
	}
	var retries []parsedRetry

	for _, entry := range batch {
		blob, err := hex.DecodeString(entry.step.TxBlob)
		if err != nil {
			r.t.Fatalf("Step %d (retry): invalid tx_blob hex: %v", entry.idx, err)
		}
		if len(blob) == 0 {
			r.t.Fatalf("Step %d (retry): empty tx_blob", entry.idx)
		}
		parsed, err := tx.ParseFromBinary(blob)
		if err != nil {
			r.t.Fatalf("Step %d (retry): failed to parse tx_blob: %v", entry.idx, err)
			return
		}
		r.remapAMMAddresses(parsed)
		seq := uint32(0)
		if entry.step.TxJSON != nil {
			var txj map[string]any
			if json.Unmarshal(entry.step.TxJSON, &txj) == nil {
				if s, ok := txj["Sequence"].(float64); ok {
					seq = uint32(s)
				}
			}
		}
		retries = append(retries, parsedRetry{
			idx:  entry.idx,
			step: entry.step,
			txn:  parsed,
			seq:  seq,
		})
	}

	// Sort by sequence number so they apply in the correct order.
	sort.Slice(retries, func(i, j int) bool {
		return retries[i].seq < retries[j].seq
	})

	// Apply all retry transactions directly, bypassing TxQ.
	r.env.SetBypassTxQ(true)
	for i := range retries {
		retries[i].result = r.env.Submit(retries[i].txn)
		state, err := r.env.Ledger().StateMapHash()
		if err != nil {
			r.t.Fatalf("Step %d (retry): read post-state hash: %v", retries[i].idx, err)
		}
		retries[i].stateHash = hex.EncodeToString(state[:])
	}
	r.env.SetBypassTxQ(false)

	// Check every retry's TER code exactly. A missing predecessor is a fixture
	// error, not a reason to accept terPRE_SEQ as a substitute result.
	for _, retry := range retries {
		if err := resultExpectationError(retry.step, retry.result, retry.stateHash); err != nil {
			r.t.Errorf("Step %d (retry seq=%d): %v", retry.idx, retry.seq, err)
		}
	}

	// Retry observations are recorded after the atomic close. Compare every
	// supplied checkpoint, including rejected outcomes.
	for _, retry := range retries {
		if retry.step.PostState != nil {
			r.assertPostState(retry.idx, retry.step.PostState)
		}
	}
}

// execEnvReset handles an "env_reset" step.
func (r *runner) execEnvReset(stepIdx int, step Step) {
	if step.Env == nil {
		r.t.Fatalf("Step %d (env_reset): missing env config", stepIdx)
	}

	// Clear accounts (keep only master which is re-registered in setupEnv)
	r.accounts = make(map[string]*jtx.Account)

	// Clear AMM address mappings — the previous ledger's AMM accounts no
	// longer exist in the new environment, and new AMMCreates will produce
	// different pseudo-account addresses (different parentHash).
	r.ammAddrMap = make(map[string]string)

	// Reset fee-vote tracking for the new scope.
	r.feeVoteApplied = false

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

	// Look up the account. If no account is specified (v3 fixtures may omit
	// it for bump_last_page), find the first non-master registered account.
	var acc *jtx.Account
	if ms.Account != "" {
		var ok bool
		acc, ok = r.accounts[ms.Account]
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
	} else {
		// No account specified — find the account from the most recent
		// preceding tx step. This correctly targets the account whose
		// directory was just modified (e.g., after TicketCreate fills it).
		for i := stepIdx - 1; i >= 0; i-- {
			s := r.fixtureSteps[i]
			if s.Op == "tx" && s.TxJSON != nil {
				var txj map[string]any
				if json.Unmarshal(s.TxJSON, &txj) == nil {
					if addr, ok := txj["Account"].(string); ok && addr != "" {
						for _, a := range r.accounts {
							if a.Address == addr {
								acc = a
								break
							}
						}
						if acc != nil {
							break
						}
					}
				}
			}
		}
		// Fallback: first non-master account
		if acc == nil {
			masterAddr := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
			for _, a := range r.accounts {
				if a.Address != masterAddr {
					acc = a
					break
				}
			}
		}
		if acc == nil {
			r.t.Fatalf("Step %d (modify_state): no account specified and no non-master account found", stepIdx)
		}
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

		gotOwnerCount := r.env.OwnerCount(acc)
		if gotOwnerCount != expected.OwnerCount {
			r.t.Errorf("Step %d: owner_count mismatch for %s: got %d, want %d",
				stepIdx, expected.Name, gotOwnerCount, expected.OwnerCount)
		}

		if expected.Sequence != nil || expected.Flags != nil {
			info := r.env.AccountInfo(acc)
			if info == nil {
				r.t.Errorf("Step %d: account %s missing while checking metadata", stepIdx, expected.Name)
				continue
			}
			if expected.Sequence != nil && info.Sequence != *expected.Sequence {
				r.t.Errorf("Step %d: sequence mismatch for %s: got %d, want %d",
					stepIdx, expected.Name, info.Sequence, *expected.Sequence)
			}
			if expected.Flags != nil && info.Flags != *expected.Flags {
				r.t.Errorf("Step %d: flags mismatch for %s: got %#x, want %#x",
					stepIdx, expected.Name, info.Flags, *expected.Flags)
			}
		}
	}

}
