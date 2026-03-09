package txq

import (
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
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

	// Check if fee is high enough to apply directly
	if feeLevel >= requiredFeeLevel {
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

	// Transaction needs to be queued
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

	// Get or create account queue
	aq, exists := q.byAccount[account]

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

	// Check per-account limit (unless replacing)
	if replacingCandidate == nil && exists && uint32(aq.Count()) >= q.config.MaximumTxnPerAccount {
		// Check if this fills a sequence gap
		if !seqProxy.IsTicket && seqProxy.Value == acctSeq {
			// This might unblock the queue, allow it
		} else {
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}
	}

	// Validate sequence/ticket ordering
	if !seqProxy.IsTicket {
		if !exists || aq.Empty() {
			// First transaction for this account, must match account sequence
			if seqProxy.Value != acctSeq {
				if seqProxy.Value < acctSeq {
					return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
				}
				return ApplyResult{Result: tx.TerPRE_SEQ, Applied: false}
			}
		} else if replacingCandidate == nil {
			// Must follow the last queued sequence
			nextSeq := q.getNextQueuableSeq(aq, acctSeq)
			if seqProxy.Value != nextSeq {
				if seqProxy.Value < nextSeq {
					return ApplyResult{Result: tx.TefPAST_SEQ, Applied: false}
				}
				return ApplyResult{Result: tx.TerPRE_SEQ, Applied: false}
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

	consequences := computeConsequences(txn, seqProxy)
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
		// TODO: Handle TicketCreate which advances by ticket count
		cons.FollowingSeq = NewSeqProxySequence(nextSeq)
	}

	// Check if this is a blocker transaction
	switch txn.TxType() {
	case tx.TypeRegularKeySet, tx.TypeSignerListSet:
		cons.IsBlocker = true
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

// parseDrops parses a drops string to uint64.
func parseDrops(s string) uint64 {
	var drops uint64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			drops = drops*10 + uint64(c-'0')
		}
	}
	return drops
}
