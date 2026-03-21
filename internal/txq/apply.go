package txq

import (
	"sort"
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/tx/ticket"
)

// ApplyResult represents the result of trying to apply or queue a transaction.
type ApplyResult struct {
	// Result is the transaction result code
	Result tx.Result

	// Applied is true if the transaction was applied to the open ledger
	Applied bool

	// Queued is true if the transaction was added to the queue
	Queued bool
}

// ApplyContext provides the context needed to apply a transaction.
// This decouples TxQ from the specific ledger implementation.
type ApplyContext interface {
	// GetAccountSequence returns the current sequence number for an account.
	// Returns 0 if the account doesn't exist.
	GetAccountSequence(account [20]byte) uint32

	// AccountExists returns true if the account exists in the ledger.
	AccountExists(account [20]byte) bool

	// TicketExists returns true if the ticket exists for the account.
	TicketExists(account [20]byte, ticketSeq uint32) bool

	// GetAccountBalance returns the XRP balance in drops.
	GetAccountBalance(account [20]byte) uint64

	// GetAccountReserve returns the reserve requirement for an account.
	GetAccountReserve(ownerCount uint32) uint64

	// GetBaseFee returns the base fee for a transaction.
	GetBaseFee(txn tx.Transaction) uint64

	// GetTxInLedger returns the number of transactions in the open ledger.
	GetTxInLedger() uint32

	// GetLedgerSequence returns the current ledger sequence.
	GetLedgerSequence() uint32

	// ApplyTransaction attempts to apply a transaction to the open ledger.
	// Returns the result and whether the transaction was applied.
	ApplyTransaction(txn tx.Transaction) (tx.Result, bool)
}

