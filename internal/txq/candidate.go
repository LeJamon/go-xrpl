package txq

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// RetriesAllowed is the starting retry count for newly queued transactions.
// If a transaction fails to apply this many times, it will be dropped.
const RetriesAllowed = 10

// SeqProxy represents either a sequence number or a ticket number.
// This mirrors rippled's SeqProxy type.
type SeqProxy struct {
	// Value is the sequence or ticket number
	Value uint32

	// IsTicket indicates whether this is a ticket number (true) or sequence (false)
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
	// Sequences come before tickets
	if !s.IsTicket && other.IsTicket {
		return true
	}
	if s.IsTicket && !other.IsTicket {
		return false
	}
	// Same type, compare values
	return s.Value < other.Value
}

// Candidate represents a transaction that may be applied to the open ledger.
// It holds all the information needed to attempt application and track retries.
type Candidate struct {
	// Txn is the transaction to apply
	Txn tx.Transaction

	// TxID is the transaction hash
	TxID [32]byte

	// Account is the account that submitted the transaction
	Account [20]byte

	// FeeLevel is the fee level paid by this transaction
	FeeLevel FeeLevel

	// SeqProxy is the sequence or ticket number
	SeqProxy SeqProxy

	// LastValid is the LastLedgerSequence if present (0 if not set)
	LastValid uint32

	// RetriesRemaining tracks how many more times we can retry this transaction.
	// Starts at RetriesAllowed.
	RetriesRemaining int

	// LastResult holds the result from the last failed application attempt.
	// Zero value means no attempt has been made yet.
	LastResult tx.Result

	// PreflightResult holds the result from the preflight check.
	PreflightResult tx.Result

	// Fee is the fee in drops
	Fee uint64

	// Consequences tracks the potential impact of this transaction
	Consequences TxConsequences
}

// TxConsequences describes the potential impact of applying a transaction.
type TxConsequences struct {
	// Fee is the fee that will be consumed
	Fee uint64

	// PotentialSpend is the maximum XRP that could be spent beyond the fee.
	// For payments this is the amount. For offers this is TakerGets if selling XRP.
	PotentialSpend uint64

	// IsBlocker indicates if this transaction could invalidate subsequent
	// transactions for the same account (e.g., SetRegularKey, SignerListSet).
	IsBlocker bool

	// FollowingSeq is the sequence number that should follow this transaction.
	// For regular transactions this is Sequence + 1.
	// For TicketCreate this is Sequence + 1 + TicketCount (to account for the gap).
	FollowingSeq SeqProxy
}

// NewCandidate creates a new Candidate for a transaction.
func NewCandidate(
	txn tx.Transaction,
	txID [32]byte,
	account [20]byte,
	feeLevel FeeLevel,
	seqProxy SeqProxy,
	lastValid uint32,
	preflightResult tx.Result,
	consequences TxConsequences,
) *Candidate {
	return &Candidate{
		Txn:              txn,
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

// AccountQueue tracks queued transactions for a single account.
type AccountQueue struct {
	// Account is the account ID
	Account [20]byte

	// Transactions maps SeqProxy to Candidate, ordered by SeqProxy
	Transactions map[SeqProxy]*Candidate

	// RetryPenalty is set when a transaction has exhausted its retries.
	// Other transactions for this account will have reduced retry allowance.
	RetryPenalty bool

	// DropPenalty is set when a transaction has failed or expired.
	// When the queue is nearly full, transactions from this account
	// may be discarded more readily.
	DropPenalty bool
}

// NewAccountQueue creates a new AccountQueue for an account.
func NewAccountQueue(account [20]byte) *AccountQueue {
	return &AccountQueue{
		Account:      account,
		Transactions: make(map[SeqProxy]*Candidate),
	}
}

// Add adds a candidate to this account's queue.
func (aq *AccountQueue) Add(c *Candidate) {
	aq.Transactions[c.SeqProxy] = c
}

// Remove removes a candidate with the given SeqProxy.
// Returns true if a candidate was removed.
func (aq *AccountQueue) Remove(seqProxy SeqProxy) bool {
	if _, exists := aq.Transactions[seqProxy]; exists {
		delete(aq.Transactions, seqProxy)
		return true
	}
	return false
}

// Count returns the number of transactions queued for this account.
func (aq *AccountQueue) Count() int {
	return len(aq.Transactions)
}

// Empty returns true if there are no queued transactions.
func (aq *AccountQueue) Empty() bool {
	return len(aq.Transactions) == 0
}

// GetPrevTx finds the transaction that precedes the given SeqProxy.
// Returns nil if there is no preceding transaction.
func (aq *AccountQueue) GetPrevTx(seqProxy SeqProxy) *Candidate {
	var prev *Candidate
	for sp, c := range aq.Transactions {
		if sp.Less(seqProxy) {
			if prev == nil || prev.SeqProxy.Less(sp) {
				prev = c
			}
		}
	}
	return prev
}

// GetFirstSeqTx returns the first sequence-based transaction (lowest sequence).
// Returns nil if there are no sequence-based transactions.
func (aq *AccountQueue) GetFirstSeqTx() *Candidate {
	var first *Candidate
	for _, c := range aq.Transactions {
		if !c.SeqProxy.IsTicket {
			if first == nil || c.SeqProxy.Value < first.SeqProxy.Value {
				first = c
			}
		}
	}
	return first
}

// GetSortedCandidates returns all candidates sorted by SeqProxy.
func (aq *AccountQueue) GetSortedCandidates() []*Candidate {
	result := make([]*Candidate, 0, len(aq.Transactions))
	for _, c := range aq.Transactions {
		result = append(result, c)
	}

	// Sort by SeqProxy
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].SeqProxy.Less(result[i].SeqProxy) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	return result
}
