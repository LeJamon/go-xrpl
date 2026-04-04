package txq

import (
	"sort"
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/account"
	"github.com/LeJamon/goXRPLd/internal/tx/offer"
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

	// PreclaimTransaction runs a simulated preclaim check against an adjusted
	// view where the account's balance and sequence have been modified to
	// reflect queued transactions. Returns 0 (tesSUCCESS) if preclaim passes
	// (likely to claim fee), or the failing TER code.
	// This is used for the multiTxn path (TxQ.cpp:1167-1170) where rippled
	// runs preclaim against a modified view to detect terINSUF_FEE_B etc.
	PreclaimTransaction(txn tx.Transaction, account [20]byte, adjustedBalance uint64, adjustedSeq uint32) tx.Result
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

	// Identify the replacement candidate (if any).
	// Reference: TxQ.cpp:860-870
	var replacingCandidate *Candidate
	if exists {
		if c, exists := aq.Transactions[seqProxy]; exists {
			replacingCandidate = c
		}
	}

	// Is there a blocker already in the account's queue? If so, don't
	// allow additional transactions in the queue (unless replacing the blocker).
	// We only need to check the first relevant entry because we require that
	// a blocker be alone in the account's queue.
	//
	// IMPORTANT: This check must come BEFORE the replacement fee check.
	// In rippled (TxQ.cpp:879-930), within the `if (acctTxCount > 0)` block:
	//   1. First check for existing blocker → telCAN_NOT_QUEUE_BLOCKED
	//   2. Then check replacement fee → telCAN_NOT_QUEUE_FEE
	// Reference: TxQ.cpp:879-893
	if acctTxCount > 0 && exists {
		firstRelevant := aq.FirstRelevant(acctSeqProx)
		if acctTxCount == 1 && firstRelevant != nil &&
			firstRelevant.Consequences.IsBlocker &&
			firstRelevant.SeqProxy != seqProxy {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_BLOCKED, Applied: false}
		}

		// Check replacement fee (requires higher fee to replace).
		// Reference: TxQ.cpp:898-930
		if replacingCandidate != nil {
			requiredRetryLevel := FeeLevel(mulDiv(uint64(replacingCandidate.FeeLevel), 100+uint64(q.config.RetrySequencePercent), 100))
			if feeLevel <= requiredRetryLevel {
				return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FEE, Applied: false}
			}
		}
	}

	// Determine if we need the multiTxn path.
	// Reference: TxQ.cpp:976 — requiresMultiTxn = true when
	// acctTxCount > 1 || !replacedTxIter (i.e. not just a simple replacement)
	requiresMultiTxn := false

	if acctTxCount == 0 {
		// There are no queued transactions for this account.
		// Reference: TxQ.cpp:946-958
		if !seqProxy.IsTicket {
			if seqProxy.Value != acctSeq {
				if seqProxy.Value < acctSeq {
					return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
				}
				return ApplyResult{Result: tx.TerPRE_SEQ, Applied: false}
			}
		}
	} else {
		// There are relevant queued transactions for this account.
		// Reference: TxQ.cpp:959-1153
		if !seqProxy.IsTicket && acctSeq > seqProxy.Value {
			return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
		}

		if acctTxCount > 1 || replacingCandidate == nil {
			// Need the multiTxn path: canBeHeld + sequence validation + balance check
			requiresMultiTxn = true

			// canBeHeld check (per-account limit).
			// Reference: TxQ.cpp:980-988 → canBeHeld (TxQ.cpp:383-447)
			if full, result := q.canBeHeld(aq, replacingCandidate, seqProxy, acctSeq); full {
				return result
			}
		}

		// Sequence validation within the multiTxn path.
		// Reference: TxQ.cpp:1006-1041
		if requiresMultiTxn && !seqProxy.IsTicket {
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
			} else if replacingCandidate == nil {
				// The tx goes after existing entries.
				// Must follow the PREVIOUS entry's followingSeq.
				// Reference: TxQ.cpp:1031-1040
				prevFollowingSeq := prevTx.Consequences.FollowingSeq.Value
				if seqProxy.Value != prevFollowingSeq {
					if seqProxy.Value < prevFollowingSeq {
						return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
					}
					// Gap not bridged by queued txns.
					return ApplyResult{Result: tx.TelCAN_NOT_QUEUE, Applied: false}
				}
			}
		}

		// In-flight balance check and multiTxn view simulation.
		// Reference: TxQ.cpp:1043-1153
		if requiresMultiTxn {
			var totalFee uint64
			var potentialSpend uint64
			for sp, c := range aq.Transactions {
				if sp.Less(acctSeqProx) {
					continue // Skip stale transactions
				}
				if sp != seqProxy {
					totalFee += c.Consequences.Fee
					potentialSpend += c.Consequences.PotentialSpend
				} else {
					// Replacement in the middle of the queue: include the
					// NEW transaction's consequences, not the old one's.
					// Reference: TxQ.cpp:1059-1066
					// Check if there's a transaction after this one in the queue.
					hasNext := false
					for sp2 := range aq.Transactions {
						if sp2.Less(acctSeqProx) {
							continue
						}
						if sp2 != seqProxy && !sp2.Less(seqProxy) {
							hasNext = true
							break
						}
					}
					if hasNext {
						totalFee += consequences.Fee
						potentialSpend += consequences.PotentialSpend
					}
				}
			}
			// NOTE: Do NOT add the new transaction's fee here.
			// In rippled (TxQ.cpp:1048-1067), the loop only iterates over
			// existing queued transactions. The new tx's fee is accounted for
			// in the preclaim check against the adjusted view, not in the
			// telCAN_NOT_QUEUE_BALANCE check.

			balance := ctx.GetAccountBalance(account)
			reserve := ctx.GetAccountReserve(0)
			baseFeeVal := ctx.GetBaseFee(txn)
			if totalFee >= balance || (reserve > 10*baseFeeVal && totalFee >= reserve) {
				return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_BALANCE, Applied: false}
			}

			// Compute potentialTotalSpend for the multiTxn view simulation.
			// Reference: TxQ.cpp:1137-1138
			// potentialTotalSpend = totalFee + min(balance - min(balance, reserve), potentialSpend)
			minBalReserve := balance
			if reserve < minBalReserve {
				minBalReserve = reserve
			}
			spendableAboveReserve := balance - minBalReserve
			if potentialSpend < spendableAboveReserve {
				spendableAboveReserve = potentialSpend
			}
			potentialTotalSpend := totalFee + spendableAboveReserve

			// Run preclaim against the adjusted balance to detect terINSUF_FEE_B.
			// Reference: TxQ.cpp:1127-1170
			// rippled creates a MultiTxn view, adjusts balance and sequence,
			// then calls preclaim(). If preclaim fails (!likelyToClaimFee),
			// the transaction is rejected with preclaim's TER code.
			if potentialTotalSpend > 0 || seqProxy.Value != acctSeq {
				var adjustedBalance uint64
				if potentialTotalSpend <= balance {
					adjustedBalance = balance - potentialTotalSpend
				}
				// The sequence should be set to the tx's sequence (if seq-based)
				// or the nextQueuableSeq (if ticket-based).
				// Reference: TxQ.cpp:1150-1152
				adjustedSeq := seqProxy.Value
				if seqProxy.IsTicket {
					adjustedSeq = q.getNextQueuableSeq(aq, acctSeq)
				}
				if result := ctx.PreclaimTransaction(txn, account, adjustedBalance, adjustedSeq); result != 0 {
					return ApplyResult{Result: result, Applied: false}
				}
			}
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
	// multiTxn.has_value() in rippled corresponds to requiresMultiTxn
	if !seqProxy.IsTicket && exists && acctTxCount > 0 && requiresMultiTxn &&
		feeLevel > requiredFeeLevel && requiredFeeLevel > FeeLevel(BaseLevel) {
		// Check if first queued sequence tx has full retries remaining
		firstSeqTx := aq.GetFirstSeqTx()
		if firstSeqTx != nil && firstSeqTx.RetriesRemaining == RetriesAllowed {
			if result := q.tryClearAccountQueue(ctx, aq, txn, seqProxy, feeLevel, txInLedger, account); result != nil {
				return *result
			}
		}
	}

	// If multiTxn was not needed, we still need canBeHeld checks.
	// Reference: TxQ.cpp:1227-1238
	if !requiresMultiTxn && exists {
		if full, result := q.canBeHeld(aq, replacingCandidate, seqProxy, acctSeq); full {
			return result
		}
	}

	// Check if queue is full (when not replacing).
	// Reference: rippled TxQ.cpp:1243-1315
	if replacingCandidate == nil && q.isFull() {
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

		endAccount := q.byAccount[lowestOther.Account]

		// Compute the effective fee level for the target account.
		// If the lowest transaction has a higher fee than ours, use its fee.
		// Otherwise, compute the average of the target account's queue.
		// Reference: rippled TxQ.cpp:1265-1292
		endEffectiveFeeLevel := lowestOther.FeeLevel
		if lowestOther.FeeLevel <= feeLevel && endAccount.Count() > 1 {
			// Compute average fee level for the target account
			var sumDiv, sumMod FeeLevel
			count := FeeLevel(endAccount.Count())
			overflow := false
			for _, txCandidate := range endAccount.Transactions {
				next := txCandidate.FeeLevel / count
				mod := txCandidate.FeeLevel % count
				if sumDiv >= ^FeeLevel(0)-next || sumMod >= ^FeeLevel(0)-mod {
					endEffectiveFeeLevel = ^FeeLevel(0)
					overflow = true
					break
				}
				sumDiv += next
				sumMod += mod
			}
			if !overflow {
				endEffectiveFeeLevel = sumDiv + sumMod/count
			}
		}

		if feeLevel > endEffectiveFeeLevel {
			// Drop the last (highest-sequence) transaction from the target account.
			// Reference: rippled TxQ.cpp:1297-1306
			var dropCandidate *Candidate
			for _, c := range endAccount.Transactions {
				if dropCandidate == nil || !c.SeqProxy.Less(dropCandidate.SeqProxy) {
					dropCandidate = c
				}
			}
			if dropCandidate != nil {
				q.erase(dropCandidate)
			}
		} else {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}
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

// canBeHeld checks whether the account queue can accept a new transaction
// without exceeding the per-account limit. Returns (true, result) when the
// transaction should be rejected, (false, _) when it can proceed.
// Reference: rippled TxQ.cpp:383-447 (canBeHeld)
func (q *TxQ) canBeHeld(aq *AccountQueue, replacingCandidate *Candidate, seqProxy SeqProxy, acctSeq uint32) (bool, ApplyResult) {
	if replacingCandidate != nil || uint32(aq.Count()) < q.config.MaximumTxnPerAccount {
		return false, ApplyResult{}
	}
	// Allow if this fills the next sequence gap in the account's queue.
	nextSeq := q.getNextQueuableSeq(aq, acctSeq)
	if !seqProxy.IsTicket && seqProxy.Value == nextSeq {
		// Check if there's a subsequent sequence-based tx in the queue
		// that this would connect to (i.e., a real gap, not just appending).
		// Reference: TxQ.cpp:440-444 upper_bound(nextQueuable)
		hasLaterSeq := false
		for sp := range aq.Transactions {
			if !sp.IsTicket && sp.Value > seqProxy.Value {
				hasLaterSeq = true
				break
			}
		}
		if !hasLaterSeq {
			return true, ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}
		// Real gap fill — allow it
		return false, ApplyResult{}
	}
	return true, ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
}

// getNextQueuableSeq returns the next sequence that can be queued for an account.
// It finds the FIRST gap in the sequence chain, not the max following sequence.
// Reference: rippled TxQ::nextQueuableSeqImpl (TxQ.cpp:1622-1666)
func (q *TxQ) getNextQueuableSeq(aq *AccountQueue, acctSeq uint32) uint32 {
	if aq == nil || aq.Empty() {
		return acctSeq
	}

	acctSeqProx := NewSeqProxySequence(acctSeq)

	// Get all sequence-based transactions sorted by SeqProxy.
	sorted := aq.GetSortedCandidates()

	// Find the first relevant sequence-based transaction (>= acctSeqProx).
	startIdx := -1
	for i, c := range sorted {
		if !c.SeqProxy.IsTicket && !c.SeqProxy.Less(acctSeqProx) {
			if c.SeqProxy == acctSeqProx {
				startIdx = i
			}
			break
		}
	}

	// If acctSeqProx is not in the queue, return acctSeq (first gap is at front).
	if startIdx < 0 {
		return acctSeq
	}

	// Walk through consecutive sequence-based transactions to find the first gap.
	attempt := sorted[startIdx].Consequences.FollowingSeq.Value
	for i := startIdx + 1; i < len(sorted); i++ {
		sp := sorted[i].SeqProxy
		if sp.IsTicket {
			continue
		}
		if sp.Less(acctSeqProx) {
			continue // Skip stale
		}
		if attempt < sp.Value {
			break // Found a gap
		}
		attempt = sorted[i].Consequences.FollowingSeq.Value
	}
	return attempt
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
		// TicketCreate consumes TicketCount sequences (including the tx itself).
		// Reference: TicketCreate.cpp makeTxConsequences returns
		// TxConsequences{tx, ticketCount}, and followingSeq() does
		// seqProx.advanceBy(sequencesConsumed) = seq + ticketCount.
		if tc, ok := txn.(*ticket.TicketCreate); ok && tc.TicketCount > 0 {
			nextSeq = seqProxy.Value + tc.TicketCount
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

	// Compute potential spend.
	// Reference: rippled Payment.cpp, CreateOffer.cpp makeTxConsequences
	switch t := txn.(type) {
	case *payment.Payment:
		if t.Amount.IsNative() {
			cons.PotentialSpend = uint64(t.Amount.Drops())
		}
	case *offer.OfferCreate:
		if t.TakerGets.IsNative() {
			cons.PotentialSpend = uint64(t.TakerGets.Drops())
		}
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