// Apply attempts to apply a transaction or queue it for later.
// This is the main entry point for submitting transactions.
//
// The transaction goes through these steps:
// 1. Preflight validation (syntax, signature, etc.)
// 2. Check if fee is high enough to apply directly
// 3. If not, check if it can be queued
// 4. Queue the transaction if conditions are met
//
// Returns terQUEUED if the transaction was queued, or the result of application.
func (q *TxQ) Apply(ctx ApplyContext, txn tx.Transaction, txID [32]byte, account [20]byte) ApplyResult {
	// Compute fee level
	common := txn.GetCommon()
	if common == nil {
		return ApplyResult{Result: tx.TefINTERNAL, Applied: false}
	}

	baseFee := ctx.GetBaseFee(txn)
	feePaid, _ := strconv.ParseUint(common.Fee, 10, 64)
	feeLevel := ToFeeLevel(feePaid, baseFee)

	// Get account info
	acctSeq := ctx.GetAccountSequence(account)
	txInLedger := ctx.GetTxInLedger()
	ledgerSeq := ctx.GetLedgerSequence()

	// Determine SeqProxy (sequence or ticket)
	var seqProxy SeqProxy
	if common.TicketSequence != nil && *common.TicketSequence != 0 {
		seqProxy = NewSeqProxyTicket(*common.TicketSequence)
	} else if common.Sequence != nil {
		seqProxy = NewSeqProxySequence(*common.Sequence)
	} else {
		// No sequence specified
		return ApplyResult{Result: tx.TefINTERNAL, Applied: false}
	}

	// Get LastLedgerSequence if set
	var lastValid uint32
	if common.LastLedgerSequence != nil {
		lastValid = *common.LastLedgerSequence
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	snapshot := q.feeMetrics.GetSnapshot()
	requiredFeeLevel := ScaleFeeLevel(snapshot, txInLedger)

	// Only attempt direct apply if sequence matches or is a ticket.
	// For future-sequence transactions, skip straight to queuing.
	// Reference: rippled TxQ::tryDirectApply(), TxQ.cpp:1696-1699
	canDirectApply := seqProxy.IsTicket || seqProxy.Value == acctSeq

	// Check if fee is high enough to apply directly
	if canDirectApply && feeLevel >= requiredFeeLevel {
		// Try to apply directly
		result, applied := ctx.ApplyTransaction(txn)
		if applied {
			// If we replaced a queued transaction, remove it
			if aq, exists := q.byAccount[account]; exists {
				if c, exists := aq.Transactions[seqProxy]; exists {
					q.erase(c)
				}
			}
			return ApplyResult{Result: result, Applied: true}
		}
		// Transaction failed to apply, may still be able to queue
		if !canBeQueued(result) {
			return ApplyResult{Result: result, Applied: false}
		}
	}

	// Transaction needs to be queued.
	// AccountTxnID is not supported by the transaction queue.
	// Reference: TxQ.cpp:394-399 (canBeHeld)
	if common.AccountTxnID != "" {
		return ApplyResult{Result: tx.TelCAN_NOT_QUEUE, Applied: false}
	}

	// Check if account exists
	if !ctx.AccountExists(account) {
		return ApplyResult{Result: tx.TerNO_ACCOUNT, Applied: false}
	}

	// For ticket-based transactions, verify the ticket exists
	if seqProxy.IsTicket {
		if !ctx.TicketExists(account, seqProxy.Value) {
			if seqProxy.Value < acctSeq {
				return ApplyResult{Result: tx.TefNO_TICKET, Applied: false}
			}
			return ApplyResult{Result: tx.TerPRE_TICKET, Applied: false}
		}
	}

	// Check if LastLedgerSequence is far enough in the future
	if lastValid != 0 && lastValid < ledgerSeq+q.config.MinimumLastLedgerBuffer {
		return ApplyResult{Result: tx.TelCAN_NOT_QUEUE, Applied: false}
	}

	// Compute consequences early for blocker detection
	consequences := computeConsequences(txn, seqProxy)

	// Get or create account queue.
	// Compute acctTxCount using only "relevant" transactions: those with
	// seqProxy >= the account's current sequence. This mirrors rippled's
	// lower_bound(acctSeqProx) filtering (TxQ.cpp:809-830) which ignores
	// stale sequence-based transactions that slipped into the ledger while
	// the queue wasn't watching.
	aq, exists := q.byAccount[account]
	acctSeqProx := NewSeqProxySequence(acctSeq)
	acctTxCount := 0
	if exists {
		acctTxCount = aq.RelevantCount(acctSeqProx)
	}

	// Is tx a blocker? If so there are very limited conditions when it
	// is allowed in the TxQ:
	//  1. If the account's queue is empty or
	//  2. If the blocker replaces the only entry in the account's queue.
	// Reference: TxQ.cpp:832-856
	if consequences.IsBlocker {
		if acctTxCount > 1 {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_BLOCKS, Applied: false}
		}
		if acctTxCount == 1 {
			firstRelevant := aq.FirstRelevant(acctSeqProx)
			if firstRelevant == nil || firstRelevant.SeqProxy != seqProxy {
				return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_BLOCKS, Applied: false}
			}
		}
	}

	// Check for replacement
	var replacingCandidate *Candidate
	if exists {
		if c, exists := aq.Transactions[seqProxy]; exists {
			replacingCandidate = c

			// Need higher fee to replace
			requiredRetryLevel := FeeLevel(mulDiv(uint64(c.FeeLevel), 100+uint64(q.config.RetrySequencePercent), 100))
			if feeLevel <= requiredRetryLevel {
				return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FEE, Applied: false}
			}
		}
	}

	// Is there a blocker already in the account's queue? If so, don't
	// allow additional transactions in the queue (unless replacing the blocker).
	// We only need to check the first relevant entry because we require that
	// a blocker be alone in the account's queue.
	// Reference: TxQ.cpp:879-893
	if acctTxCount > 0 && exists {
		firstRelevant := aq.FirstRelevant(acctSeqProx)
		if acctTxCount == 1 && firstRelevant != nil &&
			firstRelevant.Consequences.IsBlocker &&
			firstRelevant.SeqProxy != seqProxy {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_BLOCKED, Applied: false}
		}
	}

	// Check per-account limit (unless replacing).
	// Reference: TxQ.cpp:425-447
	if replacingCandidate == nil && exists && uint32(acctTxCount) >= q.config.MaximumTxnPerAccount {
		// Allow if this fills the next sequence gap in the account's queue.
		nextSeq := q.getNextQueuableSeq(aq, acctSeq)
		if !seqProxy.IsTicket && seqProxy.Value == nextSeq {
			// Check if there's a subsequent sequence-based tx in the queue
			// that this would connect to (i.e., a real gap, not just appending).
			hasLaterSeq := false
			for sp := range aq.Transactions {
				if !sp.IsTicket && sp.Value > seqProxy.Value {
					hasLaterSeq = true
					break
				}
			}
			if !hasLaterSeq {
				return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
			}
			// Real gap fill — allow it
		} else {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}
	}

	// Validate sequence/ticket ordering.
	// Use acctTxCount (relevant count) to decide between "no queued txns" and
	// "has queued txns" paths, matching rippled's logic.
	// Reference: TxQ.cpp:946-1041
	if !seqProxy.IsTicket {
		if acctTxCount == 0 {
			// No relevant transactions for this account in the queue.
			// Must match account sequence.
			if seqProxy.Value != acctSeq {
				if seqProxy.Value < acctSeq {
					return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
				}
				return ApplyResult{Result: tx.TerPRE_SEQ, Applied: false}
			}
		} else {
			// There are relevant transactions in the queue.
			// Reference: TxQ.cpp:966
			if acctSeq > seqProxy.Value {
				return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
			}

			if replacingCandidate == nil {
				// Check if the tx goes at the front of the queue (before all
				// existing relevant entries). Reference: TxQ.cpp:1006-1030
				prevTx := aq.GetPrevTx(seqProxy)
				goesAtFront := prevTx == nil || seqProxy.Less(prevTx.SeqProxy)
				// Also treat as front if prevTx is stale (< acctSeqProx)
				if prevTx != nil && prevTx.SeqProxy.Less(acctSeqProx) {
					goesAtFront = true
				}

				if goesAtFront {
					// The tx goes at the front of the queue.
					// The first Sequence in the queue must match acctSeq.
					if seqProxy.Value < acctSeq {
						return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
					}
					if seqProxy.Value > acctSeq {
						return ApplyResult{Result: tx.TerPRE_SEQ, Applied: false}
					}
				} else {
					// The tx goes after existing entries.
					// Must follow the last queued sequence.
					nextSeq := q.getNextQueuableSeq(aq, acctSeq)
					if seqProxy.Value != nextSeq {
						if seqProxy.Value < nextSeq {
							return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
						}
						// Gap not bridged by queued txns.
						// Reference: TxQ.cpp:1038-1040
						return ApplyResult{Result: tx.TelCAN_NOT_QUEUE, Applied: false}
					}
				}
			}
		}
	}

	// In-flight balance check: when multiple txns are queued for the same
	// account, verify the total fees don't exceed the account's balance or
	// the minimum reserve. Only considers relevant transactions (seqProxy >= acctSeqProx).
	// Reference: TxQ.cpp:1043-1125
	if exists && acctTxCount > 0 && replacingCandidate == nil {
		var totalFee uint64
		var totalSpend uint64
		for sp, c := range aq.Transactions {
			if sp.Less(acctSeqProx) {
				// Skip stale transactions
				continue
			}
			if sp != seqProxy {
				totalFee += c.Consequences.Fee
				totalSpend += c.Consequences.PotentialSpend
			}
		}
		// Add the new transaction's fee
		totalFee += consequences.Fee

		balance := ctx.GetAccountBalance(account)
		reserve := ctx.GetAccountReserve(0) // minimum reserve
		baseFeeVal := ctx.GetBaseFee(txn)
		if totalFee >= balance || (reserve > 10*baseFeeVal && totalFee >= reserve) {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_BALANCE, Applied: false}
		}
	}

	// Try to clear the account queue by paying the escalated series fee.
	// This allows a high-fee transaction to "rescue" earlier queued txns.
	// Reference: rippled TxQ::tryClearAccountQueueUpThruTx, TxQ.cpp:518-614
	//
	// Conditions (from rippled TxQ.cpp:1198-1200):
	// 1. Transaction uses a sequence (not ticket)
	// 2. Account has queued transactions
	// 3. Multi-tx validation passed (we got here without returning)
	// 4. First queued tx hasn't failed before (full retries)
	// 5. Fee level paid > required fee level (can afford escalation)
	// 6. Fee escalation is active (required > baseLevel)
	// multiTxn is set in rippled when (acctTxCount > 1 || !replacedTxIter)
	hasMultiTxn := acctTxCount > 1 || replacingCandidate == nil
	if !seqProxy.IsTicket && exists && acctTxCount > 0 && hasMultiTxn &&
		feeLevel > requiredFeeLevel && requiredFeeLevel > FeeLevel(BaseLevel) {
		// Check if first queued sequence tx has full retries remaining
		firstSeqTx := aq.GetFirstSeqTx()
		if firstSeqTx != nil && firstSeqTx.RetriesRemaining == RetriesAllowed {
			if result := q.tryClearAccountQueue(ctx, aq, txn, seqProxy, feeLevel, txInLedger, account); result != nil {
				return *result
			}
		}
	}

	// Check if queue is full (when not replacing)
	if replacingCandidate == nil && q.isFull() {
		// Need to kick something out
		// Find the lowest-fee candidate from a different account
		var lowestOther *Candidate
		for i := len(q.byFee) - 1; i >= 0; i-- {
			c := q.byFee[i]
			if c.Account != account {
				lowestOther = c
				break
			}
		}

		if lowestOther == nil {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}

		// Need to have a higher fee to kick it out
		if feeLevel <= lowestOther.FeeLevel {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}

		// Remove the lowest fee candidate
		q.erase(lowestOther)
	}

	// Remove the candidate being replaced
	if replacingCandidate != nil {
		q.erase(replacingCandidate)
	}

	// Create and add the new candidate
	if !exists {
		aq = NewAccountQueue(account)
		q.byAccount[account] = aq
	}

	candidate := NewCandidate(
		txn,
		txID,
		account,
		feeLevel,
		seqProxy,
		lastValid,
		tx.TesSUCCESS, // preflight passed if we got here
		consequences,
	)

	aq.Add(candidate)
	q.insertByFee(candidate)

	return ApplyResult{Result: tx.TerQUEUED, Queued: true}
}

