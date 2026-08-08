package txq

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// AcceptContext provides the context needed to accept transactions into the open ledger.
type AcceptContext interface {
	// GetTxInLedger returns the number of transactions in the open ledger.
	GetTxInLedger() uint32

	ApplyTransactionWithFlags(txn tx.Transaction, flags tx.ApplyFlags) (ter.Result, bool)
	PreflightTransactionWithFlags(txn tx.Transaction, flags tx.ApplyFlags) ter.Result
	RulesIdentity() *amendment.Rules

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
	parentHash := ctx.GetParentHash()
	q.stateMu.Lock()
	defer q.stateMu.Unlock()

	ledgerChanged := false

	// The fee snapshot is constant for the whole Accept pass (only
	// ProcessClosedLedger mutates it, under the same lock), so take it once
	// before the loop rather than re-fetching every iteration (TxQ.cpp:1447).
	snapshot := q.feeMetrics.snapshot()

	// Process candidates from highest fee to lowest
	i := 0
	for i < len(q.byFee) {
		candidate := q.byFee[i]
		if candidate == nil {
			q.byFee = append(q.byFee[:i], q.byFee[i+1:]...)
			continue
		}
		account := candidate.Account

		aq, exists := q.byAccount[account]
		if !exists {
			// Corruption repair: do not retain an orphaned fee/id index.
			q.erase(candidate)
			continue
		}

		// For sequence-based transactions, they must be applied in order.
		// Check if this is the first sequence transaction for the account.
		if !candidate.SeqProxy.IsTicket {
			firstSeqTx := aq.FirstSeqTx()
			if firstSeqTx != nil && candidate.SeqProxy.Value > firstSeqTx.SeqProxy.Value {
				// There's an earlier sequence transaction, skip this one for now
				i++
				continue
			}
		}

		var txInLedger uint32
		q.withStateUnlocked(func() {
			txInLedger = ctx.GetTxInLedger()
		})
		requiredFeeLevel := scaleFeeLevel(snapshot, txInLedger)

		if candidate.FeeLevel < requiredFeeLevel {
			// Fee escalation means remaining transactions can't afford to get in
			break
		}

		// Amendment changes or a different submission flag set invalidate a
		// cached preflight verdict. Re-run before touching the open ledger.
		var currentRules *amendment.Rules
		q.withStateUnlocked(func() {
			currentRules = ctx.RulesIdentity()
		})
		result := ter.TesSUCCESS
		shouldApply := true
		if candidate.PreflightResult != ter.TesSUCCESS || candidate.PreflightFlags != candidate.Flags || candidate.PreflightRules != currentRules {
			var preflight ter.Result
			q.withStateUnlocked(func() {
				preflight = ctx.PreflightTransactionWithFlags(candidateTransaction(candidate), candidate.Flags)
			})
			candidate.PreflightResult = preflight
			candidate.PreflightFlags = candidate.Flags
			candidate.PreflightRules = currentRules
			if preflight != ter.TesSUCCESS {
				result = preflight
				shouldApply = false
			}
		}

		applied := false
		if shouldApply {
			// Try to apply the transaction outside the state lock so callbacks may
			// query queue snapshots reentrantly.
			q.withStateUnlocked(func() {
				result, applied = ctx.ApplyTransactionWithFlags(candidateTransaction(candidate), candidate.Flags)
			})
		}

		if applied {
			// Transaction applied successfully, remove from queue
			q.eraseAndAdvance(&i, candidate)
			ledgerChanged = true
			continue
		}

		// Transaction failed
		candidate.LastResult = result

		// Check if it's a permanent failure
		if result.IsTef() || result.IsTem() || candidate.RetriesRemaining <= 0 {
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
				// Drop the account's highest-SeqProxy entry (rbegin, tickets
				// included), but never the current candidate — rippled keeps it
				// for another chance, then advances (TxQ.cpp:1541-1556).
				q.dropLastForAccount(aq, candidate, &i)
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

// eraseAndAdvance removes a candidate and adjusts the index so the next
// appropriate candidate is tried.
// Reference: TxQ.cpp:466-502
func (q *TxQ) eraseAndAdvance(idx *int, c *candidate) {
	aq, exists := q.byAccount[c.Account]
	if !exists {
		// Defensive: the account is gone from byAccount but the candidate is
		// still in byFee. Remove it from byFee so the index advances past it
		// instead of looping forever on the same element.
		q.removeByFee(c)
		return
	}

	// Check if there's a next transaction for this account that we should try.
	nextCandidate := aq.GetNextTx(c.SeqProxy)

	// Determine what comes next in byFee after the current candidate.
	var feeNext *candidate
	if *idx+1 < len(q.byFee) {
		feeNext = q.byFee[*idx+1]
	}

	// Should we try the account's next tx before the fee-ordered next?
	// Yes if the account's next tx has a higher fee level than the next
	// byFee entry (i.e., it would sort earlier).
	useAccountNext := nextCandidate != nil &&
		(feeNext == nil || q.candidateLess(nextCandidate, feeNext))

	q.erase(c)

	if useAccountNext {
		// Find the account's next candidate in the updated byFee
		for j, cand := range q.byFee {
			if cand == nextCandidate {
				*idx = j
				return
			}
		}
	}
	// Otherwise idx already points to the right element after erase
	// (the element that was at idx+1 shifted down to idx)
}

// dropLastForAccount removes the account's highest-SeqProxy queued transaction
// (rippled's account.transactions.rbegin(), tickets included) as a drop penalty.
// It never drops `current` — the candidate Accept is processing — mirroring
// rippled's `if (endIter != candidateIter) erase(endIter)` guard, and adjusts
// idx when the dropped element sits before the current one in byFee so the
// caller's i++ lands on the right next candidate (TxQ.cpp:1541-1556).
func (q *TxQ) dropLastForAccount(aq *accountQueue, current *candidate, idx *int) {
	sorted := aq.SortedCandidates()
	if len(sorted) == 0 {
		return
	}
	dropTarget := sorted[len(sorted)-1]
	if dropTarget == current {
		// rippled keeps the current candidate even though it is the last entry.
		return
	}

	dropIdx := q.indexInByFee(dropTarget)
	q.erase(dropTarget)
	if dropIdx >= 0 && dropIdx < *idx {
		// Removing an element before the current index shifts the current
		// candidate (and everything after it) down by one.
		*idx--
	}
}

// indexInByFee returns the index of candidate c in byFee, or -1 if absent.
// Caller must hold the lock.
func (q *TxQ) indexInByFee(c *candidate) int {
	for i, cand := range q.byFee {
		if cand == c {
			return i
		}
	}
	return -1
}
