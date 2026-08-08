package jtx

import (
	"bytes"
	"context"
	"crypto/sha512"
	"sort"
	"strconv"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

type pendingBatchApply struct {
	transaction tx.Transaction
	outer       tx.ApplyResult
}

// Close closes the current ledger and advances to a new one.
// This is equivalent to "ledger_accept" in rippled.
//
// When replayOnClose is enabled, Close() simulates the consensus process:
// it discards the current open ledger state, creates a fresh open ledger
// from the last closed ledger (parent), and replays all tracked
// transactions in canonical order with retry passes. This matches
// rippled's standalone consensus simulation (BuildLedger.cpp).
func (e *TestEnv) Close() {
	e.t.Helper()
	e.close(false)
}

// close closes the current ledger, advances to a fresh open ledger, and runs
// the TxQ drain. timeLeap simulates a slow consensus round: it is forwarded to
// ProcessClosedLedger (driving txnsExpected back toward the minimum) and
// suppresses the replay-on-close branch (which only ever runs for plain Close).
func (e *TestEnv) close(timeLeap bool) {
	e.t.Helper()

	// Apply any pending amendment changes from EnableFeature/DisableFeature/
	// SetAmendments(). Matches rippled where feature toggles require close()
	// for the rules to take effect.
	// Reference: rippled Env.cpp: "Env::close() must be called for feature
	// enable to take place."
	e.applyPendingAmendments()

	// A TxQ-admitted Batch must be rebuilt so its inners enter the closed ledger.
	if (!timeLeap && e.replayOnClose) || len(e.pendingBatchApplies) != 0 {
		if e.lastClosedLedger == nil {
			e.lastClosedLedger = e.genesisLedger
		}
		e.closeWithReplay(timeLeap)
		return
	}

	// Round closeTime up to next resolution boundary, matching rippled.
	// Reference: rippled Env.cpp:126 — closeTime += resolution - 1s
	resolution := time.Duration(e.ledger.CloseTimeResolution()) * time.Second
	if resolution == 0 {
		resolution = 10 * time.Second // fallback for genesis
	}
	e.clock.Advance(resolution)

	// Record the total number of transactions in the closing ledger for TxQ
	// metrics. closingTxTotal includes inner batch txns as separate entries,
	// matching rippled's closed ledger tx map behavior.
	closingTxCount := e.closingTxTotal

	// Close current ledger
	if err := e.ledger.Close(e.clock.Now(), 0); err != nil {
		e.t.Fatalf("Failed to close ledger: %v", err)
	}

	// Validate the ledger (in test mode, we auto-validate)
	if err := e.ledger.SetValidated(); err != nil {
		e.t.Fatalf("Failed to validate ledger: %v", err)
	}

	// Re-sync clock to the actual close time from the closed ledger.
	// Matches rippled's timeKeeper().set(closed()->info().closeTime).
	e.clock.Set(e.ledger.CloseTime())

	// Store lightweight state root hash in history (matching rippled's LedgerHistory pattern)
	if h, err := e.ledger.StateMapHash(); err == nil {
		e.ledgerRootHashes[e.ledger.Sequence()] = h
	}

	// Sweep nodestore caches if backed mode is enabled
	if e.stateFamily != nil {
		e.stateFamily.Sweep()
	}

	// Update TxQ metrics based on the closed ledger.
	// Reference: rippled TxQ::processClosedLedger called after ledger close.
	if e.txQueue != nil {
		// Recompute fee levels against the final closed view. If no transactions
		// were retained, fall back to
		// generating BaseLevel entries for each transaction (for backward
		// compatibility with tests that don't track fee levels).
		feeLevels := e.closingLedgerFeeLevels()
		if len(feeLevels) == 0 && closingTxCount > 0 {
			feeLevels = make([]txq.FeeLevel, closingTxCount)
			for i := range feeLevels {
				feeLevels[i] = txq.FeeLevel(txq.BaseLevel)
			}
		}
		closedCtx := &testClosedLedgerContext{
			ledgerSeq: e.ledger.Sequence(),
			feeLevels: feeLevels,
		}
		e.txQueue.ProcessClosedLedger(closedCtx, timeLeap)
	}

	// Track the closed ledger as the last closed ledger.
	// This is used by EnableOpenLedgerReplay() and closeWithReplay().
	e.lastClosedLedger = e.ledger

	// Create new open ledger
	newLedger, err := ledger.NewOpen(e.ledger, e.clock.Now())
	if err != nil {
		e.t.Fatalf("Failed to create new ledger: %v", err)
	}

	e.ledger = newLedger
	e.currentSeq++

	// Reset the open-ledger transaction counters for the new ledger.
	e.openLedgerSetupTxns = nil
	e.openLedgerUserTxns = nil
	e.txInLedger = 0
	e.closingTxTotal = 0
	e.closingFeeTransactions = nil
	e.pendingBatchApplies = nil

	// Accept queued transactions into the new open ledger.
	// Reference: rippled TxQ::accept called when new open ledger is created.
	if e.txQueue != nil {
		e.drainQueue()

		// Retry held transactions through the TxQ after drain.
		// This mirrors rippled's OpenLedger::accept() step (d) which
		// iterates localTxs and calls TxQ::apply() for each. This allows
		// transactions that were rejected with tel codes (telCAN_NOT_QUEUE_FULL
		// etc.) to be re-queued now that the queue has been drained and has
		// room. Reference: rippled OpenLedger.cpp:117-118
		e.retryAllHeldViaTxQ()
	}
}

// CloseWithTimeLeap closes the current ledger with a simulated time leap.
// A time leap indicates that consensus was slow, causing the TxQ to aggressively
// reduce txnsExpected back toward the minimum. This matches rippled's behavior
// when env.close(env.now() + 5s, 10000ms) is called in tests.
// Reference: rippled TxQ::FeeMetrics::update timeLeap handling
func (e *TestEnv) CloseWithTimeLeap() {
	e.t.Helper()
	e.close(true)
}

// CloseToParentCloseTime closes one ledger so that the new open ledger's parent
// close time lands exactly on target (Ripple epoch seconds), failing the test
// otherwise. Used by expiry tests that need ParentCloseTime at a precise value.
func (e *TestEnv) CloseToParentCloseTime(target uint32) {
	e.t.Helper()
	resolution := time.Duration(e.ledger.CloseTimeResolution()) * time.Second
	targetTime := protocol.FromRippleTime(target)
	e.SetTime(targetTime.Add(-resolution))
	e.Close()
	if got := toRippleTime(e.ledger.ParentCloseTime()); got != target {
		e.t.Fatalf("CloseToParentCloseTime: parent close time landed on %d, want %d", got, target)
	}
}

// closeWithReplay implements the replay-on-close consensus simulation.
// It creates a fresh open ledger from the parent closed ledger and replays
// all tracked transactions in canonical order with retry passes.
//
// This simulates rippled's standalone consensus:
// 1. applyHeldTransactions() -- held txns are added to the open view
// 2. onClose() -- builds initial TX set from all open ledger txns
// 3. buildLedger() -- creates fresh view from parent, applies TX set
// 4. applyTransactions() -- multiple retry passes for failed txns
//
// Reference: rippled BuildLedger.cpp, RCLConsensus.cpp
func (e *TestEnv) closeWithReplay(timeLeap bool) {
	e.t.Helper()

	// Advance time (matching Close() behavior)
	// Round closeTime up to next resolution boundary, matching rippled.
	resolution := time.Duration(e.ledger.CloseTimeResolution()) * time.Second
	if resolution == 0 {
		resolution = 10 * time.Second
	}
	e.clock.Advance(resolution)

	// Setup txns (fund, trust, reimbursement, the DefaultRipple AccountSet) are
	// scaffolding the runner synthesizes and master-signs; their go-xrpl-specific
	// hashes cannot reproduce rippled's canonical salt, so reordering them
	// canonically only risks separating a flag-setting AccountSet from the
	// TrustSet that depends on it (an issuer's DefaultRipple must precede a
	// holder's TrustSet, or the new line keeps the issuer's NoRipple and blocks
	// rippling). Keep setup in submission order — the natural dependency order —
	// and canonically order only the fixture's user txns, whose blob hashes do
	// match rippled. Setup hashes are still folded into the salt so the user
	// order is identical to canonically ordering the combined set.
	//
	// heldHashes marks transactions carried over from a previous ledger as held.
	// rippled retries held transactions against the OPEN ledger
	// (LedgerMaster::applyHeldTransactions), so their fee-adequacy is judged with
	// view.open()==true: a payer that cannot cover the fee yields the retryable
	// terINSUF_FEE_B (no fee charged) and stays held, never the closed-ledger
	// tecINSUFF_FEE that would claim its remaining balance.
	heldHashes := make(map[[32]byte]bool)
	for _, held := range e.heldTxns {
		for _, txn := range held {
			h, _ := tx.ComputeTransactionHash(txn)
			heldHashes[h] = true
		}
	}

	// Collect setup and user txns, de-duplicating by hash: a retryable txn is
	// tracked in both openLedgerUserTxns and heldTxns, and the runner's queue
	// retries re-submit the same object across ledgers. Applying the same txn
	// twice in one ledger is never valid (the second hits tefPAST_SEQ).
	seen := make(map[[32]byte]bool)
	dedupInto := func(dst []tx.Transaction, list []tx.Transaction) []tx.Transaction {
		for _, txn := range list {
			h, _ := tx.ComputeTransactionHash(txn)
			if seen[h] {
				continue
			}
			seen[h] = true
			dst = append(dst, txn)
		}
		return dst
	}
	setupTxns := dedupInto(nil, e.openLedgerSetupTxns)
	var userTxns []tx.Transaction
	userTxns = dedupInto(userTxns, e.openLedgerUserTxns)
	for _, held := range e.heldTxns {
		userTxns = dedupInto(userTxns, held)
	}

	sortCanonicalSalted(userTxns, setupTxns)

	allTxns := make([]tx.Transaction, 0, len(setupTxns)+len(userTxns))
	allTxns = append(allTxns, setupTxns...)
	allTxns = append(allTxns, userTxns...)

	// Clear held transactions -- they will be re-held if they still fail
	e.heldTxns = nil

	// Create a fresh open ledger from the last closed ledger (parent).
	// This discards all state changes from the immediate applies.
	freshLedger, err := ledger.NewOpenForBuild(e.lastClosedLedger, e.clock.Now())
	if err != nil {
		e.t.Fatalf("closeWithReplay: failed to create fresh ledger: %v", err)
	}
	e.ledger = freshLedger

	// Reset counters for the fresh replay
	e.txInLedger = 0
	e.closingTxTotal = 0
	e.closingFeeTransactions = nil
	e.pendingBatchApplies = nil

	const maxRetryPasses = 1 // LEDGER_RETRY_PASSES in rippled (OpenLedger.h line 44)
	const maxTotalPasses = 3 // LEDGER_TOTAL_PASSES in rippled (OpenLedger.h line 40)

	// Apply all transactions with retry passes.
	// Setup and user txns are in a single list in submission order.
	remaining := e.applyWithRetry(allTxns, heldHashes, maxRetryPasses, maxTotalPasses)

	// Any remaining transactions that still failed go back into the held
	// map for retry in the next ledger.
	for _, txn := range remaining {
		accountAddr := txn.GetCommon().Account
		e.addHeldTransaction(accountAddr, txn)
	}

	// Close the replayed ledger
	if err := e.ledger.Close(e.clock.Now(), 0); err != nil {
		e.t.Fatalf("closeWithReplay: failed to close ledger: %v", err)
	}
	if err := e.ledger.SetValidated(); err != nil {
		e.t.Fatalf("closeWithReplay: failed to validate ledger: %v", err)
	}

	if e.txQueue != nil {
		feeLevels := e.closingLedgerFeeLevels()
		if len(feeLevels) == 0 && e.closingTxTotal > 0 {
			feeLevels = make([]txq.FeeLevel, e.closingTxTotal)
			for i := range feeLevels {
				feeLevels[i] = txq.FeeLevel(txq.BaseLevel)
			}
		}
		e.txQueue.ProcessClosedLedger(&testClosedLedgerContext{
			ledgerSeq: e.ledger.Sequence(),
			feeLevels: feeLevels,
		}, timeLeap)
	}

	// Re-sync clock to the actual close time from the closed ledger.
	// Matches rippled's timeKeeper().set(closed()->info().closeTime).
	e.clock.Set(e.ledger.CloseTime())

	// Store state root hash in history
	if h, err := e.ledger.StateMapHash(); err == nil {
		e.ledgerRootHashes[e.ledger.Sequence()] = h
	}

	// Sweep nodestore caches if backed mode is enabled
	if e.stateFamily != nil {
		e.stateFamily.Sweep()
	}

	// Update last closed ledger
	e.lastClosedLedger = e.ledger

	// Create new open ledger
	newLedger, err := ledger.NewOpen(e.ledger, e.clock.Now())
	if err != nil {
		e.t.Fatalf("closeWithReplay: failed to create new open ledger: %v", err)
	}
	e.ledger = newLedger
	e.currentSeq++

	// Reset transaction tracking for the new open ledger
	e.openLedgerSetupTxns = nil
	e.openLedgerUserTxns = nil
	e.txInLedger = 0
	e.closingTxTotal = 0
	e.closingFeeTransactions = nil

	// Update TxQ metrics if applicable
	if e.txQueue != nil {
		e.drainQueue()
		e.retryAllHeldViaTxQ()
	}
}

// pendingRulesBuilder returns a RulesBuilder reflecting the current rules with
// all staged amendment changes applied: SetAmendments (whole-set replace) first,
// then the EnableFeature/DisableFeature deltas layered on top. It does NOT mutate
// the env — applyPendingAmendments installs the result at Close, while
// FeatureEnabled queries it without committing.
func (e *TestEnv) pendingRulesBuilder() *amendment.RulesBuilder {
	b := amendment.NewRulesBuilder()
	if len(e.pendingAmendments) > 0 {
		for _, name := range e.pendingAmendments {
			b.EnableByName(name)
		}
	} else {
		for _, id := range e.rulesBuilder.Build().EnabledIDs() {
			b.Enable(id)
		}
	}
	for _, name := range e.pendingEnable {
		b.EnableByName(name)
	}
	for _, name := range e.pendingDisable {
		b.DisableByName(name)
	}
	return b
}

// applyPendingAmendments installs the amendment changes staged by SetAmendments
// (whole-set replace) and EnableFeature/DisableFeature (deltas). Called at the
// start of every close(). Matches rippled where enableFeature/disableFeature
// modify config().features but the rules are only rebuilt when the ledger is
// closed.
// Reference: rippled Env.cpp: "Env::close() must be called for feature
// enable to take place."
func (e *TestEnv) applyPendingAmendments() {
	if len(e.pendingAmendments) == 0 && len(e.pendingEnable) == 0 && len(e.pendingDisable) == 0 {
		return
	}
	e.rulesBuilder = e.pendingRulesBuilder()
	e.pendingAmendments = nil
	e.pendingEnable = nil
	e.pendingDisable = nil
}

// applyWithRetry applies a set of transactions with multi-pass retry logic,
// matching rippled's applyTransactions() in BuildLedger.cpp. Returns any
// transactions that still failed after all retry passes.
//
// During retry passes (certainRetry=true), TapRETRY is set so that tec
// results from preclaim are NOT applied (likelyToClaimFee=false). On the
// final pass (certainRetry=false), TapRETRY is cleared so tec results
// ARE applied (fee consumed, sequence advanced).
// Reference: rippled BuildLedger.cpp lines 98-178
func (e *TestEnv) applyWithRetry(txns []tx.Transaction, heldHashes map[[32]byte]bool, maxRetryPasses, maxTotalPasses int) []tx.Transaction {
	remaining := txns
	certainRetry := true

	for pass := 0; pass < maxTotalPasses && len(remaining) > 0; pass++ {
		var retry []tx.Transaction
		changes := 0

		for _, txn := range remaining {
			h, _ := tx.ComputeTransactionHash(txn)
			result, applied := e.applyForReplay(txn, certainRetry, heldHashes[h])

			switch {
			case applied:
				changes++
			case isRetryable(result) || result.IsTec():
				// ter codes and non-applied tec codes (from TapRETRY)
				// are kept for retry on the next pass.
				retry = append(retry, txn)
			default:
				// Permanent failure (tef, tem, tel) — drop
			}
		}

		remaining = retry

		if changes == 0 && !certainRetry {
			break
		}
		if changes == 0 || pass >= maxRetryPasses {
			certainRetry = false
		}
	}

	return remaining
}

// Submit submits a transaction to the current open ledger.
// If the transaction doesn't have a sequence number set, it will be auto-filled
// from the account's current sequence in the ledger.
//
// When a TxQ is configured (via NewTestEnvWithTxQ), Submit routes through the
// TxQ for fee escalation and sequence-gap queuing. Transactions that cannot
// afford the escalated fee or have a future sequence are queued and return
// terQUEUED or terPRE_SEQ respectively.
func (e *TestEnv) Submit(transaction any) TxResult {
	txn := e.prepareSubmission(transaction)

	// If TxQ is enabled and not bypassed, route through TxQ for fee escalation and queuing.
	if e.txQueue != nil && !e.bypassTxQ {
		result := e.submitViaTxQ(txn)
		// A held tx whose sequence gap is now filled can replace a lower-fee
		// queued entry; do that before the next ledger drains the queue.
		e.retryHeldReplacementsIntoQueue()
		return result
	}

	return e.applyDirect(txn, tx.TapNONE)
}

// SubmitWithFlags applies a transaction directly with the supplied engine
// flags. It bypasses TxQ so tests can exercise one exact application pass.
func (e *TestEnv) SubmitWithFlags(transaction any, flags tx.ApplyFlags) TxResult {
	return e.applyDirect(e.prepareSubmission(transaction), flags)
}

func (e *TestEnv) prepareSubmission(transaction any) tx.Transaction {
	e.t.Helper()

	// Convert to tx.Transaction interface
	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
	}

	// Auto-fill the fee if not set. rippled requires sfFee on every STTx
	// (TxFormats.cpp: {sfFee, soeREQUIRED}); the submission layer always
	// populates it. Mirror that here so the engine never has to invent a fee.
	common := txn.GetCommon()
	if common.Fee == "" {
		config := e.engineConfig(e.ledger, engineConfigOpts{})
		common.Fee = formatUint64(sign.CalculateBaseFee(txn, e.ledger, config))
	}

	// Auto-fill sequence if not set (skip when using tickets)
	if common.Sequence == nil && common.TicketSequence == nil {
		// Look up the account to get current sequence
		_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(common.Account)
		if err != nil {
			e.t.Fatalf("Failed to decode account address: %v", err)
		}

		var id [20]byte
		copy(id[:], accountID)
		accountKey := keylet.Account(id)

		data, err := e.ledger.Read(accountKey)
		if err != nil || data == nil {
			e.t.Fatalf("Failed to read account for sequence auto-fill: %v", err)
		}

		accountRoot, err := state.ParseAccountRoot(data)
		if err != nil {
			e.t.Fatalf("Failed to parse account root: %v", err)
		}

		seq := accountRoot.Sequence
		common.Sequence = &seq
	}

	return txn
}