// tryClearAccountQueue attempts to clear all queued transactions for an account
// up through the new transaction by paying the escalated series fee.
// Returns nil if the attempt should be skipped (fall through to normal queuing),
// or an ApplyResult if the attempt produced a definitive result.
//
// Reference: rippled TxQ::tryClearAccountQueueUpThruTx, TxQ.cpp:518-614
func (q *TxQ) tryClearAccountQueue(
	ctx ApplyContext,
	aq *AccountQueue,
	txn tx.Transaction,
	seqProxy SeqProxy,
	feeLevelPaid FeeLevel,
	txInLedger uint32,
	account [20]byte,
) *ApplyResult {
	// Collect queued sequence-based transactions that come BEFORE the new tx.
	// These need to be applied first in order.
	var preceding []*Candidate
	for sp, c := range aq.Transactions {
		if sp.Less(seqProxy) {
			preceding = append(preceding, c)
		}
	}

	if len(preceding) == 0 {
		return nil
	}

	// Sort preceding by SeqProxy (ascending order for application)
	sort.Slice(preceding, func(i, j int) bool {
		return preceding[i].SeqProxy.Less(preceding[j].SeqProxy)
	})

	dist := uint32(len(preceding))

	// Compute the required total fee level for clearing dist+1 transactions.
	// This is the sum of escalated fees for positions [txInLedger+1, txInLedger+dist+1].
	snapshot := q.feeMetrics.GetSnapshot()
	requiredTotalFeeLevel, ok := EscalatedSeriesFeeLevel(snapshot, txInLedger, 0, dist+1)
	if !ok {
		// Overflow, can't verify
		return nil
	}

	// Sum the fee levels of all preceding transactions plus the new one.
	totalFeeLevelPaid := feeLevelPaid
	for _, c := range preceding {
		totalFeeLevelPaid += c.FeeLevel
	}

	// If total fee is not enough, fall through to normal queuing.
	if totalFeeLevelPaid < requiredTotalFeeLevel {
		return nil
	}

	// Total fee is sufficient. Try to apply all preceding transactions in order.
	// TODO: use sandbox view for full atomicity (matching rippled's OpenView).
	// Currently, successfully-applied preceding txns cannot be rolled back if a
	// later one fails. We track applied candidates to keep the queue consistent.
	var applied []*Candidate
	for _, c := range preceding {
		result, ok := ctx.ApplyTransaction(c.Txn)
		c.RetriesRemaining--
		c.LastResult = result

		if result == tx.TefNO_TICKET {
			// Ticket was already consumed; treat as success for clearing purposes.
			applied = append(applied, c)
			continue
		}

		if !ok {
			// A preceding transaction failed to apply. Erase already-applied
			// candidates from the queue to stay consistent with ledger state.
			for _, a := range applied {
				q.erase(a)
			}
			return nil
		}
		applied = append(applied, c)
	}

	// All preceding transactions applied. Now apply the new transaction.
	result, ok := ctx.ApplyTransaction(txn)
	if ok {
		// Remove all applied preceding transactions from the queue.
		for _, c := range applied {
			q.erase(c)
		}
		// Also remove the replacement if one exists at the new tx's seqProxy.
		if c, exists := aq.Transactions[seqProxy]; exists {
			q.erase(c)
		}
		return &ApplyResult{Result: result, Applied: true}
	}

	// New transaction failed but preceding ones were applied.
	// Remove the applied preceding transactions from the queue.
	for _, c := range applied {
		q.erase(c)
	}
	return &ApplyResult{Result: result, Applied: false}
}

