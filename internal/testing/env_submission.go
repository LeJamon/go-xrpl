package jtx

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
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
// the TxQ drain. timeLeap simulates a slow consensus round and is forwarded to
// ProcessClosedLedger, driving txnsExpected back toward the minimum.
func (e *TestEnv) close(timeLeap bool) {
	e.t.Helper()
	if e.txQueue != nil {
		e.retryHeldBeforeCloseViaTxQ()
	} else {
		e.retryHeldBeforeCloseDirect()
	}
	resolution := time.Duration(e.ledger.CloseTimeResolution()) * time.Second
	if resolution == 0 {
		resolution = 10 * time.Second
	}
	e.clock.Advance(resolution)

	closed := e.ledger
	var retries []openledger.PendingTx
	if e.replayOnClose || e.needsConsensusBuild {
		build, err := openledger.BuildClosedLedger(e.lastClosedLedger, e.pendingForClose(), openledger.BuildConfig{
			CloseTime:     e.clock.Now(),
			CanonicalSalt: e.nextCloseSalt,
			Apply:         e.openLedgerApplyConfig(e.ledger, e.VerifySignatures),
		})
		if err != nil {
			e.t.Fatalf("build closed ledger: %v", err)
		}
		closed = build.Ledger
		retries = build.Retries
	} else if err := closed.Close(e.clock.Now(), 0); err != nil {
		e.t.Fatalf("close ledger: %v", err)
	}
	e.nextCloseSalt = nil
	if err := closed.SetValidated(); err != nil {
		e.t.Fatalf("validate closed ledger: %v", err)
	}
	if err := e.localTxs.Sweep(closed); err != nil {
		e.t.Fatalf("sweep local transactions: %v", err)
	}
	e.ledger = closed
	e.lastClosedLedger = closed
	e.clock.Set(closed.CloseTime())
	if e.stateFamily != nil {
		e.stateFamily.Sweep()
	}
	if e.txQueue != nil {
		feeLevels, feeErr := e.transactionFeeLevels(closed)
		if feeErr != nil {
			e.t.Fatalf("read closed-ledger fee levels: %v", feeErr)
		}
		e.txQueue.ProcessClosedLedger(&testClosedLedgerContext{ledgerSeq: closed.Sequence(), feeLevels: feeLevels}, timeLeap)
	}
	e.applyPendingAmendments()

	newLedger, err := ledger.NewOpen(closed, e.clock.Now())
	if err != nil {
		e.t.Fatalf("create new open ledger: %v", err)
	}
	e.ledger = newLedger
	e.currentSeq = newLedger.Sequence()
	e.txInLedger = 0
	e.needsConsensusBuild = false
	for _, retry := range retries {
		e.addHeldTransaction(retry.Parsed.GetCommon().Account, retry.Parsed)
	}
	if e.txQueue != nil {
		e.drainQueue()
		e.retryLocalsViaTxQ()
	}
}

func (e *TestEnv) pendingForClose() []openledger.PendingTx {
	e.t.Helper()
	var pending []openledger.PendingTx
	var visitErr error
	if err := e.ledger.ForEachTransaction(func(hash [32]byte, data []byte) bool {
		blob, _, err := tx.SplitTxWithMetaBlobStrict(data)
		if err != nil {
			visitErr = fmt.Errorf("split pending transaction %x: %w", hash, err)
			return false
		}
		prepared, err := openledger.ParsePendingTx(blob)
		if err != nil {
			visitErr = fmt.Errorf("parse pending transaction %x: %w", hash, err)
			return false
		}
		if prepared.Hash != hash {
			visitErr = fmt.Errorf("pending transaction key %x does not match hash %x", hash, prepared.Hash)
			return false
		}
		if prepared.Parsed.GetCommon().GetFlags()&tx.TfInnerBatchTxn != 0 {
			return true
		}
		pending = append(pending, prepared)
		return true
	}); err != nil {
		e.t.Fatalf("iterate pending transactions: %v", err)
	}
	if visitErr != nil {
		e.t.Fatalf("read pending transactions: %v", visitErr)
	}
	return pending
}