// toRippleTime converts a wall-clock time to seconds since the Ripple epoch,
// the form EngineConfig.ParentCloseTime and on-ledger time fields expect.
func toRippleTime(t time.Time) uint32 {
	return protocol.ToRippleTime(t)
}

// engineConfigOpts captures the per-call-site differences in engine setup. The
// shared fields (fees, reserves, rules, network, signature verification, parent
// hash) are filled by engineConfig; only these vary across the direct-apply,
// replay, TxQ-apply, accept, preflight, pseudo and signed-submit paths.
type engineConfigOpts struct {
	// parentCloseFromClock derives ParentCloseTime from the manual clock rather
	// than the ledger header. The direct-apply and replay paths use the header
	// so the initial apply and replay-on-close agree; the TxQ/preflight/pseudo/
	// signed paths use the clock.
	parentCloseFromClock     bool
	applicationCloseFromView bool
	openLedger               bool
	feeTrack                 bool
	enforceLoadFee           bool
	applyFlags               tx.ApplyFlags
	// verifySignatures forces signature verification even when the env runs in
	// the default no-verify mode (used by the SubmitSigned/MultiSigned paths).
	verifySignatures bool
}

// engineConfig builds the EngineConfig for applying a transaction against view,
// filling the fields shared by every apply path and taking the deliberate
// differences from opts. Centralizing construction keeps those differences
// explicit and stops accidental drift between the call sites.
func (e *TestEnv) engineConfig(view *ledger.Ledger, opts engineConfigOpts) tx.EngineConfig {
	parentCloseTime := toRippleTime(view.ParentCloseTime())
	if opts.parentCloseFromClock {
		parentCloseTime = e.NowRipple()
	}
	cfg := tx.EngineConfig{
		BaseFee:                   e.baseFee,
		ReserveBase:               e.reserveBase,
		ReserveIncrement:          e.reserveIncrement,
		LedgerSequence:            view.Sequence(),
		SkipSignatureVerification: !(e.VerifySignatures || opts.verifySignatures),
		Rules:                     e.rulesBuilder.Build(),
		NumberContextOverride:     e.numberContextOverride,
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 e.networkID,
		ParentHash:                view.ParentHash(),
		OpenLedger:                opts.openLedger,
		ViewOpen:                  e.viewOpen,
		EnforceLoadFee:            opts.enforceLoadFee,
		ApplyFlags:                opts.applyFlags,
	}
	if opts.applicationCloseFromView {
		cfg.ApplicationCloseTime = toRippleTime(view.CloseTime())
		cfg.ApplicationCloseTimeSet = true
	}
	if opts.feeTrack {
		cfg.FeeTrack = e.feeTrack
	}
	return cfg
}