// getNextQueuableSeq returns the next sequence number that can be queued for an account.
func (q *TxQ) getNextQueuableSeq(aq *AccountQueue, acctSeq uint32) uint32 {
	if aq == nil || aq.Empty() {
		return acctSeq
	}

	// Find the highest sequence-based transaction
	maxSeq := acctSeq
	for seqProxy, c := range aq.Transactions {
		if !seqProxy.IsTicket {
			// Use the following sequence based on consequences
			followingSeq := c.Consequences.FollowingSeq.Value
			if followingSeq > maxSeq {
				maxSeq = followingSeq
			}
		}
	}

	return maxSeq
}

// canBeQueued returns true if the result code indicates the transaction might succeed later.
func canBeQueued(result tx.Result) bool {
	// Only certain failures are worth retrying
	switch result {
	case tx.TerPRE_SEQ, tx.TerQUEUED:
		return true
	default:
		return false
	}
}

// computeConsequences determines the potential impact of a transaction.
func computeConsequences(txn tx.Transaction, seqProxy SeqProxy) TxConsequences {
	common := txn.GetCommon()
	fee, _ := strconv.ParseUint(common.Fee, 10, 64)
	cons := TxConsequences{
		Fee: fee,
	}

	// Compute following sequence
	if seqProxy.IsTicket {
		// Tickets don't advance sequence
		cons.FollowingSeq = seqProxy
	} else {
		nextSeq := seqProxy.Value + 1
		// TicketCreate consumes TicketCount additional sequences.
		// Reference: TicketCreate.cpp makeTxConsequences
		if tc, ok := txn.(*ticket.TicketCreate); ok && tc.TicketCount > 0 {
			nextSeq = seqProxy.Value + 1 + tc.TicketCount
		}
		cons.FollowingSeq = NewSeqProxySequence(nextSeq)
	}

	// Check if this is a blocker transaction.
	// Reference: SetAccount.cpp:34-55 (makeTxConsequences), applySteps.cpp:140
	switch txn.TxType() {
	case tx.TypeRegularKeySet, tx.TypeSignerListSet:
		cons.IsBlocker = true
	case tx.TypeAccountSet:
		cons.IsBlocker = isAccountSetBlocker(txn)
	}

	// Compute potential spend
	switch t := txn.(type) {
	case *payment.Payment:
		if t.Amount.IsNative() {
			cons.PotentialSpend = uint64(t.Amount.Drops())
		}
		// TODO: Add offer.OfferCreate case when offer package is re-enabled
	}

	return cons
}

