package txq

import (
	"bytes"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TxQ is the transaction queue (mempool) that holds transactions waiting to
// be included in a ledger. It manages fee escalation, per-account queuing,
// and transaction selection based on fee level.
type TxQ struct {
	mu         sync.Mutex
	config     Config
	feeMetrics *FeeMetrics

	// byFee holds all candidates sorted by fee level (descending).
	// This is used for iterating from highest fee to lowest when accepting
	// transactions into the open ledger.
	byFee []*Candidate

	// byAccount maps account ID to their AccountQueue.
	// This allows efficient lookup and enforcement of per-account limits.
	byAccount map[[20]byte]*AccountQueue

	// maxSize is the dynamic maximum queue size.
	// nil means no limit (before the first processClosedLedger call).
	// Reference: rippled uses std::optional<size_t> maxSize_ which starts as nullopt.
	maxSize *uint32

	// parentHash is used to pseudo-randomly order transactions with the same fee.
	// This ensures different validators build similar queues.
	parentHash [32]byte

	// txqFull counts transactions rejected because the TxQ was full
	// at submission time (telCAN_NOT_QUEUE_FULL). Kept as an internal
	// diagnostic counter only — rippled has no analogous server_info
	// field for TxQ-admission-control saturation, so goxrpl does not
	// surface it either (conflating it with jq_trans_overflow misled
	// operators pre-#494; the rippled-shape signal lives at the
	// overlay, see Overlay.DroppedTransactions).
	txqFull atomic.Uint64
}

// New creates a new transaction queue with the given configuration.
func New(config Config) *TxQ {
	return &TxQ{
		config:     config,
		feeMetrics: NewFeeMetrics(config),
		byFee:      make([]*Candidate, 0),
		byAccount:  make(map[[20]byte]*AccountQueue),
		// maxSize starts as nil (no limit) matching rippled's std::optional nullopt.
		// It gets set on the first processClosedLedger call.
	}
}

// Config returns the configuration the queue was constructed with.
func (q *TxQ) Config() Config {
	return q.config
}

// Metrics holds queue metrics for monitoring and RPC.
type Metrics struct {
	TxCount               uint32
	TxQMaxSize            *uint32 // nil means no limit
	TxInLedger            uint32
	TxPerLedger           uint32
	ReferenceFeeLevel     uint64
	MinProcessingFeeLevel uint64
	MedFeeLevel           uint64
	OpenLedgerFeeLevel    uint64
	TxQFull               uint64
}

// TxQFull returns the cumulative count of transactions rejected
// because the TxQ was full at submission time
// (telCAN_NOT_QUEUE_FULL). Internal diagnostic only — see the
// txqFull field doc for why this is not surfaced via server_info.
func (q *TxQ) TxQFull() uint64 {
	return q.txqFull.Load()
}

func (q *TxQ) incTxQFull() {
	q.txqFull.Add(1)
}

// GetMetrics returns the current queue metrics.
func (q *TxQ) GetMetrics(txInLedger uint32) Metrics {
	q.mu.Lock()
	defer q.mu.Unlock()

	snapshot := q.feeMetrics.GetSnapshot()
	openLedgerFeeLevel := ScaleFeeLevel(snapshot, txInLedger)

	minProcessingFeeLevel := BaseLevel
	if q.isFull() && len(q.byFee) > 0 {
		minProcessingFeeLevel = uint64(q.byFee[len(q.byFee)-1].FeeLevel) + 1
	}

	return Metrics{
		TxCount:               uint32(len(q.byFee)),
		TxQMaxSize:            q.maxSize,
		TxInLedger:            txInLedger,
		TxPerLedger:           snapshot.TxnsExpected,
		ReferenceFeeLevel:     BaseLevel,
		MinProcessingFeeLevel: minProcessingFeeLevel,
		MedFeeLevel:           snapshot.EscalationMultiplier,
		OpenLedgerFeeLevel:    uint64(openLedgerFeeLevel),
		TxQFull:               q.txqFull.Load(),
	}
}

// isFull returns true if the queue has reached its maximum size.
// Returns false if maxSize is nil (no limit).
// Reference: rippled returns maxSize_ && byFee_.size() >= *maxSize_
// Caller must hold the lock.
func (q *TxQ) isFull() bool {
	return q.maxSize != nil && uint32(len(q.byFee)) >= *q.maxSize
}

// isFullPct returns true if the queue is at least fillPct percent full.
// Returns false if maxSize is nil (no limit).
// Reference: rippled isFull<fillPercentage>() returns false when maxSize_ is nullopt.
// Caller must hold the lock.
func (q *TxQ) isFullPct(fillPct uint32) bool {
	return q.maxSize != nil && uint32(len(q.byFee)) >= (*q.maxSize*fillPct/100)
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
	pos := q.findInsertPosition(c)
	q.byFee = append(q.byFee, nil)
	copy(q.byFee[pos+1:], q.byFee[pos:])
	q.byFee[pos] = c
}

// findInsertPosition finds where to insert a candidate to maintain order.
// Order is: descending by fee level, then ascending by (txID XOR parentHash).
// Caller must hold the lock.
func (q *TxQ) findInsertPosition(c *Candidate) int {
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
	for i := range 32 {
		result[i] = a[i] ^ b[i]
	}
	return result
}

// compareHashes compares two hashes lexicographically.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareHashes(a, b [32]byte) int {
	for i := range 32 {
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
		if aq.Empty() {
			delete(q.byAccount, c.Account)
		}
	}
}

// rebuildByFee rebuilds the byFee index from byAccount.
// Called after changing parentHash to reorder same-fee transactions.
// Caller must hold the lock.
//
// Walks accounts in sorted order and candidates via GetSortedCandidates
// so the rebuilt byFee slice is bit-identical across validators.
func (q *TxQ) rebuildByFee() {
	q.byFee = make([]*Candidate, 0, len(q.byFee))

	accounts := make([][20]byte, 0, len(q.byAccount))
	for a := range q.byAccount {
		accounts = append(accounts, a)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return bytes.Compare(accounts[i][:], accounts[j][:]) < 0
	})

	for _, a := range accounts {
		for _, c := range q.byAccount[a].GetSortedCandidates() {
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
