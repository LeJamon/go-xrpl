package txq

import (
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/offer"
	"github.com/LeJamon/go-xrpl/internal/tx/paychan"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ticket"
	"github.com/LeJamon/go-xrpl/internal/tx/xchain"
)

// ApplyResult represents the result of trying to apply or queue a transaction.
type ApplyResult struct {
	Result  tx.Result
	Applied bool
	Queued  bool
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

	// PreflightTransaction runs the preflight pipeline (syntax, signature,
	// tx-type validation) against the open view's rules, returning 0
	// (tesSUCCESS) when the transaction is well-formed or the failing TER
	// otherwise. Mirrors rippled running preflight on every submission
	// before the apply-vs-queue decision (TxQ.cpp:743-745).
	PreflightTransaction(txn tx.Transaction) tx.Result

	// PreclaimTransaction runs preclaim against a view whose account balance
	// and sequence have been set to adjustedBalance/adjustedSeq. For the
	// multiTxn path these reflect the in-flight queued chain (TxQ.cpp:1137-1170);
	// for the single-tx paths they are the account's actual balance and
	// sequence, so preclaim runs against the unmodified open view. Returns 0
	// (tesSUCCESS) if preclaim passes, or the failing TER code.
	PreclaimTransaction(txn tx.Transaction, account [20]byte, adjustedBalance uint64, adjustedSeq uint32) tx.Result

	// GetApplyFlags returns the engine ApplyFlags driving this
	// submission. Implementations that don't carry flags (legacy test
	// adapters) can return 0; TxQ only inspects TapFAIL_HARD to mirror
	// rippled TxQ.cpp:393-399 (fail-hard txs are never held).
	GetApplyFlags() tx.ApplyFlags

	// NewSandbox returns an isolated child context backed by a mutable
	// snapshot of this view. Transactions applied to the sandbox do not
	// touch the live view until Commit folds them back in; discarding the
	// sandbox (letting it fall out of scope) rolls everything back. Mirrors
	// rippled's `OpenView sandbox(open_ledger, &view, view.rules())` and
	// `sandbox.apply(view)` (TxQ.cpp:1202, 1216-1218).
	NewSandbox() (SandboxContext, error)
}