// isAccountSetBlocker returns true if the AccountSet transaction is a blocker.
// An AccountSet is a blocker if it sets/clears flags that affect auth behavior.
// Reference: SetAccount.cpp:34-55 (makeTxConsequences)
func isAccountSetBlocker(txn tx.Transaction) bool {
	as, ok := txn.(*account.AccountSet)
	if !ok {
		return false
	}

	// Check transaction flags (tfRequireAuth | tfOptionalAuth)
	common := txn.GetCommon()
	if common.Flags != nil {
		flags := *common.Flags
		if flags&(account.AccountSetTxFlagRequireAuth|account.AccountSetTxFlagOptionalAuth) != 0 {
			return true
		}
	}

	// Check SetFlag for asfRequireAuth(2), asfDisableMaster(4), asfAccountTxnID(5)
	if as.SetFlag != nil {
		switch *as.SetFlag {
		case account.AccountSetFlagRequireAuth,
			account.AccountSetFlagDisableMaster,
			account.AccountSetFlagAccountTxnID:
			return true
		}
	}

	// Check ClearFlag for asfRequireAuth(2), asfDisableMaster(4), asfAccountTxnID(5)
	if as.ClearFlag != nil {
		switch *as.ClearFlag {
		case account.AccountSetFlagRequireAuth,
			account.AccountSetFlagDisableMaster,
			account.AccountSetFlagAccountTxnID:
			return true
		}
	}

	return false
}
