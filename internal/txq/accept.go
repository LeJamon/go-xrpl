package txq

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// AcceptContext provides the context needed to accept transactions into the open ledger.
type AcceptContext interface {
	// GetTxInLedger returns the number of transactions in the open ledger.
	GetTxInLedger() uint32

	// GetAccountSequence returns the current sequence number for an account.
	GetAccountSequence(account [20]byte) uint32

	// ApplyTransaction attempts to apply a transaction to the open ledger.
	// Returns the result and whether the transaction was applied.
	ApplyTransaction(txn tx.Transaction) (tx.Result, bool)

	// GetParentHash returns the parent ledger hash for deterministic ordering.
	GetParentHash() [32]byte
}

// Accept attempts to move transactions from the queue into the open ledger.
// It iterates through queued transactions from highest fee to lowest,
// applying each one that meets the current fee requirements.
//
// This is called when a new open ledger is created after a ledger closes.
// Returns true if any transactions were applied.
func (q *TxQ) Accept(ctx AcceptContext) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	ledgerChanged := false
	parentHash := ctx.GetParentHash()

	// Process candidates from highest fee to lowest
	i := 0
	for i < len(q.byFee) {
		candidate := q.byFee[i]
		account := candidate.Account

		// Get account queue
		aq, exists := q.byAccount[account]
		if !exists {
			// Shouldn't happen, but handle it
			i++
			continue
		}

		// For sequence-based transactions, they must be applied in order.
		// Check if this is the first sequence transaction for the account.
		if !candidate.SeqProxy.IsTicket {
			firstSeqTx := aq.GetFirstSeqTx()
			if firstSeqTx != nil && candidate.SeqProxy.Value > firstSeqTx.SeqProxy.Value {
				// There's an earlier sequence transaction, skip this one for now
				i++
				continue
			}
		}

		// Check if the fee level is still sufficient
		txInLedger := ctx.GetTxInLedger()
		snapshot := q.feeMetrics.GetSnapshot()
		requiredFeeLevel := ScaleFeeLevel(snapshot, txInLedger)

		if candidate.FeeLevel < requiredFeeLevel {
			// Fee escalation means remaining transactions can't afford to get in
			break
		}

		// Try to apply the transaction
		result, applied := ctx.ApplyTransaction(candidate.Txn)

		if applied {
			// Transaction applied successfully, remove from queue
			q.eraseAndAdvance(&i, candidate)
			ledgerChanged = true
			continue
		}

		// Transaction failed
		candidate.LastResult = result

		// Check if it's a permanent failure
		if isTefFailure(result) || isTemMalformed(result) || candidate.RetriesRemaining <= 0 {
			// Mark penalties
			if candidate.RetriesRemaining <= 0 {
				aq.RetryPenalty = true
			} else {
				aq.DropPenalty = true
			}

			q.eraseAndAdvance(&i, candidate)
			continue
		}

		// Temporary failure, decrement retries
		if aq.RetryPenalty && candidate.RetriesRemaining > 2 {
			candidate.RetriesRemaining = 1
		} else {
			candidate.RetriesRemaining--
		}

		// If queue is nearly full and this account has issues, drop from back
		if aq.DropPenalty && aq.Count() > 1 && q.isFullPct(95) {
			if candidate.SeqProxy.IsTicket {
				// Drop this ticketed transaction since order doesn't matter
				q.eraseAndAdvance(&i, candidate)
			} else {
				// Drop the last transaction for this account
				q.dropLastForAccount(aq)
				i++
			}
			continue
		}

		i++
	}

	// Rebuild byFee with new parent hash for deterministic ordering
	if parentHash != q.parentHash {
		q.parentHash = parentHash
		q.rebuildByFee()
	}

	return ledgerChanged
}

// eraseAndAdvance removes a candidate and advances/adjusts the index appropriately.
func (q *TxQ) eraseAndAdvance(idx *int, c *Candidate) {
	aq, exists := q.byAccount[c.Account]
	if !exists {
		return
	}

	// Check if there's a next transaction for this account that we should try
	var nextCandidate *Candidate
	if !c.SeqProxy.IsTicket {
		// For sequence-based, look for the next sequence
		nextSeq := c.Consequences.FollowingSeq.Value
		for sp, candidate := range aq.Transactions {
			if !sp.IsTicket && sp.Value == nextSeq {
				nextCandidate = candidate
				break
			}
		}
	}

	// Remove the current candidate
	q.erase(c)

	// If there's a next candidate that should be tried before continuing
	// with the fee-ordered iteration, check its position
	if nextCandidate != nil && *idx < len(q.byFee) {
		// Find where the next candidate is in byFee
		for j, cand := range q.byFee {
			if cand == nextCandidate && j < *idx {
				// The next candidate is earlier in the list, adjust index
				*idx = j
				return
			}
		}
	}

	// Index doesn't need adjustment since we removed an element at the current position
}

// dropLastForAccount removes the last (highest sequence) transaction for an account.
func (q *TxQ) dropLastForAccount(aq *AccountQueue) {
	if aq.Empty() {
		return
	}

	// Find the highest sequence transaction
	var lastCandidate *Candidate
	for _, c := range aq.Transactions {
		if !c.SeqProxy.IsTicket {
			if lastCandidate == nil || c.SeqProxy.Value > lastCandidate.SeqProxy.Value {
				lastCandidate = c
			}
		}
	}

	if lastCandidate != nil {
		q.erase(lastCandidate)
	}
}

// isTefFailure returns true if the result is a tef (fee claimed, not applied) failure.
func isTefFailure(result tx.Result) bool {
	return result <= -180 && result >= -199
}

// isTemMalformed returns true if the result is a tem (malformed) failure.
func isTemMalformed(result tx.Result) bool {
	return result <= -200 && result >= -299
}