type stagedApplyResult struct {
	tx.ApplyResult
	ApplyInvoked      bool
	InvariantsChecked bool
}

func (e *TestEnv) applyStaged(
	txn tx.Transaction,
	config tx.EngineConfig,
	transactionCount uint32,
	applyBatchInners bool,
) stagedApplyResult {
	staged, err := e.ledger.MutableSnapshotUnflushed()
	if err != nil {
		e.t.Fatalf("snapshot ledger for transaction apply: %v", err)
	}
	engine := txengine.NewEngine(staged, config)
	engine.SetBaseTxCount(transactionCount)
	var observed stagedApplyResult
	engine.SetApplyObserverForTest(func(phase txengine.ApplyPhase) {
		switch phase {
		case txengine.ApplyPhaseTransaction:
			observed.ApplyInvoked = true
		case txengine.ApplyPhaseInvariants:
			observed.InvariantsChecked = true
		}
	})
	if e.invariantViolationHook != nil {
		engine.SetInvariantViolationHookForTest(e.invariantViolationHook)
	}
	result := engine.Apply(txn)
	if applyBatchInners {
		result = engine.ApplyBatchInnerTransactions(context.Background(), txn, result)
	}
	if result.Applied {
		if err := e.ledger.AdoptState(staged); err != nil {
			e.t.Fatalf("commit staged transaction apply: %v", err)
		}
	}
	observed.ApplyResult = result
	return observed
}

func (e *TestEnv) trackTxQAppliedTransaction(txn tx.Transaction, result tx.ApplyResult) {
	if _, ok := txn.(tx.BatchInnerApplier); ok && result.Applied && result.Result.IsSuccess() {
		e.pendingBatchApplies = append(e.pendingBatchApplies, pendingBatchApply{transaction: txn, outer: result})
	}
	if e.inSetupMode {
		e.openLedgerSetupTxns = append(e.openLedgerSetupTxns, txn)
	} else {
		e.openLedgerUserTxns = append(e.openLedgerUserTxns, txn)
	}
}

