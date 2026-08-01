package relationaldb

import (
	"context"
	"fmt"
	"time"
)

// LedgerIndex represents a ledger sequence number
type LedgerIndex uint32

// Hash represents a 256-bit hash
type Hash [32]byte

// AccountID represents an XRPL account identifier
type AccountID [20]byte

// Amount represents an XRPL amount value
type Amount int64

// LedgerInfo contains basic information about a ledger
type LedgerInfo struct {
	Hash            Hash        `json:"hash"`
	Sequence        LedgerIndex `json:"sequence"`
	ParentHash      Hash        `json:"parent_hash"`
	AccountHash     Hash        `json:"account_hash"`
	TransactionHash Hash        `json:"transaction_hash"`
	TotalCoins      Amount      `json:"total_coins"`
	CloseTime       time.Time   `json:"close_time"`
	ParentCloseTime time.Time   `json:"parent_close_time"`
	CloseTimeRes    int32       `json:"close_time_res"`
	CloseFlags      uint32      `json:"close_flags"`
}

// LedgerHashPair contains a ledger hash and its parent hash
type LedgerHashPair struct {
	LedgerHash Hash `json:"ledger_hash"`
	ParentHash Hash `json:"parent_hash"`
}

// LedgerRange represents a range of ledger sequences
type LedgerRange struct {
	Min LedgerIndex `json:"min"`
	Max LedgerIndex `json:"max"`
}

// TransactionInfo contains information about a transaction
type TransactionInfo struct {
	Hash      Hash        `json:"hash"`
	LedgerSeq LedgerIndex `json:"ledger_seq"`
	TxnSeq    uint32      `json:"txn_seq"`
	Status    string      `json:"status"`
	RawTxn    []byte      `json:"raw_txn"`
	TxnMeta   []byte      `json:"txn_meta"`
	Account   AccountID   `json:"account"`
}

// AccountTxMarker represents pagination marker for account transactions
type AccountTxMarker struct {
	LedgerSeq LedgerIndex `json:"ledger_seq"`
	TxnSeq    uint32      `json:"txn_seq"`
}

// AccountTxPageOptions contains criteria for paginated account transaction queries
type AccountTxPageOptions struct {
	Account   AccountID        `json:"account"`
	MinLedger LedgerIndex      `json:"min_ledger"`
	MaxLedger LedgerIndex      `json:"max_ledger"`
	Marker    *AccountTxMarker `json:"marker,omitempty"`
	Limit     uint32           `json:"limit"`
}

// AccountTxResult contains the result of an account transaction query
type AccountTxResult struct {
	Transactions []TransactionInfo `json:"transactions"`
	LedgerRange  LedgerRange       `json:"ledger_range"`
	Limit        uint32            `json:"limit"`
	Marker       *AccountTxMarker  `json:"marker,omitempty"`
}

// TxSearchResult represents the result of a transaction search
type TxSearchResult int

// TxSearchResult values report whether all, some, or no transactions matched.
const (
	TxSearchUnknown TxSearchResult = iota
	TxSearchSome
	TxSearchAll
)

// LedgerRepository handles ledger-related database operations
type LedgerRepository interface {
	GetMinLedgerSeq(ctx context.Context) (*LedgerIndex, error)
	GetMaxLedgerSeq(ctx context.Context) (*LedgerIndex, error)
	GetLedgerInfoBySeq(ctx context.Context, seq LedgerIndex) (*LedgerInfo, error)
	GetLedgerInfoByHash(ctx context.Context, hash Hash) (*LedgerInfo, error)
	GetNewestLedgerInfo(ctx context.Context) (*LedgerInfo, error)
	GetHashesByRange(ctx context.Context, minSeq, maxSeq LedgerIndex) (map[LedgerIndex]LedgerHashPair, error)
	// SaveValidatedLedger stores only a ledger header. Callers persisting a
	// complete validated ledger must use RepositoryManager.PersistValidatedLedger.
	SaveValidatedLedger(ctx context.Context, ledger LedgerInfo) error
	DeleteLedgersBySeq(ctx context.Context, maxSeq LedgerIndex) error
}

// TransactionRepository handles transaction-related database operations
type TransactionRepository interface {
	GetTransactionsMinLedgerSeq(ctx context.Context) (*LedgerIndex, error)
	GetTransaction(ctx context.Context, hash Hash, ledgerRange *LedgerRange) (*TransactionInfo, TxSearchResult, error)
	GetTxHistory(ctx context.Context, startIndex LedgerIndex, limit int) ([]TransactionInfo, error)
	SaveTransaction(ctx context.Context, txInfo TransactionInfo) error
	DeleteTransactionsByLedgerSeq(ctx context.Context, ledgerSeq LedgerIndex) error
	DeleteTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq LedgerIndex) error
}

// AccountTransactionRepository handles account transaction-related database operations
type AccountTransactionRepository interface {
	GetAccountTransactionsMinLedgerSeq(ctx context.Context) (*LedgerIndex, error)
	GetOldestAccountTxsPage(ctx context.Context, options AccountTxPageOptions) (*AccountTxResult, error)
	GetNewestAccountTxsPage(ctx context.Context, options AccountTxPageOptions) (*AccountTxResult, error)
	SaveAccountTransaction(ctx context.Context, accountID AccountID, txInfo TransactionInfo) error
	DeleteAccountTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq LedgerIndex) error
}

