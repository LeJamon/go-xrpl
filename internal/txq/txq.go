package txq

import (
	"bytes"
	"sort"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// TxQ is the transaction queue (mempool) that holds transactions waiting to
// be included in a ledger. It manages fee escalation, per-account queuing,
// and transaction selection based on fee level.
type TxQ struct {
	// mu serializes mutations. State readers never take it, so callbacks may
	// query the queue reentrantly while a mutation is in progress. Mutation
	// callbacks are intentionally not reentrant; state is protected separately
	// by stateMu and callbacks are invoked outside stateMu. Production callers
	// acquire their ledger writer lock before entering the queue; queue code does
	// not acquire an outer ledger lock while holding either queue lock.
	mu         sync.Mutex
	stateMu    sync.RWMutex
	config     Config
	feeMetrics *feeMetrics

	// byFee holds all candidates sorted by fee level (descending).
	// This is used for iterating from highest fee to lowest when accepting
	// transactions into the open ledger.
	byFee []*candidate

	// byID is the authoritative transaction lookup used by reduce-relay
	// backfill. It is maintained alongside byFee so a request for many hashes
	// does not rescan the fee-ordered queue for every object.
	byID map[[32]byte]*candidate

	// byAccount maps account ID to their AccountQueue.
	// This allows efficient lookup and enforcement of per-account limits.
	byAccount map[[20]byte]*accountQueue

	// maxSize is the dynamic maximum queue size.
	// nil means no limit (before the first processClosedLedger call).
	// Reference: rippled uses std::optional<size_t> maxSize_ which starts as nullopt.
	maxSize *uint64

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
	txqFull uint64
}

// New creates a new transaction queue with the given configuration.
func New(config Config) (*TxQ, error) {
	effective, err := config.normalize()
	if err != nil {
		return nil, err
	}
	return &TxQ{
		config:     effective,
		feeMetrics: newFeeMetrics(effective),
		byFee:      make([]*candidate, 0),
		byID:       make(map[[32]byte]*candidate),
		byAccount:  make(map[[20]byte]*accountQueue),
		// maxSize starts as nil (no limit) matching rippled's std::optional nullopt.
		// It gets set on the first processClosedLedger call.
	}, nil
}

// Config returns the configuration the queue was constructed with.
func (q *TxQ) Config() Config {
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()
	return q.config
}

// Metrics holds queue metrics for monitoring and RPC.
type Metrics struct {
	TxCount               uint64
	TxQMaxSize            *uint64 // nil means no limit
	TxInLedger            uint64
	TxPerLedger           uint64
	ReferenceFeeLevel     uint64
	MinProcessingFeeLevel uint64
	MedFeeLevel           uint64
	OpenLedgerFeeLevel    uint64
	TxQFull               uint64
}

func (q *TxQ) incTxQFull() {
	q.txqFull++
}

// Metrics returns the current queue metrics.
func (q *TxQ) Metrics(txInLedger uint32) Metrics {
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()

	snapshot := q.feeMetrics.snapshot()
	openLedgerFeeLevel := scaleFeeLevel(snapshot, txInLedger)

	minProcessingFeeLevel := BaseLevel
	if q.isFull() && len(q.byFee) > 0 {
		minProcessingFeeLevel = uint64(q.byFee[len(q.byFee)-1].FeeLevel) + 1
	}

	// Snapshot maxSize by value rather than handing out the interior pointer,
	// so callers can't observe a later in-place mutation through it.
	var maxSize *uint64
	if q.maxSize != nil {
		v := *q.maxSize
		maxSize = &v
	}

	return Metrics{
		TxCount:               uint64(len(q.byFee)),
		TxQMaxSize:            maxSize,
		TxInLedger:            uint64(txInLedger),
		TxPerLedger:           snapshot.TxnsExpected,
		ReferenceFeeLevel:     BaseLevel,
		MinProcessingFeeLevel: minProcessingFeeLevel,
		MedFeeLevel:           snapshot.EscalationMultiplier,
		OpenLedgerFeeLevel:    uint64(openLedgerFeeLevel),
		TxQFull:               q.txqFull,
	}
}

// isFull returns true if the queue has reached its maximum size.
// Returns false if maxSize is nil (no limit).
// Reference: rippled returns maxSize_ && byFee_.size() >= *maxSize_
// Caller must hold the lock.
func (q *TxQ) isFull() bool {
	return q.maxSize != nil && uint64(len(q.byFee)) >= *q.maxSize
}

// isFullPct returns true if the queue is at least fillPct percent full.
// Returns false if maxSize is nil (no limit).
// Reference: rippled isFull<fillPercentage>() returns false when maxSize_ is nullopt.
// Caller must hold the lock.
func (q *TxQ) isFullPct(fillPct uint32) bool {
	if q.maxSize == nil {
		return false
	}
	// Avoid maxSize*fillPct overflow while preserving integer truncation.
	if fillPct == 0 {
		return true
	}
	if fillPct >= 100 {
		return uint64(len(q.byFee)) >= *q.maxSize
	}
	threshold := *q.maxSize / 100 * uint64(fillPct)
	threshold += (*q.maxSize % 100) * uint64(fillPct) / 100
	return uint64(len(q.byFee)) >= threshold
}

// Size returns the number of transactions in the queue.
func (q *TxQ) Size() int {
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()
	return len(q.byFee)
}

// GetTxBlob returns a copy of the raw transaction held by the queue. The
// transaction queue is an authoritative cache for reduce-relay replies: a
// terQUEUED transaction is not present in the open-ledger view yet, but peers
// must still be able to fetch it by hash.
func (q *TxQ) GetTxBlob(txID [32]byte) ([]byte, bool) {
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()
	if q.byID == nil {
		return nil, false
	}
	candidate := q.byID[txID]
	if candidate == nil {
		return nil, false
	}
	raw := candidate.rawBlob()
	if len(raw) == 0 {
		return nil, false
	}
	return append([]byte(nil), raw...), true
}

// RequiredFeeLevel returns the fee level required to bypass the queue
// and get directly into the open ledger.
func (q *TxQ) RequiredFeeLevel(txInLedger uint32) FeeLevel {
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()

	snapshot := q.feeMetrics.snapshot()
	return scaleFeeLevel(snapshot, txInLedger)
}

// insertByFee inserts a candidate into the byFee slice, maintaining descending order by fee.
// Candidates with the same fee are ordered by txID XOR parentHash for deterministic ordering.
// Caller must hold the lock.
func (q *TxQ) insertByFee(c *candidate) {
	if c == nil {
		return
	}
	if q.byID == nil {
		q.byID = make(map[[32]byte]*candidate)
	}
	pos := q.findInsertPosition(c)
	q.byFee = append(q.byFee, nil)
	copy(q.byFee[pos+1:], q.byFee[pos:])
	q.byFee[pos] = c
	q.byID[c.TxID] = c
}

// findInsertPosition finds where to insert a candidate to maintain order.
// Order is: descending by fee level, then ascending by (txID XOR parentHash).
// Caller must hold the lock.
func (q *TxQ) findInsertPosition(c *candidate) int {
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
func (q *TxQ) candidateLess(a, b *candidate) bool {
	if a.FeeLevel != b.FeeLevel {
		return a.FeeLevel > b.FeeLevel // Higher fee first
	}

	// Same fee level, use pseudo-random ordering based on txID XOR parentHash
	aXor := xorHash(a.TxID, q.parentHash)
	bXor := xorHash(b.TxID, q.parentHash)
	return bytes.Compare(aXor[:], bXor[:]) < 0
}

// xorHash computes a XOR b.
func xorHash(a, b [32]byte) [32]byte {
	var result [32]byte
	for i := range 32 {
		result[i] = a[i] ^ b[i]
	}
	return result
}

// removeByFee removes a candidate from the byFee slice.
// Caller must hold the lock.
func (q *TxQ) removeByFee(c *candidate) {
	if c == nil {
		return
	}
	for i, candidate := range q.byFee {
		if candidate == c {
			q.byFee = append(q.byFee[:i], q.byFee[i+1:]...)
			if q.byID != nil && q.byID[c.TxID] == c {
				delete(q.byID, c.TxID)
			}
			return
		}
	}
}

// erase removes a candidate while retaining its AccountQueue until the next
// closed-ledger cleanup.
// Caller must hold the lock.
func (q *TxQ) erase(c *candidate) {
	if c == nil {
		return
	}
	q.removeByFee(c)

	if aq, exists := q.byAccount[c.Account]; exists {
		aq.removeCandidate(c)
	}
	if q.byID != nil && q.byID[c.TxID] == c {
		// A missing account queue is corruption. Remove the orphaned byID
		// index entry immediately rather than retaining a ghost transaction.
		delete(q.byID, c.TxID)
	}
}

// rebuildByFee rebuilds the byFee index from byAccount.
// Called after changing parentHash to reorder same-fee transactions.
// Caller must hold the lock.
//
// Walks accounts in sorted order and candidates by sequence proxy
// so the rebuilt byFee slice is bit-identical across validators.
func (q *TxQ) rebuildByFee() {
	capacity := len(q.byFee)
	q.byFee = make([]*candidate, 0, capacity)
	q.byID = make(map[[32]byte]*candidate, capacity)

	accounts := make([][20]byte, 0, len(q.byAccount))
	for a := range q.byAccount {
		accounts = append(accounts, a)
	}
	sort.Slice(accounts, func(i, j int) bool {
		return bytes.Compare(accounts[i][:], accounts[j][:]) < 0
	})

	for _, a := range accounts {
		for _, c := range q.byAccount[a].SortedCandidates() {
			q.insertByFee(c)
		}
	}
}

// rebuildByID reconstructs the transaction lookup from byFee. It is used only
// for zero-value/test queues; production queues maintain the index on every
// insertion and removal.
func (q *TxQ) rebuildByID() {
	q.byID = make(map[[32]byte]*candidate, len(q.byFee))
	for _, candidate := range q.byFee {
		if candidate != nil {
			q.byID[candidate.TxID] = candidate
		}
	}
}

// AccountTxs returns details of all queued transactions for an account.
func (q *TxQ) AccountTxs(account [20]byte) []*CandidateDetails {
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()

	aq, exists := q.byAccount[account]
	if !exists || aq.Empty() {
		return nil
	}

	result := make([]*CandidateDetails, 0, aq.Count())
	for _, c := range aq.SortedCandidates() {
		if c != nil {
			result = append(result, candidateDetails(c))
		}
	}
	return result
}

// AllTxs returns details of all queued transactions, ordered by fee (highest first).
func (q *TxQ) AllTxs() []*CandidateDetails {
	q.stateMu.RLock()
	defer q.stateMu.RUnlock()

	result := make([]*CandidateDetails, 0, len(q.byFee))
	for _, c := range q.byFee {
		if c != nil {
			result = append(result, candidateDetails(c))
		}
	}
	return result
}

// candidateDetails projects a queue Candidate into the external view
// surfaced by account_info and ledger queue_data. AuthChange mirrors
// rippled's tx.consequences.isBlocker(); Fee/PotentialSpend feed the
// per-tx fee and max_spend_drops emits (AccountInfo.cpp:251-256).
func candidateDetails(c *candidate) *CandidateDetails {
	return &CandidateDetails{
		TxID:             c.TxID,
		Account:          c.Account,
		FeeLevel:         c.FeeLevel,
		SeqProxy:         c.SeqProxy,
		LastValid:        c.LastValid,
		RetriesRemaining: c.RetriesRemaining,
		LastResult:       c.LastResult,
		HasLastResult:    c.RetriesRemaining < RetriesAllowed,
		PreflightResult:  c.PreflightResult,
		Fee:              c.Consequences.Fee,
		PotentialSpend:   c.Consequences.PotentialSpend,
		AuthChange:       c.Consequences.IsBlocker,
		TxBlob:           c.rawBlob(),
	}
}

// CandidateDetails holds information about a queued transaction for external queries.
type CandidateDetails struct {
	TxID             [32]byte
	Account          [20]byte
	FeeLevel         FeeLevel
	SeqProxy         SeqProxy
	LastValid        uint32
	RetriesRemaining int
	LastResult       ter.Result
	// HasLastResult mirrors rippled's std::optional<TER> lastResult: it is
	// only set once a candidate has been re-applied at least once, so the
	// ledger queue dump suppresses last_result for never-retried txs
	// (LedgerToJson.cpp:308-309).
	HasLastResult   bool
	PreflightResult ter.Result
	Fee             uint64
	PotentialSpend  uint64
	AuthChange      bool
	// TxBlob is a private-to-the-queue copy of the canonical submission. Query
	// callers can parse their own copy; no mutable transaction object escapes.
	TxBlob []byte
}