// applyDirect applies a transaction directly without TxQ routing.
// This is the original Submit path.
func (e *TestEnv) applyDirect(txn tx.Transaction, flags tx.ApplyFlags) TxResult {
	e.t.Helper()

	// Header-based ParentCloseTime (not the clock) keeps the initial apply and
	// replay-on-close in agreement; see engineConfig.
	engineConfig := e.engineConfig(e.ledger, engineConfigOpts{
		openLedger: e.openLedger,
		feeTrack:   true,
		applyFlags: flags,
	})

	// Seed the engine's txCount from the env's tx-in-ledger counter so
	// metadata.TransactionIndex matches what rippled assigns. e.ledger
	// is the open ledger and env.Submit does NOT call AddTransactionWithMeta,
	// so e.ledger.TxCount() always returns 0 — use the env-maintained
	// counter that tracks applied txns across submits within a close window.
	// Without this seeding the 3rd of 3 sequential TrustSets from the
	// same account differed by 100 bytes vs rippled v2.6.2 — see
	// TestReproByteDiff_MultiTrustSetThreading.
	applyResult := e.applyStaged(txn, engineConfig, e.txInLedger, true)

	if applyResult.Applied {
		e.txInLedger += 1 + uint32(len(applyResult.AppliedInnerTransactions))
		e.closingTxTotal += e.recordFeeMetricTransactions(txn, applyResult.AppliedInnerTransactions)
	}

	// Track transaction for replay-on-close.
	// Only applied (tesSUCCESS, tec*) and retryable (ter*) transactions are
	// included in the replay set. Permanent failures (tem*, tef*, tel*) are
	// dropped — they never appear in rippled's canonical TX set.
	// Reference: rippled's open ledger tx map only contains applied txns.
	e.trackDirectTransaction(txn, applyResult.ApplyResult)

	return TxResult{
		Result:                   applyResult.Result,
		Code:                     applyResult.Result.String(),
		Success:                  applyResult.Result.IsSuccess(),
		Applied:                  applyResult.Applied,
		Fee:                      applyResult.Fee,
		ApplyInvoked:             applyResult.ApplyInvoked,
		InvariantsChecked:        applyResult.InvariantsChecked,
		Message:                  applyResult.Message,
		Metadata:                 applyResult.Metadata,
		AppliedInnerTransactions: applyResult.AppliedInnerTransactions,
	}
}

func (e *TestEnv) trackDirectTransaction(txn tx.Transaction, result tx.ApplyResult) {
	if result.Applied || isRetryable(result.Result) {
		if e.inSetupMode {
			e.openLedgerSetupTxns = append(e.openLedgerSetupTxns, txn)
		} else {
			e.openLedgerUserTxns = append(e.openLedgerUserTxns, txn)
		}
	}
	if e.replayOnClose && isRetryable(result.Result) {
		e.addHeldTransaction(txn.GetCommon().Account, txn)
	}
}

// submitViaTxQ routes a transaction through the TxQ for fee escalation
// and sequence-gap queuing.
// Reference: rippled NetworkOPs::processTransaction -> TxQ::apply -> NetworkOPs::apply
func (e *TestEnv) submitViaTxQ(txn tx.Transaction) TxResult {
	e.t.Helper()

	common := txn.GetCommon()
	accountAddr := common.Account

	// Resolve account ID
	var accountID [20]byte
	_, acctBytes, err := addresscodec.DecodeClassicAddressToAccountID(accountAddr)
	if err != nil {
		e.t.Fatalf("submitViaTxQ: failed to decode account: %v", err)
	}
	copy(accountID[:], acctBytes)

	// Compute a deterministic txID from the transaction fields.
	txID := e.computeTxID(txn)

	// Build the ApplyContext adapter
	ctx := &testTxQApplyContext{
		env: e,
	}

	// Route through TxQ
	result := e.txQueue.Apply(ctx, txn, txID, accountID)

	if result.Applied {
		// After successful apply, pop and retry held transactions for this
		// account. This mirrors rippled's NetworkOPs::apply which calls
		// popAcctTransaction after tesSUCCESS.
		//
		// We do NOT drain the whole TxQ here: rippled only runs TxQ::accept
		// when a new open ledger is built (on close), never mid-ledger after
		// an individual apply. Draining mid-window would let a queued tx that
		// failed the open-ledger fee floor under load re-apply as soon as the
		// load dropped, instead of waiting for the next close — diverging from
		// rippled (see TxQ_test.cpp "clear queue failure (load)"). The
		// close-time drain in Close()/CloseWithTimeLeap() handles queued txns.
		e.retryHeldTransactions(accountAddr)

		return TxResult{
			Result:  result.Result,
			Code:    result.Result.String(),
			Success: result.Result.IsSuccess(),
			Applied: true,
			Message: result.Result.String(),
		}
	}

	if result.Queued {
		// Queued txns are owned by the TxQ, so they join the local-tx set (drained
		// only at close), not the held set (retried mid-window): retrying a queued
		// entry mid-window would let it bypass the queue into the open ledger.
		e.addLocalTransaction(accountAddr, txn)

		return TxResult{
			Result:  ter.TerQUEUED,
			Code:    ter.TerQUEUED.String(),
			Success: false,
			Queued:  true,
			Message: "Transaction queued",
		}
	}

	// A retryable (ter*) result means the transaction could not be applied yet
	// because of a sequence gap; hold it for the mid-window retry that runs when
	// the gap-filling transaction applies.
	if isRetryable(result.Result) {
		e.addHeldTransaction(accountAddr, txn)
	} else if isTelLocal(result.Result) {
		// A tel (local) result joins the local-tx set: rippled replays every
		// locally-submitted tx at the next open-ledger build regardless of result
		// code, so these apply only at close, never mid-window.
		e.addLocalTransaction(accountAddr, txn)
	}

	return TxResult{
		Result:  result.Result,
		Code:    result.Result.String(),
		Success: false,
		Message: result.Result.String(),
	}
}

// isRetryable returns true if the transaction result indicates the transaction
// might succeed later (e.g., terPRE_SEQ, terINSUF_FEE_B).
// Reference: rippled isTerRetry()
func isRetryable(result ter.Result) bool {
	return result >= -99 && result < 0
}

// isTelLocal returns true if the result is a tel (local error) code.
// tel codes are in the range -399 to -300.
// Reference: rippled TER.h telLOCAL_ERROR = -399, telCAN_NOT_QUEUE = -381
func isTelLocal(result ter.Result) bool {
	return result >= -399 && result <= -300
}

// addHeldTransaction records a sequence-gap-held (ter*) transaction for the
// mid-window retry.
func (e *TestEnv) addHeldTransaction(accountAddr string, txn tx.Transaction) {
	if e.heldTxns == nil {
		e.heldTxns = make(map[string][]tx.Transaction)
	}
	e.heldTxns[accountAddr] = append(e.heldTxns[accountAddr], txn)
}

// addLocalTransaction records a TxQ-owned transaction (queued or tel-rejected)
// in the local-tx set, replayed only at the close-time open-ledger rebuild.
func (e *TestEnv) addLocalTransaction(accountAddr string, txn tx.Transaction) {
	if e.localTxns == nil {
		e.localTxns = make(map[string][]tx.Transaction)
	}
	e.localTxns[accountAddr] = append(e.localTxns[accountAddr], txn)
}

// retryAllHeldViaTxQ retries every held and local transaction through the TxQ
// after the close-time drain, mirroring rippled's OpenLedger::accept: it iterates
// localTxs and calls TxQ::apply once the queue has drained, so previously rejected
// txns can re-queue or apply. Both sets are replayed here; the local set only at
// this close-time point, never mid-window.
// Reference: rippled OpenLedger.cpp:117-118.
func (e *TestEnv) retryAllHeldViaTxQ() {
	if len(e.heldTxns) == 0 && len(e.localTxns) == 0 {
		return
	}

	// Collect all held and local transactions from all accounts.
	var allHeld []tx.Transaction
	for _, txns := range e.heldTxns {
		allHeld = append(allHeld, txns...)
	}
	for _, txns := range e.localTxns {
		allHeld = append(allHeld, txns...)
	}

	// Clear both sets before retrying (re-added if they fail again with ter/tel).
	e.heldTxns = nil
	e.localTxns = nil

	// Sort by canonical order (account, sequence) for deterministic processing
	sortCanonical(allHeld)

	for _, heldTxn := range allHeld {
		e.submitViaTxQ(heldTxn)
	}
}

