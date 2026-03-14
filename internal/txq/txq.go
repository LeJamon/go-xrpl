package txq

import (
	"sync"

	"github.com/LeJamon/goXRPLd/internal/tx"
)

// TxQ is the transaction queue (mempool) that holds transactions waiting to
// be included in a ledger. It manages fee escalation, per-account queuing,
// and transaction selection based on fee level.
type TxQ struct {
	mu sync.Mutex

	// config holds the queue configuration
	config Config

	// feeMetrics tracks and computes fee escalation
	feeMetrics *FeeMetrics

	// byFee holds all candidates sorted by fee level (descending).
	// This is used for iterating from highest fee to lowest when accepting
	// transactions into the open ledger.
	byFee []*Candidate

	// byAccount maps account ID to their AccountQueue.
	// This allows efficient lookup and enforcement of per-account limits.
	byAccount map[[20]byte]*AccountQueue

	// maxSize is the dynamic maximum queue size.
	// It's recomputed after each ledger close based on recent transaction counts.
	maxSize uint32

	// parentHash is used to pseudo-randomly order transactions with the same fee.
	// This ensures different validators build similar queues.
	parentHash [32]byte
}

// New creates a new transaction queue with the given configuration.
func New(config Config) *TxQ {
	return &TxQ{
		config:     config,
		feeMetrics: NewFeeMetrics(config),
		byFee:      make([]*Candidate, 0),
		byAccount:  make(map[[20]byte]*AccountQueue),
		maxSize:    config.QueueSizeMin,
	}
}

// Metrics holds queue metrics for monitoring and RPC.
type Metrics struct {
	// TxCount is the number of transactions in the queue
	TxCount uint32

	// TxQMaxSize is the maximum queue size (may be nil if no limit)
	TxQMaxSize *uint32

	// TxInLedger is the number of transactions in the current open ledger
	TxInLedger uint32

	// TxPerLedger is the expected number of transactions per ledger
	TxPerLedger uint32

	// ReferenceFeeLevel is the base fee level (256)
	ReferenceFeeLevel uint64

	// MinProcessingFeeLevel is the minimum fee level to be accepted into the queue
	MinProcessingFeeLevel uint64

	// MedFeeLevel is the median fee level from the last closed ledger
	MedFeeLevel uint64

	// OpenLedgerFeeLevel is the fee level required to bypass the queue
	OpenLedgerFeeLevel uint64
}

// GetMetrics returns the current queue metrics.
func (q *TxQ) GetMetrics(txInLedger uint32) Metrics {
	q.mu.Lock()
	defer q.mu.Unlock()

	snapshot := q.feeMetrics.GetSnapshot()
	openLedgerFeeLevel := ScaleFeeLevel(snapshot, txInLedger)

	minProcessingFeeLevel := BaseLevel
	if q.isFull() && len(q.byFee) > 0 {
		// When full, need to beat the lowest fee in the queue
		minProcessingFeeLevel = uint64(q.byFee[len(q.byFee)-1].FeeLevel) + 1
	}

	maxSize := q.maxSize
	return Metrics{
		TxCount:               uint32(len(q.byFee)),
		TxQMaxSize:            &maxSize,
		TxInLedger:            txInLedger,
		TxPerLedger:           snapshot.TxnsExpected,
		ReferenceFeeLevel:     BaseLevel,
		MinProcessingFeeLevel: minProcessingFeeLevel,
		MedFeeLevel:           snapshot.EscalationMultiplier,
		OpenLedgerFeeLevel:    uint64(openLedgerFeeLevel),
	}
}

// isFull returns true if the queue has reached its maximum size.
// Caller must hold the lock.
func (q *TxQ) isFull() bool {
	return uint32(len(q.byFee)) >= q.maxSize
}

// isFullPct returns true if the queue is at least fillPct percent full.
// Caller must hold the lock.
func (q *TxQ) isFullPct(fillPct uint32) bool {
	return uint32(len(q.byFee)) >= (q.maxSize * fillPct / 100)
}

// Size returns the number of transactions in the queue.
func (q *TxQ) Size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.byFee)
}

// GetRequiredFeeLevel returns the fee level required to bypass the queue
// and get directly into the open ledger.
func (q *TxQ) GetRequiredFeeLevel(txInLedger uint32) FeeLevel {
	q.mu.Lock()
	defer q.mu.Unlock()

	snapshot := q.feeMetrics.GetSnapshot()
	return ScaleFeeLevel(snapshot, txInLedger)
}