// SandboxContext is an isolated view used to apply a batch of transactions
// atomically. Apply each transaction via ApplyTransaction; call Commit only
// when the whole batch succeeds to fold the accumulated state back into the
// parent view. If Commit is never called the sandbox is simply discarded.
type SandboxContext interface {
	// ApplyTransaction applies txn to the sandbox view, returning the result
	// and whether it was applied (same semantics as ApplyContext).
	ApplyTransaction(txn tx.Transaction) (tx.Result, bool)

	// Commit folds the sandbox's accumulated state back into the parent view.
	Commit() error
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

	// Preflight every submission before deciding apply-vs-queue, so a
	// structurally invalid or badly-signed transaction is rejected with its
	// preflight TER instead of being silently held as terQUEUED.
	// Reference: rippled TxQ.cpp:743-745.
	if result := ctx.PreflightTransaction(txn); result != tx.TesSUCCESS {
		return ApplyResult{Result: result, Applied: false}
	}

	baseFee := ctx.GetBaseFee(txn)
	feePaid, err := strconv.ParseUint(common.Fee, 10, 64)
	if err != nil {
		return ApplyResult{Result: tx.TemBAD_FEE, Applied: false}
	}
	feeLevel := ToFeeLevel(feePaid, baseFee)

	acctSeq := ctx.GetAccountSequence(account)
	txInLedger := ctx.GetTxInLedger()
	ledgerSeq := ctx.GetLedgerSequence()

	var seqProxy SeqProxy
	if common.TicketSequence != nil && *common.TicketSequence != 0 {
		seqProxy = NewSeqProxyTicket(*common.TicketSequence)
	} else if common.Sequence != nil {
		seqProxy = NewSeqProxySequence(*common.Sequence)
	} else {
		return ApplyResult{Result: tx.TefINTERNAL, Applied: false}
	}

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

	if canDirectApply && feeLevel >= requiredFeeLevel {
		result, applied := ctx.ApplyTransaction(txn)
		if applied {
			if aq, exists := q.byAccount[account]; exists {
				if c, exists := aq.Transactions[seqProxy]; exists {
					q.erase(c)
				}
			}
			return ApplyResult{Result: result, Applied: true}
		}
		// Direct apply was attempted and the tx was not applied: return its
		// result. rippled's tryDirectApply never falls through to queueing once
		// apply has run (TxQ.cpp:1711-1745).
		return ApplyResult{Result: result, Applied: false}
	}

	// Transaction needs to be queued.
	// AccountTxnID is not supported by the transaction queue;
	// tapFAIL_HARD transactions are never held. Mirrors rippled
	// TxQ.cpp:393-399 (canBeHeld). The sfPreviousTxnID guard from
	// rippled has no goxrpl counterpart — the field is metadata-only
	// in this implementation and never lives on a submitted tx.
	if common.AccountTxnID != "" {
		return ApplyResult{Result: tx.TelCAN_NOT_QUEUE, Applied: false}
	}
	if ctx.GetApplyFlags()&tx.TapFAIL_HARD != 0 {
		return ApplyResult{Result: tx.TelCAN_NOT_QUEUE, Applied: false}
	}

	if !ctx.AccountExists(account) {
		return ApplyResult{Result: tx.TerNO_ACCOUNT, Applied: false}
	}

	if seqProxy.IsTicket {
		if !ctx.TicketExists(account, seqProxy.Value) {
			if seqProxy.Value < acctSeq {
				return ApplyResult{Result: tx.TefNO_TICKET, Applied: false}
			}
			return ApplyResult{Result: tx.TerPRE_TICKET, Applied: false}
		}
	}

	if lastValid != 0 && lastValid < ledgerSeq+q.config.MinimumLastLedgerBuffer {
		return ApplyResult{Result: tx.TelCAN_NOT_QUEUE, Applied: false}
	}

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

	// View the unconditional preclaim (below) runs against. Defaults to the
	// account's actual balance and sequence — the plain open view; the multiTxn
	// path overrides these with the in-flight-adjusted values (TxQ.cpp:1137-1170).
	balance := ctx.GetAccountBalance(account)
	preclaimBalance := balance
	preclaimSeq := acctSeq

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
			// Front-of-queue is keyed solely on the predecessor's SeqProxy, like
			// rippled (txSeqProx < prevIter->first, TxQ.cpp:1019). A stale
			// predecessor (< acctSeq) is left to the after-entries branch, which
			// yields telCAN_NOT_QUEUE via getNextQueuableSeq — not terPRE_SEQ.
			goesAtFront := prevTx == nil || seqProxy.Less(prevTx.SeqProxy)

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
				// The tx goes after existing entries: it must fill the first
				// opening in the account's queued sequence chain. Anything else
				// — a sequence past an expiration gap or inside a TicketCreate
				// hole — is telCAN_NOT_QUEUE, never tefPAST_SEQ.
				// Reference: TxQ.cpp:1031-1040 (nextQueuableSeqImpl == txSeqProx).
				if q.getNextQueuableSeq(aq, acctSeq) != seqProxy.Value {
					return ApplyResult{Result: tx.TelCAN_NOT_QUEUE, Applied: false}
				}
			}
		}

		// In-flight balance check and multiTxn view simulation.
		// Reference: TxQ.cpp:1043-1153
		if requiresMultiTxn {
			var totalFee, potentialSpend uint64
			// Walk the account's relevant queued txs in SeqProxy order,
			// matching rippled's std::map walk (TxQ.cpp:1048-1067). Stale
			// sequences are already excluded by RelevantSortedCandidates.
			relevant := aq.RelevantSortedCandidates(acctSeqProx)
			for idx, c := range relevant {
				if c.SeqProxy != seqProxy {
					totalFee += c.Consequences.Fee
					potentialSpend += c.Consequences.PotentialSpend
				} else if idx+1 < len(relevant) {
					// Replacing a tx in the MIDDLE of the queue: count the NEW
					// transaction's consequences, not the replaced one's
					// (TxQ.cpp:1059-1066).
					totalFee += consequences.Fee
					potentialSpend += consequences.PotentialSpend
				}
			}
			// The new tx's fee is NOT added here; it is accounted for by the
			// preclaim against the adjusted view, not the balance gate below.

			reserve := ctx.GetAccountReserve(0)
			baseFeeVal := ctx.GetBaseFee(txn)
			if totalFee >= balance || (reserve > 10*baseFeeVal && totalFee >= reserve) {
				return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_BALANCE, Applied: false}
			}

			// Bump the preclaim view to reflect the in-flight chain
			// (TxQ.cpp:1137-1152): balance -= potentialTotalSpend, and the
			// sequence becomes the tx's own (seq) or the next queuable seq (ticket).
			minBalReserve := min(reserve, balance)
			spendableAboveReserve := min(potentialSpend, balance-minBalReserve)
			potentialTotalSpend := totalFee + spendableAboveReserve
			if potentialTotalSpend <= balance {
				preclaimBalance = balance - potentialTotalSpend
			} else {
				preclaimBalance = 0
			}
			if seqProxy.IsTicket {
				preclaimSeq = q.getNextQueuableSeq(aq, acctSeq)
			} else {
				preclaimSeq = seqProxy.Value
			}
		}
	}

	// Always run preclaim before queueing (TxQ.cpp:1167-1170), against the
	// in-flight-adjusted view for the multiTxn path or the plain open view
	// otherwise. Reject only when the result is not likely to claim a fee:
	// not tesSUCCESS and not a tec (a tec still claims a fee once applied).
	// rippled's likelyToClaimFee also guards the tec branch on !tapRETRY, but no
	// TxQ.Apply caller sets tapRETRY (it is added only in the consensus apply-
	// retry loop), so accepting every tec here is unconditionally correct.
	if result := ctx.PreclaimTransaction(txn, account, preclaimBalance, preclaimSeq); result != tx.TesSUCCESS && !result.IsTec() {
		return ApplyResult{Result: result, Applied: false}
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
		// Gate on the first RELEVANT queued tx (rippled txIter->first =
		// lower_bound(acctSeqProx)), not the lowest sequence overall — a stale
		// entry below acctSeq must not veto the clear.
		firstRelevant := aq.FirstRelevant(acctSeqProx)
		if firstRelevant != nil && firstRelevant.RetriesRemaining == RetriesAllowed {
			if result := q.tryClearAccountQueue(ctx, aq, txn, seqProxy, feeLevel, txInLedger, acctSeq); result != nil {
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
		var lowestOther *Candidate
		for i := len(q.byFee) - 1; i >= 0; i-- {
			c := q.byFee[i]
			if c.Account != account {
				lowestOther = c
				break
			}
		}

		if lowestOther == nil {
			q.incTxQFull()
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}

		endAccount := q.byAccount[lowestOther.Account]

		// Compute the effective fee level for the target account.
		// If the lowest transaction has a higher fee than ours, use its fee.
		// Otherwise, compute the average of the target account's queue.
		// Reference: rippled TxQ.cpp:1265-1292
		endEffectiveFeeLevel := lowestOther.FeeLevel
		if lowestOther.FeeLevel <= feeLevel && endAccount.Count() > 1 {
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
			sorted := endAccount.GetSortedCandidates()
			var dropCandidate *Candidate
			if n := len(sorted); n > 0 {
				dropCandidate = sorted[n-1]
			}
			if dropCandidate != nil {
				q.erase(dropCandidate)
			}
		} else {
			q.incTxQFull()
			return ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}
	}

	if replacingCandidate != nil {
		q.erase(replacingCandidate)
	}

	// q.erase drops the account from byAccount once its queue is empty — which
	// happens when replacingCandidate was the account's only queued tx. Re-check
	// the live map rather than the stale `exists` snapshot, otherwise the new
	// candidate is added to an orphaned AccountQueue and byFee/byAccount diverge,
	// hiding the queued tx from later blocker checks.
	if liveAq, stillInMap := q.byAccount[account]; stillInMap {
		aq = liveAq
	} else {
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
		tx.TesSUCCESS, // preflight ran and passed at the top of Apply
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
	acctSeq uint32,
) *ApplyResult {
	// Collect the queued txs in the clear range [acctSeqProx, seqProxy):
	// relevant entries (>= acctSeqProx) that come BEFORE the new tx, in
	// SeqProxy order. rippled restricts the clear to this half-open range
	// (TxQ.cpp:536-539); a stale entry below acctSeq must not inflate dist.
	acctSeqProx := NewSeqProxySequence(acctSeq)
	var preceding []*Candidate
	for _, c := range aq.RelevantSortedCandidates(acctSeqProx) {
		if !c.SeqProxy.Less(seqProxy) {
			break
		}
		preceding = append(preceding, c)
	}

	if len(preceding) == 0 {
		return nil
	}

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

	// Total fee is sufficient. Apply the whole batch into an isolated sandbox
	// so a later failure leaves the live view untouched — mirroring rippled's
	// `OpenView sandbox(...)` (TxQ.cpp:1202). The sandbox is committed to the
	// live view only when the new tx applies; any failure discards it.
	sandbox, err := ctx.NewSandbox()
	if err != nil {
		// Can't isolate the batch — fall through to normal queuing rather
		// than risk mutating the live view non-atomically.
		return nil
	}

	for _, c := range preceding {
		result, ok := sandbox.ApplyTransaction(c.Txn)
		// Succeed or fail, use up a retry: if the overall process fails we
		// want the attempt to count; on success the candidate is erased
		// below. Bookkeeping lives on the queued candidate, not the sandbox,
		// so it persists across a discard (rippled TxQ.cpp:566-571).
		c.RetriesRemaining--
		c.LastResult = result

		if result == tx.TefNO_TICKET {
			// A ticketed tx that is both queued and already in the ledger can
			// never succeed; treat it as cleared so the rest of the batch can
			// proceed and the dead entry is erased (rippled TxQ.cpp:573-590).
			continue
		}

		if !ok {
			// A preceding transaction failed. Discard the sandbox (the live
			// view is untouched) and fall through to normal queuing. The queue
			// is left intact, exactly as rippled does (TxQ.cpp:592-596).
			return nil
		}
	}

	// All preceding transactions applied in the sandbox. Now apply the new
	// transaction. Because the sandbox state has changed, this re-runs the
	// full apply (rippled TxQ.cpp:598-600).
	result, ok := sandbox.ApplyTransaction(txn)
	if !ok {
		// New transaction failed. Discard the sandbox (the live view is
		// untouched) and fall through to normal queueing — rippled acts only
		// on result.applied and otherwise queues the tx (TxQ.cpp:1216-1224).
		return nil
	}

	// The whole batch applied. Commit the sandbox to the live view, then
	// remove the cleared transactions from the queue (rippled TxQ.cpp:602-611).
	if err := sandbox.Commit(); err != nil {
		return nil
	}
	for _, c := range preceding {
		q.erase(c)
	}
	// Also remove the replacement if one exists at the new tx's seqProxy.
	if c, exists := aq.Transactions[seqProxy]; exists {
		q.erase(c)
	}
	return &ApplyResult{Result: result, Applied: true}
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
		// Mirrors rippled TxQ.cpp:440-444:
		//   auto const nextTxIter = txQAcct.transactions.upper_bound(nextQueuable);
		//   if (nextTxIter != end() && nextTxIter->first.isSeq())
		//     return tesSUCCESS;
		// The IMMEDIATE next SeqProxy must be sequence-based — a ticket
		// sitting next in line doesn't make this a true gap fill,
		// because tickets can't unblock a sequence-based pipeline.
		nextSP, hasNext := q.upperBoundSeqProxy(aq, seqProxy)
		if !hasNext || nextSP.IsTicket {
			q.incTxQFull()
			return true, ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
		}
		// Real gap fill — allow it.
		return false, ApplyResult{}
	}
	q.incTxQFull()
	return true, ApplyResult{Result: tx.TelCAN_NOT_QUEUE_FULL, Applied: false}
}

// upperBoundSeqProxy returns the smallest SeqProxy in aq strictly
// greater than `sp`, mirroring std::map::upper_bound semantics on the
// SeqProxy ordering (sequences come before tickets, then by numeric
// value). hasNext is false when no such entry exists.
func (q *TxQ) upperBoundSeqProxy(aq *AccountQueue, sp SeqProxy) (SeqProxy, bool) {
	var best SeqProxy
	found := false
	for candidate := range aq.Transactions {
		if !sp.Less(candidate) {
			continue
		}
		if !found || candidate.Less(best) {
			best = candidate
			found = true
		}
	}
	return best, found
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

	// Compute potential XRP spend (the max XRP an account could move beyond the
	// fee). Each case mirrors the corresponding rippled makeTxConsequences.
	switch t := txn.(type) {
	case *payment.Payment:
		// SendMax when present, else Amount; XRP only (Payment.cpp:35-48).
		maxAmount := t.Amount
		if t.SendMax != nil {
			maxAmount = *t.SendMax
		}
		if maxAmount.IsNative() {
			cons.PotentialSpend = uint64(maxAmount.Drops())
		}
	case *offer.OfferCreate:
		if t.TakerGets.IsNative() {
			cons.PotentialSpend = uint64(t.TakerGets.Drops())
		}
	case *escrow.EscrowCreate:
		// isXRP(Amount) ? xrp : 0 — IOU/MPT escrows spend no XRP (Escrow.cpp:81-86).
		if t.Amount.IsNative() {
			cons.PotentialSpend = uint64(t.Amount.Drops())
		}
	case *paychan.PaymentChannelCreate:
		// Amount is XRP-only (preflight-guaranteed); PayChan.cpp:168.
		if t.Amount.IsNative() {
			cons.PotentialSpend = uint64(t.Amount.Drops())
		}
	case *paychan.PaymentChannelFund:
		if t.Amount.IsNative() {
			cons.PotentialSpend = uint64(t.Amount.Drops())
		}
	case *xchain.XChainCommit:
		// native && signum>0 ? xrp : 0 — IOU commits spend no XRP
		// (XChainBridge.cpp:1895-1906).
		if t.Amount.IsNative() && uint64(t.Amount.Drops()) > 0 {
			cons.PotentialSpend = uint64(t.Amount.Drops())
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