// retryHeldReplacementsIntoQueue re-applies held local transactions that would
// REPLACE an already-queued entry (same account + SeqProxy) through the TxQ,
// without disturbing the held set. A transaction that earlier failed with a
// sequence gap (telCAN_NOT_QUEUE) and was held can, once the gap is filled by
// other queued entries, become a valid higher-fee replacement of the entry at
// its sequence. rippled re-applies m_localTX through TxQ::apply during
// open-ledger processing (NetworkOPs::apply -> openLedger().modify), so the
// replacement updates the queue BEFORE the queue is drained on close. Without
// this, the lower-fee entry drains first and the higher-fee held tx arrives too
// late (tefPAST_SEQ), under-charging the account.
//
// This pass intentionally handles ONLY replacements: held transactions that are
// not already represented in the queue are left for retryAllHeldViaTxQ (run
// after the drain), so they only enter once the drain frees space. Both the
// sequence-gap held set and the TxQ-owned local set are scanned, since a
// higher-fee replacement may have been recorded in either.
func (e *TestEnv) retryHeldReplacementsIntoQueue() {
	if len(e.heldTxns) == 0 && len(e.localTxns) == 0 {
		return
	}

	type replacement struct {
		accountID [20]byte
		txn       tx.Transaction
	}
	var replacements []replacement

	scan := func(accountAddr string, txns []tx.Transaction) {
		_, acctBytes, err := addresscodec.DecodeClassicAddressToAccountID(accountAddr)
		if err != nil {
			return
		}
		var accountID [20]byte
		copy(accountID[:], acctBytes)

		queued := e.txQueue.AccountTxs(accountID)
		if len(queued) == 0 {
			return
		}

		for _, txn := range txns {
			sp, ok := heldSeqProxy(txn)
			if !ok {
				continue
			}
			heldFeeLevel := e.txFeeLevel(txn)
			for _, qc := range queued {
				if qc.SeqProxy != sp {
					continue
				}
				// Only a genuinely higher-fee submission replaces the queued
				// entry. A held copy carrying the same (or lower) fee IS the
				// entry already in the queue; re-applying it would let it bypass
				// the queue straight into the open ledger once the load floor
				// drops — something rippled never does mid-window. Such entries
				// must wait for the close-time drain.
				if heldFeeLevel > qc.FeeLevel {
					replacements = append(replacements, replacement{accountID, txn})
				}
				break
			}
		}
	}

	for accountAddr, txns := range e.heldTxns {
		scan(accountAddr, txns)
	}
	for accountAddr, txns := range e.localTxns {
		scan(accountAddr, txns)
	}

	// Apply directly through the TxQ (not submitViaTxQ) so the held set is not
	// mutated: TxQ.Apply replaces the queued entry when the fee is high enough,
	// otherwise returns telCAN_NOT_QUEUE_FEE and leaves the queue unchanged.
	ctx := &testTxQApplyContext{env: e}
	for _, r := range replacements {
		txID := e.computeTxID(r.txn)
		e.txQueue.Apply(ctx, r.txn, txID, r.accountID)
	}
}

func (e *TestEnv) txFeeLevel(txn tx.Transaction) txq.FeeLevel {
	common := txn.GetCommon()
	if common == nil {
		return 0
	}
	feePaid, _ := strconv.ParseUint(common.Fee, 10, 64)
	config := e.engineConfig(e.ledger, engineConfigOpts{})
	baseFee := sign.CalculateBaseFee(txn, e.ledger, config)
	defaultBaseFee := sign.CalculateDefaultBaseFee(txn, config)
	return txq.ToFeeLevelWithDefaultBaseFee(feePaid, baseFee, defaultBaseFee)
}

// heldSeqProxy returns the SeqProxy a transaction would occupy in the TxQ.
func heldSeqProxy(txn tx.Transaction) (txq.SeqProxy, bool) {
	common := txn.GetCommon()
	if common == nil {
		return txq.SeqProxy{}, false
	}
	if common.TicketSequence != nil && *common.TicketSequence != 0 {
		return txq.NewSeqProxyTicket(*common.TicketSequence), true
	}
	if common.Sequence != nil {
		return txq.NewSeqProxySequence(*common.Sequence), true
	}
	return txq.SeqProxy{}, false
}

// retryHeldTransactions pops and retries held transactions for an account.
// This is called after a successful transaction to try applying transactions
// that may have previously failed with terPRE_SEQ.
// Reference: rippled NetworkOPs::apply -> popAcctTransaction loop
func (e *TestEnv) retryHeldTransactions(accountAddr string) {
	if e.heldTxns == nil {
		return
	}

	held, exists := e.heldTxns[accountAddr]
	if !exists || len(held) == 0 {
		return
	}

	// Sort held transactions by sequence number (lowest first)
	sortHeldBySequence(held)

	// Clear the held list for this account before retrying
	// (retried transactions may get re-added if they fail again)
	delete(e.heldTxns, accountAddr)

	for _, heldTxn := range held {
		// Retry by routing through the TxQ again
		result := e.submitViaTxQ(heldTxn)
		if result.Success {
			// Successfully applied, continue with next held transaction
			continue
		}
		// If it wasn't applied and wasn't re-held (e.g., permanent failure),
		// just drop it
	}
}

// drainQueue attempts to apply queued transactions from the TxQ.
// This is called after a successful apply to drain fee-escalation-queued
// transactions that may now meet the fee requirements.
// Reference: rippled TxQ::accept called when new open ledger is created.
func (e *TestEnv) drainQueue() {
	if e.txQueue == nil || e.txQueue.Size() == 0 {
		return
	}

	ctx := &testTxQAcceptContext{
		env: e,
	}

	// Keep trying until no more progress is made
	for e.txQueue.Accept(ctx) {
		// Accept returns true if any transactions were applied.
		// We keep looping because applying one transaction might unblock others.
	}
}

// applyForReplay applies a single transaction during the replay-on-close
// process. When certainRetry is true, TapRETRY is set so that tec results
// are not applied (matching rippled's retry pass behavior). When held is true
// the txn was carried over from a prior ledger; rippled retries held txns on
// the open ledger, so its fee-adequacy is judged with view.open()==true.
// Returns the result code and whether the transaction was actually applied.
func (e *TestEnv) applyForReplay(txn tx.Transaction, certainRetry, held bool) (ter.Result, bool) {
	// Header-based ParentCloseTime matches applyDirect so time-dependent checks
	// produce the same result during initial apply and during replay.
	opts := engineConfigOpts{
		applicationCloseFromView: true,
		openLedger:               e.openLedger || held,
	}
	if certainRetry {
		opts.applyFlags = tx.TapRETRY
	}
	engineConfig := e.engineConfig(e.ledger, opts)

	// Seed the engine's txCount from the env's tx-in-ledger counter so
	// metadata.TransactionIndex matches what rippled assigns. e.ledger
	// is the open ledger and env.Submit does NOT call AddTransactionWithMeta,
	// so e.ledger.TxCount() always returns 0 — use the env-maintained
	// counter that tracks applied txns across submits within a close window.
	// Without this seeding the 3rd of 3 sequential TrustSets from the
	// same account differed by 100 bytes vs rippled v2.6.2 — see
	// TestReproByteDiff_MultiTrustSetThreading.
	applyResult := e.applyStaged(txn, engineConfig, e.txInLedger, true)

	if applyResult.Applied {
		e.txInLedger += 1 + uint32(len(applyResult.AppliedInnerTransactions))
		e.closingTxTotal += e.recordFeeMetricTransactions(txn, applyResult.AppliedInnerTransactions)
	}

	return applyResult.Result, applyResult.Applied
}

// sortCanonical sorts transactions in canonical order matching rippled's
// CanonicalTXSet. The order is: (account address, sequence proxy, txID).
// For simplicity in the test env, we use (account, sequence/ticketSeq).
// Reference: rippled CanonicalTXSet.cpp operator<
func sortCanonical(txns []tx.Transaction) {
	sort.SliceStable(txns, func(i, j int) bool {
		ci := txns[i].GetCommon()
		cj := txns[j].GetCommon()

		// Primary: account address (lexicographic)
		if ci.Account != cj.Account {
			return ci.Account < cj.Account
		}

		// Secondary: sequence proxy (sequence-typed sorts before ticket-typed)
		seqI := ci.SeqProxyKey()
		seqJ := cj.SeqProxyKey()
		if seqI != seqJ {
			return seqI < seqJ
		}

		// Tertiary: fall back to tx type as a tiebreaker
		return txns[i].TxType() < txns[j].TxType()
	})
}