// IndexedTransaction associates a transaction with every affected account.
type IndexedTransaction struct {
	Transaction TransactionInfo
	Accounts    []AccountID
}

// ValidatedLedger contains a ledger header and its indexed transactions.
type ValidatedLedger struct {
	Ledger       LedgerInfo
	Transactions []IndexedTransaction
}

// Validate checks the invariants required for atomic ledger persistence.
func (v ValidatedLedger) Validate() error {
	if v.Ledger.Hash.IsZero() {
		return NewDataError("persist_validated_ledger", "ledger hash is zero", ErrInvalidData)
	}
	if v.Ledger.AccountHash.IsZero() {
		return NewDataError("persist_validated_ledger", "account state hash is zero", ErrInvalidData)
	}
	if len(v.Transactions) == 0 && !v.Ledger.TransactionHash.IsZero() {
		return NewDataError("persist_validated_ledger", "empty ledger has a non-zero transaction hash", ErrInvalidData)
	}
	if len(v.Transactions) > 0 && v.Ledger.TransactionHash.IsZero() {
		return NewDataError("persist_validated_ledger", "transaction hash is zero", ErrInvalidData)
	}
	transactionHashes := make(map[Hash]struct{}, len(v.Transactions))
	transactionIndexes := make(map[uint32]struct{}, len(v.Transactions))
	for i, indexed := range v.Transactions {
		if indexed.Transaction.LedgerSeq != v.Ledger.Sequence {
			return NewDataError(
				"persist_validated_ledger",
				fmt.Sprintf("transaction %d has ledger sequence %d, want %d", i, indexed.Transaction.LedgerSeq, v.Ledger.Sequence),
				ErrInvalidData,
			)
		}
		if indexed.Transaction.Hash.IsZero() {
			return NewDataError(
				"persist_validated_ledger",
				fmt.Sprintf("transaction %d has zero hash", i),
				ErrInvalidData,
			)
		}
		if _, exists := transactionHashes[indexed.Transaction.Hash]; exists {
			return NewDataError(
				"persist_validated_ledger",
				fmt.Sprintf("transaction %d has duplicate hash", i),
				ErrInvalidData,
			)
		}
		transactionHashes[indexed.Transaction.Hash] = struct{}{}
		if uint64(indexed.Transaction.TxnSeq) >= uint64(len(v.Transactions)) {
			return NewDataError(
				"persist_validated_ledger",
				fmt.Sprintf("transaction %d has index %d outside transaction set", i, indexed.Transaction.TxnSeq),
				ErrInvalidData,
			)
		}
		if _, exists := transactionIndexes[indexed.Transaction.TxnSeq]; exists {
			return NewDataError(
				"persist_validated_ledger",
				fmt.Sprintf("transaction %d has duplicate index %d", i, indexed.Transaction.TxnSeq),
				ErrInvalidData,
			)
		}
		transactionIndexes[indexed.Transaction.TxnSeq] = struct{}{}
		if len(indexed.Transaction.RawTxn) == 0 {
			return NewDataError(
				"persist_validated_ledger",
				fmt.Sprintf("transaction %d has empty payload", i),
				ErrInvalidData,
			)
		}
		if len(indexed.Transaction.TxnMeta) == 0 {
			return NewDataError(
				"persist_validated_ledger",
				fmt.Sprintf("transaction %d has empty metadata", i),
				ErrInvalidData,
			)
		}
		accounts := make(map[AccountID]struct{}, len(indexed.Accounts))
		for accountIndex, account := range indexed.Accounts {
			if account.IsZero() {
				return NewDataError(
					"persist_validated_ledger",
					fmt.Sprintf("transaction %d account %d is zero", i, accountIndex),
					ErrInvalidData,
				)
			}
			if _, exists := accounts[account]; exists {
				return NewDataError(
					"persist_validated_ledger",
					fmt.Sprintf("transaction %d account %d is duplicated", i, accountIndex),
					ErrInvalidData,
				)
			}
			accounts[account] = struct{}{}
		}
	}
	return nil
}

// TransactionRepositories exposes repositories bound to one database transaction.
type TransactionRepositories interface {
	Transaction() TransactionRepository
	AccountTransaction() AccountTransactionRepository
}

// RepositoryManager provides access to all repositories and transaction management
type RepositoryManager interface {
	// Repository access
	Ledger() LedgerRepository
	Transaction() TransactionRepository
	AccountTransaction() AccountTransactionRepository
	Validation() ValidationRepository
	Amendment() AmendmentVoteRepository
	Close() error
	WithTransaction(ctx context.Context, fn func(TransactionRepositories) error) error
	// PersistValidatedLedger stores a header, transactions, and account indexes
	// as one recoverable unit. PostgreSQL commits them in one transaction.
	// SQLite removes any same-sequence header, commits the complete indexes, and
	// publishes the replacement header last. A failed call is safe to retry and
	// never exposes a header whose indexes are only partially persisted.
	PersistValidatedLedger(ctx context.Context, ledger ValidatedLedger) error
}

// Helper methods for Hash type
func (h Hash) String() string {
	return fmt.Sprintf("%x", h[:])
}

// IsZero reports whether the hash is all zero bytes.
func (h Hash) IsZero() bool {
	return h == Hash{}
}

// Helper methods for AccountID type
func (a AccountID) String() string {
	return fmt.Sprintf("%x", a[:])
}

// IsZero reports whether the account ID is all zero bytes.
func (a AccountID) IsZero() bool {
	return a == AccountID{}
}