func (e *TestEnv) transactionFeeLevels(view *ledger.Ledger) ([]txq.FeeLevel, error) {
	var levels []txq.FeeLevel
	var visitErr error
	err := view.ForEachTransaction(func(hash [32]byte, data []byte) bool {
		blob, _, err := tx.SplitTxWithMetaBlobStrict(data)
		if err != nil {
			visitErr = fmt.Errorf("split transaction %x: %w", hash, err)
			return false
		}
		transaction, err := tx.ParseFromBinary(blob)
		if err != nil {
			visitErr = fmt.Errorf("parse transaction %x: %w", hash, err)
			return false
		}
		transaction.SetRawBytes(blob)
		fee, err := strconv.ParseUint(transaction.GetCommon().Fee, 10, 64)
		if err != nil {
			visitErr = fmt.Errorf("parse fee for transaction %x: %w", hash, err)
			return false
		}
		config := e.engineConfig(view, engineConfigOpts{})
		baseFee := sign.CalculateBaseFee(transaction, view, config)
		defaultBaseFee := sign.CalculateDefaultBaseFee(transaction, config)
		levels = append(levels, txq.ToFeeLevelWithDefaultBaseFee(fee, baseFee, defaultBaseFee))
		return true
	})
	if err != nil {
		return nil, err
	}
	if visitErr != nil {
		return nil, visitErr
	}
	return levels, nil
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

// pendingRulesBuilder returns a RulesBuilder reflecting the current rules with
// all staged amendment changes applied: SetAmendments (whole-set replace) first,
// then the EnableFeature/DisableFeature deltas layered on top. It does NOT mutate
// the env — applyPendingAmendments installs the result at Close, while
// FeatureEnabled queries it without committing.
func (e *TestEnv) pendingRulesBuilder() *amendment.RulesBuilder {
	b := amendment.NewRulesBuilder()
	if e.pendingAmendments != nil {
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
// (whole-set replace) and EnableFeature/DisableFeature (deltas). Called after
// the closing ledger is built. Matches rippled where
// enableFeature/disableFeature modify config().features but the rules are only
// rebuilt for the successor open ledger.
// Reference: rippled Env.cpp: "Env::close() must be called for feature
// enable to take place."
func (e *TestEnv) applyPendingAmendments() {
	if e.pendingAmendments == nil && len(e.pendingEnable) == 0 && len(e.pendingDisable) == 0 {
		return
	}
	e.rulesBuilder = e.pendingRulesBuilder()
	e.pendingAmendments = nil
	e.pendingEnable = nil
	e.pendingDisable = nil
}

// Submit submits a transaction to the current open ledger.
// If the transaction doesn't have a sequence number set, it will be auto-filled
// from the account's current sequence in the ledger.
//
// When a TxQ is configured (via NewTestEnvWithTxQ), Submit routes through the
// TxQ for fee escalation and sequence-gap queuing. Transactions that cannot
// afford the escalated fee or have a future sequence are queued and return
// terQUEUED or terPRE_SEQ respectively.
func (e *TestEnv) Submit(transaction tx.Transaction) TxResult {
	return e.SubmitWithOptions(transaction, SubmitOptions{})
}

func (e *TestEnv) SubmitWithOptions(txn tx.Transaction, options SubmitOptions) TxResult {
	e.t.Helper()
	e.autoFill(txn, options)

	// If TxQ is enabled and not bypassed, route through TxQ for fee escalation and queuing.
	if e.txQueue != nil && !e.bypassTxQ {
		return e.submitViaTxQ(txn)
	}

	// Direct apply path (no TxQ)
	return e.applyDirect(txn)
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

func (e *TestEnv) openLedgerApplyConfig(view *ledger.Ledger, verifySignatures bool) openledger.ApplyConfig {
	return openledger.ApplyConfig{
		BaseFee:                   e.baseFee,
		ReserveBase:               e.reserveBase,
		ReserveIncrement:          e.reserveIncrement,
		LedgerSequence:            view.Sequence(),
		NetworkID:                 e.networkID,
		ParentCloseTime:           toRippleTime(view.ParentCloseTime()),
		SkipSignatureVerification: !verifySignatures,
		Rules:                     e.rulesBuilder.Build(),
		NumberContextOverride:     e.numberContextOverride,
		ApplyFlags:                e.txQApplyFlags,
		FeeTrack:                  e.feeTrack,
	}
}

func (e *TestEnv) applyStaged(
	txn tx.Transaction,
	config tx.EngineConfig,
	transactionCount uint32,
) tx.ApplyResult {
	blob, err := tx.SerializeTransaction(txn)
	if err != nil {
		staged, snapshotErr := e.ledger.MutableSnapshotUnflushed()
		if snapshotErr != nil {
			e.t.Fatalf("snapshot malformed transaction apply: %v", snapshotErr)
		}
		engine := txengine.NewEngine(staged, config)
		engine.SetBaseTxCount(transactionCount)
		if e.invariantViolationHook != nil {
			engine.SetInvariantViolationHookForTest(e.invariantViolationHook)
		}
		applyResult := engine.Apply(txn)
		if applyResult.Applied {
			e.t.Fatalf("applied transaction cannot be serialized: %v", err)
		}
		return applyResult
	}
	if err := tx.BindRawBytes(txn, blob); err != nil {
		e.t.Fatalf("bind serialized transaction: %v", err)
	}
	engine := txengine.NewEngine(e.ledger, config)
	engine.SetBaseTxCount(transactionCount)
	if e.invariantViolationHook != nil {
		engine.SetInvariantViolationHookForTest(e.invariantViolationHook)
	}
	processor := txengine.NewBlockProcessor(engine)
	blockResult, err := processor.ApplyTransaction(txn, blob)
	if err != nil {
		e.t.Fatalf("apply transaction atomically: %v", err)
	}
	return blockResult.ApplyResult
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

	// Open-ledger admission commits only the outer Batch. Consensus replay at
	// close applies the inner transactions in canonical order.
	applyResult := e.applyStaged(txn, engineConfig, e.txInLedger)

	if applyResult.Result.IsApplied() {
		e.txInLedger++
	}
	if _, batch := txn.(tx.BatchInnerApplier); batch && applyResult.Applied {
		e.needsConsensusBuild = true
	}

	if e.replayOnClose && isRetryable(applyResult.Result) {
		e.addHeldTransaction(txn.GetCommon().Account, txn)
	}

	return txResultFromApply(applyResult)
}

// submitViaTxQ routes a transaction through the TxQ for fee escalation
// and sequence-gap queuing.
// Reference: rippled NetworkOPs::processTransaction -> TxQ::apply -> NetworkOPs::apply
func (e *TestEnv) submitViaTxQ(txn tx.Transaction) TxResult {
	return e.submitViaTxQWithLocal(txn, true)
}

func (e *TestEnv) submitViaTxQWithLocal(txn tx.Transaction, local bool) TxResult {
	e.t.Helper()

	accountAddr := txn.GetCommon().Account
	blob, err := tx.SerializeTransaction(txn)
	if err != nil {
		e.t.Fatalf("submitViaTxQ: serialize transaction: %v", err)
	}
	pending, err := openledger.ParsePendingTx(blob)
	if err != nil {
		e.t.Fatalf("submitViaTxQ: prepare transaction: %v", err)
	}
	txn = pending.Parsed
	adapter := openledger.NewTxqAdapter(e.ledger, e.openLedgerApplyConfig(e.ledger, e.VerifySignatures))
	result := e.txQueue.Apply(adapter, txn, pending.Hash, pending.Account)
	if local && (e.txQApplyFlags&tx.TapFAIL_HARD == 0 || result.Result.IsSuccess()) && result.Result != ter.TefALREADY {
		e.localTxs.PushBack(e.ledger.Sequence(), pending)
	}

	if result.Applied {
		applyResult := adapter.LastApplyResult()
		if applyResult == nil {
			e.t.Fatal("submitViaTxQ: applied transaction has no engine result")
		}
		if _, batch := txn.(tx.BatchInnerApplier); batch && applyResult.Applied {
			e.needsConsensusBuild = true
		}
		e.txInLedger = e.ledger.TxCount()
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
		if result.Result.IsSuccess() {
			e.retryHeldTransactions(accountAddr)
		}

		return txResultFromApply(*applyResult)
	}

	if result.Queued {
		return txResultFromTER(ter.TerQUEUED, true)
	}

	// A retryable (ter*) result means the transaction could not be applied yet
	// because of a sequence gap; hold it for the mid-window retry that runs when
	// the gap-filling transaction applies.
	if isRetryable(result.Result) {
		e.addHeldTransaction(accountAddr, txn)
	}

	return txResultFromTER(result.Result, false)
}

// isRetryable returns true if the transaction result indicates the transaction
// might succeed later (e.g., terPRE_SEQ, terINSUF_FEE_B).
// Reference: rippled isTerRetry()
func isRetryable(result ter.Result) bool {
	return result >= -99 && result < 0
}

// addHeldTransaction records a sequence-gap-held (ter*) transaction for the
// mid-window retry.
func (e *TestEnv) addHeldTransaction(accountAddr string, txn tx.Transaction) {
	if e.heldTxns == nil {
		e.heldTxns = make(map[string][]tx.Transaction)
	}
	e.heldTxns[accountAddr] = append(e.heldTxns[accountAddr], txn)
}

func (e *TestEnv) preparePendingTransactions(groups map[string][]tx.Transaction) []openledger.PendingTx {
	var pending []openledger.PendingTx
	for _, transactions := range groups {
		for _, transaction := range transactions {
			blob, err := tx.SerializeTransaction(transaction)
			if err != nil {
				e.t.Fatalf("serialize replay transaction: %v", err)
			}
			prepared, err := openledger.ParsePendingTx(blob)
			if err != nil {
				e.t.Fatalf("prepare replay transaction: %v", err)
			}
			pending = append(pending, prepared)
		}
	}
	return pending
}

func (e *TestEnv) retryHeldBeforeCloseViaTxQ() {
	if len(e.heldTxns) == 0 {
		return
	}
	pending := e.preparePendingTransactions(e.heldTxns)
	e.heldTxns = nil
	openledger.CanonicalSort(pending, e.ledger.ParentHash())
	for _, held := range pending {
		e.submitViaTxQWithLocal(held.Parsed, false)
	}
}

func (e *TestEnv) retryHeldBeforeCloseDirect() {
	if len(e.heldTxns) == 0 {
		return
	}
	pending := e.preparePendingTransactions(e.heldTxns)
	e.heldTxns = nil
	openledger.CanonicalSort(pending, e.ledger.ParentHash())
	var retries []openledger.PendingTx
	if err := openledger.ApplyTxs(
		e.ledger,
		pending,
		&retries,
		e.openLedgerApplyConfig(e.ledger, e.VerifySignatures),
	); err != nil {
		e.t.Fatalf("retry held transactions: %v", err)
	}
	e.txInLedger = e.ledger.TxCount()
	for _, retry := range retries {
		e.addHeldTransaction(retry.Parsed.GetCommon().Account, retry.Parsed)
	}
}

func (e *TestEnv) retryLocalsViaTxQ() {
	if e.localTxs.Size() == 0 {
		return
	}
	for _, local := range e.localTxs.GetTxSet() {
		prepared, err := openledger.ParsePendingTx(local.Blob)
		if err != nil {
			e.t.Fatalf("parse local transaction %x: %v", local.Hash, err)
		}
		if prepared.Hash != local.Hash || prepared.Account != local.Account {
			e.t.Fatalf("local transaction identity does not match blob %x", local.Hash)
		}
		e.submitViaTxQWithLocal(prepared.Parsed, false)
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
		result := e.submitViaTxQWithLocal(heldTxn, false)
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

	adapter := openledger.NewTxqAdapter(e.ledger, e.openLedgerApplyConfig(e.ledger, e.VerifySignatures))
	for e.txQueue.Accept(adapter) {
		// Accept returns true if any transactions were applied.
		// We keep looping because applying one transaction might unblock others.
	}
	e.txInLedger = e.ledger.TxCount()
}

// sortHeldBySequence sorts transactions by SeqProxy key (sequence-typed first,
// then ticket-typed, each ordered by value), matching the canonical ordering
// used at consensus close.
func sortHeldBySequence(txns []tx.Transaction) {
	sort.SliceStable(txns, func(i, j int) bool {
		return txns[i].GetCommon().SeqProxyKey() < txns[j].GetCommon().SeqProxyKey()
	})
}

// testClosedLedgerContext implements txq.ClosedLedgerContext for the test environment.
type testClosedLedgerContext struct {
	ledgerSeq uint32
	feeLevels []txq.FeeLevel
}

func (c *testClosedLedgerContext) GetLedgerSequence() uint32               { return c.ledgerSeq }
func (c *testClosedLedgerContext) GetTransactionFeeLevels() []txq.FeeLevel { return c.feeLevels }

// EnableOpenLedgerReplay enables the open-ledger consensus replay behavior.
// When enabled, Close() rebuilds the closed ledger from the parent closed
// ledger by replaying all tracked transactions in canonical order with
// retry passes. This matches rippled's standalone consensus simulation.
//
// Use this for tests that depend on:
//   - terPRE_SEQ transactions being retried as the ledger closes
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

// SubmitPseudo submits a pseudo-transaction (EnableAmendment, SetFee, UNLModify)
// directly to the engine. Pseudo-transactions bypass account lookup, sequence
// auto-fill, fee deduction, and signature verification, and are always applied
// against a closed ledger (rippled's Change::preclaim rejects them otherwise).
// Reference: rippled Change.cpp:82-91 — pseudo-txs require !view.open().
func (e *TestEnv) SubmitPseudo(txn tx.Transaction) TxResult {
	e.t.Helper()
	if txn == nil || txn.GetCommon() == nil {
		e.t.Fatal("SubmitPseudo: nil transaction")
	}

	engineConfig := e.engineConfig(e.ledger, engineConfigOpts{parentCloseFromClock: true})

	engine := txengine.NewEngine(e.ledger, engineConfig)
	applyResult := engine.ApplyPseudo(txn)

	return txResultFromApply(applyResult)
}
