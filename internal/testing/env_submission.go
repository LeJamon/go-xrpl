package testing

import (
	"bytes"
	"crypto/sha512"
	"sort"
	"strconv"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/txq"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
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

	// Apply any pending amendment changes from SetAmendments().
	// Matches rippled where enableFeature/disableFeature require close()
	// for changes to take effect.
	// Reference: rippled Env.cpp: "Env::close() must be called for feature
	// enable to take place."
	e.applyPendingAmendments()

	if e.replayOnClose {
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
		e.txQueue.ProcessClosedLedger(closedCtx, false)
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

	// Apply any pending amendment changes (same as Close).
	e.applyPendingAmendments()

	closingTxCount := e.closingTxTotal
	// Round closeTime up to next resolution boundary, matching rippled.
	resolution := time.Duration(e.ledger.CloseTimeResolution()) * time.Second
	if resolution == 0 {
		resolution = 10 * time.Second
	}
	e.clock.Advance(resolution)

	if err := e.ledger.Close(e.clock.Now(), 0); err != nil {
		e.t.Fatalf("Failed to close ledger: %v", err)
	}
	if err := e.ledger.SetValidated(); err != nil {
		e.t.Fatalf("Failed to validate ledger: %v", err)
	}

	// Re-sync clock to the actual close time from the closed ledger.
	// Matches rippled's timeKeeper().set(closed()->info().closeTime).
	e.clock.Set(e.ledger.CloseTime())

	if h, err := e.ledger.StateMapHash(); err == nil {
		e.ledgerRootHashes[e.ledger.Sequence()] = h
	}
	if e.stateFamily != nil {
		e.stateFamily.Sweep()
	}

	// Process with timeLeap=true to reset metrics
	if e.txQueue != nil {
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
		e.txQueue.ProcessClosedLedger(closedCtx, true) // timeLeap = true
	}

	e.lastClosedLedger = e.ledger
	newLedger, err := ledger.NewOpen(e.ledger, e.clock.Now())
	if err != nil {
		e.t.Fatalf("Failed to create new ledger: %v", err)
	}
	e.ledger = newLedger
	e.currentSeq++
	e.openLedgerSetupTxns = nil
	e.openLedgerUserTxns = nil
	e.txInLedger = 0
	e.closingTxTotal = 0
	e.closingFeeLevels = nil

	if e.txQueue != nil {
		e.drainQueue()
		e.retryAllHeldViaTxQ()
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

	// Collect ALL transactions to replay in submission order:
	// setup txns first (fund, trust, reimbursement), then user txns (fixture),
	// then held transactions from previous ledgers.
	// Submission order preserves dependencies (e.g., TrustSet before Payment).
	//
	// Note: rippled applies all txns in canonical (SHAMap-salted) order via
	// buildLedger(). We use submission order because goXRPL's setup txns have
	// different hashes than rippled's, making the canonical salt impossible to
	// match. Submission order produces the correct closed-ledger state for
	// most cases because the retry mechanism handles ordering-dependent failures.
	var allTxns []tx.Transaction
	allTxns = append(allTxns, e.openLedgerSetupTxns...)
	allTxns = append(allTxns, e.openLedgerUserTxns...)
	for _, held := range e.heldTxns {
		allTxns = append(allTxns, held...)
	}

	// Sort all transactions using SHAMap-salted canonical ordering.
	// The salt is the SHAMap root hash of all transaction hashes,
	// matching rippled's CanonicalTXSet (RCLConsensus.cpp onClose).
	sortCanonicalSalted(allTxns)

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
	remaining := e.applyWithRetry(allTxns, maxRetryPasses, maxTotalPasses)

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

// applyPendingAmendments applies any deferred amendment changes from
// SetAmendments(). Called at the start of Close() and CloseWithTimeLeap().
// Matches rippled where enableFeature/disableFeature modify config().features
// but the rules are only rebuilt when the ledger is closed.
// Reference: rippled Env.cpp: "Env::close() must be called for feature
// enable to take place."
func (e *TestEnv) applyPendingAmendments() {
	if len(e.pendingAmendments) == 0 {
		return
	}
	e.rulesBuilder = amendment.NewRulesBuilder()
	for _, name := range e.pendingAmendments {
		e.rulesBuilder.EnableByName(name)
	}
	e.pendingAmendments = nil
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
func (e *TestEnv) applyWithRetry(txns []tx.Transaction, maxRetryPasses, maxTotalPasses int) []tx.Transaction {
	remaining := txns
	certainRetry := true

	for pass := 0; pass < maxTotalPasses && len(remaining) > 0; pass++ {
		var retry []tx.Transaction
		changes := 0

		for _, txn := range remaining {
			result, applied := e.applyForReplay(txn, certainRetry)

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

// CloseAt closes ledgers until the ledger reaches the target sequence.
// If already at or past target, does nothing.
func (e *TestEnv) CloseAt(targetSeq uint32) {
	e.t.Helper()
	for e.ledger.Sequence() < targetSeq {
		e.Close()
	}
}

// Submit submits a transaction to the current open ledger.
// If the transaction doesn't have a sequence number set, it will be auto-filled
// from the account's current sequence in the ledger.
//
// When a TxQ is configured (via NewTestEnvWithTxQ), Submit routes through the
// TxQ for fee escalation and sequence-gap queuing. Transactions that cannot
// afford the escalated fee or have a future sequence are queued and return
// terQUEUED or terPRE_SEQ respectively.
func (e *TestEnv) Submit(transaction interface{}) TxResult {
	e.t.Helper()

	// Convert to tx.Transaction interface
	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	// Auto-fill sequence if not set (skip when using tickets)
	common := txn.GetCommon()
	if common.Sequence == nil && common.TicketSequence == nil {
		// Look up the account to get current sequence
		_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(common.Account)
		if err != nil {
			e.t.Fatalf("Failed to decode account address: %v", err)
			return TxResult{Code: "temINVALID", Success: false, Message: "Invalid account address"}
		}

		var id [20]byte
		copy(id[:], accountID)
		accountKey := keylet.Account(id)

		data, err := e.ledger.Read(accountKey)
		if err != nil || data == nil {
			e.t.Fatalf("Failed to read account for sequence auto-fill: %v", err)
			return TxResult{Code: "terNO_ACCOUNT", Success: false, Message: "Account not found"}
		}

		accountRoot, err := state.ParseAccountRootFromBytes(data)
		if err != nil {
			e.t.Fatalf("Failed to parse account root: %v", err)
			return TxResult{Code: "temINVALID", Success: false, Message: "Failed to parse account"}
		}

		seq := accountRoot.Sequence
		common.Sequence = &seq
	}

	// If TxQ is enabled and not bypassed, route through TxQ for fee escalation and queuing.
	if e.txQueue != nil && !e.bypassTxQ {
		return e.submitViaTxQ(txn)
	}

	// Direct apply path (no TxQ)
	return e.applyDirect(txn)
}

// applyDirect applies a transaction directly without TxQ routing.
// This is the original Submit path.
func (e *TestEnv) applyDirect(txn tx.Transaction) TxResult {
	e.t.Helper()

	// Use the ledger's stored ParentCloseTime, not the clock.
	// This matches rippled where parentCloseTime is derived from the
	// parent ledger's closeTime (OpenView.cpp line 106), not from the
	// network time. Using the ledger header ensures consistency between
	// initial apply and replay-on-close.
	parentCloseTime := uint32(e.ledger.ParentCloseTime().Unix() - protocol.RippleEpochUnix)
	engineConfig := tx.EngineConfig{
		BaseFee:                   e.baseFee,
		ReserveBase:               e.reserveBase,
		ReserveIncrement:          e.reserveIncrement,
		LedgerSequence:            e.ledger.Sequence(),
		SkipSignatureVerification: !e.VerifySignatures,
		Rules:                     e.rulesBuilder.Build(),
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 e.networkID,
		ParentHash:                e.ledger.ParentHash(),
		OpenLedger:                e.openLedger,
	}

	engine := tx.NewEngine(e.ledger, engineConfig)
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
		return TxResult{Code: "temINVALID", Success: false}
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
		e.retryHeldTransactions(accountAddr)

		// Also drain the TxQ in case queued transactions are now unblocked
		e.drainQueue()

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
			Code:    tx.TerQUEUED.String(),
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
func isRetryable(result tx.Result) bool {
	return result >= -99 && result < 0
}

// isTelLocal returns true if the result is a tel (local error) code.
// tel codes are in the range -399 to -300.
// Reference: rippled TER.h telLOCAL_ERROR = -399, telCAN_NOT_QUEUE = -381
func isTelLocal(result tx.Result) bool {
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
// are not applied (matching rippled's retry pass behavior).
// Returns the result code and whether the transaction was actually applied.
func (e *TestEnv) applyForReplay(txn tx.Transaction, certainRetry bool) (tx.Result, bool) {
	// Use the ledger's stored ParentCloseTime, matching applyDirect().
	// Both paths use the ledger header so time-dependent checks produce
	// the same result during initial apply and during replay.
	parentCloseTime := uint32(e.ledger.ParentCloseTime().Unix() - protocol.RippleEpochUnix)
	engineConfig := tx.EngineConfig{
		BaseFee:                   e.baseFee,
		ReserveBase:               e.reserveBase,
		ReserveIncrement:          e.reserveIncrement,
		LedgerSequence:            e.ledger.Sequence(),
		SkipSignatureVerification: !e.VerifySignatures,
		Rules:                     e.rulesBuilder.Build(),
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 e.networkID,
		ParentHash:                e.ledger.ParentHash(),
		OpenLedger:                e.openLedger,
	}
	if certainRetry {
		engineConfig.ApplyFlags = tx.TapRETRY
	}

	engine := tx.NewEngine(e.ledger, engineConfig)
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

		// Secondary: sequence proxy (sequence takes priority over tickets)
		seqI := canonicalSeq(ci)
		seqJ := canonicalSeq(cj)
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
	sequence uint32
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
			sequence: common.SeqProxy(),
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
		for j := 0; j < 32; j++ {
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
		if entries[ei.idx].sequence != entries[ej.idx].sequence {
			return entries[ei.idx].sequence < entries[ej.idx].sequence
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
	for i := 0; i < 16; i++ {
		if node.children[i] != nil {
			computeTreeHash(node.children[i])
		}
	}

	// Inner node hash: MIN\0 prefix + 16 child hashes
	minPrefix := [4]byte{'M', 'I', 'N', 0x00}
	h := sha512.New()
	h.Write(minPrefix[:])
	for i := 0; i < 16; i++ {
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

// canonicalSeq returns the effective sequence number for canonical ordering.
// Sequence-based transactions sort before ticket-based ones (sequence values
// are typically lower than ticket sequence values in practice).
// Reference: rippled SeqProxy ordering: Seq < Ticket when values equal,
// but in practice sequence numbers are always present.
func canonicalSeq(c *tx.Common) uint64 {
	if c.Sequence != nil && *c.Sequence != 0 {
		return uint64(*c.Sequence)
	}
	if c.TicketSequence != nil {
		// Tickets sort after sequences. Use a high base to ensure this.
		return uint64(*c.TicketSequence) + (1 << 32)
	}
	return 0
}

// sortHeldBySequence sorts transactions by sequence number (lowest first).
func sortHeldBySequence(txns []tx.Transaction) {
	for i := 0; i < len(txns)-1; i++ {
		for j := i + 1; j < len(txns); j++ {
			seqI := getSeqFromTx(txns[i])
			seqJ := getSeqFromTx(txns[j])
			if seqJ < seqI {
				txns[i], txns[j] = txns[j], txns[i]
			}
		}
	}
}

// getSeqFromTx extracts the sequence number from a transaction.
func getSeqFromTx(txn tx.Transaction) uint32 {
	common := txn.GetCommon()
	if common.Sequence != nil {
		return *common.Sequence
	}
	if common.TicketSequence != nil {
		return *common.TicketSequence
	}
	return 0
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
type testTxQApplyContext struct {
	env *TestEnv
}

func (c *testTxQApplyContext) GetAccountSequence(account [20]byte) uint32 {
	accountKey := keylet.Account(account)
	data, err := c.env.ledger.Read(accountKey)
	if err != nil || data == nil {
		return 0
	}
	accountRoot, err := state.ParseAccountRootFromBytes(data)
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
	accountRoot, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		return 0
	}
	return accountRoot.Balance
}

func (c *testTxQApplyContext) GetAccountReserve(ownerCount uint32) uint64 {
	return c.env.reserveBase + uint64(ownerCount)*c.env.reserveIncrement
}

func (c *testTxQApplyContext) GetBaseFee(txn tx.Transaction) uint64 {
	// For batch transactions, the base fee includes extra signers and inner
	// txns. Reference: rippled calculateBaseFee() in Transactor.cpp.
	if calc, ok := txn.(baseFeeCalculator); ok {
		return calc.CalculateMinimumFee(c.env.baseFee)
	}

	// SetRegularKey free password change: baseFee = 0 when signed with master
	// key and lsfPasswordSpent is not set.
	// Reference: rippled SetRegularKey.cpp calculateBaseFee
	if txn.TxType() == tx.TypeRegularKeySet {
		common := txn.GetCommon()
		if common != nil && common.SigningPubKey != "" {
			sigAddr, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(common.SigningPubKey)
			if err == nil && sigAddr == common.Account {
				// Signed with master key. Check if lsfPasswordSpent is set.
				acctID, acctErr := state.DecodeAccountID(common.Account)
				if acctErr == nil {
					accountKey := keylet.Account(acctID)
					data, readErr := c.env.ledger.Read(accountKey)
					if readErr == nil && data != nil {
						accountRoot, parseErr := state.ParseAccountRootFromBytes(data)
						if parseErr == nil && accountRoot.Flags&state.LsfPasswordSpent == 0 {
							return 0
						}
					}
				}
			}
		}
	}

	return c.env.baseFee
}

func (c *testTxQApplyContext) GetTxInLedger() uint32 {
	return c.env.txInLedger
}

func (c *testTxQApplyContext) GetLedgerSequence() uint32 {
	return c.env.ledger.Sequence()
}

func (c *testTxQApplyContext) ApplyTransaction(txn tx.Transaction) (tx.Result, bool) {
	parentCloseTime := uint32(c.env.clock.Now().Unix() - protocol.RippleEpochUnix)
	// Transactions applied through the TxQ must NOT check open-ledger fee
	// adequacy. In rippled, TxQ::tryDirectApply calls ripple::apply() with
	// tapNONE flags (NOT tapOPEN_LEDGER). The TxQ's own fee-level check is
	// sufficient; the engine's baseFee floor would incorrectly reject
	// fee=0 transactions that have already passed fee-level validation.
	// Reference: rippled NetworkOPsImp::apply (flags = tapNONE),
	//   TxQ::tryDirectApply (uses same flags as NetworkOPs),
	//   TxQ::tryClearAccountQueueUpThruTx (uses stored MaybeTx flags)
	engineConfig := tx.EngineConfig{
		BaseFee:                   c.env.baseFee,
		ReserveBase:               c.env.reserveBase,
		ReserveIncrement:          c.env.reserveIncrement,
		LedgerSequence:            c.env.ledger.Sequence(),
		SkipSignatureVerification: !c.env.VerifySignatures,
		Rules:                     c.env.rulesBuilder.Build(),
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 c.env.networkID,
		ParentHash:                c.env.ledger.ParentHash(),
		OpenLedger:                false,
	}

	engine := tx.NewEngine(c.env.ledger, engineConfig)
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

func (c *testTxQApplyContext) PreclaimTransaction(txn tx.Transaction, account [20]byte, adjustedBalance uint64, adjustedSeq uint32) tx.Result {
	// Simplified simulation of rippled's multiTxn preclaim path (TxQ.cpp:1167-1170).
	// rippled creates a modified view with adjusted balance and sequence,
	// then runs a full preclaim(). We only check the checkFee portion here
	// (terINSUF_FEE_B when adjusted balance < fee), which is the primary
	// check that differs with an adjusted view. Other preclaim failures
	// (e.g., tecINSUFFICIENT_RESERVE) are not yet simulated.
	// Reference: rippled Transactor::checkFee (Transactor.cpp line ~310)
	common := txn.GetCommon()
	if common == nil {
		return tx.TefINTERNAL
	}

	fee, _ := strconv.ParseUint(common.Fee, 10, 64)

	if adjustedBalance < fee {
		return tx.TerINSUF_FEE_B
	}

	// If preclaim passes, return 0 (tesSUCCESS) to indicate likely to claim fee.
	return 0
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
	accountRoot, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		return 0
	}
	return accountRoot.Sequence
}

func (c *testTxQAcceptContext) ApplyTransaction(txn tx.Transaction) (tx.Result, bool) {
	parentCloseTime := uint32(c.env.clock.Now().Unix() - protocol.RippleEpochUnix)
	// TxQ accept (drain on close) applies queued transactions with tapNONE
	// flags in rippled — NOT tapOPEN_LEDGER. This prevents the engine's
	// fee adequacy check from rejecting fee=0 transactions that were
	// already validated by the TxQ's fee-level mechanism.
	// Reference: rippled TxQ::accept calls MaybeTx::apply with stored
	//   flags (which have tapRETRY cleared but NOT tapOPEN_LEDGER set)
	engineConfig := tx.EngineConfig{
		BaseFee:                   c.env.baseFee,
		ReserveBase:               c.env.reserveBase,
		ReserveIncrement:          c.env.reserveIncrement,
		LedgerSequence:            c.env.ledger.Sequence(),
		SkipSignatureVerification: !c.env.VerifySignatures,
		Rules:                     c.env.rulesBuilder.Build(),
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 c.env.networkID,
		ParentHash:                c.env.ledger.ParentHash(),
		OpenLedger:                false,
	}

	engine := tx.NewEngine(c.env.ledger, engineConfig)
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
	if txn.TxType() == tx.TypeRegularKeySet && common.SigningPubKey != "" {
		sigAddr, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(common.SigningPubKey)
		if err == nil && sigAddr == common.Account {
			acctID, acctErr := state.DecodeAccountID(common.Account)
			if acctErr == nil {
				accountKey := keylet.Account(acctID)
				data, readErr := e.ledger.Read(accountKey)
				if readErr == nil && data != nil {
					accountRoot, parseErr := state.ParseAccountRootFromBytes(data)
					if parseErr == nil && accountRoot.Flags&state.LsfPasswordSpent == 0 {
						baseFee = 0
					}
				}
			}
		}
	}

	feeLevel := txq.ToFeeLevel(feePaid, baseFee)
	e.closingFeeLevels = append(e.closingFeeLevels, feeLevel)
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

// IsReplayEnabled returns whether open-ledger replay is enabled.
func (e *TestEnv) IsReplayEnabled() bool {
	return e.replayOnClose
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
// auto-fill, fee deduction, and signature verification.
// Reference: rippled Change.cpp -- pseudo-txs have zero account, zero fee, no sigs.
func (e *TestEnv) SubmitPseudo(transaction interface{}) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	parentCloseTime := uint32(e.clock.Now().Unix() - protocol.RippleEpochUnix)
	engineConfig := tx.EngineConfig{
		BaseFee:                   e.baseFee,
		ReserveBase:               e.reserveBase,
		ReserveIncrement:          e.reserveIncrement,
		LedgerSequence:            e.ledger.Sequence(),
		SkipSignatureVerification: !e.VerifySignatures,
		Rules:                     e.rulesBuilder.Build(),
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 e.networkID,
		ParentHash:                e.ledger.ParentHash(),
		OpenLedger:                e.openLedger,
	}

	engine := tx.NewEngine(e.ledger, engineConfig)
	applyResult := engine.ApplyPseudo(txn)

	return TxResult{
		Code:     applyResult.Result.String(),
		Success:  applyResult.Result.IsSuccess(),
		Message:  applyResult.Message,
		Metadata: applyResult.Metadata,
	}
}