// canonicalEntry holds pre-computed data for canonical sorting of a transaction.
type canonicalEntry struct {
	txn      tx.Transaction
	hash     [32]byte
	account  [20]byte
	seqProxy uint64
}

// buildCanonicalEntries pre-computes hashes, account IDs, and sequences for
// a set of transactions, preparing them for canonical sorting.
func buildCanonicalEntries(txns []tx.Transaction) []canonicalEntry {
	entries := make([]canonicalEntry, len(txns))
	for i, txn := range txns {
		h, _ := tx.ComputeTransactionHash(txn)

		common := txn.GetCommon()
		var accountID [20]byte
		_, acctBytes, _ := addresscodec.DecodeClassicAddressToAccountID(common.Account)
		copy(accountID[:], acctBytes)

		entries[i] = canonicalEntry{
			txn:      txn,
			hash:     h,
			account:  accountID,
			seqProxy: common.SeqProxyKey(),
		}
	}
	return entries
}

// applyCanonicalSort sorts transactions in-place using the CanonicalTXSet
// ordering with the given salt. The sort key is (accountKey XOR salt, sequence, txHash).
// Reference: rippled CanonicalTXSet.cpp
func applyCanonicalSort(txns []tx.Transaction, entries []canonicalEntry, salt [32]byte) {
	// Pre-compute account keys: accountID XOR salt (32 bytes).
	// Mirrors rippled CanonicalTXSet::accountKey(): copy 20-byte account into
	// 32-byte uint256 (zero-padded), then XOR with full 32-byte salt.
	type sortEntry struct {
		accountKey [32]byte
		idx        int
	}
	sortEntries := make([]sortEntry, len(entries))
	for i, e := range entries {
		var key [32]byte
		copy(key[:20], e.account[:])
		for j := range 32 {
			key[j] ^= salt[j]
		}
		sortEntries[i] = sortEntry{accountKey: key, idx: i}
	}

	sort.SliceStable(sortEntries, func(i, j int) bool {
		ei, ej := sortEntries[i], sortEntries[j]
		cmp := bytes.Compare(ei.accountKey[:], ej.accountKey[:])
		if cmp != 0 {
			return cmp < 0
		}
		if entries[ei.idx].seqProxy != entries[ej.idx].seqProxy {
			return entries[ei.idx].seqProxy < entries[ej.idx].seqProxy
		}
		return bytes.Compare(entries[ei.idx].hash[:], entries[ej.idx].hash[:]) < 0
	})

	// Write sorted results back to the slice
	sorted := make([]tx.Transaction, len(txns))
	for i, se := range sortEntries {
		sorted[i] = entries[se.idx].txn
	}
	copy(txns, sorted)
}

// sortCanonicalSalted sorts transactions using the production CanonicalTXSet
// ordering from rippled. The sort key is (accountKey, sequence, txHash) where
// accountKey = accountID XOR salt. The salt is the SHAMap root hash built from
// the transaction set, matching rippled's RCLConsensus.cpp onClose().
// Reference: rippled CanonicalTXSet.cpp, internal/ledger/service/canonical_txset.go
func sortCanonicalSalted(txns []tx.Transaction, extraSaltTxns ...[]tx.Transaction) {
	if len(txns) <= 1 {
		return
	}

	entries := buildCanonicalEntries(txns)

	// Compute salt: SHAMap root hash of the transaction set.
	// Matches rippled's CanonicalTXSet salt (RCLConsensus.cpp onClose).
	// We compute the tree hash manually instead of using the SHAMap struct
	// because the SHAMap's Hash() returns stale cached values after insertion.
	//
	// The transaction SHAMap uses leaf hash = SHA512Half(TXN\0 + blob),
	// which equals the transaction hash (the key). Inner nodes use
	// SHA512Half(MIN\0 + 16 × child_hash).
	hashes := make([][32]byte, 0, len(entries))
	for _, e := range entries {
		hashes = append(hashes, e.hash)
	}
	// Include extra transactions (e.g., setup txns) in the salt computation.
	// In rippled, the salt is the SHAMap root hash of ALL open-ledger transactions,
	// including fund/trust setup. The extraSaltTxns parameter allows callers to
	// include these additional transactions so the sort order matches rippled's.
	// Reference: rippled RCLConsensus.cpp onClose() — builds SHAMap from ALL txs.
	for _, extra := range extraSaltTxns {
		for _, txn := range extra {
			h, err := tx.ComputeTransactionHash(txn)
			if err == nil {
				hashes = append(hashes, h)
			}
		}
	}
	salt := computeTxSetHash(hashes)
	applyCanonicalSort(txns, entries, salt)
}

// computeTxSetHash computes the SHAMap root hash for a set of transaction
// hashes, matching rippled's SHAMap(TypeTransaction) behavior. Each hash is
// both the item key and the leaf hash (since SHA512Half(TXN\0+data) = txHash).
// The tree uses 16-ary branching on key nibbles. Inner node hash =
// SHA512Half(MIN\0 + 16 × child_hash), where empty children contribute zeros.
// Reference: rippled SHAMapTxLeafNode::updateHash(), SHAMapInnerNode::updateHash()
// txSetTreeNode represents a node in the 16-ary radix tree for computing
// the SHAMap root hash of a transaction set.
type txSetTreeNode struct {
	isLeaf   bool
	hash     [32]byte           // leaf: tx hash; inner: computed
	children [16]*txSetTreeNode // inner only
}

func computeTxSetHash(hashes [][32]byte) [32]byte {
	if len(hashes) == 0 {
		return [32]byte{}
	}

	// Insert all hashes into a 16-ary radix tree
	root := &txSetTreeNode{}

	for _, h := range hashes {
		insertIntoTree(root, h, 0)
	}

	// Compute hashes bottom-up
	computeTreeHash(root)
	return root.hash
}

// insertIntoTree inserts a leaf hash into the radix tree at the given depth.
func insertIntoTree(node *txSetTreeNode, h [32]byte, depth int) {
	if depth >= 64 { // 32 bytes × 2 nibbles = 64 levels max
		return
	}

	nibble := getNibble(h, depth)

	if node.children[nibble] == nil {
		// Empty slot — place leaf here
		node.children[nibble] = &txSetTreeNode{isLeaf: true, hash: h}
		return
	}

	child := node.children[nibble]
	if child.isLeaf {
		if child.hash == h {
			return // duplicate
		}
		// Collision — split: create inner node, re-insert both
		inner := &txSetTreeNode{}
		insertIntoTree(inner, child.hash, depth+1)
		insertIntoTree(inner, h, depth+1)
		node.children[nibble] = inner
		return
	}

	// Existing inner node — recurse
	insertIntoTree(child, h, depth+1)
}

// computeTreeHash recursively computes inner node hashes (post-order).
// Leaf hashes are already set (= transaction hash).
// Inner hash = SHA512Half(MIN\0 + 16 × child_hash).
func computeTreeHash(node *txSetTreeNode) {
	if node.isLeaf {
		return // leaf hash is already the tx hash
	}

	// Compute children first
	for i := range 16 {
		if node.children[i] != nil {
			computeTreeHash(node.children[i])
		}
	}

	// Inner node hash: MIN\0 prefix + 16 child hashes
	minPrefix := [4]byte{'M', 'I', 'N', 0x00}
	h := sha512.New()
	h.Write(minPrefix[:])
	for i := range 16 {
		if node.children[i] != nil {
			childHash := node.children[i].hash
			h.Write(childHash[:])
		} else {
			h.Write(make([]byte, 32)) // zero hash for empty slot
		}
	}
	full := h.Sum(nil)
	copy(node.hash[:], full[:32])
}

// getNibble returns the nibble (4-bit value) at the given position in a hash.
// Position 0 is the high nibble of byte 0, position 1 is the low nibble, etc.
func getNibble(h [32]byte, pos int) int {
	byteIdx := pos / 2
	if pos%2 == 0 {
		return int(h[byteIdx] >> 4)
	}
	return int(h[byteIdx] & 0x0F)
}

// sortHeldBySequence sorts transactions by SeqProxy key (sequence-typed first,
// then ticket-typed, each ordered by value), matching the canonical ordering
// used at consensus close.
func sortHeldBySequence(txns []tx.Transaction) {
	sort.SliceStable(txns, func(i, j int) bool {
		return txns[i].GetCommon().SeqProxyKey() < txns[j].GetCommon().SeqProxyKey()
	})
}