// insertByFee inserts a candidate into the byFee slice, maintaining descending order by fee.
// Candidates with the same fee are ordered by txID XOR parentHash for deterministic ordering.
// Caller must hold the lock.
func (q *TxQ) insertByFee(c *Candidate) {
	// Find insertion point using binary search
	pos := q.findInsertPosition(c)

	// Insert at position
	q.byFee = append(q.byFee, nil)
	copy(q.byFee[pos+1:], q.byFee[pos:])
	q.byFee[pos] = c
}

// findInsertPosition finds where to insert a candidate to maintain order.
// Order is: descending by fee level, then ascending by (txID XOR parentHash).
// Caller must hold the lock.
func (q *TxQ) findInsertPosition(c *Candidate) int {
	// Binary search for the right position
	lo, hi := 0, len(q.byFee)
	for lo < hi {
		mid := (lo + hi) / 2
		if q.candidateLess(c, q.byFee[mid]) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// candidateLess returns true if a should come before b in the fee-ordered list.
// Higher fees come first. For same fees, use XOR with parentHash for determinism.
func (q *TxQ) candidateLess(a, b *Candidate) bool {
	if a.FeeLevel != b.FeeLevel {
		return a.FeeLevel > b.FeeLevel // Higher fee first
	}

	// Same fee level, use pseudo-random ordering based on txID XOR parentHash
	aXor := xorHash(a.TxID, q.parentHash)
	bXor := xorHash(b.TxID, q.parentHash)
	return compareHashes(aXor, bXor) < 0
}

// xorHash computes a XOR b.
func xorHash(a, b [32]byte) [32]byte {
	var result [32]byte
	for i := 0; i < 32; i++ {
		result[i] = a[i] ^ b[i]
	}
	return result
}

// compareHashes compares two hashes lexicographically.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareHashes(a, b [32]byte) int {
	for i := 0; i < 32; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// removeByFee removes a candidate from the byFee slice.
// Caller must hold the lock.
func (q *TxQ) removeByFee(c *Candidate) {
	for i, candidate := range q.byFee {
		if candidate == c {
			q.byFee = append(q.byFee[:i], q.byFee[i+1:]...)
			return
		}
	}
}

// erase removes a candidate from both byFee and byAccount.
// Caller must hold the lock.
func (q *TxQ) erase(c *Candidate) {
	q.removeByFee(c)

	if aq, exists := q.byAccount[c.Account]; exists {
		aq.Remove(c.SeqProxy)
		// Clean up empty account queues
		if aq.Empty() {
			delete(q.byAccount, c.Account)
		}
	}
}

// rebuildByFee rebuilds the byFee index from byAccount.
// Called after changing parentHash to reorder same-fee transactions.
// Caller must hold the lock.
func (q *TxQ) rebuildByFee() {
	q.byFee = make([]*Candidate, 0, len(q.byFee))

	for _, aq := range q.byAccount {
		for _, c := range aq.Transactions {
			q.insertByFee(c)
		}
	}
}

// GetAccountTxs returns details of all queued transactions for an account.
func (q *TxQ) GetAccountTxs(account [20]byte) []*CandidateDetails {
	q.mu.Lock()
	defer q.mu.Unlock()

	aq, exists := q.byAccount[account]
	if !exists || aq.Empty() {
		return nil
	}

	result := make([]*CandidateDetails, 0, aq.Count())
	for _, c := range aq.GetSortedCandidates() {
		result = append(result, &CandidateDetails{
			TxID:             c.TxID,
			Account:          c.Account,
			FeeLevel:         c.FeeLevel,
			SeqProxy:         c.SeqProxy,
			LastValid:        c.LastValid,
			RetriesRemaining: c.RetriesRemaining,
			LastResult:       c.LastResult,
		})
	}
	return result
}

// GetAllTxs returns details of all queued transactions, ordered by fee (highest first).
func (q *TxQ) GetAllTxs() []*CandidateDetails {
	q.mu.Lock()
	defer q.mu.Unlock()

	result := make([]*CandidateDetails, 0, len(q.byFee))
	for _, c := range q.byFee {
		result = append(result, &CandidateDetails{
			TxID:             c.TxID,
			Account:          c.Account,
			FeeLevel:         c.FeeLevel,
			SeqProxy:         c.SeqProxy,
			LastValid:        c.LastValid,
			RetriesRemaining: c.RetriesRemaining,
			LastResult:       c.LastResult,
		})
	}
	return result
}

// CandidateDetails holds information about a queued transaction for external queries.
type CandidateDetails struct {
	TxID             [32]byte
	Account          [20]byte
	FeeLevel         FeeLevel
	SeqProxy         SeqProxy
	LastValid        uint32
	RetriesRemaining int
	LastResult       tx.Result
}
