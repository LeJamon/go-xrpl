package testing

import (
	"crypto/sha512"
	"sort"
	"time"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/txq"
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

	if e.replayOnClose {
		e.closeWithReplay()
		return
	}

	// Record the total number of transactions in the closing ledger for TxQ
	// metrics. closingTxTotal includes inner batch txns as separate entries,
	// matching rippled's closed ledger tx map behavior.
	closingTxCount := e.closingTxTotal

	// Advance time
	e.clock.Advance(10 * time.Second)

	// Close current ledger
	if err := e.ledger.Close(e.clock.Now(), 0); err != nil {
		e.t.Fatalf("Failed to close ledger: %v", err)
	}

	// Validate the ledger (in test mode, we auto-validate)
	if err := e.ledger.SetValidated(); err != nil {
		e.t.Fatalf("Failed to validate ledger: %v", err)
	}

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
		// Build fee levels for all transactions in the closed ledger.
		// In the test env, all transactions pay the base fee, so their fee
		// level is BaseLevel (256).
		feeLevels := make([]txq.FeeLevel, closingTxCount)
		for i := range feeLevels {
			feeLevels[i] = txq.FeeLevel(txq.BaseLevel)
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
	e.txInLedger = 0
	e.closingTxTotal = 0

	// Accept queued transactions into the new open ledger.
	// Reference: rippled TxQ::accept called when new open ledger is created.
	if e.txQueue != nil {
		e.drainQueue()
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

	// Advance time
	e.clock.Advance(10 * time.Second)

	// Collect all transactions to replay:
	// 1. Transactions submitted during this open ledger
	// 2. Held transactions from previous ledgers (terPRE_SEQ etc)
	var allTxns []tx.Transaction
	allTxns = append(allTxns, e.openLedgerTxns...)
	for _, held := range e.heldTxns {
		allTxns = append(allTxns, held...)
	}

	// Clear held transactions -- they will be re-held if they still fail
	e.heldTxns = nil

	// Sort transactions in canonical order.
	// Reference: rippled CanonicalTXSet orders by (account, seqProxy, txID).
	// We use (account, sequence) as a simplified deterministic ordering.
	sortCanonical(allTxns)

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

	// Apply all transactions with retry passes, matching rippled's
	// applyTransactions() in BuildLedger.cpp.
	// Multiple passes are needed because:
	// - A batch in pass 1 may create objects (Check, Ticket) that
	//   a standalone transaction needs
	// - A payment in pass 1 may advance sequences for a later transaction
	const maxRetryPasses = 5  // LEDGER_RETRY_PASSES in rippled
	const maxTotalPasses = 10 // LEDGER_TOTAL_PASSES in rippled

	remaining := allTxns
	certainRetry := true

	for pass := 0; pass < maxTotalPasses && len(remaining) > 0; pass++ {
		var retry []tx.Transaction
		changes := 0

		for _, txn := range remaining {
			result := e.applyForReplay(txn)

			switch {
			case result.IsApplied():
				// Transaction was applied to ledger (tesSUCCESS or tec).
				// In rippled's applyTransaction(), applied results return
				// Success and are erased from the canonical set -- NOT retried.
				// Retrying an applied transaction would cause double fee
				// charging and state corruption.
				// Reference: rippled apply.cpp applyTransaction() line 260-275
				changes++
			case isRetryable(result):
				// Transaction may succeed later (terPRE_SEQ etc)
				retry = append(retry, txn)
			default:
				// Permanent failure (tef, tem, tel) -- drop the transaction
			}
		}

		remaining = retry

		// A non-retry pass made no changes
		if changes == 0 && !certainRetry {
			break
		}

		// Stop retry passes if no progress
		if changes == 0 || pass >= maxRetryPasses {
			certainRetry = false
		}
	}

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
	e.openLedgerTxns = nil
	e.txInLedger = 0
	e.closingTxTotal = 0

	// Update TxQ metrics if applicable
	// (Not typically used together with replay, but handle for completeness)
	if e.txQueue != nil {
		e.drainQueue()
	}
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

	// If TxQ is enabled, route through TxQ for fee escalation and queuing.
	if e.txQueue != nil {
		return e.submitViaTxQ(txn)
	}

	// Direct apply path (no TxQ)
	return e.applyDirect(txn)
}

// applyDirect applies a transaction directly without TxQ routing.
// This is the original Submit path.
func (e *TestEnv) applyDirect(txn tx.Transaction) TxResult {
	e.t.Helper()

	parentCloseTime := uint32(e.clock.Now().Unix() - 946684800)
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
		// For batch transactions, also count inner txns for fee metrics.
		// Reference: rippled counts inner batch txns as separate entries in
		// the closed ledger's tx map, which affects ProcessClosedLedger.
		if counter, ok := txn.(innerTxCounter); ok {
			e.closingTxTotal += uint32(counter.InnerTxCount())
		}
	}

	// Track transaction for replay-on-close.
	// All submitted transactions (success, tec, or retryable failures) are
	// recorded so Close() can rebuild the ledger from the parent state.
	// Reference: rippled's open ledger tx map includes ALL applied txns.
	if e.replayOnClose {
		e.openLedgerTxns = append(e.openLedgerTxns, txn)

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
	if isRetryable(result.Result) {
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

// addHeldTransaction adds a transaction to the held map for later retry.
// Reference: rippled LedgerMaster::addHeldTransaction
func (e *TestEnv) addHeldTransaction(accountAddr string, txn tx.Transaction) {
	if e.heldTxns == nil {
		e.heldTxns = make(map[string][]tx.Transaction)
	}
	e.heldTxns[accountAddr] = append(e.heldTxns[accountAddr], txn)
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
// process. Returns the result code. The transaction is applied to the
// current e.ledger.
func (e *TestEnv) applyForReplay(txn tx.Transaction) tx.Result {
	parentCloseTime := uint32(e.clock.Now().Unix() - 946684800)
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
		if counter, ok := txn.(innerTxCounter); ok {
			e.closingTxTotal += uint32(counter.InnerTxCount())
		}
	}

	return applyResult.Result
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

func (c *testClosedLedgerContext) GetLedgerSequence() uint32            { return c.ledgerSeq }
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
	return c.env.baseFee
}

func (c *testTxQApplyContext) GetTxInLedger() uint32 {
	return c.env.txInLedger
}

func (c *testTxQApplyContext) GetLedgerSequence() uint32 {
	return c.env.ledger.Sequence()
}

func (c *testTxQApplyContext) ApplyTransaction(txn tx.Transaction) (tx.Result, bool) {
	parentCloseTime := uint32(c.env.clock.Now().Unix() - 946684800)
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
		OpenLedger:                c.env.openLedger,
	}

	engine := tx.NewEngine(c.env.ledger, engineConfig)
	applyResult := engine.Apply(txn)

	applied := applyResult.Result.IsApplied()
	if applied {
		c.env.txInLedger++
		c.env.closingTxTotal++
		if counter, ok := txn.(innerTxCounter); ok {
			c.env.closingTxTotal += uint32(counter.InnerTxCount())
		}
	}
	return applyResult.Result, applied
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
	parentCloseTime := uint32(c.env.clock.Now().Unix() - 946684800)
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
		OpenLedger:                c.env.openLedger,
	}

	engine := tx.NewEngine(c.env.ledger, engineConfig)
	applyResult := engine.Apply(txn)

	applied := applyResult.Result.IsApplied()
	if applied {
		c.env.txInLedger++
		c.env.closingTxTotal++
		if counter, ok := txn.(innerTxCounter); ok {
			c.env.closingTxTotal += uint32(counter.InnerTxCount())
		}
	}
	return applyResult.Result, applied
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
// - terPRE_SEQ transactions being retried after close
// - tec transactions being re-applied from a clean state after
//   prerequisite objects are created by batch transactions
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
// auto-fill, fee deduction, and signature verification.
// Reference: rippled Change.cpp -- pseudo-txs have zero account, zero fee, no sigs.
func (e *TestEnv) SubmitPseudo(transaction interface{}) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	parentCloseTime := uint32(e.clock.Now().Unix() - 946684800)
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