func (e *TestEnv) computeTxID(txn tx.Transaction) [32]byte {
	id, err := tx.ComputeTransactionHash(txn)
	if err != nil {
		e.t.Fatalf("compute canonical transaction hash: %v", err)
	}
	return id
}

// testClosedLedgerContext implements txq.ClosedLedgerContext for the test environment.
type testClosedLedgerContext struct {
	ledgerSeq uint32
	feeLevels []txq.FeeLevel
}

func (c *testClosedLedgerContext) GetLedgerSequence() uint32               { return c.ledgerSeq }
func (c *testClosedLedgerContext) GetTransactionFeeLevels() []txq.FeeLevel { return c.feeLevels }

// testTxQApplyContext implements txq.ApplyContext for the test environment.
//
// When view is non-nil the context is operating as a sandbox child: applies
// target that isolated snapshot instead of env.ledger, and the env counter
// mutations (txInLedger / closingTxTotal / fee-level metrics) are deferred
// into accum so they roll back with the sandbox unless Commit is called.
type testTxQApplyContext struct {
	env   *TestEnv
	view  *ledger.Ledger
	accum *txqSandboxAccum
}

// txqSandboxAccum buffers the env-counter side effects produced while applying
// a batch into a sandbox, so they take effect only on Commit.
type txqSandboxAccum struct {
	txInLedger          uint32
	closingTxTotal      uint32
	feeLevelTxns        []tx.Transaction
	appliedTransactions []pendingBatchApply
}

// applyView returns the ledger this context applies transactions to: the
// sandbox snapshot when set, otherwise the live env ledger.
func (c *testTxQApplyContext) applyView() *ledger.Ledger {
	if c.view != nil {
		return c.view
	}
	return c.env.ledger
}

func (c *testTxQApplyContext) GetAccountSequence(account [20]byte) (uint32, error) {
	accountRoot, err := state.ReadAccountRoot(c.env.ledger, account)
	if err != nil || accountRoot == nil {
		return 0, err
	}
	return accountRoot.Sequence, nil
}

func (c *testTxQApplyContext) AccountExists(account [20]byte) bool {
	accountKey := keylet.Account(account)
	exists, err := c.env.ledger.Exists(accountKey)
	return err == nil && exists
}

func (c *testTxQApplyContext) TicketExists(account [20]byte, ticketSeq uint32) bool {
	ticketKey := keylet.Ticket(account, ticketSeq)
	exists, err := c.env.ledger.Exists(ticketKey)
	return err == nil && exists
}

func (c *testTxQApplyContext) GetAccountBalance(account [20]byte) (uint64, error) {
	accountRoot, err := state.ReadAccountRoot(c.env.ledger, account)
	if err != nil || accountRoot == nil {
		return 0, err
	}
	return accountRoot.Balance, nil
}

func (c *testTxQApplyContext) GetAccountReserve(ownerCount uint32) uint64 {
	return c.env.reserveBase + uint64(ownerCount)*c.env.reserveIncrement
}

func (c *testTxQApplyContext) GetReferenceFee() uint64 {
	return c.env.baseFee
}

func (c *testTxQApplyContext) GetBaseFees(txn tx.Transaction) (uint64, uint64) {
	config := c.env.engineConfig(c.env.ledger, engineConfigOpts{})
	return sign.CalculateBaseFee(txn, c.env.ledger, config), sign.CalculateDefaultBaseFee(txn, config)
}

func (c *testTxQApplyContext) GetTxInLedger() uint32 {
	return c.env.txInLedger
}

func (c *testTxQApplyContext) GetLedgerSequence() uint32 {
	return c.env.ledger.Sequence()
}

func (c *testTxQApplyContext) ApplyTransaction(txn tx.Transaction) (ter.Result, bool) {
	return c.ApplyTransactionWithFlags(txn, tx.TapNONE)
}

func (c *testTxQApplyContext) ApplyTransactionWithFlags(txn tx.Transaction, flags tx.ApplyFlags) (ter.Result, bool) {
	// Transactions applied through the TxQ must NOT check open-ledger fee
	// adequacy. In rippled, TxQ::tryDirectApply calls ripple::apply() with
	// tapNONE flags (NOT tapOPEN_LEDGER). The TxQ's own fee-level check is
	// sufficient; the engine's baseFee floor would incorrectly reject
	// fee=0 transactions that have already passed fee-level validation.
	// Reference: rippled NetworkOPsImp::apply (flags = tapNONE),
	//   TxQ::tryDirectApply (uses same flags as NetworkOPs),
	//   TxQ::tryClearAccountQueueUpThruTx (uses stored MaybeTx flags)
	view := c.applyView()
	engineConfig := c.env.engineConfig(view, engineConfigOpts{
		parentCloseFromClock: true,
		feeTrack:             true,
		enforceLoadFee:       true,
	})
	engineConfig.ApplyFlags = flags

	engine := txengine.NewEngine(view, engineConfig)
	applyResult := engine.Apply(txn)

	applied := applyResult.Result.IsApplied()
	if applied {
		feeMetricTxns := feeMetricTransactions(txn, nil)
		const appliedCount = uint32(1)
		if c.accum != nil {
			// Sandbox child: defer the env-counter side effects until Commit.
			c.accum.txInLedger += appliedCount
			c.accum.closingTxTotal += uint32(len(feeMetricTxns))
			c.accum.feeLevelTxns = append(c.accum.feeLevelTxns, feeMetricTxns...)
			c.accum.appliedTransactions = append(c.accum.appliedTransactions, pendingBatchApply{
				transaction: txn,
				outer:       applyResult,
			})
		} else {
			c.env.txInLedger += appliedCount
			c.env.closingTxTotal += c.env.recordFeeMetricTransactions(txn, nil)
			c.env.trackTxQAppliedTransaction(txn, applyResult)
		}
	}
	return applyResult.Result, applied
}

// NewSandbox returns an isolated child context over a mutable snapshot of the
// context's current view, mirroring the production TxqAdapter sandbox. Applies
// land on the snapshot and env-counter mutations are buffered until Commit.
func (c *testTxQApplyContext) NewSandbox() (txq.SandboxContext, error) {
	snap, err := c.applyView().MutableSnapshot()
	if err != nil {
		return nil, err
	}
	accum := &txqSandboxAccum{}
	child := &testTxQApplyContext{env: c.env, view: snap, accum: accum}
	return &testTxQSandbox{parent: c, child: child, snap: snap, accum: accum}, nil
}

// testTxQSandbox implements txq.SandboxContext for the test environment.
type testTxQSandbox struct {
	parent *testTxQApplyContext
	child  *testTxQApplyContext
	snap   *ledger.Ledger
	accum  *txqSandboxAccum
}

func (s *testTxQSandbox) ApplyTransaction(txn tx.Transaction) (ter.Result, bool) {
	return s.child.ApplyTransaction(txn)
}

func (s *testTxQSandbox) ApplyTransactionWithFlags(txn tx.Transaction, flags tx.ApplyFlags) (ter.Result, bool) {
	return s.child.ApplyTransactionWithFlags(txn, flags)
}

// Commit folds the sandbox snapshot back into the parent view and applies the
// buffered env-counter side effects.
func (s *testTxQSandbox) Commit() error {
	if err := s.parent.applyView().AdoptState(s.snap); err != nil {
		return err
	}
	s.parent.env.txInLedger += s.accum.txInLedger
	s.parent.env.closingTxTotal += s.accum.closingTxTotal
	s.parent.env.closingFeeTransactions = append(s.parent.env.closingFeeTransactions, s.accum.feeLevelTxns...)
	for _, applied := range s.accum.appliedTransactions {
		s.parent.env.trackTxQAppliedTransaction(applied.transaction, applied.outer)
	}
	return nil
}

func (c *testTxQApplyContext) PreflightTransaction(txn tx.Transaction) ter.Result {
	return c.PreflightTransactionWithFlags(txn, c.GetApplyFlags())
}

