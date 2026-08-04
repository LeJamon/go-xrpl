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
func (q *TxQ) ProcessClosedLedger(ctx ClosedLedgerContext, timeLeap bool) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()

	ledgerSeq := ctx.GetLedgerSequence()
	feeLevels := ctx.GetTransactionFeeLevels()
	q.stateMu.Lock()
	defer q.stateMu.Unlock()

	txCount := q.feeMetrics.update(feeLevels, timeLeap, q.config)

	// Reference: rippled sets maxSize_ = max(txnsExpected * ledgersInQueue, queueSizeMin)
	if !timeLeap {
		snapshot := q.feeMetrics.snapshot()
		product := snapshot.TxnsExpected
		if product > ^uint64(0)/uint64(q.config.LedgersInQueue) {
			product = ^uint64(0)
		} else {
			product *= uint64(q.config.LedgersInQueue)
		}
		newMaxSize := max(product, uint64(q.config.QueueSizeMin))
		q.maxSize = &newMaxSize
	}

	// Remove expired transactions (where LastLedgerSequence <= ledgerSeq)
	toRemove := make([]*candidate, 0)
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

func (q *TxQ) clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stateMu.Lock()
	defer q.stateMu.Unlock()

	q.byFee = make([]*candidate, 0)
	q.byID = make(map[[32]byte]*candidate)
	q.byAccount = make(map[[20]byte]*accountQueue)
}

func (q *TxQ) setMaxSize(maxSize uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stateMu.Lock()
	defer q.stateMu.Unlock()
	q.maxSize = &maxSize
}

// NextQueuableSeq returns the next sequence number that can be queued for an account.
// This is useful for clients to know what sequence to use for their next transaction.
func (q *TxQ) NextQueuableSeq(account [20]byte, acctSeq uint32) uint32 {
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()

	aq, exists := q.byAccount[account]
	if !exists || aq.Empty() {
		return acctSeq
	}

	return q.getNextQueuableSeq(aq, acctSeq)
}
