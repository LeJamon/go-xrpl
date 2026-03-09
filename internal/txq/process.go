package txq

// ClosedLedgerContext provides the context from a closed ledger for updating fee metrics.
type ClosedLedgerContext interface {
	// GetLedgerSequence returns the closed ledger's sequence number.
	GetLedgerSequence() uint32

	// GetTransactionFeeLevels returns the fee levels of all transactions in the closed ledger.
	// This is used to compute the median fee level for fee escalation.
	GetTransactionFeeLevels() []FeeLevel
}

// ProcessClosedLedger updates the queue state after a ledger closes.
// This method:
// 1. Updates fee metrics based on the closed ledger's transactions
// 2. Adjusts the maximum queue size
// 3. Removes expired transactions (where LastLedgerSequence has passed)
// 4. Cleans up empty account entries
//
// The timeLeap parameter indicates if consensus was slow (ledger close took longer
// than expected). When true, the queue will be more conservative about capacity.
func (q *TxQ) ProcessClosedLedger(ctx ClosedLedgerContext, timeLeap bool) uint32 {
	q.mu.Lock()
	defer q.mu.Unlock()

	ledgerSeq := ctx.GetLedgerSequence()
	feeLevels := ctx.GetTransactionFeeLevels()

	// Update fee metrics and get transaction count
	txCount := q.feeMetrics.Update(feeLevels, timeLeap, q.config)

	// Update maximum queue size
	if !timeLeap {
		snapshot := q.feeMetrics.GetSnapshot()
		newMaxSize := snapshot.TxnsExpected * q.config.LedgersInQueue
		if newMaxSize < q.config.QueueSizeMin {
			newMaxSize = q.config.QueueSizeMin
		}
		q.maxSize = newMaxSize
	}

	// Remove expired transactions (where LastLedgerSequence <= ledgerSeq)
	toRemove := make([]*Candidate, 0)
	for _, c := range q.byFee {
		if c.LastValid != 0 && c.LastValid <= ledgerSeq {
			// Mark the account as having dropped transactions
			if aq, exists := q.byAccount[c.Account]; exists {
				aq.DropPenalty = true
			}
			toRemove = append(toRemove, c)
		}
	}

	for _, c := range toRemove {
		q.erase(c)
	}

	// Clean up empty account queues
	for account, aq := range q.byAccount {
		if aq.Empty() {
			delete(q.byAccount, account)
		}
	}

	return txCount
}

// Clear removes all transactions from the queue.
// This is primarily useful for testing.
func (q *TxQ) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.byFee = make([]*Candidate, 0)
	q.byAccount = make(map[[20]byte]*AccountQueue)
}

// SetMaxSize sets the maximum queue size.
// This is primarily useful for testing.
func (q *TxQ) SetMaxSize(maxSize uint32) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.maxSize = maxSize
}

// GetConfig returns the queue configuration.
func (q *TxQ) GetConfig() Config {
	return q.config
}

// NextQueuableSeq returns the next sequence number that can be queued for an account.
// This is useful for clients to know what sequence to use for their next transaction.
func (q *TxQ) NextQueuableSeq(account [20]byte, acctSeq uint32) uint32 {
	q.mu.Lock()
	defer q.mu.Unlock()

	aq, exists := q.byAccount[account]
	if !exists || aq.Empty() {
		return acctSeq
	}

	return q.getNextQueuableSeq(aq, acctSeq)
}

// GetFeeAndSeq returns the required fee and next sequence for a transaction.
// This is useful for RPC methods that help users construct transactions.
type FeeAndSeq struct {
	// RequiredFee is the minimum fee in drops to bypass the queue
	RequiredFee uint64

	// AccountSeq is the account's current sequence number
	AccountSeq uint32

	// AvailableSeq is the next queueable sequence number
	AvailableSeq uint32
}

// GetTxRequiredFeeAndSeq returns fee and sequence information for constructing transactions.
func (q *TxQ) GetTxRequiredFeeAndSeq(account [20]byte, acctSeq uint32, baseFee uint64, txInLedger uint32) FeeAndSeq {
	q.mu.Lock()
	defer q.mu.Unlock()

	snapshot := q.feeMetrics.GetSnapshot()
	feeLevel := ScaleFeeLevel(snapshot, txInLedger)
	requiredFee := feeLevel.ToDrops(baseFee)

	availableSeq := acctSeq
	if aq, exists := q.byAccount[account]; exists {
		availableSeq = q.getNextQueuableSeq(aq, acctSeq)
	}

	return FeeAndSeq{
		RequiredFee:  requiredFee,
		AccountSeq:   acctSeq,
		AvailableSeq: availableSeq,
	}
}