func (c *testTxQApplyContext) PreflightTransactionWithFlags(txn tx.Transaction, flags tx.ApplyFlags) ter.Result {
	// Mirror the engine config used by ApplyTransaction so TxQ admission
	// preflight (rippled TxQ.cpp:743-745) matches the direct-apply path.
	view := c.applyView()
	engineConfig := c.env.engineConfig(view, engineConfigOpts{
		parentCloseFromClock: true,
		feeTrack:             true,
	})
	engineConfig.ApplyFlags = flags
	return txengine.NewEngine(view, engineConfig).Preflight(txn)
}

func (c *testTxQApplyContext) PreclaimTransaction(txn tx.Transaction, account [20]byte, adjustedBalance uint64, adjustedSeq uint32) ter.Result {
	// Simplified simulation of rippled's multiTxn preclaim path (TxQ.cpp:1167-1170).
	// rippled creates a modified view with adjusted balance and sequence,
	// then runs a full preclaim(). We only check the checkFee portion here
	// (terINSUF_FEE_B when adjusted balance < fee), which is the primary
	// check that differs with an adjusted view. Other preclaim failures
	// (e.g., tecINSUFFICIENT_RESERVE) are not yet simulated.
	// Reference: rippled Transactor::checkFee (Transactor.cpp line ~310)
	common := txn.GetCommon()
	if common == nil {
		return ter.TefINTERNAL
	}

	fee, _ := strconv.ParseUint(common.Fee, 10, 64)

	if adjustedBalance < fee {
		return ter.TerINSUF_FEE_B
	}

	// If preclaim passes, return 0 (tesSUCCESS) to indicate likely to claim fee.
	return 0
}

func (c *testTxQApplyContext) PreclaimTransactionWithFlags(txn tx.Transaction, account [20]byte, adjustedBalance uint64, adjustedSeq uint32, _ tx.ApplyFlags) ter.Result {
	return c.PreclaimTransaction(txn, account, adjustedBalance, adjustedSeq)
}

// GetApplyFlags returns the engine ApplyFlags currently driving the
// test env. The test env stores the flag on TestEnv.txQApplyFlags and
// resets it after each Submit; default is 0 so TxQ admission behaves
// as if no flag is set.
func (c *testTxQApplyContext) GetApplyFlags() tx.ApplyFlags {
	return c.env.txQApplyFlags
}

func (c *testTxQApplyContext) RulesIdentity() *amendment.Rules {
	return c.env.rulesBuilder.Build()
}

// testTxQAcceptContext implements txq.AcceptContext for draining the queue.
type testTxQAcceptContext struct {
	env *TestEnv
}

func (c *testTxQAcceptContext) GetTxInLedger() uint32 {
	return c.env.txInLedger
}

func (c *testTxQAcceptContext) GetAccountSequence(account [20]byte) uint32 {
	accountKey := keylet.Account(account)
	data, err := c.env.ledger.Read(accountKey)
	if err != nil || data == nil {
		return 0
	}
	accountRoot, err := state.ParseAccountRoot(data)
	if err != nil {
		return 0
	}
	return accountRoot.Sequence
}

func (c *testTxQAcceptContext) ApplyTransaction(txn tx.Transaction) (ter.Result, bool) {
	return c.ApplyTransactionWithFlags(txn, tx.TapNONE)
}

func (c *testTxQAcceptContext) ApplyTransactionWithFlags(txn tx.Transaction, flags tx.ApplyFlags) (ter.Result, bool) {
	// TxQ accept (drain on close) applies queued transactions with tapNONE
	// flags in rippled — NOT tapOPEN_LEDGER. This prevents the engine's
	// fee adequacy check from rejecting fee=0 transactions that were
	// already validated by the TxQ's fee-level mechanism.
	// Reference: rippled TxQ::accept calls MaybeTx::apply with stored
	//   flags (which have tapRETRY cleared but NOT tapOPEN_LEDGER set)
	engineConfig := c.env.engineConfig(c.env.ledger, engineConfigOpts{
		parentCloseFromClock: true,
		feeTrack:             true,
		enforceLoadFee:       true,
	})
	engineConfig.ApplyFlags = flags

	engine := txengine.NewEngine(c.env.ledger, engineConfig)
	applyResult := engine.Apply(txn)

	applied := applyResult.Result.IsApplied()
	if applied {
		c.env.txInLedger++
		c.env.closingTxTotal += c.env.recordFeeMetricTransactions(txn, nil)
		c.env.trackTxQAppliedTransaction(txn, applyResult)
	}
	return applyResult.Result, applied
}

func (c *testTxQAcceptContext) PreflightTransactionWithFlags(txn tx.Transaction, flags tx.ApplyFlags) ter.Result {
	engineConfig := c.env.engineConfig(c.env.ledger, engineConfigOpts{parentCloseFromClock: true, feeTrack: true})
	engineConfig.ApplyFlags = flags
	return txengine.NewEngine(c.env.ledger, engineConfig).Preflight(txn)
}

func (c *testTxQAcceptContext) RulesIdentity() *amendment.Rules {
	return c.env.rulesBuilder.Build()
}

func feeMetricTransactions(
	txn tx.Transaction,
	inners []tx.AppliedInnerTransaction,
) []tx.Transaction {
	transactions := make([]tx.Transaction, 0, 1+len(inners))
	if txn != nil && txn.GetCommon() != nil {
		transactions = append(transactions, txn)
	}
	for _, inner := range inners {
		if inner.Transaction != nil {
			transactions = append(transactions, inner.Transaction)
		}
	}
	return transactions
}

func (e *TestEnv) recordFeeMetricTransactions(
	txn tx.Transaction,
	inners []tx.AppliedInnerTransaction,
) uint32 {
	transactions := feeMetricTransactions(txn, inners)
	e.closingFeeTransactions = append(e.closingFeeTransactions, transactions...)
	return uint32(len(transactions))
}

func (e *TestEnv) closingLedgerFeeLevels() []txq.FeeLevel {
	feeLevels := make([]txq.FeeLevel, 0, len(e.closingFeeTransactions))
	for _, txn := range e.closingFeeTransactions {
		feeLevels = append(feeLevels, e.txFeeLevel(txn))
	}
	return feeLevels
}

func (c *testTxQAcceptContext) GetParentHash() [32]byte {
	return c.env.ledger.ParentHash()
}

// EnableOpenLedgerReplay enables the open-ledger consensus replay behavior.
// When enabled, Close() rebuilds the closed ledger from the parent closed
// ledger by replaying all tracked transactions in canonical order with
// retry passes. This matches rippled's standalone consensus simulation.
//
// Use this for tests that depend on:
//   - terPRE_SEQ transactions being retried after close
//   - tec transactions being re-applied from a clean state after
//     prerequisite objects are created by batch transactions
//
// Must be called before any Submit calls in the test.
// Reference: rippled BuildLedger.cpp applyTransactions()
func (e *TestEnv) EnableOpenLedgerReplay() {
	e.replayOnClose = true
	// If lastClosedLedger hasn't been set yet (no Close() called before
	// this), fall back to the genesis ledger.
	if e.lastClosedLedger == nil {
		e.lastClosedLedger = e.genesisLedger
	}
}

// SetInSetupMode controls whether subsequent transactions are tagged as
// setup (fund/trust) or user (fixture) for replay purposes. Setup
// transactions are replayed first in submission order; user transactions
// are replayed second in canonical sorted order.
func (e *TestEnv) SetInSetupMode(setup bool) {
	e.inSetupMode = setup
}

// SubmitPseudo submits a pseudo-transaction (EnableAmendment, SetFee, UNLModify)
// directly to the engine. Pseudo-transactions bypass account lookup, sequence
// auto-fill, fee deduction, and signature verification, and are always applied
// against a closed ledger (rippled's Change::preclaim rejects them otherwise).
// Reference: rippled Change.cpp:82-91 — pseudo-txs require !view.open().
func (e *TestEnv) SubmitPseudo(transaction any) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
	}

	engineConfig := e.engineConfig(e.ledger, engineConfigOpts{parentCloseFromClock: true})

	engine := txengine.NewEngine(e.ledger, engineConfig)
	applyResult := engine.ApplyPseudo(txn)

	return TxResult{
		Result:   applyResult.Result,
		Code:     applyResult.Result.String(),
		Success:  applyResult.Result.IsSuccess(),
		Applied:  applyResult.Applied,
		Fee:      applyResult.Fee,
		Message:  applyResult.Message,
		Metadata: applyResult.Metadata,
	}
}
