package txq

import (
	"sort"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// RetriesAllowed is the starting retry count for newly queued transactions.
// If a transaction fails to apply this many times, it will be dropped.
const RetriesAllowed = 10

// SeqProxy represents either a sequence number or a ticket number.
// This mirrors rippled's SeqProxy type.
type SeqProxy struct {
	Value    uint32
	IsTicket bool
}

// NewSeqProxySequence creates a SeqProxy for a sequence number.
func NewSeqProxySequence(seq uint32) SeqProxy {
	return SeqProxy{Value: seq, IsTicket: false}
}

// NewSeqProxyTicket creates a SeqProxy for a ticket number.
func NewSeqProxyTicket(ticket uint32) SeqProxy {
	return SeqProxy{Value: ticket, IsTicket: true}
}

// Less returns true if this SeqProxy is less than other.
// Sequences come before tickets, and within each category, lower values come first.
func (s SeqProxy) Less(other SeqProxy) bool {
	if !s.IsTicket && other.IsTicket {
		return true
	}
	if s.IsTicket && !other.IsTicket {
		return false
	}
	return s.Value < other.Value
}

// Candidate represents a transaction that may be applied to the open ledger.
// It holds all the information needed to attempt application and track retries.
type candidate struct {
	// Txn is only populated by same-package synthetic tests. Production
	// admission stores the canonical blob and never retains the caller object.
	Txn              tx.Transaction
	blob             []byte
	TxID             [32]byte
	Account          [20]byte
	FeeLevel         FeeLevel
	SeqProxy         SeqProxy
	LastValid        uint32
	RetriesRemaining int // starts at RetriesAllowed; drops to 0 before removal
	LastResult       ter.Result
	// PreflightResult holds the result from the preflight check.
	PreflightResult ter.Result
	Flags           tx.ApplyFlags
	PreflightFlags  tx.ApplyFlags
	PreflightRules  *amendment.Rules
	Consequences    txConsequences
}

// TxConsequences describes the potential impact of applying a transaction.
type txConsequences struct {
	Fee uint64

	// PotentialSpend is the maximum XRP that could be spent beyond the fee.
	// For payments this is the amount. For offers this is TakerGets if selling XRP.
	PotentialSpend uint64

	// IsBlocker indicates if this transaction could invalidate subsequent
	// transactions for the same account (e.g., SetRegularKey, SignerListSet).
	IsBlocker bool

	// FollowingSeq is the sequence number that should follow this transaction.
	// For regular transactions this is Sequence + 1.
	// For TicketCreate this is Sequence + TicketCount.
	FollowingSeq SeqProxy
}

// NewCandidate creates a new Candidate for a transaction.
func newCandidate(
	txID [32]byte,
	account [20]byte,
	feeLevel FeeLevel,
	seqProxy SeqProxy,
	lastValid uint32,
	preflightResult ter.Result,
	consequences txConsequences,
) *candidate {
	return &candidate{
		TxID:             txID,
		Account:          account,
		FeeLevel:         feeLevel,
		SeqProxy:         seqProxy,
		LastValid:        lastValid,
		RetriesRemaining: RetriesAllowed,
		PreflightResult:  preflightResult,
		Consequences:     consequences,
	}
}

func (c *candidate) rawBlob() []byte {
	if c == nil {
		return nil
	}
	if len(c.blob) > 0 {
		return append([]byte(nil), c.blob...)
	}
	if c.Txn != nil {
		return append([]byte(nil), c.Txn.GetRawBytes()...)
	}
	return nil
}

// AccountQueue tracks queued transactions for a single account.
type accountQueue struct {
	Account      [20]byte
	Transactions map[SeqProxy]*candidate

	// RetryPenalty is set when a transaction has exhausted its retries.
	// Other transactions for this account will have reduced retry allowance.
	RetryPenalty bool

	// DropPenalty is set when a transaction has failed or expired.
	// When the queue is nearly full, transactions from this account
	// may be discarded more readily.
	DropPenalty bool
}

// NewAccountQueue creates a new AccountQueue for an account.
func newAccountQueue(account [20]byte) *accountQueue {
	return &accountQueue{
		Account:      account,
		Transactions: make(map[SeqProxy]*candidate),
	}
}

// Add adds a candidate to this account's queue.
func (aq *accountQueue) Add(c *candidate) {
	aq.Transactions[c.SeqProxy] = c
}

// Remove removes a candidate with the given SeqProxy.
// Returns true if a candidate was removed.
func (aq *accountQueue) Remove(seqProxy SeqProxy) bool {
	if _, exists := aq.Transactions[seqProxy]; exists {
		delete(aq.Transactions, seqProxy)
		return true
	}
	return false
}

func (aq *accountQueue) removeCandidate(c *candidate) bool {
	if aq == nil || c == nil {
		return false
	}
	if existing, ok := aq.Transactions[c.SeqProxy]; ok && existing == c {
		delete(aq.Transactions, c.SeqProxy)
		return true
	}
	return false
}

// Count returns the number of transactions queued for this account.
func (aq *accountQueue) Count() int {
	return len(aq.Transactions)
}

// Empty returns true if there are no queued transactions.
func (aq *accountQueue) Empty() bool {
	return len(aq.Transactions) == 0
}

// PrevTx finds the transaction that precedes the given SeqProxy.
// Returns nil if there is no preceding transaction.
func (aq *accountQueue) PrevTx(seqProxy SeqProxy) *candidate {
	var prev *candidate
	for sp, c := range aq.Transactions {
		if sp.Less(seqProxy) {
			if prev == nil || prev.SeqProxy.Less(sp) {
				prev = c
			}
		}
	}
	return prev
}

func (aq *accountQueue) GetNextTx(seqProxy SeqProxy) *candidate {
	var next *candidate
	for sp, c := range aq.Transactions {
		if seqProxy.Less(sp) {
			if next == nil || sp.Less(next.SeqProxy) {
				next = c
			}
		}
	}
	return next
}

// FirstSeqTx returns the first sequence-based transaction (lowest sequence).
// Returns nil if there are no sequence-based transactions.
func (aq *accountQueue) FirstSeqTx() *candidate {
	var first *candidate
	for _, c := range aq.Transactions {
		if !c.SeqProxy.IsTicket {
			if first == nil || c.SeqProxy.Value < first.SeqProxy.Value {
				first = c
			}
		}
	}
	return first
}

// RelevantCount returns the number of queued transactions with seqProxy >= acctSeqProx.
// This mirrors rippled's lower_bound(acctSeqProx) filtering which ignores stale
// sequence-based transactions that slipped into the ledger while the queue wasn't
// watching.
// Reference: TxQ.cpp:809-830
func (aq *accountQueue) RelevantCount(acctSeqProx SeqProxy) int {
	count := 0
	for sp := range aq.Transactions {
		if !sp.Less(acctSeqProx) { // sp >= acctSeqProx
			count++
		}
	}
	return count
}

// FirstRelevant returns the first (lowest) relevant transaction with seqProxy >= acctSeqProx.
// Returns nil if no relevant transactions exist.
// Reference: TxQ.cpp:818 lower_bound(acctSeqProx)
func (aq *accountQueue) FirstRelevant(acctSeqProx SeqProxy) *candidate {
	var first *candidate
	for sp, c := range aq.Transactions {
		if !sp.Less(acctSeqProx) { // sp >= acctSeqProx
			if first == nil || sp.Less(first.SeqProxy) {
				first = c
			}
		}
	}
	return first
}

// RelevantSortedCandidates returns the account's candidates with SeqProxy >=
// acctSeqProx, sorted ascending by SeqProxy. Stale sequence-based entries that
// slipped into the ledger are excluded, mirroring rippled's lower_bound(
// acctSeqProx) range (TxQ.cpp:818).
func (aq *accountQueue) RelevantSortedCandidates(acctSeqProx SeqProxy) []*candidate {
	sorted := aq.SortedCandidates()
	relevant := make([]*candidate, 0, len(sorted))
	for _, c := range sorted {
		if !c.SeqProxy.Less(acctSeqProx) {
			relevant = append(relevant, c)
		}
	}
	return relevant
}

// SortedCandidates returns all candidates sorted by SeqProxy.
func (aq *accountQueue) SortedCandidates() []*candidate {
	result := make([]*candidate, 0, len(aq.Transactions))
	for _, c := range aq.Transactions {
		result = append(result, c)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].SeqProxy.Less(result[j].SeqProxy)
	})

	return result
}
