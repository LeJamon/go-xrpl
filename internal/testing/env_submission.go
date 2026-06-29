package testing

import (
	"bytes"
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
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

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

	// Replay-on-close consensus simulation only applies to plain Close(); the
	// time-leap path never rebuilds the ledger from its parent.
	if !timeLeap && e.replayOnClose {
		e.closeWithReplay()
		return
	}

	// Record the total number of transactions in the closing ledger for TxQ
	// metrics. closingTxTotal includes inner batch txns as separate entries,
	// matching rippled's closed ledger tx map behavior.
	closingTxCount := e.closingTxTotal

	// Round closeTime up to next resolution boundary, matching rippled.
	// Reference: rippled Env.cpp:126 — closeTime += resolution - 1s
	resolution := time.Duration(e.ledger.CloseTimeResolution()) * time.Second
	if resolution == 0 {
		resolution = 10 * time.Second // fallback for genesis
	}
	e.clock.Advance(resolution)

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
		// Use the actual fee levels recorded during this ledger.
		// If we have tracked fee levels, use those. Otherwise fall back to
		// generating BaseLevel entries for each transaction (for backward
		// compatibility with tests that don't track fee levels).
		feeLevels := e.closingFeeLevels
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
	e.closingFeeLevels = nil

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
	targetTime := time.Unix(int64(target)+protocol.RippleEpochUnix, 0).UTC()
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
func (e *TestEnv) closeWithReplay() {
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
	freshLedger, err := ledger.NewOpen(e.lastClosedLedger, e.clock.Now())
	if err != nil {
		e.t.Fatalf("closeWithReplay: failed to create fresh ledger: %v", err)
	}
	e.ledger = freshLedger

	// Reset counters for the fresh replay
	e.txInLedger = 0
	e.closingTxTotal = 0
	e.closingFeeLevels = nil

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
	e.closingFeeLevels = nil

	// Update TxQ metrics if applicable
	if e.txQueue != nil {
		e.drainQueue()
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
		for _, id := range e.rulesBuilder.Build().GetEnabled() {
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
		common.Fee = formatUint64(e.baseFee)
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

	// If TxQ is enabled and not bypassed, route through TxQ for fee escalation and queuing.
	if e.txQueue != nil && !e.bypassTxQ {
		result := e.submitViaTxQ(txn)
		// A held tx whose sequence gap is now filled can replace a lower-fee
		// queued entry; do that before the next ledger drains the queue.
		e.retryHeldReplacementsIntoQueue()
		return result
	}

	// Direct apply path (no TxQ)
	return e.applyDirect(txn)
}

// toRippleTime converts a wall-clock time to seconds since the Ripple epoch,
// the form EngineConfig.ParentCloseTime and on-ledger time fields expect.
func toRippleTime(t time.Time) uint32 {
	return uint32(t.Unix() - protocol.RippleEpochUnix)
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
	parentCloseFromClock bool
	openLedger           bool
	feeTrack             bool
	enforceLoadFee       bool
	applyFlags           tx.ApplyFlags
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
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 e.networkID,
		ParentHash:                view.ParentHash(),
		OpenLedger:                opts.openLedger,
		ViewOpen:                  e.viewOpen,
		EnforceLoadFee:            opts.enforceLoadFee,
		ApplyFlags:                opts.applyFlags,
	}
	if opts.feeTrack {
		cfg.FeeTrack = e.feeTrack
	}
	return cfg
}

// applyDirect applies a transaction directly without TxQ routing.
// This is the original Submit path.
func (e *TestEnv) applyDirect(txn tx.Transaction) TxResult {
	e.t.Helper()

	// Header-based ParentCloseTime (not the clock) keeps the initial apply and
	// replay-on-close in agreement; see engineConfig.
	engineConfig := e.engineConfig(e.ledger, engineConfigOpts{
		openLedger: e.openLedger,
		feeTrack:   true,
	})

	engine := txengine.NewEngine(e.ledger, engineConfig)
	// Seed the engine's txCount from the env's tx-in-ledger counter so
	// metadata.TransactionIndex matches what rippled assigns. e.ledger
	// is the open ledger and env.Submit does NOT call AddTransactionWithMeta,
	// so e.ledger.TxCount() always returns 0 — use the env-maintained
	// counter that tracks applied txns across submits within a close window.
	// Without this seeding the 3rd of 3 sequential TrustSets from the
	// same account differed by 100 bytes vs rippled v2.6.2 — see
	// TestReproByteDiff_MultiTrustSetThreading.
	engine.SetBaseTxCount(e.txInLedger)
	if e.invariantViolationHook != nil {
		engine.SetInvariantViolationHookForTest(e.invariantViolationHook)
	}
	applyResult := engine.Apply(txn)

	if applyResult.Result.IsApplied() {
		e.txInLedger++
		e.closingTxTotal++
		e.recordTxFeeLevel(txn)
		// For batch transactions, also count inner txns for fee metrics.
		// Reference: rippled counts inner batch txns as separate entries in
		// the closed ledger's tx map, which affects ProcessClosedLedger.
		if counter, ok := txn.(innerTxCounter); ok {
			e.closingTxTotal += uint32(counter.InnerTxCount())
		}
	}

	// Track transaction for replay-on-close.
	// Only applied (tesSUCCESS, tec*) and retryable (ter*) transactions are
	// included in the replay set. Permanent failures (tem*, tef*, tel*) are
	// dropped — they never appear in rippled's canonical TX set.
	// Reference: rippled's open ledger tx map only contains applied txns.
	if e.replayOnClose {
		if applyResult.Result.IsApplied() || isRetryable(applyResult.Result) {
			if e.inSetupMode {
				e.openLedgerSetupTxns = append(e.openLedgerSetupTxns, txn)
			} else {
				e.openLedgerUserTxns = append(e.openLedgerUserTxns, txn)
			}
		}

		// For retryable results (terPRE_SEQ etc), also hold the transaction
		// so it can be retried in subsequent ledgers if the replay doesn't
		// resolve it.
		if isRetryable(applyResult.Result) {
			accountAddr := txn.GetCommon().Account
			e.addHeldTransaction(accountAddr, txn)
		}
	}

	return TxResult{
		Code:     applyResult.Result.String(),
		Success:  applyResult.Result.IsSuccess(),
		Message:  applyResult.Message,
		Metadata: applyResult.Metadata,
	}
}

// innerTxCounter is an optional interface implemented by transaction types that
// contain inner transactions (e.g., Batch). It returns the number of inner
// transactions, which affects fee metrics computation in ProcessClosedLedger.
type innerTxCounter interface {
	InnerTxCount() int
}

// baseFeeCalculator is an optional interface for transaction types that have
// a custom base fee calculation (e.g., Batch, which includes extra signers and
// inner transactions in its base fee).
type baseFeeCalculator interface {
	CalculateMinimumFee(baseFee uint64) uint64
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
			Code:    result.Result.String(),
			Success: result.Result.IsSuccess(),
			Message: result.Result.String(),
		}
	}

	if result.Queued {
		// Transaction was queued in the TxQ (fee escalation queue).
		// Also add to held transactions so it can be retried after a close
		// if it gets kicked out of the TxQ.
		// Reference: rippled NetworkOPs::apply adds queued txns to held map.
		e.addHeldTransaction(accountAddr, txn)

		return TxResult{
			Code:    ter.TerQUEUED.String(),
			Success: false,
			Message: "Transaction queued",
		}
	}

	// Handle retryable results by holding the transaction.
	// Reference: rippled NetworkOPs::apply holds isTerRetry results in
	// LedgerMaster's held transaction map.
	//
	// Also hold tel results (telCAN_NOT_QUEUE_FULL, telCAN_NOT_QUEUE_FEE, etc.)
	// because rippled's localTxs mechanism retries ALL locally-submitted
	// transactions at the next close, regardless of result code. This is
	// critical for TxQ tests where transactions rejected with tel codes get
	// re-queued after the queue drains during close.
	// Reference: rippled NetworkOPs.cpp:1677-1682 (m_localTX->push_back)
	if isRetryable(result.Result) || isTelLocal(result.Result) {
		e.addHeldTransaction(accountAddr, txn)
	}

	return TxResult{
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

// addHeldTransaction adds a transaction to the held map for later retry.
// Reference: rippled LedgerMaster::addHeldTransaction
func (e *TestEnv) addHeldTransaction(accountAddr string, txn tx.Transaction) {
	if e.heldTxns == nil {
		e.heldTxns = make(map[string][]tx.Transaction)
	}
	e.heldTxns[accountAddr] = append(e.heldTxns[accountAddr], txn)
}

// retryAllHeldViaTxQ retries ALL held transactions through the TxQ.
// This mirrors rippled's OpenLedger::accept() step (d) which iterates
// localTxs and calls TxQ::apply() for each after the queue drain.
// This allows transactions that were previously rejected (tel codes,
// ter codes, etc.) to be re-queued or applied now that the queue has
// been drained and conditions may have changed.
// Reference: rippled OpenLedger.cpp:117-118
func (e *TestEnv) retryAllHeldViaTxQ() {
	if e.heldTxns == nil || len(e.heldTxns) == 0 {
		return
	}

	// Collect all held transactions from all accounts
	var allHeld []tx.Transaction
	for _, txns := range e.heldTxns {
		allHeld = append(allHeld, txns...)
	}

	// Clear all held transactions before retrying
	// (successfully retried ones may get re-added if they result in ter/tel)
	e.heldTxns = nil

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
// after the drain), so they only enter once the drain frees space.
func (e *TestEnv) retryHeldReplacementsIntoQueue() {
	if len(e.heldTxns) == 0 {
		return
	}

	type replacement struct {
		accountID [20]byte
		txn       tx.Transaction
	}
	var replacements []replacement

	for accountAddr, txns := range e.heldTxns {
		_, acctBytes, err := addresscodec.DecodeClassicAddressToAccountID(accountAddr)
		if err != nil {
			continue
		}
		var accountID [20]byte
		copy(accountID[:], acctBytes)

		queued := e.txQueue.GetAccountTxs(accountID)
		if len(queued) == 0 {
			continue
		}

		for _, txn := range txns {
			sp, ok := heldSeqProxy(txn)
			if !ok {
				continue
			}
			for _, qc := range queued {
				if qc.SeqProxy == sp {
					replacements = append(replacements, replacement{accountID, txn})
					break
				}
			}
		}
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
	opts := engineConfigOpts{openLedger: e.openLedger || held}
	if certainRetry {
		opts.applyFlags = tx.TapRETRY
	}
	engineConfig := e.engineConfig(e.ledger, opts)

	engine := txengine.NewEngine(e.ledger, engineConfig)
	// Seed the engine's txCount from the env's tx-in-ledger counter so
	// metadata.TransactionIndex matches what rippled assigns. e.ledger
	// is the open ledger and env.Submit does NOT call AddTransactionWithMeta,
	// so e.ledger.TxCount() always returns 0 — use the env-maintained
	// counter that tracks applied txns across submits within a close window.
	// Without this seeding the 3rd of 3 sequential TrustSets from the
	// same account differed by 100 bytes vs rippled v2.6.2 — see
	// TestReproByteDiff_MultiTrustSetThreading.
	engine.SetBaseTxCount(e.txInLedger)
	applyResult := engine.Apply(txn)

	if applyResult.Applied {
		e.txInLedger++
		e.closingTxTotal++
		if counter, ok := txn.(innerTxCounter); ok {
			e.closingTxTotal += uint32(counter.InnerTxCount())
		}
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

// computeTxID generates a deterministic transaction ID for a transaction.
// Uses account + sequence/ticket to generate a unique hash.
func (e *TestEnv) computeTxID(txn tx.Transaction) [32]byte {
	common := txn.GetCommon()
	var data []byte
	data = append(data, []byte(common.Account)...)
	if common.Sequence != nil {
		data = append(data, byte(*common.Sequence>>24), byte(*common.Sequence>>16),
			byte(*common.Sequence>>8), byte(*common.Sequence))
	}
	if common.TicketSequence != nil {
		data = append(data, byte(*common.TicketSequence>>24), byte(*common.TicketSequence>>16),
			byte(*common.TicketSequence>>8), byte(*common.TicketSequence))
	}
	data = append(data, []byte(common.Fee)...)
	txType := txn.TxType()
	data = append(data, byte(txType>>8), byte(txType))
	// Add a nonce based on the current ledger sequence and txInLedger
	// to ensure unique IDs for same-account, same-seq transactions
	data = append(data, byte(e.currentSeq>>8), byte(e.currentSeq))
	data = append(data, byte(e.txInLedger>>8), byte(e.txInLedger))

	return sha512HalfForTxID(data)
}

// sha512HalfForTxID computes SHA-512 and returns the first 32 bytes (SHA-512 Half).
// Used for generating deterministic transaction IDs in the test environment.
func sha512HalfForTxID(data []byte) [32]byte {
	h := sha512.Sum512(data)
	var result [32]byte
	copy(result[:], h[:32])
	return result
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
	txInLedger     uint32
	closingTxTotal uint32
	feeLevelTxns   []tx.Transaction
}

// applyView returns the ledger this context applies transactions to: the
// sandbox snapshot when set, otherwise the live env ledger.
func (c *testTxQApplyContext) applyView() *ledger.Ledger {
	if c.view != nil {
		return c.view
	}
	return c.env.ledger
}

func (c *testTxQApplyContext) GetAccountSequence(account [20]byte) uint32 {
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

func (c *testTxQApplyContext) GetAccountBalance(account [20]byte) uint64 {
	accountKey := keylet.Account(account)
	data, err := c.env.ledger.Read(accountKey)
	if err != nil || data == nil {
		return 0
	}
	accountRoot, err := state.ParseAccountRoot(data)
	if err != nil {
		return 0
	}
	return accountRoot.Balance
}

func (c *testTxQApplyContext) GetAccountReserve(ownerCount uint32) uint64 {
	return c.env.reserveBase + uint64(ownerCount)*c.env.reserveIncrement
}

// isFreeRegularKeySet reports whether txn is a SetRegularKey that qualifies for
// the free password change: signed with the account's master key while
// lsfPasswordSpent is clear. rippled charges a zero base fee in that case.
// Reference: rippled SetRegularKey.cpp calculateBaseFee.
func (e *TestEnv) isFreeRegularKeySet(txn tx.Transaction) bool {
	if txn.TxType() != tx.TypeRegularKeySet {
		return false
	}
	common := txn.GetCommon()
	if common == nil || common.SigningPubKey == "" {
		return false
	}
	sigAddr, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(common.SigningPubKey)
	if err != nil || sigAddr != common.Account {
		return false // not signed with the master key
	}
	acctID, err := state.DecodeAccountID(common.Account)
	if err != nil {
		return false
	}
	data, err := e.ledger.Read(keylet.Account(acctID))
	if err != nil || data == nil {
		return false
	}
	accountRoot, err := state.ParseAccountRoot(data)
	if err != nil {
		return false
	}
	return accountRoot.Flags&state.LsfPasswordSpent == 0
}

func (c *testTxQApplyContext) GetBaseFee(txn tx.Transaction) uint64 {
	// For batch transactions, the base fee includes extra signers and inner
	// txns. Reference: rippled calculateBaseFee() in Transactor.cpp.
	if calc, ok := txn.(baseFeeCalculator); ok {
		return calc.CalculateMinimumFee(c.env.baseFee)
	}
	if c.env.isFreeRegularKeySet(txn) {
		return 0
	}
	return c.env.baseFee
}

func (c *testTxQApplyContext) GetTxInLedger() uint32 {
	return c.env.txInLedger
}

func (c *testTxQApplyContext) GetLedgerSequence() uint32 {
	return c.env.ledger.Sequence()
}

func (c *testTxQApplyContext) ApplyTransaction(txn tx.Transaction) (ter.Result, bool) {
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

	engine := txengine.NewEngine(view, engineConfig)
	applyResult := engine.Apply(txn)

	applied := applyResult.Result.IsApplied()
	if applied {
		innerCount := uint32(0)
		if counter, ok := txn.(innerTxCounter); ok {
			innerCount = uint32(counter.InnerTxCount())
		}
		if c.accum != nil {
			// Sandbox child: defer the env-counter side effects until Commit.
			c.accum.txInLedger++
			c.accum.closingTxTotal += 1 + innerCount
			c.accum.feeLevelTxns = append(c.accum.feeLevelTxns, txn)
		} else {
			c.env.txInLedger++
			c.env.closingTxTotal += 1 + innerCount
			c.env.recordTxFeeLevel(txn)
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

// Commit folds the sandbox snapshot back into the parent view and applies the
// buffered env-counter side effects.
func (s *testTxQSandbox) Commit() error {
	if err := s.parent.applyView().AdoptState(s.snap); err != nil {
		return err
	}
	s.parent.env.txInLedger += s.accum.txInLedger
	s.parent.env.closingTxTotal += s.accum.closingTxTotal
	for _, txn := range s.accum.feeLevelTxns {
		s.parent.env.recordTxFeeLevel(txn)
	}
	return nil
}

func (c *testTxQApplyContext) PreflightTransaction(txn tx.Transaction) ter.Result {
	// Mirror the engine config used by ApplyTransaction so TxQ admission
	// preflight (rippled TxQ.cpp:743-745) matches the direct-apply path.
	view := c.applyView()
	engineConfig := c.env.engineConfig(view, engineConfigOpts{
		parentCloseFromClock: true,
		feeTrack:             true,
	})
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

// GetApplyFlags returns the engine ApplyFlags currently driving the
// test env. The test env stores the flag on TestEnv.txQApplyFlags and
// resets it after each Submit; default is 0 so TxQ admission behaves
// as if no flag is set.
func (c *testTxQApplyContext) GetApplyFlags() tx.ApplyFlags {
	return c.env.txQApplyFlags
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

	engine := txengine.NewEngine(c.env.ledger, engineConfig)
	applyResult := engine.Apply(txn)

	applied := applyResult.Result.IsApplied()
	if applied {
		c.env.txInLedger++
		c.env.closingTxTotal++
		c.env.recordTxFeeLevel(txn)
		if counter, ok := txn.(innerTxCounter); ok {
			c.env.closingTxTotal += uint32(counter.InnerTxCount())
		}
	}
	return applyResult.Result, applied
}

// recordTxFeeLevel computes and records the fee level of an applied transaction.
// This is used to compute the median fee level for ProcessClosedLedger, which
// determines the escalation multiplier. Without tracking actual fee levels,
// the escalation multiplier would always be the minimum (128000), causing
// fee escalation to be less aggressive than rippled when high-fee transactions
// are in the ledger.
// Reference: rippled getFeeLevelPaid in TxQ.cpp:38-64
func (e *TestEnv) recordTxFeeLevel(txn tx.Transaction) {
	common := txn.GetCommon()
	if common == nil {
		return
	}

	feePaid, _ := strconv.ParseUint(common.Fee, 10, 64)
	baseFee := e.baseFee

	// Use the actual base fee for the transaction type (e.g., batch tx may
	// have a higher base fee). The TxQ apply context uses GetBaseFee which
	// calls CalculateMinimumFee for batch transactions.
	if calc, ok := txn.(baseFeeCalculator); ok {
		baseFee = calc.CalculateMinimumFee(e.baseFee)
	}

	// SetRegularKey free password change: baseFee = 0 when signed with master key.
	// Reference: rippled SetRegularKey.cpp calculateBaseFee + TxQ.cpp getFeeLevelPaid
	if e.isFreeRegularKeySet(txn) {
		baseFee = 0
	}

	feeLevel := txq.ToFeeLevel(feePaid, baseFee)
	e.closingFeeLevels = append(e.closingFeeLevels, feeLevel)

	// Inner batch txns are counted as separate entries in the closed ledger's
	// tx map (matching rippled), so they each contribute a fee level entry to
	// keep closingFeeLevels aligned with closingTxTotal. Inner txns inherit
	// the outer batch's effective fee level — the outer batch fee covers all
	// inner txns per Batch.cpp::CalculateBaseFee.
	if counter, ok := txn.(innerTxCounter); ok {
		for i := uint32(0); i < uint32(counter.InnerTxCount()); i++ {
			e.closingFeeLevels = append(e.closingFeeLevels, feeLevel)
		}
	}
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
		Code:     applyResult.Result.String(),
		Success:  applyResult.Result.IsSuccess(),
		Message:  applyResult.Message,
		Metadata: applyResult.Metadata,
	}
}
